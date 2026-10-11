package runtime_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/runtime"
	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// T23 fix round 1 (VER-3): real-Docker checks of the exact capability sets and
// flags the deploy activities use. Same gating as the Task 16 test in
// docker_integration_test.go: skipped under -short and without a daemon.
// This file is an external test package so it can use workflows.Managed and
// workflows.TenantCaps directly instead of copies that could drift
// (workflows imports runtime, so package runtime cannot import it).

type realDocker struct {
	runtime.Docker
	ctx    context.Context
	suffix string
}

func newRealDocker(t *testing.T) realDocker {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
	return realDocker{
		Docker: runtime.Docker{R: runtime.ExecRunner{}, Limits: runtime.Limits{Memory: "512m", CPUs: "1", Pids: 512}, ProbeImage: "busybox:1.36"},
		ctx:    ctx,
		suffix: fmt.Sprintf("%d", time.Now().UnixNano()),
	}
}

// ensureImage pulls only when the image is not already local, so the test never
// re-downloads (disk) an image that is present.
func (d realDocker) ensureImage(t *testing.T, image string) {
	t.Helper()
	if exec.CommandContext(d.ctx, "docker", "image", "inspect", image).Run() == nil {
		return
	}
	if err := d.Pull(d.ctx, image); err != nil {
		t.Fatal(err)
	}
}

// cleanup removes exactly the named resources, never by label.
func (d realDocker) cleanup(t *testing.T, containers []string, volume, network string) {
	t.Helper()
	t.Cleanup(func() {
		for _, c := range containers {
			_ = exec.Command("docker", "rm", "-f", "-v", c).Run()
		}
		if volume != "" {
			if out, err := exec.Command("docker", "volume", "rm", "-f", volume).CombinedOutput(); err != nil {
				t.Logf("cleanup volume %s: %v: %s", volume, err, out)
			}
		}
		if network != "" {
			if out, err := exec.Command("docker", "network", "rm", network).CombinedOutput(); err != nil {
				t.Logf("cleanup network %s: %v: %s", network, err, out)
			}
		}
	})
}

func (d realDocker) waitProbe(t *testing.T, network, host string, port int) {
	t.Helper()
	var err error
	for range 40 {
		if err = d.Probe(d.ctx, network, host, port); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("probe %s:%d: %v", host, port, err)
}

// managedSpec builds the spec RunContainer builds for a managed service.
func managedSpec(typ, name, network, volume string, env map[string]string) runtime.RunSpec {
	m := workflows.Managed[typ]
	e := map[string]string{}
	for k, v := range m.Env {
		e[k] = v
	}
	for k, v := range env {
		e[k] = v
	}
	return runtime.RunSpec{
		Image: m.Image, Name: name, Network: network, ContainerPort: m.Port, Volume: volume,
		VolumeTarget: m.DataDir, Tmpfs: m.Tmpfs, CapAdd: m.CapAdd, Cmd: m.Cmd, Env: e,
	}
}

// VER-1: a Mongo redeploy on an existing volume used to crash-loop with
// "find: '/data/db/journal': Permission denied" under the old cap set.
func TestT23MongoRedeployOnExistingVolumeStaysUp(t *testing.T) {
	d := newRealDocker(t)
	network, volume := "t23-mongo-net-"+d.suffix, "t23-mongo-vol-"+d.suffix
	first, second := "t23-mongo-a-"+d.suffix, "t23-mongo-b-"+d.suffix
	d.cleanup(t, []string{first, second}, volume, network)
	d.ensureImage(t, "mongo:7")
	if err := d.EnsureNetwork(d.ctx, network); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateVolume(d.ctx, volume); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"MONGO_INITDB_ROOT_PASSWORD": "t23-" + d.suffix}

	if _, err := d.Run(d.ctx, managedSpec(store.TypeMongoDB, first, network, volume, env)); err != nil {
		t.Fatal(err)
	}
	d.waitProbe(t, network, first, 27017)
	if err := d.Remove(d.ctx, first); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Run(d.ctx, managedSpec(store.TypeMongoDB, second, network, volume, env)); err != nil {
		t.Fatal(err)
	}
	d.waitProbe(t, network, second, 27017)
	time.Sleep(15 * time.Second) // a crash loop shows up within this window
	running, err := d.IsRunning(d.ctx, second)
	if err != nil || !running {
		t.Fatalf("redeploy not running after 15s: running=%v err=%v", running, err)
	}
	if err := d.Probe(d.ctx, network, second, 27017); err != nil {
		t.Fatalf("redeploy stopped accepting connections: %v", err)
	}
}

