package api

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

type canvasResp struct {
	Services []struct {
		ID              string  `json:"id"`
		Name            string  `json:"name"`
		PositionX       float64 `json:"positionX"`
		DeploymentCount int     `json:"deploymentCount"`
	} `json:"services"`
	Connections []map[string]any `json:"connections"`
}

func TestCanvasRead(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	hs.st.CreateDeployment(ctx, f.ServiceID, "main")
	gone, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "gone", ServiceType: "redis", Branch: "main"})
	if err := hs.st.SoftDeleteService(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	label := "uses"
	if _, err := hs.st.CreateConnection(ctx, f.ServiceID, db.ID, &label); err != nil {
		t.Fatal(err)
	}
	tok := hs.token(f.UserID)

	rec := hs.do("GET", "/projects/"+f.ProjectID+"/canvas", tok, nil)
	expectStatus(t, rec, 200)
	c := decode[canvasResp](t, rec)
	if len(c.Services) != 2 || c.Services[0].ID != f.ServiceID || c.Services[0].DeploymentCount != 1 ||
		c.Services[1].ID != db.ID || len(c.Connections) != 1 || len(c.Connections[0]) != 4 ||
		c.Connections[0]["label"] != "uses" || c.Connections[0]["sourceId"] != f.ServiceID {
		t.Fatalf("canvas %+v", c)
	}
}

