package api

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var isoTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)

func TestProjects(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	u, err := hs.st.CreateUser(ctx, "p@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := hs.st.CreateUser(ctx, "q@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := hs.token(u.ID)
	otherTok := hs.token(other.ID)

	expectStatus(t, hs.do("GET", "/projects", "", nil), 401)
	expectStatus(t, hs.do("POST", "/projects", "", map[string]any{"name": "Shop"}), 401)
	expectStatus(t, hs.do("GET", "/projects/anything", "", nil), 401)
	rec := hs.do("GET", "/projects", tok, nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `[]`)

	for name, body := range map[string]any{
		"short":     map[string]any{"name": "x"},
		"missing":   map[string]any{},
		"empty":     nil,
		"wrongtype": map[string]any{"name": 5},
		"too long":  map[string]any{"name": strings.Repeat("a", 101)},
		"control":   map[string]any{"name": "Sh\u0000op"},
	} {
		rec := hs.do("POST", "/projects", tok, body)
		if rec.Code != 400 {
			t.Fatalf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
		expectBody(t, rec, `{"error":"invalid_request"}`)
	}

	rec = hs.do("POST", "/projects", tok, map[string]any{"name": "Shop"})
	expectStatus(t, rec, 200)
	p := decode[map[string]any](t, rec)
	if p["name"] != "Shop" || p["ownerUserId"] != u.ID || !isoTime.MatchString(p["createdAt"].(string)) || len(p) != 4 {
		t.Fatalf("project %v", p)
	}
	id := p["id"].(string)

	if list := decode[[]map[string]any](t, hs.do("GET", "/projects", tok, nil)); len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	rec = hs.do("GET", "/projects", otherTok, nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `[]`)
	expectStatus(t, hs.do("GET", "/projects/"+id, tok, nil), 200)

	// A foreign id and a missing id are indistinguishable.
	for _, pid := range []string{id, "does-not-exist"} {
		tk := otherTok
		if pid != id {
			tk = tok
		}
		rec = hs.do("GET", "/projects/"+pid, tk, nil)
		expectStatus(t, rec, 404)
		expectBody(t, rec, `{"error":"Project not found"}`)
	}
}
