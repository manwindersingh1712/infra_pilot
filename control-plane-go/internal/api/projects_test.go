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

// SEC-1: an id that cannot be a stored id answers the same 404 as a missing one
// (no 500 from Postgres rejecting a NUL byte or invalid UTF-8).
func TestProjectPathIDValidation(t *testing.T) {
	hs := newHarness(t)
	u, err := hs.st.CreateUser(context.Background(), "pid@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := hs.token(u.ID)
	for name, id := range map[string]string{
		"nul":       "%00",
		"bad utf8":  "%ff",
		"too long":  strings.Repeat("a", 65),
		"traversal": "..%2fx",
		"space":     "a%20b",
		"missing":   "cabc123missing",
	} {
		rec := hs.do("GET", "/projects/"+id, tok, nil)
		if rec.Code != 404 {
			t.Fatalf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
		expectBody(t, rec, `{"error":"Project not found"}`)
	}
}

// SEC-2: a validly signed token for a user that does not exist is a 401, not a
// 500 from the foreign-key violation.
func TestCreateProjectUnknownUserIs401(t *testing.T) {
	hs := newHarness(t)
	rec := hs.do("POST", "/projects", hs.token("ghostuser"), map[string]any{"name": "Shop"})
	expectStatus(t, rec, 401)
	expectBody(t, rec, `{"error":"unauthorized"}`)
}

// STAFF-1: body fields other than name are ignored, and the name limits count
// runes, not bytes.
func TestCreateProjectBodyAndNameBoundaries(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	u, err := hs.st.CreateUser(ctx, "bnd@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := hs.st.CreateUser(ctx, "bnd2@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := hs.token(u.ID)

	rec := hs.do("POST", "/projects", tok, map[string]any{"name": "Shop", "ownerUserId": other.ID, "id": "chosenid"})
	expectStatus(t, rec, 200)
	p := decode[map[string]any](t, rec)
	if p["ownerUserId"] != u.ID || p["id"] == "chosenid" {
		t.Fatalf("body fields were honoured: %v", p)
	}
	if list := decode[[]map[string]any](t, hs.do("GET", "/projects", hs.token(other.ID), nil)); len(list) != 0 {
		t.Fatalf("other user owns %v", list)
	}

	for name, tc := range map[string]struct {
		name string
		want int
	}{
		"2 runes":        {"ab", 200},
		"100 runes":      {strings.Repeat("a", 100), 200},
		"100 multi-byte": {strings.Repeat("é", 100), 200},
		"101 multi-byte": {strings.Repeat("é", 101), 400},
		"101 ascii":      {strings.Repeat("a", 101), 400},
		"1 multi-byte":   {"é", 400},
		"2 multi-byte":   {"éé", 200},
	} {
		rec := hs.do("POST", "/projects", tok, map[string]any{"name": tc.name})
		if rec.Code != tc.want {
			t.Fatalf("%s: status %d want %d body %s", name, rec.Code, tc.want, rec.Body.String())
		}
	}
}
