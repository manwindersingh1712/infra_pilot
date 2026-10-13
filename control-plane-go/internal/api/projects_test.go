package api

import (
	"context"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
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

// PROD-3: rows written outside the create handler (legacy Node data) with names
// the handler would reject must still load through list and get.
func TestStoredOddNamesStillLoad(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	u, err := hs.st.CreateUser(ctx, "odd@x.co", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := hs.token(u.ID)
	names := map[string]string{
		"one char":  "x",
		"101 chars": strings.Repeat("a", 101),
		"control":   "Sh\u0001op\tx",
	}
	ids := map[string]string{}
	for k, n := range names {
		p, err := hs.st.CreateProject(ctx, n, u.ID)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		ids[k] = p.ID
	}
	list := decode[[]map[string]any](t, hs.do("GET", "/projects", tok, nil))
	if len(list) != len(names) {
		t.Fatalf("list has %d projects, want %d", len(list), len(names))
	}
	for k, n := range names {
		rec := hs.do("GET", "/projects/"+ids[k], tok, nil)
		expectStatus(t, rec, 200)
		if got := decode[map[string]any](t, rec)["name"]; got != n {
			t.Fatalf("%s: name %q want %q", k, got, n)
		}
	}
}

// Names are trimmed before validation and storage (Plan 2 Task 15 backlog).
func TestProjectAndServiceNamesAreTrimmed(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)

	for name, c := range map[string]struct{ in, want string }{
		"spaces":              {"  Shop  ", "Shop"},
		"tabs and newline":    {"\t\nShop\n", "Shop"},
		"no-break space":      {" Shop ", "Shop"},
		"ideographic space":   {"　商店　", "商店"},
		"inner space kept":    {" My  Shop ", "My  Shop"},
		"multi-byte, 2 runes": {" 😀😀 ", "😀😀"},
	} {
		rec := hs.do("POST", "/projects", tok, map[string]any{"name": c.in})
		expectStatus(t, rec, 200)
		if got := decode[map[string]any](t, rec)["name"]; got != c.want {
			t.Errorf("project %s: stored %q, want %q", name, got, c.want)
		}
		rec = hs.do("POST", "/services", tok, map[string]any{"projectId": f.ProjectID, "name": c.in, "serviceType": "redis"})
		expectStatus(t, rec, 200)
		if got := decode[map[string]any](t, rec)["name"]; got != c.want {
			t.Errorf("service %s: stored %q, want %q", name, got, c.want)
		}
	}

	// Validation sees the trimmed name: padding cannot satisfy the length bounds.
	for name, in := range map[string]string{
		"padded one rune":         "   x   ",
		"only spaces":             "      ",
		"one multi-byte rune":     " 😀 ",
		"padded over the maximum": " " + strings.Repeat("é", 101) + " ",
	} {
		rec := hs.do("POST", "/projects", tok, map[string]any{"name": in})
		if rec.Code != 400 {
			t.Errorf("project %s: status %d", name, rec.Code)
		}
		rec = hs.do("POST", "/services", tok, map[string]any{"projectId": f.ProjectID, "name": in, "serviceType": "redis"})
		if rec.Code != 400 {
			t.Errorf("service %s: status %d", name, rec.Code)
		}
	}
	// 100 multi-byte runes plus padding is still accepted (limit counts runes).
	long := strings.Repeat("é", 100)
	expectStatus(t, hs.do("POST", "/projects", tok, map[string]any{"name": "  " + long + "  "}), 200)
}
