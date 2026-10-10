package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/worker/build"
)

// ContainerRuntime is implemented by runtime.Docker.
type ContainerRuntime interface {
	Pull(ctx context.Context, image string) error
	DetectContainerPort(ctx context.Context, image string) (int, error)
	Run(ctx context.Context, spec runtime.RunSpec) (string, error)
	Remove(ctx context.Context, name string) error
	EnsureNetwork(ctx context.Context, name string) error
	VolumeExists(ctx context.Context, name string) bool
	CreateVolume(ctx context.Context, name string) error
	CopyIntoVolume(ctx context.Context, hostDir, volume string) error
	Probe(ctx context.Context, network, host string, port int) error
	// UsedPorts always returns a usable (possibly empty) map; a non-nil error
	// means the docker scan failed and the map may be incomplete.
	UsedPorts(ctx context.Context) (map[int]bool, error)
}

// Router is implemented by runtime.Nginx.
type Router interface {
	UpsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error
	RuntimeURL(serviceID string) string
}

// ImageBuilder is implemented by build.Builder.
type ImageBuilder interface {
	BuildAndPush(ctx context.Context, s build.Spec) error
}

// LogStarter is implemented by *runtime.Streamer.
type LogStarter interface {
	Start(deploymentID, containerID string)
}

type ActivityConfig struct {
	RegistryHost         string
	BuildWorkdir         string
	DefaultContainerPort int
	PortStart, PortEnd   int
	ExposeManagedPorts   bool
	LegacyVolumeRoot     string
	HealthTimeout        time.Duration
	HealthInterval       time.Duration // default 2s
}

type Activities struct {
	Store   *store.Store
	Secrets *secrets.Box
	Docker  ContainerRuntime
	Router  Router
	Builder ImageBuilder
	Logs    LogStarter
	Metrics *metrics.Worker // may be nil
	Log     *slog.Logger
	Cfg     ActivityConfig
}

type ClaimInput struct{ DeploymentID, From, To string }
type MarkBuiltInput struct{ DeploymentID, Image string }

// BuildInput carries only the deployment ID: the repo URL may embed a token,
// so BuildImage reads it from the store instead of workflow history.
type BuildInput struct{ DeploymentID string }
type RunInput struct {
	Job     store.Job
	Network string
	Volume  string // managed DBs only
}
type RunResult struct {
	ContainerID   string
	ContainerName string
	ContainerPort int
	HostPort      *int
}
type WaitInput struct {
	Network string
	Host    string
	Port    int
}
type RouteInput struct {
	ServiceID     string
	ContainerName string
	Port          int
}
type RetireInput struct{ ServiceID, KeepDeploymentID string }
type DeployedInput struct {
	DeploymentID  string
	ContainerID   string
	HostPort      *int
	ContainerPort int
	RuntimeURL    *string
	VolumePath    *string
}
type StreamInput struct{ DeploymentID, ContainerID string }

func notFound(what string, err error) error {
	return temporal.NewNonRetryableApplicationError(what+" not found", "NotFound", err)
}

func (a *Activities) LoadJob(ctx context.Context, deploymentID string) (store.Job, error) {
	job, err := a.Store.GetJob(ctx, deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Job{}, notFound("deployment", err)
	}
	// RepoURL may embed a token; it must never enter workflow history.
	// BuildImage reads it from the store.
	job.RepoURL = nil
	return job, err
}

func (a *Activities) Claim(ctx context.Context, in ClaimInput) (bool, error) {
	return a.Store.ClaimDeployment(ctx, in.DeploymentID, in.From, in.To)
}

func (a *Activities) BuildImage(ctx context.Context, in BuildInput) (string, error) {
	job, err := a.Store.GetJob(ctx, in.DeploymentID)
	if errors.Is(err, store.ErrNotFound) {
		return "", notFound("deployment", err)
	}
	if err != nil {
		return "", err
	}
	if job.RepoURL == nil || *job.RepoURL == "" {
		return "", temporal.NewNonRetryableApplicationError("repoUrl_required_for_build", "MissingRepo", nil)
	}
	stop := keepAlive(ctx, 20*time.Second, "building")
	defer stop()

	start := time.Now()
	image := a.Cfg.RegistryHost + "/" + job.ServiceID + ":" + job.CommitSha
	err = a.Builder.BuildAndPush(ctx, build.Spec{
		RepoURL: *job.RepoURL, Branch: job.Branch, CommitSha: job.CommitSha, ImageTag: image,
		WorkDir: filepath.Join(a.Cfg.BuildWorkdir, job.DeploymentID), ServiceType: job.ServiceType,
	})
	if errors.Is(err, gitsrc.ErrInvalid) {
		// No cause: the wrapped message must not carry anything from the source.
		return "", temporal.NewNonRetryableApplicationError("invalid_build_source", "InvalidSource", nil)
	}
	if err != nil {
		return "", err // already scrubbed by the builder
	}
	a.Metrics.ObserveBuild(time.Since(start))
	return image, nil
}

