// Package build clones repositories, builds and pushes images, and consumes
// build.requested messages.
package build

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

type PackageJSON struct {
	Scripts map[string]string `json:"scripts"`
	Engines struct {
		Node string `json:"node"`
	} `json:"engines"`
}

// maxPackageJSONBytes caps how much of a cloned repo's package.json is read.
const maxPackageJSONBytes = 1 << 20 // 1 MiB

// ReadPackageJSON returns an empty PackageJSON when the file is missing,
// malformed, not a regular file, larger than maxPackageJSONBytes, or a symlink
// that escapes dir. The clone is attacker-controlled, so it is only read
// through os.OpenRoot.
func ReadPackageJSON(dir string) PackageJSON {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return PackageJSON{}
	}
	defer root.Close()
	f, err := root.Open("package.json")
	if err != nil {
		return PackageJSON{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxPackageJSONBytes {
		return PackageJSON{}
	}
	// The LimitReader also guards against the file growing after Stat; a
	// truncated document fails to decode and reads as malformed.
	b, err := io.ReadAll(io.LimitReader(f, maxPackageJSONBytes))
	if err != nil {
		return PackageJSON{}
	}
	return parsePackageJSON(b)
}

var nonVersionChars = regexp.MustCompile(`[^0-9a-zA-Z.-]`)

func baseImage(pkg PackageJSON) string {
	v := pkg.Engines.Node
	if v == "" {
		v = "20-alpine"
	}
	clean := nonVersionChars.ReplaceAllString(v, "")
	if strings.Contains(clean, "alpine") {
		return "node:" + clean
	}
	return "node:" + clean + "-alpine"
}

// cmdString renders s as a JSON string literal so a tenant-controlled value
// cannot close the CMD array or start a new Dockerfile line.
func cmdString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func NodejsDockerfile(pkg PackageJSON) string {
	scripts := pkg.Scripts
	start := "node index.js"
	switch {
	case scripts["start"] != "":
		start = "npm start"
	case scripts["start:prod"] != "":
		start = "npm run start:prod"
	case scripts["serve"] != "":
		start = "npm run serve"
	}
	// nodemon is a devDependency and is not installed in the production image.
	if strings.Contains(scripts["start"], "nodemon") {
		start = strings.Replace(scripts["start"], "nodemon", "node", 1)
	}

	lines := []string{
		"FROM " + baseImage(pkg), "",
		"WORKDIR /app", "",
		"# Install dependencies first (better caching)",
		"COPY package*.json ./", "",
		"RUN npm ci --only=production", "",
		"# Copy source code",
		"COPY . .",
	}
	if scripts["build"] != "" {
		lines = append(lines, "", "# Build application", "RUN npm run build")
	}
	lines = append(lines, "",
		"# Expose port", "EXPOSE 3000", "",
		"# Start application", `CMD ["sh", "-c", `+cmdString(start)+`]`)
	return strings.Join(lines, "\n")
}

func NextjsDockerfile(pkg PackageJSON) string {
	return strings.Join([]string{
		"FROM " + baseImage(pkg), "",
		"WORKDIR /app", "",
		"# Install dependencies",
		"COPY package*.json ./",
		"RUN npm ci", "",
		"# Copy source code",
		"COPY . .", "",
		"# Build Next.js app",
		"RUN npm run build", "",
		"# Expose port (Next.js default is 3000)",
		"EXPOSE 3000", "",
		"# Start Next.js in production mode",
		`CMD ["npm", "start"]`,
		"",
	}, "\n")
}

// ReactDockerfile builds static files, then serves outDir with nginx.
func ReactDockerfile(pkg PackageJSON, outDir string) string {
	return strings.Join([]string{
		"# Build stage",
		"FROM " + baseImage(pkg) + " AS builder", "",
		"WORKDIR /app", "",
		"# Install dependencies",
		"COPY package*.json ./",
		"RUN npm ci", "",
		"# Copy source and build",
		"COPY . .",
		"RUN npm run build", "",
		"# Production stage with nginx",
		"FROM nginx:alpine", "",
		"# Copy built files from builder stage",
		"COPY --from=builder /app/" + outDir + " /usr/share/nginx/html", "",
		"# Create nginx config for SPA routing",
		"RUN echo 'server { listen 80; location / { root /usr/share/nginx/html; try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf", "",
		"EXPOSE 80", "",
		`CMD ["nginx", "-g", "daemon off;"]`,
		"",
	}, "\n")
}

// ReactOutputDir is "dist" for Vite projects and "build" for Create React App.
func ReactOutputDir(dir string) string {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "build"
	}
	defer root.Close()
	for _, name := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"} {
		if _, err := root.Stat(name); err == nil {
			return "dist"
		}
	}
	return "build"
}

// GenerateDockerfile returns a Dockerfile for nodejs, nextjs and react
// services, or "" for types that must ship their own.
func GenerateDockerfile(serviceType, dir string) string {
	pkg := ReadPackageJSON(dir)
	switch serviceType {
	case "nodejs":
		return NodejsDockerfile(pkg)
	case "nextjs":
		return NextjsDockerfile(pkg)
	case "react":
		return ReactDockerfile(pkg, ReactOutputDir(dir))
	}
	return ""
}

// parsePackageJSON decodes the fields it needs one at a time so a wrong type
// in one (say "engines": "node >=14") does not discard the others. Wrong-typed
// fields and non-string script entries are ignored. Syntactically invalid JSON,
// or a top level that is not an object, yields an empty PackageJSON.
func parsePackageJSON(b []byte) PackageJSON {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return PackageJSON{}
	}
	var pkg PackageJSON
	var scripts map[string]json.RawMessage
	if json.Unmarshal(top["scripts"], &scripts) == nil {
		for name, raw := range scripts {
			var cmd string
			if json.Unmarshal(raw, &cmd) != nil {
				continue
			}
			if pkg.Scripts == nil {
				pkg.Scripts = map[string]string{}
			}
			pkg.Scripts[name] = cmd
		}
	}
	var engines struct {
		Node string `json:"node"`
	}
	if json.Unmarshal(top["engines"], &engines) == nil {
		pkg.Engines.Node = engines.Node
	}
	return pkg
}
