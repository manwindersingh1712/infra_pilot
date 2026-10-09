package runtime

import "context"

// Running identifies a deployment whose container logs should be streamed.
type Running struct {
	DeploymentID string
	ContainerID  string
}

// Resume re-attaches log streams for deployments whose containers are still
// running (streams live in worker memory and are lost on restart). It
// returns how many streams were started. Check errors are logged and skipped.
func (s *Streamer) Resume(ctx context.Context, deps []Running, isRunning func(context.Context, string) (bool, error)) int {
	n := 0
	for _, d := range deps {
		ok, err := isRunning(ctx, d.ContainerID)
		if err != nil {
			s.log.Warn("resume: cannot inspect container", "deploymentId", d.DeploymentID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		s.Start(d.DeploymentID, d.ContainerID)
		n++
	}
	return n
}