func (a *Activities) MarkBuilt(ctx context.Context, in MarkBuiltInput) error {
	err := a.Store.MarkBuilt(ctx, in.DeploymentID, in.Image)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("deployment", err)
	}
	return err
}

func (a *Activities) EnsureNetwork(ctx context.Context, projectID string) (string, error) {
	name := NetworkName(projectID)
	return name, a.Docker.EnsureNetwork(ctx, name)
}

// EnsureCredentials stores an encrypted random password for a managed DB on
// first deploy. A password that already exists is never replaced.
func (a *Activities) EnsureCredentials(ctx context.Context, job store.Job) error {
	m, ok := Managed[job.ServiceType]
	if !ok {
		return nil
	}
	env, err := a.Store.EnvMap(ctx, job.ServiceID)
	if err != nil {
		return err
	}
	if env[m.PasswordEnv] != "" {
		return nil
	}
	password, err := secrets.RandomPassword()
	if err != nil {
		return err
	}
	enc, err := a.Secrets.Encrypt(password)
	if err != nil {
		return err
	}
	// Insert-if-absent: a concurrent or retried call must not replace a
	// password the volume may already have been initialised with.
	_, err = a.Store.InsertEnvVarIfAbsent(ctx, job.ServiceID, m.PasswordEnv, enc)
	return err
}

// EnsureVolume returns the managed DB's named volume, creating it on first
// use. Only Redis copies Node-era data from the legacy host directory: Node
// never persisted Mongo data there (it moves through the Task 27 runbook).
// The copy is idempotent (marker file), so retries and later deploys are safe.
func (a *Activities) EnsureVolume(ctx context.Context, job store.Job) (string, error) {
	name := VolumeName(job.ServiceID)
	if !a.Docker.VolumeExists(ctx, name) {
		if err := a.Docker.CreateVolume(ctx, name); err != nil {
			return "", err
		}
	}
	if job.ServiceType == store.TypeRedis {
		if dir := a.legacyDir(job); dirExists(dir) {
			if err := a.Docker.CopyIntoVolume(ctx, dir, name); err != nil {
				return "", err
			}
		}
	}
	return name, nil
}

// legacyDir is the Node-era host directory for the service's data. A stored
// volumePath is used only when it is an absolute path strictly inside
// LegacyVolumeRoot; anything else falls back to <root>/<serviceId>.
func (a *Activities) legacyDir(job store.Job) string {
	root := filepath.Clean(a.Cfg.LegacyVolumeRoot)
	def := filepath.Join(a.Cfg.LegacyVolumeRoot, job.ServiceID)
	if job.VolumePath == nil {
		return def
	}
	p := *job.VolumePath
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(root, filepath.Clean(p))
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p
		}
	}
	a.Log.Warn("ignoring stored volume path outside the legacy volume root", "deploymentId", job.DeploymentID)
	return def
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// RetirePrevious removes containers of the service's other deployments and
// records them as stopped.
func (a *Activities) RetirePrevious(ctx context.Context, in RetireInput) error {
	old, err := a.Store.ListLiveDeployments(ctx, in.ServiceID, in.KeepDeploymentID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(old))
	for _, d := range old {
		if err := a.Docker.Remove(ctx, ContainerName(d.ID)); err != nil {
			return err
		}
		ids = append(ids, d.ID)
	}
	return a.Store.MarkStopped(ctx, ids)
}

func (a *Activities) RunContainer(ctx context.Context, in RunInput) (RunResult, error) {
	job := in.Job
	env, err := a.decryptedEnv(ctx, job.ServiceID) // plaintext stays in this activity: never a result, input or heartbeat
	if err != nil {
		return RunResult{}, err
	}
	spec := runtime.RunSpec{
		Name:    ContainerName(job.DeploymentID),
		Network: in.Network,
		Aliases: []string{ServiceAlias(job.ServiceID)},
		Labels: map[string]string{
			"cp.deploymentId": job.DeploymentID, "cp.serviceId": job.ServiceID, "cp.projectId": job.ProjectID,
		},
	}
	if m, ok := Managed[job.ServiceType]; ok {
		spec.Image, spec.ContainerPort, spec.Volume, spec.Cmd = m.Image, m.Port, in.Volume, m.Cmd
		spec.VolumeTarget, spec.Tmpfs, spec.CapAdd = m.DataDir, m.Tmpfs, m.CapAdd
		spec.Env = merge(m.Env, env)
		if a.Cfg.ExposeManagedPorts {
			if spec.PublishPort, err = a.allocatePort(ctx); err != nil {
				return RunResult{}, err
			}
		}
	} else {
		if job.Image == nil {
			return RunResult{}, temporal.NewNonRetryableApplicationError("image_not_built_yet", "ImageMissing", nil)
		}
		spec.Image = *job.Image
		if err := a.Docker.Pull(ctx, spec.Image); err != nil {
			return RunResult{}, err
		}
		port, err := a.Docker.DetectContainerPort(ctx, spec.Image)
		if err != nil {
			port = a.Cfg.DefaultContainerPort
		}
		spec.ContainerPort = port
		if job.ServiceType == store.TypeReact {
			spec.CapAdd = StaticSiteCaps
		}
		spec.Env = merge(map[string]string{"NODE_ENV": "production"}, env)
	}

	id, err := a.Docker.Run(ctx, spec)
	if errors.Is(err, runtime.ErrInvalidSpec) {
		// Retrying cannot help. The message names keys only, never values; no cause.
		return RunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidSpec", nil)
	}
	if err != nil {
		return RunResult{}, err
	}
	res := RunResult{ContainerID: id, ContainerName: spec.Name, ContainerPort: spec.ContainerPort}
	if spec.PublishPort > 0 {
		p := spec.PublishPort
		res.HostPort = &p
	}
	return res, nil
}