// Legacy rows linking services of different tenants, projects or a deleted end
// must not put a foreign id or label in the response, nor feed auto-layout.
func TestCanvasNeverShowsLegacyCrossTenantConnections(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	a := testutil.Seed(t, hs.st, store.TypeNodejs)
	b := testutil.Seed(t, hs.st, store.TypeNodejs)
	mk := func(project, name string) store.Service {
		s, err := hs.st.CreateService(ctx, store.NewService{ProjectID: project, Name: name, ServiceType: "redis", Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	aDB := mk(a.ProjectID, "a-db")
	p2, _ := hs.st.CreateProject(ctx, "a-second", a.UserID)
	a2 := mk(p2.ID, "a2-secret-name")
	gone := mk(a.ProjectID, "gone-secret-name")
	secret := "SECRET-LABEL-FROM-A"
	mustConn := func(src, dst string, label *string) {
		t.Helper()
		if _, err := hs.st.CreateConnection(ctx, src, dst, label); err != nil {
			t.Fatal(err)
		}
	}
	mustConn(a.ServiceID, aDB.ID, nil) // the one legitimate edge
	mustConn(a.ServiceID, b.ServiceID, &secret)
	mustConn(b.ServiceID, a.ServiceID, &secret)
	mustConn(a.ServiceID, a2.ID, &secret)
	mustConn(a2.ID, a.ServiceID, &secret)
	mustConn(a.ServiceID, gone.ID, &secret)
	if err := hs.st.SoftDeleteService(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}

	for name, u := range map[string]string{"A": a.UserID, "B": b.UserID} {
		project := a.ProjectID
		if name == "B" {
			project = b.ProjectID
		}
		rec := hs.do("GET", "/projects/"+project+"/canvas", hs.token(u), nil)
		expectStatus(t, rec, 200)
		body := rec.Body.Bytes()
		c := decode[canvasResp](t, rec)
		wantConns := 0
		if name == "A" {
			wantConns = 1
		}
		if len(c.Connections) != wantConns {
			t.Fatalf("%s: connections %+v", name, c.Connections)
		}
		foreign := []string{secret, "a2-secret-name", "gone-secret-name", a2.ID, gone.ID}
		if name == "A" {
			foreign = append(foreign, b.ServiceID)
		} else {
			foreign = append(foreign, a.ServiceID, aDB.ID)
		}
		for _, x := range foreign {
			if bytes.Contains(body, []byte(x)) {
				t.Fatalf("%s: response leaks %q: %s", name, x, body)
			}
		}
	}
	// Auto-layout on A must not place anything by a cross-tenant edge: with
	// only a->aDB valid, aDB is one rank right of a and nothing else moves.
	rec := hs.do("POST", "/projects/"+a.ProjectID+"/canvas/auto-layout", hs.token(a.UserID), nil)
	expectStatus(t, rec, 200)
	if bytes.Contains(rec.Body.Bytes(), []byte(b.ServiceID)) {
		t.Fatalf("auto-layout leaks: %s", rec.Body.String())
	}
}

func TestCanvasEmptyProjectIsEmptyArrays(t *testing.T) {
	hs := newHarness(t)
	u, _ := hs.st.CreateUser(context.Background(), "empty@x.co", "h", nil)
	p, _ := hs.st.CreateProject(context.Background(), "empty", u.ID)
	rec := hs.do("GET", "/projects/"+p.ID+"/canvas", hs.token(u.ID), nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"connections":[],"services":[]}`)
}

// The canvas never carries a repo URL (the UI does not read it); URL masking
// is covered by TestRepoURLCredentialsMasked on the services endpoints.
func TestCanvasHasNoRepoURL(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	raw := "https://tok123@github.com/x/y"
	if _, err := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "private", ServiceType: store.TypeNodejs, RepoURL: &raw, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	rec := hs.do("GET", "/projects/"+f.ProjectID+"/canvas", hs.token(f.UserID), nil)
	expectStatus(t, rec, 200)
	body := rec.Body.String()
	if strings.Contains(body, "repoUrl") || strings.Contains(body, "tok123") || strings.Contains(body, "github.com") {
		t.Fatalf("canvas carries a repo url: %s", body)
	}
}

func TestCanvasRequiresOwnershipAndRejectsBadIDs(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	intruder, _ := hs.st.CreateUser(context.Background(), "evil@x.co", "h", nil)
	for name, id := range map[string]string{
		"foreign": f.ProjectID, "missing": "nope", "nul": "a%00b",
		"badutf8": "%ff%fe", "long": strings.Repeat("a", 65),
	} {
		rec := hs.do("GET", "/projects/"+id+"/canvas", hs.token(intruder.ID), nil)
		if rec.Code != 404 || rec.Body.String() != `{"error":"Project not found"}`+"\n" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestUpdatePosition(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)

	rec := hs.do("POST", "/services/"+f.ServiceID+"/position", tok, map[string]any{"positionX": 12.5, "positionY": -7})
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"success":true}`)
	if svc, _ := hs.st.GetService(ctx, f.ServiceID); svc.PositionX != 12.5 || svc.PositionY != -7 {
		t.Fatalf("not saved: %+v", svc)
	}
	// Zero is a valid coordinate (a required field is not the same as non-zero).
	expectStatus(t, hs.do("POST", "/services/"+f.ServiceID+"/position", tok, map[string]any{"positionX": 0, "positionY": 0}), 200)

	for name, body := range map[string]string{
		"missing y":  `{"positionX":1}`,
		"missing x":  `{"positionY":1}`,
		"null":       `{"positionX":null,"positionY":1}`,
		"huge":       `{"positionX":1e9,"positionY":1}`,
		"overflow":   `{"positionX":1e999,"positionY":1}`,
		"negative":   `{"positionX":1,"positionY":-1e7}`,
		"string":     `{"positionX":"1","positionY":1}`,
		"not json":   `nope`,
		"trailing":   `{"positionX":1,"positionY":1} x`,
		"empty body": ``,
	} {
		rec := hs.doRaw("POST", "/services/"+f.ServiceID+"/position", tok, strings.NewReader(body), nil)
		if rec.Code != 400 || rec.Body.String() != `{"error":"invalid_request"}`+"\n" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if svc, _ := hs.st.GetService(ctx, f.ServiceID); svc.PositionX != 0 || svc.PositionY != 0 {
		t.Fatalf("rejected request changed the row: %+v", svc)
	}
}

func TestUpdatePositionHidesForeignMissingAndBadServices(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	gone, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "gone", ServiceType: "redis", Branch: "main"})
	if err := hs.st.SoftDeleteService(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	intruder, _ := hs.st.CreateUser(ctx, "evil@x.co", "h", nil)
	owner := hs.token(f.UserID)
	body := map[string]any{"positionX": 1, "positionY": 1}
	cases := map[string]struct{ id, tok string }{
		"foreign": {f.ServiceID, hs.token(intruder.ID)}, "missing": {"nope", owner}, "deleted": {gone.ID, owner},
		"nul": {"a%00b", owner}, "badutf8": {"%ff%fe", owner}, "long": {strings.Repeat("a", 65), owner},
	}
	for name, c := range cases {
		rec := hs.do("POST", "/services/"+c.id+"/position", c.tok, body)
		if rec.Code != 404 || rec.Body.String() != `{"error":"Service not found"}`+"\n" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if svc, _ := hs.st.GetService(ctx, f.ServiceID); svc.PositionX != 0 {
		t.Fatalf("foreign write landed: %+v", svc)
	}
	// Position of an unauthenticated caller.
	expectStatus(t, hs.do("POST", "/services/"+f.ServiceID+"/position", "", body), 401)
}

func TestBulkCanvasUpdate(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	// A second project of the same owner, and another tenant's project.
	p2, _ := hs.st.CreateProject(ctx, "other", f.UserID)
	sameOwnerOther, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: p2.ID, Name: "o", ServiceType: "redis", Branch: "main"})
	g := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	url := "/projects/" + f.ProjectID + "/canvas"

	rec := hs.do("PUT", url, tok, map[string]any{"services": []map[string]any{
		{"id": db.ID, "positionX": 99, "positionY": 98},
		{"id": sameOwnerOther.ID, "positionX": 5, "positionY": 5}, // other project: ignored
		{"id": g.ServiceID, "positionX": 5, "positionY": 5},       // other tenant: ignored
		{"id": "a\u0000b", "positionX": 5, "positionY": 5},        // malformed: ignored, no 500
		{"id": "nope", "positionX": 5, "positionY": 5},
	}})
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"success":true}`)
	pos := func(id string) [2]float64 {
		s, err := hs.st.GetService(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return [2]float64{s.PositionX, s.PositionY}
	}
	if pos(db.ID) != [2]float64{99, 98} || pos(sameOwnerOther.ID) != [2]float64{} || pos(g.ServiceID) != [2]float64{} {
		t.Fatalf("positions: db %v other %v foreign %v", pos(db.ID), pos(sameOwnerOther.ID), pos(g.ServiceID))
	}

	// One bad coordinate rejects the whole request: nothing is saved.
	rec = hs.do("PUT", url, tok, map[string]any{"services": []map[string]any{
		{"id": db.ID, "positionX": 1, "positionY": 1},
		{"id": f.ServiceID, "positionX": 1e12, "positionY": 1},
	}})
	expectStatus(t, rec, 400)
	expectBody(t, rec, `{"error":"invalid_request"}`)
	if pos(db.ID) != [2]float64{99, 98} {
		t.Fatal("partial update after a rejected request")
	}
	for name, body := range map[string]string{"no services": `{}`, "null": `{"services":null}`, "missing y": `{"services":[{"id":"a","positionX":1}]}`} {
		if rec := hs.doRaw("PUT", url, tok, strings.NewReader(body), nil); rec.Code != 400 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	expectStatus(t, hs.do("PUT", url, tok, map[string]any{"services": []any{}}), 200)

	// Ownership.
	intruder, _ := hs.st.CreateUser(ctx, "evil@x.co", "h", nil)
	rec = hs.do("PUT", url, hs.token(intruder.ID), map[string]any{"services": []any{}})
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Project not found"}`)
	expectStatus(t, hs.do("PUT", "/projects/a%00b/canvas", tok, map[string]any{"services": []any{}}), 404)
}

