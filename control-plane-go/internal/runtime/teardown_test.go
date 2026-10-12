package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

// vsvc is a cuid-shaped service id.
const vsvc = "cm1a1b2c3d4e5f6g7h8i9j0k1"

func TestValidServiceID(t *testing.T) {
	for id, want := range map[string]bool{
		vsvc: true, strings.Repeat("a", 20): true, strings.Repeat("9", 32): true,
		strings.Repeat("a", 19): false, strings.Repeat("a", 33): false, "": false, "svc-1": false,
		"CM1A1B2C3D4E5F6G7H8I9J0K1": false, vsvc + "\n": false, "../" + vsvc: false, "cm1a1b2c3d4e5f6g7h8i9j0é1": false,
	} {
		if got := ValidServiceID(id); got != want {
			t.Errorf("ValidServiceID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestRemoveVolumeAcceptsOnlyNamedServiceVolumes(t *testing.T) {
	r := &testutil.FakeRunner{}
	if err := (Docker{R: r}).RemoveVolume(context.Background(), "cp-vol-"+vsvc); err != nil {
		t.Fatal(err)
	}
	if got := r.Commands(); len(got) != 1 || got[0] != "docker volume rm -f cp-vol-"+vsvc {
		t.Fatalf("cmds %v", got)
	}
}

func TestRemoveServiceContainersAndRouteRejectShortIDs(t *testing.T) {
	r := &testutil.FakeRunner{}
	dir := t.TempDir()
	for _, bad := range []string{"svc1", "svc-1", strings.Repeat("a", 19), "../" + vsvc} {
		if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), bad); err == nil {
			t.Errorf("RemoveServiceContainers accepted %q", bad)
		}
		if err := (Nginx{R: r, RoutesDir: dir, Container: "cp_nginx"}).RemoveRoute(context.Background(), bad); err == nil {
			t.Errorf("RemoveRoute accepted %q", bad)
		}
	}
	if len(r.Commands()) != 0 {
		t.Fatalf("docker ran for a bad id: %v", r.Commands())
	}
}

func TestRemoveVolumeIgnoresMissing(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) {
		return "", errors.New("Error response from daemon: get cp-vol-x: no such volume")
	}}
	if err := (Docker{R: r}).RemoveVolume(context.Background(), "cp-vol-"+vsvc); err != nil {
		t.Fatal(err)
	}
	if got := r.Commands(); len(got) != 1 || got[0] != "docker volume rm -f cp-vol-"+vsvc {
		t.Fatalf("cmds %v", got)
	}
}

func TestRemoveVolumeReportsOtherErrorsAndRejectsBadNames(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("volume is in use") }}
	if err := (Docker{R: r}).RemoveVolume(context.Background(), "cp-vol-"+vsvc); err == nil {
		t.Fatal("in-use error was swallowed")
	}
	r = &testutil.FakeRunner{}
	for _, bad := range []string{"", "-f", "a b", "../x", "x;y", "cp_pgdata", "cp-vol-", "cp-vol-../x", "cp-vol-SHORT", "cp-vol-short", "cp-vol-" + strings.ToUpper(vsvc),
		"cp-vol-" + vsvc + "-x", "cp-vol-" + vsvc + "/..", "xcp-vol-" + vsvc, "cp-vol-" + strings.Repeat("a", 33)} {
		if err := (Docker{R: r}).RemoveVolume(context.Background(), bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if len(r.Commands()) != 0 {
		t.Fatalf("docker ran for a bad name: %v", r.Commands())
	}
}

func TestRemoveServiceContainersRemovesOnlyLabelled(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "ps" {
			return "abc123def456\nfff000fff000\n", nil
		}
		return "", nil
	}}
	if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), vsvc); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker ps -a -q --filter label=cp.serviceId=" + vsvc,
		"docker rm -f abc123def456",
		"docker rm -f fff000fff000",
	}
	if got := r.Commands(); !slices.Equal(got, want) {
		t.Fatalf("cmds %v", got)
	}
}

func TestRemoveServiceContainersRejectsBadIDsAndOutput(t *testing.T) {
	r := &testutil.FakeRunner{}
	if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), "x --all"); err == nil {
		t.Fatal("bad service id accepted")
	}
	if len(r.Commands()) != 0 {
		t.Fatal("docker ran")
	}
	r = &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "--force\n", nil }}
	err := (Docker{R: r}).RemoveServiceContainers(context.Background(), vsvc)
	if err == nil || !strings.Contains(err.Error(), "container id") {
		t.Fatalf("err = %v", err)
	}
	// A container that vanished between ps and rm is fine.
	r = &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "ps" {
			return "abc123def456\n", nil
		}
		return "", errors.New("Error: No such container: abc123def456")
	}}
	if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), vsvc); err != nil {
		t.Fatal(err)
	}
}
