package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errNoExposedPorts = errors.New("no_exposed_ports_found")

// Limits caps every container we start.
type Limits struct {
	Memory string // e.g. "512m"
	CPUs   string // e.g. "0.5"
	Pids   int
}

// Docker implements the container runtime with the docker CLI.
type Docker struct {
	R          Runner
	Limits     Limits
	Nginx      string // nginx container joined to every project network
	ProbeImage string // small image with sh, nc and cp (busybox)
}

type RunSpec struct {
	Image         string
	Name          string
	Network       string
	Aliases       []string
	Labels        map[string]string
	Env           map[string]string
	Cmd           []string // appended after the image
	ContainerPort int
	PublishPort   int      // 0: not published; otherwise bound to 127.0.0.1 only
	Volume        string   // named volume, mounted at VolumeTarget
	VolumeTarget  string   // absolute mount path inside the container (e.g. /data/db for mongo); required with Volume
	Tmpfs         []string // absolute paths mounted as tmpfs (e.g. mongo's /data/configdb, so no anonymous volume is created)
}

func (d Docker) Pull(ctx context.Context, image string) error {
	_, err := d.R.Run(ctx, "", "docker", "pull", image)
	return err
}

// DetectContainerPort returns the lowest port the image EXPOSEs.
func (d Docker) DetectContainerPort(ctx context.Context, image string) (int, error) {
	out, err := d.R.Run(ctx, "", "docker", "image", "inspect", image, "--format", "{{json .Config.ExposedPorts}}")
	if err != nil {
		return 0, err
	}
	return parseExposedPorts(out)
}

func parseExposedPorts(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return 0, errNoExposedPorts
	}
	var exposed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &exposed); err != nil {
		return 0, fmt.Errorf("parse exposed ports: %w", err)
	}
	var ports []int
	for k := range exposed {
		if p, err := strconv.Atoi(strings.SplitN(k, "/", 2)[0]); err == nil {
			ports = append(ports, p)
		}
	}
	if len(ports) == 0 {
		return 0, errNoExposedPorts
	}
	return slices.Min(ports), nil
}

// Run replaces any container with the same name and starts a new one
// detached. It returns the container ID.
func (d Docker) Run(ctx context.Context, s RunSpec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	envFile, err := writeEnvFile(s.Env)
	if err != nil {
		return "", err
	}
	if envFile != "" {
		defer os.Remove(envFile)
	}
	if err := d.Remove(ctx, s.Name); err != nil {
		return "", err
	}
	out, err := d.R.Run(ctx, "", "docker", d.runArgs(s, envFile)...)
	return strings.TrimSpace(out), err
}

// limitArgs caps memory, CPU and pids for every container we start,
// including the short-lived helper containers.
func (d Docker) limitArgs() []string {
	return []string{"--memory", d.Limits.Memory, "--cpus", d.Limits.CPUs, "--pids-limit", strconv.Itoa(d.Limits.Pids)}
}

