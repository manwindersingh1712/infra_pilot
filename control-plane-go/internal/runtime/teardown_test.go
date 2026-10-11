package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

func TestRemoveVolumeIgnoresMissing(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) {
		return "", errors.New("Error response from daemon: get cp-vol-x: no such volume")
	}}
	if err := (Docker{R: r}).RemoveVolume(context.Background(), "cp-vol-x"); err != nil {
		t.Fatal(err)
	}
	if got := r.Commands(); len(got) != 1 || got[0] != "docker volume rm -f cp-vol-x" {
		t.Fatalf("cmds %v", got)
	}
}

func TestRemoveVolumeReportsOtherErrorsAndRejectsBadNames(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("volume is in use") }}
	if err := (Docker{R: r}).RemoveVolume(context.Background(), "cp-vol-x"); err == nil {
		t.Fatal("in-use error was swallowed")
	}
	r = &testutil.FakeRunner{}
	for _, bad := range []string{"", "-f", "a b", "../x", "x;y"} {
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
	if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), "svc1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker ps -a -q --filter label=cp.serviceId=svc1",
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
	err := (Docker{R: r}).RemoveServiceContainers(context.Background(), "svc1")
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
	if err := (Docker{R: r}).RemoveServiceContainers(context.Background(), "svc1"); err != nil {
		t.Fatal(err)
	}
}
