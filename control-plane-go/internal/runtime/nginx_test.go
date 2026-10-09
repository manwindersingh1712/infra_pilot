package runtime

import (
	"context"
	"os"
	"path/filepath"
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
