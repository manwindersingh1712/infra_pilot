package workflows_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// fakeWorld is an in-memory stand-in for Postgres, Docker and nginx, shared by
// every worker a test starts (a restarted worker sees the same world, as a
// restarted real worker sees the same host). It follows the contracts of the
// real activities: Claim/ClaimDeploy semantics, idempotent container create by
// name, RetirePrevious removing the service's other containers, RevertRoute
// deciding from the rows.
type fakeWorld struct {
	mu         sync.Mutex
	rows       map[string]*fakeRow
	containers map[string]bool   // by container name
	route      map[string]string // serviceID -> container name
	steps      []string
	busy       int // ServiceBusy results returned
	hooks      map[string]*hook
}

type fakeRow struct {
	id, serviceID, projectID, status string
	image                            *string
	container                        string // set by MarkDeployed
	stopped                          bool
}

// hook blocks one activity (keyed "<activity>:<deploymentID>") until released
// or its context ends. once makes only the first call block.
type hook struct {
	entered  chan struct{}
	release  chan struct{}
	once     bool
	enterOne sync.Once
	used     bool
}

func newWorld() *fakeWorld {
	return &fakeWorld{rows: map[string]*fakeRow{}, containers: map[string]bool{}, route: map[string]string{}, hooks: map[string]*hook{}}
}

func (w *fakeWorld) addRow(id, service string, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rows[id] = &fakeRow{id: id, serviceID: service, projectID: "proj", status: status}
}

// serve marks a row as the live, serving deployment of its service.
func (w *fakeWorld) serve(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.rows[id]
	r.status, r.container = store.StatusDeployed, workflows.ContainerName(id)
	w.containers[r.container] = true
	w.route[r.serviceID] = r.container
}

func (w *fakeWorld) addHook(key string, once bool) *hook {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := &hook{entered: make(chan struct{}), release: make(chan struct{}), once: once}
	w.hooks[key] = h
	return h
}

func (w *fakeWorld) rec(step string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.steps = append(w.steps, step)
}

func (w *fakeWorld) trace() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.steps)
}

func (w *fakeWorld) status(id string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rows[id].status
}

func (w *fakeWorld) liveContainers() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for n, ok := range w.containers {
		if ok {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func (w *fakeWorld) routeOf(service string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.route[service]
}

func (w *fakeWorld) busyCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy
}

// block parks the calling activity at its hook, heartbeating, until the hook
// is released (returns nil) or ctx ends (returns ctx.Err()).
func (w *fakeWorld) block(ctx context.Context, key string) error {
	w.mu.Lock()
	h := w.hooks[key]
	skip := h == nil || (h.once && h.used)
	if h != nil {
		h.used = true
	}
	w.mu.Unlock()
	if skip {
		return nil
	}
	h.enterOne.Do(func() { close(h.entered) })
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-h.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if activity.IsActivity(ctx) {
				activity.RecordHeartbeat(ctx, key)
			}
		}
	}
}

// fakeActs has the same exported method names and signatures as
// workflows.Activities, so the workflow's activity names resolve to it.
type fakeActs struct{ w *fakeWorld }

func (f fakeActs) LoadJob(_ context.Context, id string) (store.Job, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	r, ok := f.w.rows[id]
	if !ok {
		return store.Job{}, temporal.NewNonRetryableApplicationError("deployment not found", "NotFound", nil)
	}
	return store.Job{DeploymentID: id, Status: r.status, CommitSha: "main", Image: r.image, ServiceID: r.serviceID,
		ServiceType: store.TypeNodejs, Branch: "main", ProjectID: r.projectID}, nil
}

func (f fakeActs) Claim(_ context.Context, in workflows.ClaimInput) (bool, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	r := f.w.rows[in.DeploymentID]
	if r.status == in.From {
		r.status = in.To
		return true, nil
	}
	return r.status == in.To, nil
}

func (f fakeActs) ClaimDeploy(_ context.Context, id string) (bool, error) {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	r := f.w.rows[id]
	if r.status == store.StatusDeploying {
		return true, nil // our own retry
	}
	for _, o := range f.w.rows {
		if o.serviceID == r.serviceID && o.id != id && o.status == store.StatusDeploying {
			f.w.busy++
			f.w.steps = append(f.w.steps, "busy:"+id)
			return false, temporal.NewApplicationError("service busy", "ServiceBusy")
		}
	}
	if r.status != store.StatusQueued && r.status != store.StatusBuilding {
		return false, nil
	}
	r.status = store.StatusDeploying
	f.w.steps = append(f.w.steps, "claim-deploy:"+id)
	return true, nil
}

