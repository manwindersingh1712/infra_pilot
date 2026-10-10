package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

var (
	nonAlnum    = regexp.MustCompile(`[^a-zA-Z0-9]`)
	serviceIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	hostnameRE  = regexp.MustCompile(`^[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)*$`)
)

// checkRoute guards the values that become a file name and nginx directives
// (SEC-1 T18): the service ID is a hostname label, the base domain a hostname.
func checkRoute(serviceID, baseDomain, containerName string) error {
	if !serviceIDRE.MatchString(serviceID) {
		return fmt.Errorf("invalid service id %q", serviceID)
	}
	if !hostnameRE.MatchString(baseDomain) {
		return fmt.Errorf("invalid base domain %q", baseDomain)
	}
	return checkName("container", containerName)
}

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

// UpsertRoute writes the service's route and reloads nginx. If the reload
// fails, the previous route (or none) is put back so one bad file does not
// break every later reload (STAFF-1 T18).
func (n Nginx) UpsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error {
	if err := n.upsertRoute(ctx, serviceID, containerName, containerPort); err != nil {
		return fmt.Errorf("nginx route %q: %w", serviceID, err)
	}
	return nil
}

func (n Nginx) upsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error {
	if err := checkRoute(serviceID, n.BaseDomain, containerName); err != nil {
		return err
	}
	if err := os.MkdirAll(n.RoutesDir, 0o755); err != nil { // #nosec G301 -- nginx (separate container, bind mount) must read route files; contents are generated from our IDs
		return err
	}
	path := filepath.Join(n.RoutesDir, "svc-"+serviceID+".conf")
	prev, err := os.ReadFile(path) // #nosec G304 -- RoutesDir is operator config; serviceID is checked above
	hadPrev := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	conf := RenderRoute(serviceID, n.BaseDomain, containerName, containerPort)
	if err := writeFileAtomic(path, []byte(conf)); err != nil {
		return err
	}
	if _, err := n.R.Run(ctx, "", "docker", "exec", n.Container, "nginx", "-s", "reload"); err != nil {
		var rbErr error
		if hadPrev {
			rbErr = writeFileAtomic(path, prev)
		} else {
			rbErr = os.Remove(path)
		}
		if rbErr != nil {
			return errors.Join(fmt.Errorf("reload: %w", err), fmt.Errorf("roll back route file: %w", rbErr))
		}
		return fmt.Errorf("reload (route file rolled back): %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it onto path (STAFF-2 T18), so nginx never reads a partial route. The temp
// name does not match the routes/*.conf include.
func writeFileAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".svc-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(f.Name(), 0o644); err != nil { // #nosec G302 -- nginx (separate container, bind mount) must read route files; contents are generated from our IDs
		return err
	}
	return os.Rename(f.Name(), path)
}

func (n Nginx) RuntimeURL(serviceID string) string {
	suffix := ""
	if n.Port != "80" {
		suffix = ":" + n.Port
	}
	return "http://" + serviceID + "." + n.BaseDomain + suffix
}
