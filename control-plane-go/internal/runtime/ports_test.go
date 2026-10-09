package runtime

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/testutil"
)

func TestParseDockerPorts(t *testing.T) {
	out := "0.0.0.0:20000->27017/tcp, :::20000->27017/tcp\n\n0.0.0.0:20003->3000/tcp\n"
	got := parseDockerPorts(out)
	if len(got) != 2 || !got[20000] || !got[20003] {
		t.Fatalf("got %v", got)
	}
}

func TestPickFreePort(t *testing.T) {
	p, err := PickFreePort(20000, 20002, map[int]bool{20000: true, 20001: true})
	if err != nil || p != 20002 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	if _, err := PickFreePort(1, 1, map[int]bool{1: true}); err == nil || err.Error() != "no_free_ports_in_range" {
		t.Fatalf("err = %v", err)
	}
}

func TestUsedPortsIgnoresDockerErrors(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("docker down") }}
	if got := (Docker{R: r}).UsedPorts(context.Background()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if cmds := r.Commands(); cmds[0] != "docker ps --format {{.Ports}}" {
		t.Fatalf("cmds = %v", cmds)
	}
}