func (f fakeActs) BuildImage(_ context.Context, in workflows.BuildInput) (string, error) {
	return "reg/" + in.DeploymentID + ":main", nil
}

func (f fakeActs) MarkBuilt(_ context.Context, in workflows.MarkBuiltInput) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	img := in.Image
	f.w.rows[in.DeploymentID].image = &img
	return nil
}

func (f fakeActs) EnsureNetwork(_ context.Context, p string) (string, error) {
	return workflows.NetworkName(p), nil
}
func (f fakeActs) EnsureCredentials(context.Context, store.Job) error          { return nil }
func (f fakeActs) EnsureVolume(context.Context, store.Job) (string, error)     { return "", nil }
func (f fakeActs) WaitHealthy(context.Context, workflows.WaitInput) error      { return nil }
func (f fakeActs) StartLogStream(context.Context, workflows.StreamInput) error { return nil }

// RetirePrevious removes the service's other containers, but never the one
// the route currently serves unless the caller is the new serving deployment.
func (f fakeActs) RetirePrevious(_ context.Context, in workflows.RetireInput) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	for _, r := range f.w.rows {
		if r.serviceID == in.ServiceID && r.id != in.KeepDeploymentID && r.container != "" && !r.stopped {
			delete(f.w.containers, r.container)
			r.stopped = true
			f.w.steps = append(f.w.steps, "retire:"+r.id)
		}
	}
	return nil
}

func (f fakeActs) RunContainer(ctx context.Context, in workflows.RunInput) (workflows.RunResult, error) {
	name := workflows.ContainerName(in.Job.DeploymentID)
	f.w.mu.Lock()
	f.w.containers[name] = true // docker run is create-or-replace by name
	f.w.steps = append(f.w.steps, "run:"+in.Job.DeploymentID)
	f.w.mu.Unlock()
	if err := f.w.block(ctx, "RunContainer:"+in.Job.DeploymentID); err != nil {
		return workflows.RunResult{}, err
	}
	return workflows.RunResult{ContainerID: "id-" + name, ContainerName: name, ContainerPort: 3000}, nil
}

func (f fakeActs) SwitchRoute(ctx context.Context, in workflows.RouteInput) (string, error) {
	f.w.mu.Lock()
	f.w.route[in.ServiceID] = in.ContainerName
	f.w.steps = append(f.w.steps, "route:"+in.ContainerName)
	f.w.mu.Unlock()
	if err := f.w.block(ctx, "SwitchRoute:"+in.ContainerName); err != nil {
		return "", err
	}
	return "route-" + in.ServiceID, nil
}

// RevertRoute decides from the rows, like the real activity.
func (f fakeActs) RevertRoute(_ context.Context, in workflows.RevertInput) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.w.steps = append(f.w.steps, "revert:"+in.DeploymentID)
	delete(f.w.route, in.ServiceID)
	for _, r := range f.w.rows {
		if r.serviceID == in.ServiceID && r.id != in.DeploymentID && r.status == store.StatusDeployed && r.container != "" && !r.stopped {
			f.w.route[in.ServiceID] = r.container
		}
	}
	return nil
}

func (f fakeActs) MarkDeployed(_ context.Context, in workflows.DeployedInput) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	r := f.w.rows[in.DeploymentID]
	r.status, r.container = store.StatusDeployed, workflows.ContainerName(in.DeploymentID)
	f.w.steps = append(f.w.steps, "deployed:"+in.DeploymentID)
	return nil
}

func (f fakeActs) MarkFailed(_ context.Context, id string) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	if r := f.w.rows[id]; r.status != store.StatusDeployed {
		r.status = store.StatusFailed
	}
	f.w.steps = append(f.w.steps, "failed:"+id)
	return nil
}

func (f fakeActs) RemoveContainer(_ context.Context, name string) error {
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	delete(f.w.containers, name)
	f.w.steps = append(f.w.steps, "remove:"+name)
	return nil
}
