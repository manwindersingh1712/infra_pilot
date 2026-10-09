package runtime

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

var hostPortRe = regexp.MustCompile(`:(\d+)->`)

// UsedPorts returns host ports published by running containers. Errors are
// treated as "none in use", as in the Node worker.
func (d Docker) UsedPorts(ctx context.Context) map[int]bool {
	out, err := d.R.Run(ctx, "", "docker", "ps", "--format", "{{.Ports}}")
	if err != nil {
		return map[int]bool{}
	}
	return parseDockerPorts(out)
}

func parseDockerPorts(out string) map[int]bool {
	used := map[int]bool{}
	for _, m := range hostPortRe.FindAllStringSubmatch(out, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil {
			used[p] = true
		}
	}
	return used
}

// PickFreePort returns the first port in [start, end] not in used.
func PickFreePort(start, end int, used map[int]bool) (int, error) {
	for p := start; p <= end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("no_free_ports_in_range")
}
