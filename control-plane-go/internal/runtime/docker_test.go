package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

func newDocker(r Runner) Docker {
	return Docker{R: r, Limits: Limits{Memory: "512m", CPUs: "0.5", Pids: 256}, Nginx: "cp_nginx", ProbeImage: "busybox:1.36"}
}

func TestParseExposedPortsPicksLowest(t *testing.T) {
	p, err := parseExposedPorts(`{"8080/tcp":{},"3000/tcp":{},"9229/udp":{}}` + "\n")
	if err != nil || p != 3000 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	for _, raw := range []string{"", "null\n", "{}"} {
		if _, err := parseExposedPorts(raw); err == nil {
			t.Errorf("parseExposedPorts(%q) should fail", raw)
		}
	}
}

func TestRunBuildsIsolatedLimitedContainer(t *testing.T) {
	var envFile string
	var envContent string
	var envMode os.FileMode
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "rm" {
			return "", errors.New("Error: No such container: cp-d1")
		}
		for i, a := range c.Args {
			if a == "--env-file" {
				envFile = c.Args[i+1]
				b, err := os.ReadFile(envFile)
				if err != nil {
					return "", err
				}
				envContent = string(b)
				info, err := os.Stat(envFile)
				if err != nil {
					return "", err
				}
				envMode = info.Mode().Perm()
			}
		}
		return "abc123\n", nil
	}}
	id, err := newDocker(r).Run(context.Background(), RunSpec{
		Image: "redis:7-alpine", Name: "cp-d1", Network: "cp-proj-p1", Aliases: []string{"svc-s1"},
		Labels:        map[string]string{"cp.serviceId": "s1", "cp.deploymentId": "d1"},
		Env:           map[string]string{"B": "2", "A": "1"},
		Cmd:           []string{"sh", "-c", "exec redis-server"},
		ContainerPort: 6379, PublishPort: 20000, Volume: "cp-vol-s1", VolumeTarget: "/data",
	})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	cmds := r.Commands()
	want := "docker run -d --name cp-d1 --network cp-proj-p1 --network-alias svc-s1 " +
		"--memory 512m --cpus 0.5 --pids-limit 256 --cap-drop ALL --security-opt no-new-privileges --restart unless-stopped " +
		"--label cp.deploymentId=d1 --label cp.serviceId=s1 " +
		"-p 127.0.0.1:20000:6379 -v cp-vol-s1:/data --env-file " + envFile + " " +
		"redis:7-alpine sh -c exec redis-server"
	if cmds[0] != "docker rm -f cp-d1" || cmds[1] != want {
		t.Fatalf("cmds:\n%s\nwant:\n%s", strings.Join(cmds, "\n"), want)
	}
	if envContent != "A=1\nB=2\n" || envMode != 0o600 {
		t.Fatalf("env file %q mode %v", envContent, envMode)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatal("env file must be deleted after docker run")
	}
}

func TestRunWithoutPortOrVolumeOrEnv(t *testing.T) {
	r := &testutil.FakeRunner{}
	if _, err := newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d2", Network: "n", ContainerPort: 3000}); err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[1]
	if strings.Contains(got, " -p ") || strings.Contains(got, " -v ") || strings.Contains(got, "--env-file") {
		t.Fatalf("unexpected flags: %s", got)
	}
}

func TestRunRejectsMultilineEnv(t *testing.T) {
	_, err := newDocker(&testutil.FakeRunner{}).Run(context.Background(), RunSpec{
		Image: "img", Name: "n", Network: "n", Env: map[string]string{"X": "a\nb"},
	})
	if err == nil {
		t.Fatal("newline in env value must be rejected")
	}
}

func TestRemoveIgnoresMissingContainer(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) {
		return "", errors.New("Error response from daemon: No such container: cp-x")
	}}
	if err := newDocker(r).Remove(context.Background(), "cp-x"); err != nil {
		t.Fatal(err)
	}
	failing := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("daemon down") }}
	if err := newDocker(failing).Remove(context.Background(), "cp-x"); err == nil {
		t.Fatal("other errors must be returned")
	}
}

