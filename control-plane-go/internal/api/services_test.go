package api

import (
	"context"
	"strings"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestCreateAndListServices(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	intruder := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)

	rec := hs.do("POST", "/services", tok, map[string]any{
		"projectId": f.ProjectID, "name": "web", "serviceType": "nextjs", "repoUrl": "https://github.com/x/y",
	})
	expectStatus(t, rec, 200)
	svc := decode[map[string]any](t, rec)
	if svc["serviceType"] != "nextjs" || svc["branch"] != "main" || svc["positionX"] != 0.0 || svc["projectId"] != f.ProjectID {
		t.Fatalf("service %v", svc)
	}
	rec = hs.do("POST", "/services", tok, map[string]any{"projectId": f.ProjectID, "name": "cache", "serviceType": "redis"})
	expectStatus(t, rec, 200)
	if decode[map[string]any](t, rec)["repoUrl"] != nil {
		t.Fatal("redis repoUrl should be null")
	}

	rec = hs.do("POST", "/services", tok, map[string]any{"projectId": intruder.ProjectID, "name": "sneaky", "serviceType": "redis"})
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Project not found"}`)

	good := func(m map[string]any) map[string]any {
		out := map[string]any{"projectId": f.ProjectID, "name": "api", "repoUrl": "https://x.y/z"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"docker without repo":    {map[string]any{"projectId": f.ProjectID, "name": "api"}, "repoUrl required for docker/nodejs/nextjs/react services"},
		"unknown type":           {good(map[string]any{"serviceType": "php"}), "invalid_request"},
		"bad url":                {good(map[string]any{"repoUrl": "not a url"}), "invalid_repo_url"},
		"short name":             {good(map[string]any{"name": "a"}), "invalid_request"},
		"long name":              {good(map[string]any{"name": strings.Repeat("a", 101)}), "invalid_request"},
		"nul in name":            {good(map[string]any{"name": "ap\u0000i"}), "invalid_request"},
		"no project":             {map[string]any{"name": "api", "repoUrl": "https://x.y/z"}, "invalid_request"},
		"file url":               {good(map[string]any{"repoUrl": "file:///etc/passwd"}), "invalid_repo_url"},
		"ext url":                {good(map[string]any{"repoUrl": "ext::sh -c id"}), "invalid_repo_url"},
		"ssh url":                {good(map[string]any{"repoUrl": "ssh://git@h/x"}), "invalid_repo_url"},
		"scp url":                {good(map[string]any{"repoUrl": "git@github.com:x/y.git"}), "invalid_repo_url"},
		"http url":               {good(map[string]any{"repoUrl": "http://x.y/z"}), "invalid_repo_url"},
		"no-break space":         {good(map[string]any{"repoUrl": "https://h/x y"}), "invalid_repo_url"},
		"line separator":         {good(map[string]any{"repoUrl": "https://h/x y"}), "invalid_repo_url"},
		"fullwidth host":         {good(map[string]any{"repoUrl": "https://ｈost/x"}), "invalid_repo_url"},
		"nul in url":             {good(map[string]any{"repoUrl": "https://h/x\u0000"}), "invalid_repo_url"},
		"option branch":          {good(map[string]any{"branch": "--upload-pack=x"}), "invalid_request"},
		"empty branch":           {good(map[string]any{"branch": ""}), "invalid_request"},
		"wrong type serviceType": {good(map[string]any{"serviceType": 5}), "invalid_request"},
	} {
		rec := hs.do("POST", "/services", tok, c.body)
		if rec.Code != 400 {
			t.Errorf("%s: status %d", name, rec.Code)
			continue
		}
		expectBody(t, rec, `{"error":"`+c.code+`"}`)
	}

	all := decode[[]map[string]any](t, hs.do("GET", "/services", tok, nil))
	scoped := decode[[]map[string]any](t, hs.do("GET", "/services?projectId="+f.ProjectID, tok, nil))
	foreign := decode[[]map[string]any](t, hs.do("GET", "/services?projectId="+intruder.ProjectID, tok, nil))
	if len(all) != 3 || len(scoped) != 3 || len(foreign) != 0 {
		t.Fatalf("all=%d scoped=%d foreign=%d", len(all), len(scoped), len(foreign))
	}
	// A project id that cannot exist (NUL byte) is an empty list, not a 500.
	rec = hs.do("GET", "/services?projectId=%00", tok, nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `[]`)
	rec = hs.do("POST", "/services", tok, map[string]any{"projectId": "a\u0000b", "name": "api", "serviceType": "redis"})
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"Project not found"}`)
	expectStatus(t, hs.do("POST", "/services", "", map[string]any{}), 401)
	expectStatus(t, hs.do("GET", "/services", "", nil), 401)
}

func TestCreateServiceBodyErrors(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeRedis)
	tok := hs.token(f.UserID)
	rec := hs.doRaw("POST", "/services", tok, strings.NewReader(`{"projectId":`), nil)
	expectStatus(t, rec, 400)
	rec = hs.doRaw("POST", "/services", tok, strings.NewReader(`{"projectId":"`+f.ProjectID+`","name":"`+strings.Repeat("a", 2<<20)+`"}`), nil)
	expectStatus(t, rec, 413)
}

func TestRepoURLCredentialsMasked(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)

	rec := hs.do("POST", "/services", tok, map[string]any{
		"projectId": f.ProjectID, "name": "private", "repoUrl": "https://tok123@github.com/x/y",
	})
	expectStatus(t, rec, 200)
	created := decode[map[string]any](t, rec)
	if created["repoUrl"] != "https://****@github.com/x/y" {
		t.Fatalf("create response repoUrl %v", created["repoUrl"])
	}
	stored, err := hs.st.GetService(ctx, created["id"].(string))
	if err != nil || stored.RepoURL == nil || *stored.RepoURL != "https://tok123@github.com/x/y" {
		t.Fatalf("store row %+v err %v", stored, err)
	}

	// A token in the query string of a stored (Node-era) URL is masked too.
	q := "https://h/x?token=ghp_Q"
	if _, err := hs.st.CreateService(ctx, store.NewService{
		ProjectID: f.ProjectID, Name: "query", ServiceType: store.TypeNodejs, RepoURL: &q, Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	list := hs.do("GET", "/services", tok, nil)
	expectStatus(t, list, 200)
	if body := list.Body.String(); strings.Contains(body, "tok123") || strings.Contains(body, "ghp_Q") ||
		!strings.Contains(body, "https://****@github.com/x/y") {
		t.Fatalf("list leaks or lost the url: %s", body)
	}
}