func TestAutoLayout(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	if _, err := hs.st.CreateConnection(ctx, f.ServiceID, db.ID, nil); err != nil {
		t.Fatal(err)
	}
	tok := hs.token(f.UserID)

	type layoutResp struct {
		Services []position `json:"services"`
	}
	lr := decode[layoutResp](t, hs.do("POST", "/projects/"+f.ProjectID+"/canvas/auto-layout", tok, nil))
	if len(lr.Services) != 2 || lr.Services[0] != (position{f.ServiceID, 0, 0}) || lr.Services[1] != (position{db.ID, 380, 0}) {
		t.Fatalf("layout %+v", lr)
	}
	if svc, _ := hs.st.GetService(ctx, db.ID); svc.PositionX != 380 {
		t.Fatalf("layout not saved: %+v", svc)
	}

	u, _ := hs.st.CreateUser(ctx, "empty@x.co", "h", nil)
	p, _ := hs.st.CreateProject(ctx, "empty", u.ID)
	rec := hs.do("POST", "/projects/"+p.ID+"/canvas/auto-layout", hs.token(u.ID), nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"services":[]}`)

	rec = hs.do("POST", "/projects/"+f.ProjectID+"/canvas/auto-layout", hs.token(u.ID), nil)
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Project not found"}`)
	expectStatus(t, hs.do("POST", "/projects/a%00b/canvas/auto-layout", tok, nil), 404)
}