// WaitHealthy polls a TCP probe until the container accepts connections or
// HealthTimeout passes.
func (a *Activities) WaitHealthy(ctx context.Context, in WaitInput) error {
	interval := a.Cfg.HealthInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(a.Cfg.HealthTimeout)
	for {
		activity.RecordHeartbeat(ctx, in.Host)
		err := a.Docker.Probe(ctx, in.Network, in.Host, in.Port)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s:%d not reachable after %s: %w", in.Host, in.Port, a.Cfg.HealthTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// SwitchRoute points <serviceId>.<domain> at the new container and returns the URL.
func (a *Activities) SwitchRoute(ctx context.Context, in RouteInput) (string, error) {
	if err := a.Router.UpsertRoute(ctx, in.ServiceID, in.ContainerName, in.Port); err != nil {
		return "", err
	}
	return a.Router.RuntimeURL(in.ServiceID), nil
}

// MarkDeployed records the running container. The deployed counter is
// best-effort: if the worker dies after the increment and before Temporal
// records completion, the retry counts again. This is accepted for counters.
func (a *Activities) MarkDeployed(ctx context.Context, in DeployedInput) error {
	err := a.Store.MarkDeployed(ctx, in.DeploymentID, store.DeployedInfo{
		ContainerID: in.ContainerID, HostPort: in.HostPort, ContainerPort: in.ContainerPort,
		RuntimeURL: in.RuntimeURL, VolumePath: in.VolumePath,
	})
	if errors.Is(err, store.ErrNotFound) {
		return notFound("deployment", err)
	}
	if err != nil {
		return err
	}
	if d, err := a.Store.GetDeployment(ctx, in.DeploymentID); err == nil {
		a.Metrics.DeploymentFinished(metrics.OutcomeDeployed, time.Since(d.CreatedAt.Time))
	}
	return nil
}

func (a *Activities) StartLogStream(_ context.Context, in StreamInput) error {
	a.Logs.Start(in.DeploymentID, in.ContainerID)
	return nil
}

// MarkFailed never overwrites a deployed row. The failed counter moves only
// when a row actually changed.
func (a *Activities) MarkFailed(ctx context.Context, deploymentID string) error {
	changed, err := a.Store.MarkFailed(ctx, deploymentID)
	if err != nil {
		return err
	}
	if changed {
		a.Metrics.DeploymentFinished(metrics.OutcomeFailed, 0)
	}
	return nil
}

func (a *Activities) RemoveContainer(ctx context.Context, name string) error {
	return a.Docker.Remove(ctx, name)
}

func (a *Activities) decryptedEnv(ctx context.Context, serviceID string) (map[string]string, error) {
	raw, err := a.Store.EnvMap(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		plain, err := a.Secrets.Decrypt(v)
		if err != nil {
			return nil, fmt.Errorf("env var %s: %w", k, err)
		}
		out[k] = plain
	}
	return out, nil
}

// allocatePort picks a host port used neither by a running container nor by
// a live deployment row. Only managed DBs in dev publish ports. A failed
// docker scan is logged and the database view alone is used.
func (a *Activities) allocatePort(ctx context.Context) (int, error) {
	used, err := a.Docker.UsedPorts(ctx)
	if err != nil {
		a.Log.Warn("docker port scan failed; allocating from database state only", "error", err)
	}
	if used == nil {
		used = map[int]bool{}
	}
	dbPorts, err := a.Store.UsedHostPorts(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range dbPorts {
		used[p] = true
	}
	return runtime.PickFreePort(a.Cfg.PortStart, a.Cfg.PortEnd, used)
}

// keepAlive heartbeats every interval until the returned stop func is called,
// so long builds don't hit the heartbeat timeout.
func keepAlive(ctx context.Context, every time.Duration, details string) func() {
	activity.RecordHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx, details)
			}
		}
	}()
	return func() { close(done) }
}

// merge returns base overlaid with overrides, without modifying either.
func merge(base, overrides map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, overrides)
	return out
}
