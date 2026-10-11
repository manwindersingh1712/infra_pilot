package api

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"infrapilot/internal/logs"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestParseLogQuery(t *testing.T) {
	q, err := parseLogQuery("d1", url.Values{
		"limit": {"5000"}, "sources": {"stdout,stderr"}, "search": {"err"}, "after": {"2026-01-01T00:00:00.000Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.DeploymentID != "d1" || q.Limit != 1000 || len(q.Sources) != 2 || q.Search != "err" || q.Start == nil || q.End != nil {
		t.Fatalf("q = %+v", q)
	}
	for _, bad := range []string{"abc", "-5", "0", ""} {
		if q, _ := parseLogQuery("d1", url.Values{"limit": {bad}}); q.Limit != 100 {
			t.Fatalf("limit %q should default to 100, got %d", bad, q.Limit)
		}
	}
	if _, err := parseLogQuery("d1", url.Values{"before": {"yesterday"}}); err == nil {
		t.Fatal("expected error for bad timestamp")
	}
}

// Search is user input for ClickHouse ILIKE: wildcards are escaped and the
// length is capped; sources are limited to the three real values.
func TestParseLogQueryHardensSearchAndSources(t *testing.T) {
	q, err := parseLogQuery("d1", url.Values{"search": {`100%_done\`}})
	if err != nil || q.Search != `100\%\_done\\` {
		t.Fatalf("search %q err %v", q.Search, err)
	}
	if _, err := parseLogQuery("d1", url.Values{"search": {strings.Repeat("a", maxSearchLen+1)}}); err == nil {
		t.Fatal("over-long search must be rejected")
	}
	if _, err := parseLogQuery("d1", url.Values{"search": {strings.Repeat("a", maxSearchLen)}}); err != nil {
		t.Fatalf("search at the cap must pass: %v", err)
	}
	if _, err := parseLogQuery("d1", url.Values{"sources": {"stdout,bogus"}}); err == nil {
		t.Fatal("unknown source must be rejected")
	}
	if _, err := parseLogQuery("d1", url.Values{"sources": {"stdout,stdout,stdout,stdout"}}); err == nil {
		t.Fatal("more sources than exist must be rejected")
	}
}

func TestCapEntry(t *testing.T) {
	long := strings.Repeat("é", maxLogLineBytes) // 2 bytes per rune
	e := capEntry(logs.Entry{Message: long})
	if len(e.Message) > maxLogLineBytes+len(truncatedSuffix) || !strings.HasSuffix(e.Message, truncatedSuffix) {
		t.Fatalf("len %d", len(e.Message))
	}
	if !utf8.ValidString(e.Message) {
		t.Fatal("truncation split a rune")
	}
	exact := strings.Repeat("a", maxLogLineBytes)
	if got := capEntry(logs.Entry{Message: exact}); got.Message != exact {
		t.Fatal("a line exactly at the cap must not be cut")
	}
	if got := capEntry(logs.Entry{Message: "short"}); got.Message != "short" {
		t.Fatalf("short message changed: %q", got.Message)
	}
}

func TestGetLogsIsOwnerScoped(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	tok := hs.token(f.UserID)
	hs.hot.entries[dep.ID] = []logs.Entry{
		{DeploymentID: dep.ID, Source: "stdout", Message: "one", Timestamp: "2026-01-01T00:00:00.000Z"},
		{DeploymentID: dep.ID, Source: "stdout", Message: "two", Timestamp: "2026-01-01T00:00:01.000Z"},
	}

	rec := hs.do("GET", "/logs/deployments/"+dep.ID+"?limit=1", tok, nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"logs":[{"deploymentId":"`+dep.ID+`","source":"stdout","message":"one","timestamp":"2026-01-01T00:00:00.000Z"}],"meta":{"source":"redis","count":1,"hasMore":false}}`)

	rec = hs.do("GET", "/logs/deployments/"+dep.ID, hs.token("intruder"), nil)
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"deployment not found"}`)
	expectStatus(t, hs.do("GET", "/logs/deployments/"+dep.ID+"?after=nope", tok, nil), 400)
	expectStatus(t, hs.do("GET", "/logs/deployments/"+dep.ID, "", nil), 401)
	expectBody(t, hs.do("GET", "/logs/health", tok, nil), `{"activeLogStreams":1,"status":"healthy"}`)
	expectStatus(t, hs.do("GET", "/logs/health", "", nil), 401)
}

// No logs yet is an empty array, never null (the UI maps over it).
func TestGetLogsEmptyIsArray(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	rec := hs.do("GET", "/logs/deployments/"+dep.ID, hs.token(f.UserID), nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"logs":[],"meta":{"source":"clickhouse","count":0,"totalCount":0,"hasMore":false}}`)
}

// Missing, malformed, foreign and soft-deleted ids all answer the same 404.
func TestGetLogsNotFoundIsUniform(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	gone := testutil.Seed(t, hs.st, store.TypeNodejs)
	depGone, _ := hs.st.CreateDeployment(context.Background(), gone.ServiceID, "main")
	tokGone := hs.token(gone.UserID)
	expectStatus(t, hs.do("GET", "/logs/deployments/"+depGone.ID, tokGone, nil), 200)
	if err := hs.st.SoftDeleteService(context.Background(), gone.ServiceID); err != nil {
		t.Fatal(err)
	}
	tok := hs.token(f.UserID)
	for name, c := range map[string]struct{ path, tok string }{
		"missing":      {"/logs/deployments/nosuchdeployment", tok},
		"malformed":    {"/logs/deployments/a%00b", tok},
		"invalid utf8": {"/logs/deployments/%ff%fe", tok},
		"too long":     {"/logs/deployments/" + strings.Repeat("a", 65), tok},
		"soft-deleted": {"/logs/deployments/" + depGone.ID, tokGone},
	} {
		rec := hs.do("GET", c.path, c.tok, nil)
		if rec.Code != 404 || rec.Body.String() != `{"error":"deployment not found"}`+"\n" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
}

// Log content is tenant data: an over-long line is cut, and an HTML-looking
// line is returned verbatim as JSON data, never interpreted.
func TestGetLogsCapsLineLengthAndLeavesContentAlone(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	hs.hot.entries[dep.ID] = []logs.Entry{
		{DeploymentID: dep.ID, Source: "stdout", Message: strings.Repeat("x", 10*maxLogLineBytes), Timestamp: "t"},
		{DeploymentID: dep.ID, Source: "stdout", Message: "<script>alert(1)</script>", Timestamp: "t"},
	}
	rec := hs.do("GET", "/logs/deployments/"+dep.ID, hs.token(f.UserID), nil)
	expectStatus(t, rec, 200)
	body := decode[struct{ Logs []logs.Entry }](t, rec)
	if len(body.Logs[0].Message) > maxLogLineBytes+len(truncatedSuffix) {
		t.Fatalf("line not capped: %d", len(body.Logs[0].Message))
	}
	if body.Logs[1].Message != "<script>alert(1)</script>" {
		t.Fatalf("content changed: %q", body.Logs[1].Message)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
}
