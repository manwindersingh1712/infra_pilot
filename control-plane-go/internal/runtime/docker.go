package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	CapAdd        []string // capabilities added back after --cap-drop ALL (managed images that drop privileges with gosu)
	Tmpfs         []string // absolute paths mounted as tmpfs (e.g. mongo's /data/configdb, so no anonymous volume is created)
}

func (d Docker) Pull(ctx context.Context, image string) error {
	if err := checkImage(image); err != nil {
		return err
	}
	_, err := d.R.Run(ctx, "", "docker", "pull", image)
	return err
}

// DetectContainerPort returns the lowest port the image EXPOSEs.
func (d Docker) DetectContainerPort(ctx context.Context, image string) (int, error) {
	if err := checkImage(image); err != nil {
		return 0, err
	}
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

// ErrInvalidSpec marks a RunSpec that can never succeed (bad names, env keys or
// values, paths). Retrying will not help, so activities map it to a
// non-retryable error.
var ErrInvalidSpec = errors.New("invalid run spec")

// validate rejects a spec before any docker call is made. Errors wrap
// ErrInvalidSpec and name keys, never values.
func (s RunSpec) validate() error {
	if err := s.check(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSpec, err)
	}
	return nil
}

func (s RunSpec) check() error {
	if err := checkImage(s.Image); err != nil {
		return err
	}
	if err := checkName("container", s.Name); err != nil {
		return err
	}
	if err := checkName("network", s.Network); err != nil {
		return err
	}
	for _, a := range s.Aliases {
		if err := checkName("alias", a); err != nil {
			return err
		}
	}
	if s.PublishPort > 0 && s.ContainerPort <= 0 {
		return errors.New("PublishPort needs a ContainerPort")
	}
	if err := validateEnv(s.Env); err != nil {
		return err
	}
	if s.Volume != "" {
		if err := checkName("volume", s.Volume); err != nil {
			return err
		}
		if err := checkContainerPath("volume target", s.VolumeTarget); err != nil {
			return err
		}
	}
	for _, p := range s.Tmpfs {
		if err := checkContainerPath("tmpfs path", p); err != nil {
			return err
		}
	}
	for _, c := range s.CapAdd {
		if !allowedCaps[c] {
			return fmt.Errorf("capability %q is not allowed", c)
		}
	}
	return nil
}

// allowedCaps is the union of the managed-database and static-site sets
// (spec section 5); anything else is refused before docker is called.
var allowedCaps = map[string]bool{
	"CHOWN": true, "SETUID": true, "SETGID": true,
	"DAC_OVERRIDE": true, "DAC_READ_SEARCH": true, "NET_BIND_SERVICE": true,
}

// envKeyRE and maxEnvKeyLen follow what `docker run --env-file` accepts (dash,
// dot and a leading digit included); Node passed such keys as `-e KEY=VALUE`,
// so existing services use them. The charset still excludes `=`, `#`,
// whitespace, NUL and newlines, so a key cannot start a comment, split a line
// or smuggle a second variable.
var envKeyRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

const maxEnvKeyLen = 256

// ValidEnvKey is the one rule for an env var name: the API checks it on write
// and validateEnv checks it again before the worker writes the env file.
func ValidEnvKey(k string) bool { return len(k) <= maxEnvKeyLen && envKeyRE.MatchString(k) }

