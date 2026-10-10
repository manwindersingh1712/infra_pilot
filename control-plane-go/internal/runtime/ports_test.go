package runtime

import (
	"context"
	"errors"
	"maps"
	"testing"

	"infrapilot/internal/testutil"
)

func TestParseDockerPorts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[int]bool
	}{
		{"ipv4", "0.0.0.0:20000->80/tcp", map[int]bool{20000: true}},
		{"ipv6 colons", ":::20001->80/tcp", map[int]bool{20001: true}},
		{"ipv6 brackets", "[::]:20002->80/tcp", map[int]bool{20002: true}},
		{"loopback udp", "127.0.0.1:20003->53/udp", map[int]bool{20003: true}},
		{"two bindings", "0.0.0.0:20004->80/tcp, 0.0.0.0:20005->443/tcp", map[int]bool{20004: true, 20005: true}},
		{"range", "0.0.0.0:8000-8002->8000-8002/tcp", map[int]bool{8000: true, 8001: true, 8002: true}},
		{"unpublished", "80/tcp", map[int]bool{}},
		{"malformed range", "0.0.0.0:9-1->1/tcp", map[int]bool{}},
		{"range above 65535", "0.0.0.0:9223372036854775807->1/tcp", map[int]bool{}},
		{"high end beyond 65535", "0.0.0.0:65535-65536->1/tcp", map[int]bool{}},
		{"port zero", "0.0.0.0:0->1/tcp", map[int]bool{}},
		{"blank lines", "\n\n0.0.0.0:20006->80/tcp\n\n", map[int]bool{20006: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDockerPorts(tc.in); !maps.Equal(got, tc.want) {
				t.Fatalf("parseDockerPorts(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
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

func TestUsedPortsReturnsEmptyMapWithDockerError(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("docker down") }}
	got, err := (Docker{R: r}).UsedPorts(context.Background())
	if err == nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v err=%v: want an empty non-nil map and the docker error", got, err)
	}
	if cmds := r.Commands(); cmds[0] != "docker ps --format {{.Ports}}" {
		t.Fatalf("cmds = %v", cmds)
	}
}
