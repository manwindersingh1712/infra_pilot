package runtime

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

// hostPortRe matches a published host port or range, e.g. "0.0.0.0:20000->"
// or "0.0.0.0:8000-8002->".
var hostPortRe = regexp.MustCompile(`:(\d+)(?:-(\d+))?->`)

// UsedPorts returns host ports published by running containers. When the
// docker scan fails it returns an empty (non-nil) map together with the error,
// so a caller can fail open as the Node worker did, but log it.
func (d Docker) UsedPorts(ctx context.Context) (map[int]bool, error) {
	out, err := d.R.Run(ctx, "", "docker", "ps", "--format", "{{.Ports}}")
	if err != nil {
		return map[int]bool{}, err
	}
	return parseDockerPorts(out), nil
}

func parseDockerPorts(out string) map[int]bool {
	used := map[int]bool{}
	for _, m := range hostPortRe.FindAllStringSubmatch(out, -1) {
		lo, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		hi := lo
		if m[2] != "" {
			if hi, err = strconv.Atoi(m[2]); err != nil {
				continue
			}
		}
		if lo < 1 || hi > 65535 || hi < lo { // bounds the loop below
			continue
		}
		for p := lo; p <= hi; p++ {
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