func TestEnsureNetworkCreatesAndAttachesNginx(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch c.Args[1] {
		case "inspect":
			return "", errors.New("Error: No such network: cp-proj-p1")
		case "connect":
			return "", errors.New("endpoint with name cp_nginx already exists in network cp-proj-p1")
		}
		return "", nil
	}}
	if err := newDocker(r).EnsureNetwork(context.Background(), "cp-proj-p1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker network inspect cp-proj-p1",
		"docker network create --driver bridge --label cp.managed=true cp-proj-p1",
		"docker network connect cp-proj-p1 cp_nginx",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestVolumesProbeAndInspect(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "inspect" {
			return "true\n", nil
		}
		if c.Args[0] == "volume" && c.Args[1] == "inspect" {
			return "", errors.New("no such volume")
		}
		return "", nil
	}}
	d := newDocker(r)
	ctx := context.Background()
	if d.VolumeExists(ctx, "cp-vol-s1") {
		t.Fatal("volume should not exist")
	}
	for _, err := range []error{
		d.CreateVolume(ctx, "cp-vol-s1"),
		d.CopyIntoVolume(ctx, "/tmp/cp-volumes/s1", "cp-vol-s1"),
		d.Probe(ctx, "cp-proj-p1", "cp-d1", 3000),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	running, err := d.IsRunning(ctx, "abc")
	if err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	want := []string{
		"docker volume inspect cp-vol-s1",
		"docker volume create --label cp.managed=true cp-vol-s1",
		"docker run --rm --network none --memory 512m --cpus 0.5 --pids-limit 256 -v /tmp/cp-volumes/s1:/from:ro -v cp-vol-s1:/to busybox:1.36 sh -c [ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)",
		"docker run --rm --network cp-proj-p1 --memory 512m --cpus 0.5 --pids-limit 256 busybox:1.36 nc -z -w 2 cp-d1 3000",
		"docker inspect -f {{.State.Running}} abc",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestDetectContainerPortAndPull(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return `{"8080/tcp":{}}`, nil }}
	d := newDocker(r)
	if p, err := d.DetectContainerPort(context.Background(), "img"); err != nil || p != 8080 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	if err := d.Pull(context.Background(), "img"); err != nil {
		t.Fatal(err)
	}
	cmds := r.Commands()
	if cmds[0] != "docker image inspect img --format {{json .Config.ExposedPorts}}" || cmds[1] != "docker pull img" {
		t.Fatalf("cmds = %v", cmds)
	}
}

// VER-1 (T16): Docker 29 prints "Error: no such object: <id>" (lower case).
func TestIsRunningTreatsMissingContainerAsNotRunning(t *testing.T) {
	for _, msg := range []string{"Error: No such object: abc", "error: no such object: abc"} {
		r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New(msg) }}
		running, err := newDocker(r).IsRunning(context.Background(), "abc")
		if err != nil || running {
			t.Errorf("%q: running=%v err=%v", msg, running, err)
		}
	}
	failing := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("daemon down") }}
	if _, err := newDocker(failing).IsRunning(context.Background(), "abc"); err == nil {
		t.Fatal("other errors must be returned")
	}
}

