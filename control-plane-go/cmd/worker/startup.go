package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// healthInterval is how often WaitHealthy probes (the activity default).
const healthInterval = 2 * time.Second

const (
	minGitMajor = 2
	minGitMinor = 30 // spec section 14: git switch --detach --end-of-options
)

var gitVersionRE = regexp.MustCompile(`git version (\d+)\.(\d+)\.(\d+)`)

// checkGitVersion parses `git --version` output and returns the version as
// "major.minor.patch", or an error when it is unreadable or below 2.30.
func checkGitVersion(out string) (string, error) {
	m := gitVersionRE.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("cannot parse git version from %q", out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	v := m[1] + "." + m[2] + "." + m[3]
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return v, fmt.Errorf("git %s is too old: the worker needs git %d.%d or newer", v, minGitMajor, minGitMinor)
	}
	return v, nil
}

// prepareEnv sets process environment the build steps inherit. A private repo
// without credentials must fail fast instead of waiting for a prompt until the
// build timeout (FWD-A2).
func prepareEnv() error { return os.Setenv("GIT_TERMINAL_PROMPT", "0") }

// activityDeps are the collaborators of workflows.Activities.
type activityDeps struct {
	Store   *store.Store
	Secrets *secrets.Box
	Docker  workflows.ContainerRuntime
	Router  workflows.Router
	Builder workflows.ImageBuilder
	Logs    workflows.LogStarter
	// Temporal cancels in-flight deploy workflows during teardown.
	Temporal workflows.WorkflowController
	Metrics  *metrics.Worker
	Log      *slog.Logger
	Cfg      workflows.ActivityConfig
}

// newActivities builds Activities and refuses a nil collaborator or a zero
// health timeout, so a wiring mistake fails at startup, not mid-deployment.
func newActivities(d activityDeps) (*workflows.Activities, error) {
	var missing []string
	for name, nilField := range map[string]bool{
		"Store": d.Store == nil, "Secrets": d.Secrets == nil, "Docker": d.Docker == nil,
		"Router": d.Router == nil, "Builder": d.Builder == nil, "Logs": d.Logs == nil,
		"Metrics": d.Metrics == nil, "Temporal": d.Temporal == nil, "Log": d.Log == nil,
	} {
		if nilField {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("activities: nil dependencies: %v", missing)
	}
	if d.Cfg.HealthTimeout <= 0 {
		return nil, errors.New("activities: HealthTimeout must be positive")
	}
	d.Cfg.HealthInterval = healthInterval
	return &workflows.Activities{
		Store: d.Store, Secrets: d.Secrets, Docker: d.Docker, Router: d.Router, Builder: d.Builder,
		Logs: d.Logs, Temporal: d.Temporal, Metrics: d.Metrics, Log: d.Log, Cfg: d.Cfg,
	}, nil
}

// supervisor runs the worker's background loops. Any loop that ends while the
// process is still running cancels the root context, so a supervisor restarts
// the whole process instead of leaving it half alive.
type supervisor struct {
	log    *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newSupervisor(log *slog.Logger, cancel context.CancelFunc) *supervisor {
	return &supervisor{log: log, cancel: cancel}
}

// Start runs fn in a goroutine and logs how it ended.
func (s *supervisor) Start(ctx context.Context, name string, fn func() error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		switch err := fn(); {
		case err != nil:
			s.log.Error(name+" stopped", "err", err)
			s.cancel()
		case ctx.Err() != nil:
			s.log.Info(name + " stopped")
		default:
			s.log.Error(name + " exited unexpectedly")
			s.cancel()
		}
	}()
}

// Wait blocks until every started loop has returned.
func (s *supervisor) Wait() { s.wg.Wait() }

// resumeList converts running deployments to log-stream resume entries,
// skipping rows without a container.
func resumeList(running []store.Deployment) []runtime.Running {
	out := make([]runtime.Running, 0, len(running))
	for _, d := range running {
		if d.ContainerID != nil {
			out = append(out, runtime.Running{DeploymentID: d.ID, ContainerID: *d.ContainerID})
		}
	}
	return out
}

// resumeLoop adapts a log-stream resume to a supervised loop. resume may be
// slow (one docker inspect per container), so it runs under the supervisor and
// does not delay the other loops. A normal finish is not "unexpected": the
// loop then waits for ctx so the supervisor does not cancel the process (T26 STAFF-1).
func resumeLoop(ctx context.Context, log *slog.Logger, resume func() int) func() error {
	return func() error {
		log.Info("resumed log streams", "count", resume())
		<-ctx.Done()
		return nil
	}
}

// resolveRoutesDir returns NGINX_ROUTES_DIR as an absolute path (relative
// values depend on the working directory) and warns when it was relative.
func resolveRoutesDir(dir string, log *slog.Logger) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	log.Warn("NGINX_ROUTES_DIR is relative; set an absolute path", "value", dir)
	abs, err := filepath.Abs(dir)
	if err != nil { // only fails if the working directory is unreadable
		return dir
	}
	return abs
}
