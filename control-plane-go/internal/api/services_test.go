package api

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"infrapilot/internal/secrets"
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

func outboxEvents(t *testing.T) [][2]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT "type", "payload"::text FROM "OutboxEvent" ORDER BY "createdAt"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var e [2]string
		if err := rows.Scan(&e[0], &e[1]); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func deploymentCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM "Deployment"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeployService(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	node := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(node.UserID)

	rec := hs.do("POST", "/services/"+node.ServiceID+"/deploy", tok, map[string]any{"commitSha": "HEAD"})
	expectStatus(t, rec, 200)
	resp := decode[map[string]string](t, rec)
	dep, _ := hs.st.GetDeployment(ctx, resp["deploymentId"])
	if resp["status"] != "queued" || dep.CommitSha != "HEAD" {
		t.Fatalf("resp %v dep %+v", resp, dep)
	}
	ev := outboxEvents(t)
	if len(ev) != 1 || ev[0][0] != store.EventBuildRequested || ev[0][1] != `{"deploymentId": "`+dep.ID+`"}` {
		t.Fatalf("outbox %v", ev)
	}
	expectStatus(t, hs.do("POST", "/services/"+node.ServiceID+"/deploy", tok, map[string]any{"commitSha": "ab"}), 400)

	redis := testutil.Seed(t, hs.st, store.TypeRedis)
	rec = hs.do("POST", "/services/"+redis.ServiceID+"/deploy", tok, nil)
	expectStatus(t, rec, 404) // not the caller's service
	expectBody(t, rec, `{"error":"service_not_found"}`)

	resp = decode[map[string]string](t, hs.do("POST", "/services/"+redis.ServiceID+"/deploy", hs.token(redis.UserID), nil))
	dep, _ = hs.st.GetDeployment(ctx, resp["deploymentId"])
	ev = outboxEvents(t)
	if dep.CommitSha != "latest" || ev[len(ev)-1][0] != store.EventDeployRequested {
		t.Fatalf("managed deploy: sha=%s outbox=%v", dep.CommitSha, ev)
	}
}

func TestDeployDefaultsToTheServiceBranch(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	for branch, want := range map[string]string{"master": "master", "main": "main", "feature/x": "HEAD", "v1": "HEAD"} {
		rec := hs.do("POST", "/services", tok, map[string]any{
			"projectId": f.ProjectID, "name": "b-" + strings.ReplaceAll(branch, "/", "-"),
			"repoUrl": "https://github.com/x/y", "branch": branch,
		})
		expectStatus(t, rec, 200)
		id := decode[map[string]any](t, rec)["id"].(string)
		resp := decode[map[string]string](t, hs.do("POST", "/services/"+id+"/deploy", tok, nil))
		dep, err := hs.st.GetDeployment(ctx, resp["deploymentId"])
		if err != nil || dep.CommitSha != want {
			t.Errorf("branch %q: commitSha %q (err %v), want %q", branch, dep.CommitSha, err, want)
		}
	}
}

func TestDeployRejectsUnsafeRefsAndUnknownServices(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	base := "/services/" + f.ServiceID + "/deploy"
	for _, sha := range []string{"--upload-pack=touch /tmp/pwn", "-abc", "a..bc", "feature/x", "ab", "", "abc\u0000d", strings.Repeat("a", 101)} {
		rec := hs.do("POST", base, tok, map[string]any{"commitSha": sha})
		if rec.Code != 400 {
			t.Errorf("commitSha %q: status %d", sha, rec.Code)
			continue
		}
		expectBody(t, rec, `{"error":"invalid_request"}`)
	}
	expectStatus(t, hs.do("POST", base, tok, map[string]any{"commitSha": "abcdef1234"}), 200)
	rec := hs.doRaw("POST", base, tok, strings.NewReader(`{"commitSha":`), nil)
	expectStatus(t, rec, 400)
	if deploymentCount(t) != 1 || len(outboxEvents(t)) != 1 {
		t.Fatalf("rejected requests left rows: %d deployments, %v", deploymentCount(t), outboxEvents(t))
	}

	// Invalid, missing and foreign ids are indistinguishable.
	other := testutil.Seed(t, hs.st, store.TypeNodejs)
	for name, id := range map[string]string{"foreign": other.ServiceID, "missing": "cabc123missing", "nul": "%00", "bad utf8": "%ff", "too long": strings.Repeat("a", 65)} {
		rec := hs.do("POST", "/services/"+id+"/deploy", tok, nil)
		if rec.Code != 404 {
			t.Errorf("%s: status %d", name, rec.Code)
			continue
		}
		expectBody(t, rec, `{"error":"service_not_found"}`)
	}
	expectStatus(t, hs.do("POST", base, "", nil), 401)
	if deploymentCount(t) != 1 {
		t.Fatal("a denied deploy created a deployment")
	}
}