func TestConnectionLifecycle(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	tok := hs.token(f.UserID)

	rec := hs.do("POST", "/services/connections", tok, map[string]any{"sourceId": f.ServiceID, "targetId": db.ID, "label": "uses"})
	expectStatus(t, rec, 200)
	conn := decode[map[string]any](t, rec)
	if conn["sourceId"] != f.ServiceID || conn["targetId"] != db.ID || conn["label"] != "uses" || conn["createdAt"] == nil || conn["id"] == nil {
		t.Fatalf("connection %v", conn)
	}
	// No label: null, like Node.
	rec = hs.do("POST", "/services/connections", tok, map[string]any{"sourceId": db.ID, "targetId": f.ServiceID})
	expectStatus(t, rec, 200)
	if got := decode[map[string]any](t, rec); got["label"] != nil {
		t.Fatalf("label %v", got["label"])
	}

	rec = hs.do("POST", "/services/connections", tok, map[string]any{"sourceId": f.ServiceID, "targetId": db.ID})
	expectStatus(t, rec, 409)
	expectBody(t, rec, `{"error":"Connection already exists"}`)
	rec = hs.do("POST", "/services/connections", tok, map[string]any{"sourceId": db.ID, "targetId": db.ID})
	expectStatus(t, rec, 400)
	expectBody(t, rec, `{"error":"Cannot connect service to itself"}`)

	c := decode[canvasResp](t, hs.do("GET", "/projects/"+f.ProjectID+"/canvas", tok, nil))
	if len(c.Connections) != 2 {
		t.Fatalf("canvas %+v", c)
	}

	id := conn["id"].(string)
	expectBody(t, hs.do("DELETE", "/services/connections/"+id, tok, nil), `{"success":true}`)
	rec = hs.do("DELETE", "/services/connections/"+id, tok, nil)
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Connection not found"}`)
	if l, _ := hs.st.ListProjectConnections(ctx, f.ProjectID); len(l) != 1 {
		t.Fatalf("connections after delete: %+v", l)
	}
}

// Every way the two ends can be wrong answers with the same 404 body, so a
// caller cannot tell a foreign service from a missing, deleted, malformed or
// other-project one.
func TestCreateConnectionNeverRevealsWhichEndFailed(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	mine, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "mine", ServiceType: "redis", Branch: "main"})
	gone, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "gone", ServiceType: "redis", Branch: "main"})
	if err := hs.st.SoftDeleteService(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	p2, _ := hs.st.CreateProject(ctx, "p2", f.UserID)
	otherProject, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: p2.ID, Name: "op", ServiceType: "redis", Branch: "main"})
	g := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)

	ends := map[string]string{
		"foreign": g.ServiceID, "missing": "nope", "deleted": gone.ID, "other project": otherProject.ID,
		"nul": "a\u0000b", "badutf8": "\xff\xfe", "long": strings.Repeat("a", 65), "empty": "",
	}
	for name, bad := range ends {
		for pos, body := range map[string]map[string]any{
			"as target": {"sourceId": f.ServiceID, "targetId": bad},
			"as source": {"sourceId": bad, "targetId": mine.ID},
			"self":      {"sourceId": bad, "targetId": bad},
		} {
			if name == "other project" && pos == "self" {
				continue // the caller owns that service: self-connection is a 400 for them
			}
			rec := hs.do("POST", "/services/connections", tok, body)
			if rec.Code != 404 || rec.Body.String() != `{"error":"Service not found"}`+"\n" {
				t.Fatalf("%s %s: %d %s", name, pos, rec.Code, rec.Body.String())
			}
		}
	}
	// A foreign caller gets the same answer for services that do exist.
	intruder, _ := hs.st.CreateUser(ctx, "evil@x.co", "h", nil)
	rec := hs.do("POST", "/services/connections", hs.token(intruder.ID), map[string]any{"sourceId": f.ServiceID, "targetId": mine.ID})
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Service not found"}`)
	// Self-connection of a foreign service is 404, not 400.
	rec = hs.do("POST", "/services/connections", hs.token(intruder.ID), map[string]any{"sourceId": f.ServiceID, "targetId": f.ServiceID})
	expectStatus(t, rec, 404)

	// Nothing was written by any of the above.
	for _, p := range []string{f.ProjectID, p2.ID, g.ProjectID} {
		if l, _ := hs.st.ListProjectConnections(ctx, p); len(l) != 0 {
			t.Fatalf("rejected requests created %+v", l)
		}
	}
}

