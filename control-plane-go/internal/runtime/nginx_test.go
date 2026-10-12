package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

const wantRoute = `# auto-generated – service svc-1
server {
    listen 80;
    server_name svc-1.localhost;

    resolver 127.0.0.11 valid=10s;

    location / {
        set $upstream_svc_1 http://cp-dep1:3000;
        proxy_pass $upstream_svc_1;

        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
`

func TestRenderRouteMatchesNodeOutput(t *testing.T) {
	if got := RenderRoute("svc-1", "localhost", "cp-dep1", 3000); got != wantRoute {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUpsertRouteWritesFileAndReloads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	r := &testutil.FakeRunner{}
	n := Nginx{R: r, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}

	if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "svc-svc-1.conf"))
	if err != nil || string(b) != wantRoute {
		t.Fatalf("file = %q err=%v", b, err)
	}
	if cmds := r.Commands(); len(cmds) != 1 || cmds[0] != "docker exec cp_nginx nginx -s reload" {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestRuntimeURL(t *testing.T) {
	if got := (Nginx{BaseDomain: "localhost", Port: "80"}).RuntimeURL("abc"); got != "http://abc.localhost" {
		t.Fatalf("got %s", got)
	}
	if got := (Nginx{BaseDomain: "example.com", Port: "8088"}).RuntimeURL("abc"); got != "http://abc.example.com:8088" {
		t.Fatalf("got %s", got)
	}
}

func TestUpsertRouteRejectsBadInput(t *testing.T) {
	cases := []struct{ name, svc, ctr, domain string }{
		{"svc traversal", "../x", "cp-dep1", "localhost"},
		{"svc semicolon", "a;b", "cp-dep1", "localhost"},
		{"svc newline", "a\nb", "cp-dep1", "localhost"},
		{"svc upper dot", "A.b", "cp-dep1", "localhost"},
		{"svc empty", "", "cp-dep1", "localhost"},
		{"container space", "svc-1", "x y", "localhost"},
		{"domain semicolon", "svc-1", "cp-dep1", "bad;domain"},
		{"domain empty", "svc-1", "cp-dep1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "routes")
			r := &testutil.FakeRunner{}
			n := Nginx{R: r, RoutesDir: dir, Container: "cp_nginx", BaseDomain: tc.domain, Port: "80"}
			if err := n.UpsertRoute(context.Background(), tc.svc, tc.ctr, 3000); err == nil {
				t.Fatal("want error")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("routes dir touched: %v", err)
			}
			if cmds := r.Commands(); len(cmds) != 0 {
				t.Fatalf("cmds = %v", cmds)
			}
		})
	}
}

func routeFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestUpsertRouteLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	for range 2 { // a second upsert replaces the existing file
		if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000); err != nil {
			t.Fatal(err)
		}
	}
	if got := routeFiles(t, dir); len(got) != 1 || got[0] != "svc-svc-1.conf" {
		t.Fatalf("files = %v", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "svc-svc-1.conf"))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v err=%v", fi.Mode(), err)
	}
}

// A reader holding the old file (nginx mid-reload) must never see a partial
// write: the new route replaces the old inode instead of rewriting it.
func TestUpsertRouteReplacesFileAtomically(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(t.TempDir(), "old.conf")
	if err := os.Link(filepath.Join(dir, "svc-svc-1.conf"), old); err != nil {
		t.Fatal(err)
	}
	if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep2", 3000); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); string(b) != wantRoute {
		t.Fatalf("old file rewritten in place: %q", b)
	}
}

var errReload = errors.New("nginx: [emerg] unexpected end of file")

func failingReload() *testutil.FakeRunner {
	return &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errReload }}
}

func checkReloadErr(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errReload) || !strings.Contains(err.Error(), "svc-1") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpsertRouteReloadFailureRemovesNewFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	r := failingReload()
	n := Nginx{R: r, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}

	checkReloadErr(t, n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000))
	if got := routeFiles(t, dir); len(got) != 0 {
		t.Fatalf("files = %v", got)
	}
	if cmds := r.Commands(); len(cmds) != 1 {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestUpsertRouteReloadFailureRestoresOldFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}

	n.R = failingReload()
	checkReloadErr(t, n.UpsertRoute(context.Background(), "svc-1", "cp-dep2", 4000))
	b, err := os.ReadFile(filepath.Join(dir, "svc-svc-1.conf"))
	if err != nil || string(b) != wantRoute {
		t.Fatalf("file = %q err=%v", b, err)
	}
	if got := routeFiles(t, dir); len(got) != 1 {
		t.Fatalf("files = %v", got)
	}
}

func TestUpsertRouteMkdirFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &testutil.FakeRunner{}
	n := Nginx{R: r, RoutesDir: filepath.Join(file, "routes"), Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000)
	if err == nil || !strings.Contains(err.Error(), "svc-1") {
		t.Fatalf("err = %v", err)
	}
	if cmds := r.Commands(); len(cmds) != 0 {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestUpsertRouteErrorQuotesServiceID(t *testing.T) {
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: t.TempDir(), Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	err := n.UpsertRoute(context.Background(), "bad\nINJECT", "cp-dep1", 3000)
	if err == nil || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), `bad\nINJECT`) {
		t.Fatalf("err = %q", err)
	}
}

// T24 SEC-1: RemoveRoute is idempotent and reloads nginx so the route stops serving.
func TestRemoveRouteDeletesFileAndReloads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	r := &testutil.FakeRunner{}
	n := Nginx{R: r, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	if err := n.UpsertRoute(context.Background(), vsvc, "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}
	for range 2 { // the second call finds no file and still succeeds
		if err := n.RemoveRoute(context.Background(), vsvc); err != nil {
			t.Fatal(err)
		}
	}
	if got := routeFiles(t, dir); len(got) != 0 {
		t.Fatalf("files = %v", got)
	}
	if cmds := r.Commands(); len(cmds) != 3 || cmds[2] != "docker exec cp_nginx nginx -s reload" {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestRemoveRouteRejectsBadServiceID(t *testing.T) {
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: t.TempDir(), Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	if err := n.RemoveRoute(context.Background(), "../etc"); err == nil {
		t.Fatal("want error")
	}
}

func TestRemoveRouteReloadFailureIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	n := Nginx{R: &testutil.FakeRunner{}, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}
	if err := n.UpsertRoute(context.Background(), vsvc, "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}
	n.R = failingReload()
	if err := n.RemoveRoute(context.Background(), vsvc); err == nil || !strings.Contains(err.Error(), "reload") {
		t.Fatalf("err = %v", err)
	}
}
