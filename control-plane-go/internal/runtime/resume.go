package runtime

import (
	"context"
	"time"
)

// inspectTimeout bounds one isRunning check, so a hung docker cannot hold
// worker startup (SEC-2 T20). A var so tests can shorten it.
var inspectTimeout = 5 * time.Second

// Running identifies a deployment whose container logs should be streamed.
type Running struct {
	DeploymentID string
	ContainerID  string
}

// Resume re-attaches log streams for deployments whose containers are still
// running (streams live in worker memory and are lost on restart). It
// returns how many new streams were started. Check errors are logged and
// skipped; a cancelled ctx ends the loop early.
func (s *Streamer) Resume(ctx context.Context, deps []Running, isRunning func(context.Context, string) (bool, error)) int {
	n := 0
	for _, d := range deps {
		if ctx.Err() != nil { // STAFF-2 T20: shutting down
			break
		}
		ictx, cancel := context.WithTimeout(ctx, inspectTimeout)
		ok, err := isRunning(ictx, d.ContainerID)
		cancel()
		if err != nil {
			s.log.Warn("resume: cannot inspect container", "deploymentId", d.DeploymentID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		if s.start(d.DeploymentID, d.ContainerID) {
			n++
		}
	}
	return n
}
