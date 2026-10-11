package api

import (
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
		RepoURL         *string `json:"repoUrl"`
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

func TestCanvasEmptyProjectIsEmptyArrays(t *testing.T) {
	hs := newHarness(t)
	u, _ := hs.st.CreateUser(context.Background(), "empty@x.co", "h", nil)
	p, _ := hs.st.CreateProject(context.Background(), "empty", u.ID)
	rec := hs.do("GET", "/projects/"+p.ID+"/canvas", hs.token(u.ID), nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"connections":[],"services":[]}`)
}

func TestCanvasMasksRepoURLCredentials(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	raw := "https://tok123@github.com/x/y"
	svc, err := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "private", ServiceType: store.TypeNodejs, RepoURL: &raw, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	q := "https://h/x?token=ghp_Q"
	if _, err := hs.st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "query", ServiceType: store.TypeNodejs, RepoURL: &q, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	rec := hs.do("GET", "/projects/"+f.ProjectID+"/canvas", hs.token(f.UserID), nil)
	expectStatus(t, rec, 200)
	body := rec.Body.String()
	if strings.Contains(body, "tok123") || strings.Contains(body, "ghp_Q") || !strings.Contains(body, "https://****@github.com/x/y") {
		t.Fatalf("canvas leaks or lost the url: %s", body)
	}
	stored, err := hs.st.GetService(ctx, svc.ID)
	if err != nil || stored.RepoURL == nil || *stored.RepoURL != raw {
		t.Fatalf("store row must stay raw: %+v %v", stored, err)
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