// validateEnv keeps the env file well-formed: one KEY=VALUE per line. Errors
// name the key only; values can be secrets.
func validateEnv(env map[string]string) error {
	for _, k := range sortedKeys(env) {
		if !ValidEnvKey(k) {
			return fmt.Errorf("env var %q: name must match %s and be at most %d bytes", k, envKeyRE, maxEnvKeyLen)
		}
		v := env[k]
		if strings.ContainsAny(v, "\n\r") || !utf8.ValidString(v) {
			return fmt.Errorf("env var %q: value must be valid UTF-8 without line breaks", k)
		}
	}
	return nil
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// checkName guards container, network, volume and alias names (SEC-5):
// a name docker could read as a flag or a host path is rejected.
func checkName(what, n string) error {
	if !nameRE.MatchString(n) {
		return fmt.Errorf("invalid %s name %q", what, n)
	}
	return nil
}

func checkImage(img string) error {
	if img == "" || strings.HasPrefix(img, "-") {
		return fmt.Errorf("invalid image %q", img)
	}
	return nil
}

// checkHostDir requires an absolute host path without ':' (a mount separator).
func checkHostDir(dir string) error {
	if !filepath.IsAbs(dir) || strings.Contains(dir, ":") {
		return fmt.Errorf("host dir %q must be an absolute path without ':'", dir)
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
	// Tenant code runs here: no capabilities beyond the explicit CapAdd, and no
	// privilege gain through setuid binaries (T21 requirement).
	args = append(args, "--cap-drop", "ALL")
	for _, c := range s.CapAdd {
		args = append(args, "--cap-add", c)
	}
	args = append(args, "--security-opt", "no-new-privileges")
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
	if err := checkName("container", name); err != nil {
		return err
	}
	_, err := d.R.Run(ctx, "", "docker", "rm", "-f", name)
	if err != nil && !errContains(err, "no such container") {
		return err
	}
	return nil
}

// EnsureNetwork creates the network if needed and attaches nginx to it.
func (d Docker) EnsureNetwork(ctx context.Context, name string) error {
	if err := checkName("network", name); err != nil {
		return err
	}
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
	if checkName("volume", name) != nil {
		return false
	}
	_, err := d.R.Run(ctx, "", "docker", "volume", "inspect", name)
	return err == nil
}

func (d Docker) CreateVolume(ctx context.Context, name string) error {
	if err := checkName("volume", name); err != nil {
		return err
	}
	_, err := d.R.Run(ctx, "", "docker", "volume", "create", "--label", "cp.managed=true", name)
	return err
}

// CopyIntoVolume copies hostDir into the volume once. A marker file makes
// repeated calls (activity retries, later deploys) no-ops.
func (d Docker) CopyIntoVolume(ctx context.Context, hostDir, volume string) error {
	if err := checkHostDir(hostDir); err != nil {
		return err
	}
	if err := checkName("volume", volume); err != nil {
		return err
	}
	// Root inside the helper must read Node-written 0600/0700 files owned by
	// other uids and preserve their ownership; nothing else is added back.
	args := append([]string{"run", "--rm", "--network", "none"}, d.limitArgs()...)
	args = append(args, "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE",
		"--cap-add", "DAC_READ_SEARCH", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges",
		"-v", hostDir+":/from:ro", "-v", volume+":/to", d.ProbeImage,
		"sh", "-c", "[ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)")
	_, err := d.R.Run(ctx, "", "docker", args...)
	return err
}

// Probe checks that host:port accepts TCP connections from inside network.
func (d Docker) Probe(ctx context.Context, network, host string, port int) error {
	if err := checkName("network", network); err != nil {
		return err
	}
	if err := checkName("host", host); err != nil {
		return err
	}
	args := append([]string{"run", "--rm", "--network", network}, d.limitArgs()...)
	args = append(args, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", d.ProbeImage, "nc", "-z", "-w", "2", host, strconv.Itoa(port))
	_, err := d.R.Run(ctx, "", "docker", args...)
	return err
}

// IsRunning reports whether the container exists and is running.
func (d Docker) IsRunning(ctx context.Context, id string) (bool, error) {
	if err := checkName("container", id); err != nil {
		return false, err
	}
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

// NewLimits builds Limits and rejects values docker would read as "no limit"
// or fail on late: an empty memory or CPU setting, or Pids below 1
// (--pids-limit 0 means unlimited). A zero-value Limits struct has the same
// problem, so production code constructs it here.
func NewLimits(memory, cpus string, pids int) (Limits, error) {
	if strings.TrimSpace(memory) == "" {
		return Limits{}, errors.New("runtime: container memory limit is empty")
	}
	if strings.TrimSpace(cpus) == "" {
		return Limits{}, errors.New("runtime: container CPU limit is empty")
	}
	if pids < 1 {
		return Limits{}, fmt.Errorf("runtime: container pids limit must be at least 1, got %d", pids)
	}
	return Limits{Memory: memory, CPUs: cpus, Pids: pids}, nil
}

// Validate runs the same checks Run performs before any docker call, so callers
// can fail fast before slow steps such as an image pull. Errors wrap
// ErrInvalidSpec and name keys, never values.
func (s RunSpec) Validate() error { return s.validate() }

// RemoveVolume deletes the data volume cp-vol-<serviceId>; a missing volume is
// not an error. Any other name is refused without running docker.
func (d Docker) RemoveVolume(ctx context.Context, name string) error {
	// Only a service data volume (cp-vol-<serviceId>) may be removed here, so a
	// bug elsewhere can never name another volume (cp_pgdata, a registry volume).
	id, ok := strings.CutPrefix(name, "cp-vol-")
	if !ok || !ValidServiceID(id) {
		return fmt.Errorf("refusing to remove volume %q: not a service data volume", name)
	}
	if err := checkName("volume", name); err != nil {
		return err
	}
	_, err := d.R.Run(ctx, "", "docker", "volume", "rm", "-f", name)
	if err != nil && !errContains(err, "no such volume") {
		return err
	}
	return nil
}

var containerIDRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// RemoveServiceContainers force-removes every container (running or not)
// carrying the label cp.serviceId=<serviceID>. That label is set only on
// containers this worker created, so nothing else on the host is touched.
// It catches containers that no Deployment row points at (a deploy cancelled
// after the container started). Idempotent.
func (d Docker) RemoveServiceContainers(ctx context.Context, serviceID string) error {
	if !ValidServiceID(serviceID) {
		return fmt.Errorf("invalid service id %q", serviceID)
	}
	out, err := d.R.Run(ctx, "", "docker", "ps", "-a", "-q", "--filter", "label=cp.serviceId="+serviceID)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(out) {
		if !containerIDRE.MatchString(id) {
			return fmt.Errorf("docker ps returned an unexpected container id %q", id)
		}
		if _, err := d.R.Run(ctx, "", "docker", "rm", "-f", id); err != nil && !errContains(err, "no such container") {
			return err
		}
	}
	return nil
}