// validate rejects a spec before any docker call is made.
func (s RunSpec) validate() error {
	if s.PublishPort > 0 && s.ContainerPort <= 0 {
		return errors.New("PublishPort needs a ContainerPort")
	}
	if err := validateEnv(s.Env); err != nil {
		return err
	}
	if s.Volume != "" {
		if err := checkContainerPath("volume target", s.VolumeTarget); err != nil {
			return err
		}
	}
	for _, p := range s.Tmpfs {
		if err := checkContainerPath("tmpfs path", p); err != nil {
			return err
		}
	}
	return nil
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateEnv keeps the env file well-formed: one KEY=VALUE per line. Errors
// name the key only; values can be secrets.
func validateEnv(env map[string]string) error {
	for _, k := range sortedKeys(env) {
		if !envKeyRE.MatchString(k) {
			return fmt.Errorf("env var %q: name must match %s", k, envKeyRE)
		}
		v := env[k]
		if strings.ContainsAny(v, "\n\r") || !utf8.ValidString(v) {
			return fmt.Errorf("env var %q: value must be valid UTF-8 without line breaks", k)
		}
	}
	return nil
}

// checkContainerPath requires an absolute path without ':' (which docker
// would read as a mount-option separator).
func checkContainerPath(what, p string) error {
	if !path.IsAbs(p) || strings.Contains(p, ":") {
		return fmt.Errorf("%s %q must be an absolute path without ':'", what, p)
	}
	return nil
}

func (d Docker) runArgs(s RunSpec, envFile string) []string {
	args := []string{"run", "-d", "--name", s.Name, "--network", s.Network}
	for _, a := range s.Aliases {
		args = append(args, "--network-alias", a)
	}
	args = append(args, d.limitArgs()...)
	args = append(args, "--restart", "unless-stopped")
	for _, k := range sortedKeys(s.Labels) {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	if s.PublishPort > 0 {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", s.PublishPort, s.ContainerPort))
	}
	if s.Volume != "" {
		args = append(args, "-v", s.Volume+":"+s.VolumeTarget)
	}
	for _, p := range s.Tmpfs {
		args = append(args, "--tmpfs", p)
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, s.Image)
	return append(args, s.Cmd...)
}

// writeEnvFile writes KEY=VALUE lines to a 0600 temp file, so values never
// appear in the docker command line. It returns "" when env is empty.
func writeEnvFile(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		b.WriteString(k + "=" + env[k] + "\n") // validated by validateEnv
	}
	f, err := os.CreateTemp("", "cp-env-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Remove force-removes a container; a missing container is not an error.
func (d Docker) Remove(ctx context.Context, name string) error {
	_, err := d.R.Run(ctx, "", "docker", "rm", "-f", name)
	if err != nil && !errContains(err, "no such container") {
		return err
	}
	return nil
}

// EnsureNetwork creates the network if needed and attaches nginx to it.
func (d Docker) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := d.R.Run(ctx, "", "docker", "network", "inspect", name); err != nil {
		_, err := d.R.Run(ctx, "", "docker", "network", "create", "--driver", "bridge", "--label", "cp.managed=true", name)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	if d.Nginx == "" {
		return nil
	}
	_, err := d.R.Run(ctx, "", "docker", "network", "connect", name, d.Nginx)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

func (d Docker) VolumeExists(ctx context.Context, name string) bool {
	_, err := d.R.Run(ctx, "", "docker", "volume", "inspect", name)
	return err == nil
}

func (d Docker) CreateVolume(ctx context.Context, name string) error {
	_, err := d.R.Run(ctx, "", "docker", "volume", "create", "--label", "cp.managed=true", name)
	return err
}

// CopyIntoVolume copies hostDir into the volume once. A marker file makes
// repeated calls (activity retries, later deploys) no-ops.
func (d Docker) CopyIntoVolume(ctx context.Context, hostDir, volume string) error {
	args := append([]string{"run", "--rm", "--network", "none"}, d.limitArgs()...)
	args = append(args, "-v", hostDir+":/from:ro", "-v", volume+":/to", d.ProbeImage,
		"sh", "-c", "[ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)")
	_, err := d.R.Run(ctx, "", "docker", args...)
	return err
}

// Probe checks that host:port accepts TCP connections from inside network.
func (d Docker) Probe(ctx context.Context, network, host string, port int) error {
	args := append([]string{"run", "--rm", "--network", network}, d.limitArgs()...)
	args = append(args, d.ProbeImage, "nc", "-z", "-w", "2", host, strconv.Itoa(port))
	_, err := d.R.Run(ctx, "", "docker", args...)
	return err
}

// IsRunning reports whether the container exists and is running.
func (d Docker) IsRunning(ctx context.Context, id string) (bool, error) {
	out, err := d.R.Run(ctx, "", "docker", "inspect", "-f", "{{.State.Running}}", id)
	if err != nil {
		if errContains(err, "no such object") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// errContains reports whether err's message contains substr, ignoring case.
// Docker's wording changes case between versions ("No such object" before,
// "no such object" in Docker 29).
func errContains(err error, substr string) bool {
	return strings.Contains(strings.ToLower(err.Error()), strings.ToLower(substr))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
