package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// STAFF-4 (T16): a real-Docker check of the managed volume layout. mongo:7
// declares VOLUME /data/db and /data/configdb; the deploy must mount our named
// volume at /data/db and tmpfs at /data/configdb so Docker creates no
// anonymous volumes (STAFF-1). Skipped under -short and without docker.
func TestDockerIntegrationMongoVolumeLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	name, network, volume := "cp-it-mongo-"+suffix, "cp-it-net-"+suffix, "cp-it-vol-"+suffix
	t.Cleanup(func() {
		// Best-effort cleanup; -v also drops anonymous volumes if a regression made any.
		for _, args := range [][]string{
			{"rm", "-f", "-v", name}, {"volume", "rm", "-f", volume}, {"network", "rm", network},
		} {
			if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
				t.Logf("cleanup docker %v: %v: %s", args, err, out)
			}
		}
	})

	d := Docker{R: ExecRunner{}, Limits: Limits{Memory: "512m", CPUs: "1", Pids: 512}, ProbeImage: "busybox:1.36"}
	if err := d.Pull(ctx, "mongo:7"); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureNetwork(ctx, network); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateVolume(ctx, volume); err != nil {
		t.Fatal(err)
	}
	if !d.VolumeExists(ctx, volume) {
		t.Fatal("volume should exist after CreateVolume")
	}
	id, err := d.Run(ctx, RunSpec{
		Image: "mongo:7", Name: name, Network: network, Aliases: []string{"svc-" + suffix},
		Labels: map[string]string{"cp.test": "integration"}, ContainerPort: 27017,
		Volume: volume, VolumeTarget: "/data/db", Tmpfs: []string{"/data/configdb"},
		CapAdd: []string{"CHOWN", "SETUID", "SETGID"}, // the mongo entrypoint chowns, then drops to mongodb with gosu
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := ExecRunner{}.Run(ctx, "", "docker", "inspect", "-f", "{{json .Mounts}}", id)
	if err != nil {
		t.Fatal(err)
	}
	var mounts []struct{ Type, Name, Destination string }
	if err := json.Unmarshal([]byte(out), &mounts); err != nil {
		t.Fatalf("parse mounts %q: %v", out, err)
	}
	var dataDB bool
	for _, m := range mounts {
		if m.Type == "volume" && m.Name != volume {
			t.Errorf("anonymous volume %s at %s: the mount layout is shadowed", m.Name, m.Destination)
		}
		if m.Destination == "/data/db" && m.Name == volume {
			dataDB = true
		}
	}
	if !dataDB {
		t.Fatalf("named volume not mounted at /data/db: %s", strings.TrimSpace(out))
	}

	// Probe until mongod listens (startup takes a few seconds).
	var probeErr error
	for range 30 {
		if probeErr = d.Probe(ctx, network, name, 27017); probeErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if probeErr != nil {
		t.Fatalf("probe: %v", probeErr)
	}
	if running, err := d.IsRunning(ctx, name); err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	if err := d.Remove(ctx, name); err != nil {
		t.Fatal(err)
	}
	// VER-1 on the real daemon: a missing container is "not running", not an error.
	if running, err := d.IsRunning(ctx, name); err != nil || running {
		t.Fatalf("after remove: running=%v err=%v", running, err)
	}
	if err := d.Remove(ctx, name); err != nil {
		t.Fatalf("removing a missing container: %v", err)
	}
}
