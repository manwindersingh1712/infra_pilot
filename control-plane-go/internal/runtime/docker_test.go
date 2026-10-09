package runtime

import (
	"context"
	"errors"
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
				b, _ := os.ReadFile(envFile)
				envContent = string(b)
				info, _ := os.Stat(envFile)
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
		"--memory 512m --cpus 0.5 --pids-limit 256 --restart unless-stopped " +
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
	_, _ = newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d2", Network: "n", ContainerPort: 3000})
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
	_ = d.CreateVolume(ctx, "cp-vol-s1")
	_ = d.CopyIntoVolume(ctx, "/tmp/cp-volumes/s1", "cp-vol-s1")
	_ = d.Probe(ctx, "cp-proj-p1", "cp-d1", 3000)
	running, err := d.IsRunning(ctx, "abc")
	if err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	want := []string{
		"docker volume inspect cp-vol-s1",
		"docker volume create --label cp.managed=true cp-vol-s1",
		"docker run --rm -v /tmp/cp-volumes/s1:/from:ro -v cp-vol-s1:/to busybox:1.36 sh -c [ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)",
		"docker run --rm --network cp-proj-p1 busybox:1.36 nc -z -w 2 cp-d1 3000",
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
	_ = d.Pull(context.Background(), "img")
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