// STAFF-1 (T16): the volume mounts at the image's data dir (mongo: /data/db),
// and extra paths (mongo: /data/configdb) get tmpfs so no anonymous volume is made.
func TestRunMountsVolumeAtTargetAndTmpfs(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, err := newDocker(r).Run(context.Background(), RunSpec{
		Image: "mongo:7", Name: "cp-d3", Network: "n", ContainerPort: 27017,
		Volume: "cp-vol-s3", VolumeTarget: "/data/db", Tmpfs: []string{"/data/configdb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[1]
	if !strings.Contains(got, " -v cp-vol-s3:/data/db --tmpfs /data/configdb mongo:7") {
		t.Fatalf("got: %s", got)
	}
}

func TestRunRejectsVolumeWithoutTarget(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, err := newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d4", Network: "n", Volume: "cp-vol-s4"})
	if err == nil {
		t.Fatal("a volume without VolumeTarget must be rejected")
	}
	if len(r.Commands()) != 0 {
		t.Fatalf("no docker call expected, got %v", r.Commands())
	}
}

// STAFF-6 (T16): publishing needs a container port to map to.
func TestRunRejectsPublishWithoutContainerPort(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, err := newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d5", Network: "n", PublishPort: 20000})
	if err == nil || len(r.Commands()) != 0 {
		t.Fatalf("err=%v cmds=%v: want an error and no docker call", err, r.Commands())
	}
}

// STAFF-5 / SEC-2 (T16): env keys are shell-style identifiers; values cannot
// hold line breaks or invalid UTF-8; errors name the key, never the value.
func TestRunValidatesEnv(t *testing.T) {
	bad := []map[string]string{
		{"1X": "v"}, {"A-B": "v"}, {"A=B": "v"}, {"": "v"}, {"A B": "v"}, {"X\n": "v"},
		{"X": "a\nsecret"}, {"X": "a\rsecret"}, {"X": "secret\xff"},
	}
	for _, env := range bad {
		r := &testutil.FakeRunner{}
		_, err := newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d6", Network: "n", Env: env})
		if err == nil {
			t.Errorf("env %q must be rejected", env)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("error leaks the value: %v", err)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("env %q: no docker call expected, got %v", env, r.Commands())
		}
	}
	r := &testutil.FakeRunner{}
	ok := map[string]string{"_A1": "x=y z", "PATH_2": "", "u": "héllo"}
	if _, err := newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d6", Network: "n", Env: ok}); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
}

// SEC-5 (T16): names, images and host paths that docker could read as flags
// or mount options are rejected before any docker call.
func TestRejectsUnsafeArgumentsBeforeDocker(t *testing.T) {
	ctx := context.Background()
	ok := RunSpec{Image: "img", Name: "cp-d1", Network: "cp-proj-p1", ContainerPort: 3000}
	spec := func(f func(*RunSpec)) RunSpec { s := ok; f(&s); return s }
	cases := map[string]func(Docker) error{
		"run name": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Name = "--privileged" }))
			return err
		},
		"run empty name": func(d Docker) error { _, err := d.Run(ctx, spec(func(s *RunSpec) { s.Name = "" })); return err },
		"run network": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Network = "host x" }))
			return err
		},
		"run alias": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Aliases = []string{"-a"} }))
			return err
		},
		"run volume": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Volume, s.VolumeTarget = "/etc", "/data" }))
			return err
		},
		"run volume tgt": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Volume, s.VolumeTarget = "v1", "data" }))
			return err
		},
		"run tmpfs": func(d Docker) error {
			_, err := d.Run(ctx, spec(func(s *RunSpec) { s.Tmpfs = []string{"/x:exec"} }))
			return err
		},
		"run image":       func(d Docker) error { _, err := d.Run(ctx, spec(func(s *RunSpec) { s.Image = "-v" })); return err },
		"run empty image": func(d Docker) error { _, err := d.Run(ctx, spec(func(s *RunSpec) { s.Image = "" })); return err },
		"pull":            func(d Docker) error { return d.Pull(ctx, "--all-tags") },
		"detect port":     func(d Docker) error { _, err := d.DetectContainerPort(ctx, "-f"); return err },
		"remove":          func(d Docker) error { return d.Remove(ctx, "-v") },
		"network":         func(d Docker) error { return d.EnsureNetwork(ctx, "--internal") },
		"create volume":   func(d Docker) error { return d.CreateVolume(ctx, "a/b") },
		"copy volume":     func(d Docker) error { return d.CopyIntoVolume(ctx, "/tmp/x", "-v") },
		"copy relative":   func(d Docker) error { return d.CopyIntoVolume(ctx, "tmp/x", "cp-vol-s1") },
		"copy colon":      func(d Docker) error { return d.CopyIntoVolume(ctx, "/tmp/x:/etc", "cp-vol-s1") },
		"probe network":   func(d Docker) error { return d.Probe(ctx, "-n", "cp-d1", 3000) },
		"probe host":      func(d Docker) error { return d.Probe(ctx, "cp-proj-p1", "--x", 3000) },
		"is running":      func(d Docker) error { _, err := d.IsRunning(ctx, "-f"); return err },
	}
	for name, call := range cases {
		r := &testutil.FakeRunner{}
		if err := call(newDocker(r)); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(r.Commands()) != 0 {
			t.Errorf("%s: no docker call expected, got %v", name, r.Commands())
		}
	}
	r := &testutil.FakeRunner{}
	if newDocker(r).VolumeExists(ctx, "-v") || len(r.Commands()) != 0 {
		t.Errorf("VolumeExists with an invalid name must be false without a docker call")
	}
}

