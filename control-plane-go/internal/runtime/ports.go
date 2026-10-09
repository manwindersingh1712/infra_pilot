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

// maxPortSpan caps how many ports one published range may mark, so malformed
// input cannot drive a huge loop.
const maxPortSpan = 65536

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
		if hi < lo || hi-lo >= maxPortSpan {
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