func TestDeployDeletedServiceIs404(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	if err := hs.st.SoftDeleteService(ctx, f.ServiceID); err != nil {
		t.Fatal(err)
	}
	rec := hs.do("POST", "/services/"+f.ServiceID+"/deploy", hs.token(f.UserID), nil)
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"service_not_found"}`)
	if deploymentCount(t) != 0 || len(outboxEvents(t)) != 0 {
		t.Fatal("deploy of a deleted service left rows")
	}
}

func TestServiceEnvIsEncryptedAndMasked(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	base := "/services/" + f.ServiceID + "/env"

	rec := hs.do("POST", base, tok, map[string]any{"key": "API_KEY", "value": "s3cret"})
	expectStatus(t, rec, 200)
	if v := decode[map[string]any](t, rec); v["value"] != "********" || v["key"] != "API_KEY" {
		t.Fatalf("set response %v", v)
	}
	raw, _ := hs.st.EnvMap(ctx, f.ServiceID)
	if !secrets.IsEncrypted(raw["API_KEY"]) {
		t.Fatalf("stored value not encrypted: %q", raw["API_KEY"])
	}

	if _, err := hs.st.UpsertEnvVar(ctx, f.ServiceID, "LEGACY", "plain"); err != nil { // written by the Node API before encryption
		t.Fatal(err)
	}
	masked := decode[[]map[string]any](t, hs.do("GET", base, tok, nil))
	revealed := decode[[]map[string]any](t, hs.do("GET", base+"?reveal=true", tok, nil))
	if len(masked) != 2 || masked[0]["value"] != "********" || masked[1]["value"] != "********" ||
		revealed[0]["value"] != "s3cret" || revealed[1]["value"] != "plain" {
		t.Fatalf("masked %v revealed %v", masked, revealed)
	}
	if body := hs.do("GET", base, tok, nil).Body.String(); strings.Contains(body, "s3cret") || strings.Contains(body, "enc:v1:") {
		t.Fatalf("masked list leaks: %s", body)
	}
	// Setting an existing key replaces the value (still one row).
	expectStatus(t, hs.do("POST", base, tok, map[string]any{"key": "API_KEY", "value": "n3w"}), 200)
	revealed = decode[[]map[string]any](t, hs.do("GET", base+"?reveal=true", tok, nil))
	if len(revealed) != 2 || revealed[0]["value"] != "n3w" {
		t.Fatalf("after overwrite %v", revealed)
	}

	for _, body := range []map[string]any{
		{"key": "", "value": "x"}, {"key": "B"}, {"key": "A B", "value": "x"}, {"key": "A=B", "value": "x"},
		{"key": "#X", "value": "x"}, {"key": "-X", "value": "x"}, {"key": ".X", "value": "x"},
		{"key": "Kéy", "value": "x"}, {"key": "K\u0000", "value": "x"}, {"key": strings.Repeat("A", 257), "value": "x"},
		{"key": "OK", "value": "a\nb"}, {"key": "OK", "value": "a\rb"}, {"key": "OK", "value": "a\u0000b"},
		{"key": "OK", "value": strings.Repeat("v", 32<<10+1)},
	} {
		rec := hs.do("POST", base, tok, body)
		if rec.Code != 400 {
			t.Errorf("%.40v: status %d", body, rec.Code)
		}
	}
	// Keys the worker accepts (dash, dot, leading digit) and an empty value are fine.
	for _, key := range []string{"BAD-KEY", "my.key", "1ST", "_X", strings.Repeat("A", 256)} {
		expectStatus(t, hs.do("POST", base, tok, map[string]any{"key": key, "value": ""}), 200)
	}

	rec = hs.do("DELETE", base+"/API_KEY", tok, nil)
	expectBody(t, rec, `{"deleted":true}`)
	if raw, _ := hs.st.EnvMap(ctx, f.ServiceID); raw["API_KEY"] != "" {
		t.Fatal("key not deleted")
	}
	// Unknown and unstorable keys answer the same way.
	for _, key := range []string{"NOPE", "%00", "%ff"} {
		rec = hs.do("DELETE", base+"/"+key, tok, nil)
		expectStatus(t, rec, 200)
		expectBody(t, rec, `{"deleted":true}`)
	}
}

func TestServiceEnvOwnership(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	other := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	if _, err := hs.st.UpsertEnvVar(ctx, other.ServiceID, "KEEP", "v"); err != nil {
		t.Fatal(err)
	}
	deleted := testutil.Seed(t, hs.st, store.TypeNodejs)
	if err := hs.st.SoftDeleteService(ctx, deleted.ServiceID); err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]string{
		"foreign": other.ServiceID, "missing": "cabc123missing", "nul": "%00", "bad utf8": "%ff",
		"too long": strings.Repeat("a", 65),
	} {
		for _, c := range []struct {
			method, suffix string
			body           any
		}{
			{"GET", "/env", nil}, {"GET", "/env?reveal=true", nil},
			{"POST", "/env", map[string]any{"key": "X", "value": "y"}},
			{"DELETE", "/env/KEEP", nil},
		} {
			rec := hs.do(c.method, "/services/"+id+c.suffix, tok, c.body)
			if rec.Code != 404 || rec.Body.String() != `{"error":"service_not_found"}`+"\n" {
				t.Errorf("%s %s %s: %d %s", name, c.method, c.suffix, rec.Code, rec.Body.String())
			}
		}
	}
	// A soft-deleted service's own owner cannot reach its env either.
	expectStatus(t, hs.do("GET", "/services/"+deleted.ServiceID+"/env", hs.token(deleted.UserID), nil), 404)
	expectStatus(t, hs.do("GET", "/services/"+other.ServiceID+"/env", "", nil), 401)

	raw, _ := hs.st.EnvMap(ctx, other.ServiceID)
	if len(raw) != 1 || raw["KEEP"] != "v" {
		t.Fatalf("the intruder changed another tenant's env: %v", raw)
	}
}

func TestServiceEnvBodyErrors(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	base := "/services/" + f.ServiceID + "/env"
	expectStatus(t, hs.doRaw("POST", base, tok, strings.NewReader(`{"key":`), nil), 400)
	expectStatus(t, hs.doRaw("POST", base, tok, strings.NewReader(`{"key":"A","value":1}`), nil), 400)
	expectStatus(t, hs.doRaw("POST", base, tok, strings.NewReader(`{"key":"A","value":"`+strings.Repeat("v", 2<<20)+`"}`), nil), 413)
}

func TestDeleteServiceQueuesTeardown(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeRedis)
	tok := hs.token(f.UserID)

	expectStatus(t, hs.do("DELETE", "/services/"+f.ServiceID, hs.token("someone-else"), nil), 404)
	if _, err := hs.st.GetService(ctx, f.ServiceID); err != nil || len(outboxEvents(t)) != 0 {
		t.Fatalf("a foreign delete changed state: %v %v", err, outboxEvents(t))
	}

	rec := hs.do("DELETE", "/services/"+f.ServiceID+"?purgeData=true", tok, nil)
	expectStatus(t, rec, 202)
	expectBody(t, rec, `{"deleted":true}`)
	if _, err := hs.st.GetService(ctx, f.ServiceID); err == nil {
		t.Fatal("service still visible")
	}
	ev := outboxEvents(t)
	if len(ev) != 1 || ev[0][0] != store.EventTeardownRequested ||
		ev[0][1] != `{"purgeData": true, "serviceId": "`+f.ServiceID+`"}` {
		t.Fatalf("outbox %v", ev)
	}
	rec = hs.do("DELETE", "/services/"+f.ServiceID, tok, nil)
	expectStatus(t, rec, 404)
	expectBody(t, rec, `{"error":"service_not_found"}`)
	if len(outboxEvents(t)) != 1 {
		t.Fatal("a repeated delete queued a second teardown")
	}
	// Gone from every owner-scoped read.
	if list := decode[[]map[string]any](t, hs.do("GET", "/services", tok, nil)); len(list) != 0 {
		t.Fatalf("deleted service listed: %v", list)
	}
}

func TestDeleteServiceDefaultsToKeepingData(t *testing.T) {
	hs := newHarness(t)
	for _, q := range []string{"", "?purgeData=false", "?purgeData=1", "?purgeData=TRUE"} {
		g := testutil.Seed(t, hs.st, store.TypeRedis)
		expectStatus(t, hs.do("DELETE", "/services/"+g.ServiceID+q, hs.token(g.UserID), nil), 202)
		ev := outboxEvents(t)
		if last := ev[len(ev)-1][1]; last != `{"purgeData": false, "serviceId": "`+g.ServiceID+`"}` {
			t.Errorf("query %q: payload %s", q, last)
		}
	}
}

func TestDeleteServiceRejectsBadIDs(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeRedis)
	tok := hs.token(f.UserID)
	for _, id := range []string{"%00", "%ff", strings.Repeat("a", 65), "cabc123missing", "..%2fx"} {
		rec := hs.do("DELETE", "/services/"+id, tok, nil)
		expectStatus(t, rec, 404)
		expectBody(t, rec, `{"error":"service_not_found"}`)
	}
	expectStatus(t, hs.do("DELETE", "/services/"+f.ServiceID, "", nil), 401)
	if len(outboxEvents(t)) != 0 {
		t.Fatal("a rejected delete queued a teardown")
	}
}

func TestConcurrentDeletesQueueOneTeardown(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeRedis)
	tok := hs.token(f.UserID)
	const n = 8
	codes := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			codes <- hs.do("DELETE", "/services/"+f.ServiceID, tok, nil).Code
		}()
	}
	close(start)
	got := map[int]int{}
	for i := 0; i < n; i++ {
		got[<-codes]++
	}
	if got[202] != 1 || got[404] != n-1 || len(outboxEvents(t)) != 1 {
		t.Fatalf("codes %v, outbox %v", got, outboxEvents(t))
	}
}

// SEC-1 / STAFF-1: the worker generates the managed password and keeps it in
// the env table; the API must not let a caller overwrite or drop it.
func TestManagedPasswordKeysAreReserved(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	for _, c := range []struct{ typ, key string }{
		{store.TypeRedis, "REDIS_PASSWORD"}, {store.TypeMongoDB, "MONGO_INITDB_ROOT_PASSWORD"},
	} {
		f := testutil.Seed(t, hs.st, c.typ)
		tok := hs.token(f.UserID)
		base := "/services/" + f.ServiceID + "/env"
		if _, err := hs.st.UpsertEnvVar(ctx, f.ServiceID, c.key, "generated"); err != nil {
			t.Fatal(err)
		}
		rec := hs.do("POST", base, tok, map[string]any{"key": c.key, "value": "attacker"})
		expectStatus(t, rec, 400)
		expectBody(t, rec, `{"error":"reserved_env_var"}`)
		rec = hs.do("DELETE", base+"/"+c.key, tok, nil)
		expectStatus(t, rec, 400)
		expectBody(t, rec, `{"error":"reserved_env_var"}`)
		if raw, _ := hs.st.EnvMap(ctx, f.ServiceID); raw[c.key] != "generated" {
			t.Fatalf("%s: managed password changed: %q", c.typ, raw[c.key])
		}
		// Other keys on a managed service stay writable.
		expectStatus(t, hs.do("POST", base, tok, map[string]any{"key": "OTHER", "value": "v"}), 200)
	}
	// The same names are ordinary on a non-managed service.
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	base := "/services/" + f.ServiceID + "/env"
	for _, key := range []string{"REDIS_PASSWORD", "MONGO_INITDB_ROOT_PASSWORD"} {
		expectStatus(t, hs.do("POST", base, hs.token(f.UserID), map[string]any{"key": key, "value": "mine"}), 200)
		expectStatus(t, hs.do("DELETE", base+"/"+key, hs.token(f.UserID), nil), 200)
	}
}

// SEC-2: a reveal leaves an audit line with who/what/which request, and never
// the keys or values themselves.
func TestRevealIsLoggedWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	hs := newHarnessWith(t, func(d *Deps) { d.Log = slog.New(slog.NewTextHandler(&buf, nil)) })
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	base := "/services/" + f.ServiceID + "/env"
	expectStatus(t, hs.do("POST", base, tok, map[string]any{"key": "TOP_SECRET_KEY", "value": "hunter2-value"}), 200)

	buf.Reset()
	expectStatus(t, hs.do("GET", base, tok, nil), 200)
	if strings.Contains(buf.String(), "reveal") {
		t.Fatalf("a masked read logged a reveal: %s", buf.String())
	}
	rec := hs.do("GET", base+"?reveal=true", tok, nil)
	expectStatus(t, rec, 200)
	out := buf.String()
	for _, want := range []string{"env revealed", "serviceId=" + f.ServiceID, "userId=" + f.UserID, "requestId=" + rec.Header().Get("X-Request-ID")} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
	for _, leak := range []string{"TOP_SECRET_KEY", "hunter2-value"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
}
