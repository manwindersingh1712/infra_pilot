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