// VER-2: stock nginx:alpine (the production stage of a generated React
// Dockerfile) needs TenantCaps under --cap-drop ALL, and fails without.
func TestT23StaticSiteNeedsTenantCaps(t *testing.T) {
	d := newRealDocker(t)
	network := "t23-static-net-" + d.suffix
	with, without := "t23-static-caps-"+d.suffix, "t23-static-nocaps-"+d.suffix
	d.cleanup(t, []string{with, without}, "", network)
	d.ensureImage(t, "nginx:alpine")
	if err := d.EnsureNetwork(d.ctx, network); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Run(d.ctx, runtime.RunSpec{
		Image: "nginx:alpine", Name: with, Network: network, ContainerPort: 80, CapAdd: workflows.TenantCaps,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	if running, err := d.IsRunning(d.ctx, with); err != nil || !running {
		t.Fatalf("with caps: running=%v err=%v", running, err)
	}
	if err := d.Probe(d.ctx, network, with, 80); err != nil {
		t.Fatalf("with caps: probe: %v", err)
	}

	// Negative control: the same image without the caps never serves.
	if _, err := d.Run(d.ctx, runtime.RunSpec{Image: "nginx:alpine", Name: without, Network: network, ContainerPort: 80}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	for range 3 {
		if err := d.Probe(d.ctx, network, without, 80); err == nil {
			t.Fatal("without caps: nginx served on port 80; the cap set would not be needed")
		}
		time.Sleep(time.Second)
	}
	logs, _ := exec.CommandContext(d.ctx, "docker", "logs", without).CombinedOutput()
	if !strings.Contains(string(logs), "Operation not permitted") {
		t.Logf("without caps: unexpected log tail: %s", logs)
	}
}

// CTO 2026-10-11: a docker-type service with its own Dockerfile on stock
// nginx (root, chown/setuid entrypoint) needs TenantCaps and fails without.
// Uses the local nginx:alpine (the only nginx image kept on the dev box).
func TestT23DockerTypeNginxNeedsTenantCaps(t *testing.T) {
	d := newRealDocker(t)
	network := "t23-dockertype-net-" + d.suffix
	with, without := "t23-dockertype-caps-"+d.suffix, "t23-dockertype-nocaps-"+d.suffix
	d.cleanup(t, []string{with, without}, "", network)
	d.ensureImage(t, "nginx:alpine")
	if err := d.EnsureNetwork(d.ctx, network); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Run(d.ctx, runtime.RunSpec{
		Image: "nginx:alpine", Name: with, Network: network, ContainerPort: 80, CapAdd: workflows.TenantCaps,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	if running, err := d.IsRunning(d.ctx, with); err != nil || !running {
		t.Fatalf("with caps: running=%v err=%v", running, err)
	}
	if err := d.Probe(d.ctx, network, with, 80); err != nil {
		t.Fatalf("with caps: probe: %v", err)
	}

	if _, err := d.Run(d.ctx, runtime.RunSpec{Image: "nginx:alpine", Name: without, Network: network, ContainerPort: 80}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	if running, _ := d.IsRunning(d.ctx, without); running {
		if err := d.Probe(d.ctx, network, without, 80); err == nil {
			t.Fatal("without caps: nginx served on port 80; the cap set would not be needed")
		}
	}
}

// STAFF-1/SEC-2: a Node-era dump.rdb (0600, owned by another uid) copied by
// CopyIntoVolume must be loaded by the managed Redis spec.
func TestT23RedisLoadsSeededDump(t *testing.T) {
	d := newRealDocker(t)
	network, volume := "t23-redis-net-"+d.suffix, "t23-redis-vol-"+d.suffix
	name, seeder := "t23-redis-"+d.suffix, "t23-redis-seed-"+d.suffix
	d.cleanup(t, []string{name, seeder}, volume, network)
	d.ensureImage(t, "redis:7-alpine")

	// Build the dump with stock redis, then make it look like Node's: uid 999, 0600.
	host, err := os.MkdirTemp("", "t23-redis-seed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "run", "--rm", "-v", host+":/d", "busybox:1.36", "rm", "-rf", "/d/dump.rdb").Run()
		_ = os.RemoveAll(host)
	})
	const script = "redis-server --daemonize yes --save '' --dir /data >/dev/null && sleep 1 && " +
		"redis-cli set t23key t23value >/dev/null && redis-cli save >/dev/null && redis-cli shutdown nosave; " +
		"chown 999:999 /data/dump.rdb && chmod 600 /data/dump.rdb && chmod 700 /data"
	if out, err := exec.CommandContext(d.ctx, "docker", "run", "--rm", "--name", seeder, "-v", host+":/data",
		"redis:7-alpine", "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("seed dump: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(host, "dump.rdb")); err != nil {
		t.Fatalf("seed dump missing: %v", err)
	}

	if err := d.EnsureNetwork(d.ctx, network); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateVolume(d.ctx, volume); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyIntoVolume(d.ctx, host, volume); err != nil {
		t.Fatalf("CopyIntoVolume with the helper cap set: %v", err)
	}

	password := "t23-" + d.suffix
	if _, err := d.Run(d.ctx, managedSpec(store.TypeRedis, name, network, volume, map[string]string{"REDIS_PASSWORD": password})); err != nil {
		t.Fatal(err)
	}
	d.waitProbe(t, network, name, 6379)
	run := runtime.ExecRunner{}
	size, err := run.Run(d.ctx, "", "docker", "exec", name, "redis-cli", "-a", password, "--no-auth-warning", "DBSIZE")
	if err != nil || strings.TrimSpace(size) == "0" || strings.TrimSpace(size) == "" {
		t.Fatalf("DBSIZE=%q err=%v: the seeded dump was not loaded", size, err)
	}
	val, err := run.Run(d.ctx, "", "docker", "exec", name, "redis-cli", "-a", password, "--no-auth-warning", "GET", "t23key")
	if err != nil || strings.TrimSpace(val) != "t23value" {
		t.Fatalf("GET t23key=%q err=%v", val, err)
	}
}