// STAFF-3 (T16): branches the first tests missed.
func TestEnsureNetworkBranches(t *testing.T) {
	ctx := context.Background()
	// Network exists and nginx is unset: only the inspect runs.
	r := &testutil.FakeRunner{}
	d := newDocker(r)
	d.Nginx = ""
	if err := d.EnsureNetwork(ctx, "cp-proj-p1"); err != nil {
		t.Fatal(err)
	}
	if got := r.Commands(); len(got) != 1 || got[0] != "docker network inspect cp-proj-p1" {
		t.Fatalf("got %v", got)
	}
	// A concurrent create ("already exists") is fine.
	race := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch c.Args[1] {
		case "inspect":
			return "", errors.New("no such network")
		case "create":
			return "", errors.New("network with name cp-proj-p1 already exists")
		}
		return "", nil
	}}
	if err := newDocker(race).EnsureNetwork(ctx, "cp-proj-p1"); err != nil {
		t.Fatal(err)
	}
	// Other create and connect errors are returned.
	for _, failOn := range []string{"create", "connect"} {
		r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
			switch c.Args[1] {
			case "inspect":
				return "", errors.New("no such network")
			case failOn:
				return "", errors.New("daemon down")
			}
			return "", nil
		}}
		if err := newDocker(r).EnsureNetwork(ctx, "cp-proj-p1"); err == nil {
			t.Errorf("%s failure must be returned", failOn)
		}
	}
}

func TestRunReturnsRemoveAndRunErrors(t *testing.T) {
	spec := RunSpec{Image: "img", Name: "cp-d7", Network: "n", ContainerPort: 3000, Env: map[string]string{"A": "1"}}
	for _, failOn := range []string{"rm", "run"} {
		var envFile string
		r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
			for i, a := range c.Args {
				if a == "--env-file" {
					envFile = c.Args[i+1]
				}
			}
			if c.Args[0] == failOn {
				return "", errors.New("daemon down")
			}
			return "id\n", nil
		}}
		if _, err := newDocker(r).Run(context.Background(), spec); err == nil {
			t.Errorf("%s failure must be returned", failOn)
		}
		if failOn == "rm" && len(r.Commands()) != 1 {
			t.Errorf("docker run must not follow a failed rm: %v", r.Commands())
		}
		if envFile != "" {
			if _, err := os.Stat(envFile); !os.IsNotExist(err) {
				t.Errorf("env file left behind after %s failure", failOn)
			}
		}
	}
}

