package runtime

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Nginx writes one server block per service into RoutesDir (mounted into the
// nginx container) and reloads nginx.
type Nginx struct {
	R          Runner
	RoutesDir  string
	Container  string
	BaseDomain string
	Port       string // public nginx port; "80" adds no :port suffix to URLs
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// RenderRoute returns the server block for <serviceID>.<baseDomain>. The
// upstream is held in a variable so nginx resolves the container name through
// Docker DNS at request time, not only at reload time.
func RenderRoute(serviceID, baseDomain, containerName string, containerPort int) string {
	v := "$upstream_" + nonAlnum.ReplaceAllString(serviceID, "_")
	return strings.Join([]string{
		"# auto-generated – service " + serviceID,
		"server {",
		"    listen 80;",
		"    server_name " + serviceID + "." + baseDomain + ";",
		"",
		"    resolver 127.0.0.11 valid=10s;",
		"",
		"    location / {",
		"        set " + v + " http://" + containerName + ":" + strconv.Itoa(containerPort) + ";",
		"        proxy_pass " + v + ";",
		"",
		"        proxy_http_version 1.1;",
		"        proxy_set_header Host $host;",
		"        proxy_set_header X-Real-IP $remote_addr;",
		"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
		"        proxy_set_header X-Forwarded-Proto $scheme;",
		"        proxy_set_header Upgrade $http_upgrade;",
		`        proxy_set_header Connection "upgrade";`,
		"    }",
		"}",
		"",
	}, "\n")
}

func (n Nginx) UpsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error {
	if err := os.MkdirAll(n.RoutesDir, 0o755); err != nil { // #nosec G301 -- nginx (separate container, bind mount) must read route files; contents are generated from our IDs
		return err
	}
	conf := RenderRoute(serviceID, n.BaseDomain, containerName, containerPort)
	if err := os.WriteFile(filepath.Join(n.RoutesDir, "svc-"+serviceID+".conf"), []byte(conf), 0o644); err != nil { // #nosec G306 -- nginx (separate container, bind mount) must read route files; contents are generated from our IDs
		return err
	}
	_, err := n.R.Run(ctx, "", "docker", "exec", n.Container, "nginx", "-s", "reload")
	return err
}

func (n Nginx) RuntimeURL(serviceID string) string {
	suffix := ""
	if n.Port != "80" {
		suffix = ":" + n.Port
	}
	return "http://" + serviceID + "." + n.BaseDomain + suffix
}