func TestCreateConnectionRejectsBadBodies(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	tok := hs.token(f.UserID)
	for name, body := range map[string]string{
		"not json": `nope`, "trailing": `{} x`, "label control": `{"label":"a\u0000b"}`,
		"label too long": `{"label":"` + strings.Repeat("x", 101) + `"}`, "label number": `{"label":5}`,
	} {
		if strings.HasPrefix(body, "{\"label") {
			body = strings.Replace(body, "{", `{"sourceId":"`+f.ServiceID+`","targetId":"`+db.ID+`",`, 1)
		}
		rec := hs.doRaw("POST", "/services/connections", tok, strings.NewReader(body), nil)
		if rec.Code != 400 || rec.Body.String() != `{"error":"invalid_request"}`+"\n" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	big := strings.NewReader(`{"label":"` + strings.Repeat("x", 2<<20) + `"}`)
	expectStatus(t, hs.doRaw("POST", "/services/connections", tok, big, nil), 413)
	if l, _ := hs.st.ListProjectConnections(ctx, f.ProjectID); len(l) != 0 {
		t.Fatalf("created %+v", l)
	}
}

func TestDeleteConnectionRequiresOwnershipOfBothEnds(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	db, _ := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	g := testutil.Seed(t, hs.st, store.TypeNodejs)
	own, _ := hs.st.CreateConnection(ctx, f.ServiceID, db.ID, nil)
	// A row the API never creates: owner's source, foreign target.
	mixed, _ := hs.st.CreateConnection(ctx, f.ServiceID, g.ServiceID, nil)
	intruder, _ := hs.st.CreateUser(ctx, "evil@x.co", "h", nil)

	notFound := `{"error":"Connection not found"}`
	for name, c := range map[string]struct{ id, tok string }{
		"foreign user":      {own.ID, hs.token(intruder.ID)},
		"source-only owner": {mixed.ID, hs.token(f.UserID)},
		"target-only owner": {mixed.ID, hs.token(g.UserID)},
		"missing":           {"nope", hs.token(f.UserID)},
		"nul":               {"a%00b", hs.token(f.UserID)},
		"badutf8":           {"%ff%fe", hs.token(f.UserID)},
		"long":              {strings.Repeat("a", 65), hs.token(f.UserID)},
	} {
		rec := hs.do("DELETE", "/services/connections/"+c.id, c.tok, nil)
		if rec.Code != 404 || rec.Body.String() != notFound+"\n" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// The project list now hides the cross-tenant row, so count both rows raw.
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "ServiceConnection" WHERE "id" = ANY($1)`, []string{own.ID, mixed.ID}).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("a rejected delete removed a row: %d %v", rows, err)
	}
	expectStatus(t, hs.do("DELETE", "/services/connections/"+own.ID, "", nil), 401)
	expectStatus(t, hs.do("DELETE", "/services/connections/"+own.ID, hs.token(f.UserID), nil), 200)
}