func TestDetectContainerPortErrors(t *testing.T) {
	ctx := context.Background()
	failing := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("no such image") }}
	if _, err := newDocker(failing).DetectContainerPort(ctx, "img"); err == nil {
		t.Fatal("inspect failure must be returned")
	}
	for _, raw := range []string{"not json", `{"http/tcp":{}}`} {
		r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return raw, nil }}
		if _, err := newDocker(r).DetectContainerPort(ctx, "img"); err == nil {
			t.Errorf("%q must fail", raw)
		}
	}
}

func TestVolumeExistsAndCopyErrors(t *testing.T) {
	ctx := context.Background()
	r := &testutil.FakeRunner{}
	if !newDocker(r).VolumeExists(ctx, "cp-vol-s1") {
		t.Fatal("inspect success means the volume exists")
	}
	failing := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("daemon down") }}
	d := newDocker(failing)
	for name, err := range map[string]error{
		"create": d.CreateVolume(ctx, "cp-vol-s1"),
		"copy":   d.CopyIntoVolume(ctx, "/tmp/cp-volumes/s1", "cp-vol-s1"),
		"probe":  d.Probe(ctx, "cp-proj-p1", "cp-d1", 3000),
		"pull":   d.Pull(ctx, "img"),
	} {
		if err == nil {
			t.Errorf("%s failure must be returned", name)
		}
	}
	notRunning := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "false\n", nil }}
	if running, err := newDocker(notRunning).IsRunning(ctx, "abc"); err != nil || running {
		t.Fatalf("running=%v err=%v", running, err)
	}
}

// T21 requirement: every container drops all capabilities and cannot gain
// privileges; only CapAdd entries come back.
func TestRunDropsCapabilitiesAndAddsOnlyRequested(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, err := newDocker(r).Run(context.Background(), RunSpec{
		Image: "redis:7-alpine", Name: "cp-d5", Network: "n", CapAdd: []string{"CHOWN", "SETUID"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Commands()[1]
	if !strings.Contains(got, "--cap-drop ALL --cap-add CHOWN --cap-add SETUID --security-opt no-new-privileges --restart") {
		t.Fatalf("got: %s", got)
	}
	for _, ok := range []string{"CHOWN", "SETUID", "SETGID", "DAC_OVERRIDE", "DAC_READ_SEARCH", "NET_BIND_SERVICE"} {
		if _, err := newDocker(&testutil.FakeRunner{}).Run(context.Background(), RunSpec{
			Image: "img", Name: "cp-d6", Network: "n", CapAdd: []string{ok},
		}); err != nil {
			t.Fatalf("allow-listed cap %s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"--privileged", "chown", "CHOWN x", "", "SYS_ADMIN", "ALL", "NET_ADMIN"} {
		fr := &testutil.FakeRunner{}
		_, err := newDocker(fr).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d7", Network: "n", CapAdd: []string{bad}})
		if !errors.Is(err, ErrInvalidSpec) || len(fr.Commands()) != 0 {
			t.Fatalf("cap %q: err=%v cmds=%v", bad, err, fr.Commands())
		}
	}
}

// STAFF-7 (T16r1): spec validation errors are a typed, non-retryable class.
func TestValidationErrorsWrapErrInvalidSpec(t *testing.T) {
	ctx := context.Background()
	for name, spec := range map[string]RunSpec{
		"env key":    {Image: "img", Name: "cp-d8", Network: "n", Env: map[string]string{"my-key": "v"}},
		"env value":  {Image: "img", Name: "cp-d8", Network: "n", Env: map[string]string{"K": "a\nb"}},
		"image":      {Image: "-x", Name: "cp-d8", Network: "n"},
		"name":       {Image: "img", Name: "--x", Network: "n"},
		"volume dir": {Image: "img", Name: "cp-d8", Network: "n", Volume: "v"},
	} {
		_, err := newDocker(&testutil.FakeRunner{}).Run(ctx, spec)
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v", name, err)
		}
		if strings.Contains(fmt.Sprint(err), "a\nb") {
			t.Errorf("%s: error leaks the value: %v", name, err)
		}
	}
}
