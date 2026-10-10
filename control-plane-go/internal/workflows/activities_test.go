package workflows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/worker/build"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

type fakeDocker struct {
	mu        sync.Mutex
	calls     []string
	specs     []runtime.RunSpec
	volumes   map[string]bool
	probeFail int // failing probes before success; -1 = always fail
	port      int
	portErr   error
	used      map[int]bool
	usedErr   error
	runErr    error
	removeErr map[string]int // name -> number of Remove calls that fail before one succeeds
}

func (f *fakeDocker) rec(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeDocker) Pull(_ context.Context, image string) error { f.rec("pull " + image); return nil }
func (f *fakeDocker) DetectContainerPort(context.Context, string) (int, error) {
	return f.port, f.portErr
}
func (f *fakeDocker) Run(_ context.Context, s runtime.RunSpec) (string, error) {
	if err := s.Validate(); err != nil { // the real Docker.Run validates first
		return "", err
	}
	f.mu.Lock()
	f.specs = append(f.specs, s)
	f.mu.Unlock()
	f.rec("run " + s.Name)
	if f.runErr != nil {
		return "", f.runErr
	}
	return "ctr-" + s.Name, nil
}
func (f *fakeDocker) Remove(_ context.Context, name string) error {
	f.rec("remove " + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr[name] > 0 {
		f.removeErr[name]--
		return errors.New("docker rm failed")
	}
	return nil
}
func (f *fakeDocker) EnsureNetwork(_ context.Context, name string) error {
	f.rec("network " + name)
	return nil
}
func (f *fakeDocker) VolumeExists(_ context.Context, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes[name]
}
func (f *fakeDocker) CreateVolume(_ context.Context, name string) error {
	f.mu.Lock()
	f.volumes[name] = true
	f.mu.Unlock()
	f.rec("create " + name)
	return nil
}
func (f *fakeDocker) CopyIntoVolume(_ context.Context, dir, volume string) error {
	f.rec("copy " + volume)
	return nil
}
func (f *fakeDocker) Probe(_ context.Context, _, host string, _ int) error {
	f.rec("probe " + host)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeFail == -1 {
		return errors.New("connection refused")
	}
	if f.probeFail > 0 {
		f.probeFail--
		return errors.New("connection refused")
	}
	return nil
}
func (f *fakeDocker) UsedPorts(context.Context) (map[int]bool, error) {
	out := map[int]bool{}
	for p := range f.used {
		out[p] = true
	}
	return out, f.usedErr
}

type fakeRouter struct {
	routes  []string
	removed []string
	err     error // returned by UpsertRoute and RemoveRoute
}

func (f *fakeRouter) UpsertRoute(_ context.Context, serviceID, name string, port int) error {
	f.routes = append(f.routes, serviceID+"->"+name)
	return f.err
}
func (f *fakeRouter) RemoveRoute(_ context.Context, serviceID string) error {
	f.removed = append(f.removed, serviceID)
	return f.err
}
func (f *fakeRouter) RuntimeURL(serviceID string) string { return "http://" + serviceID + ".localhost" }

type fakeBuilder struct {
	specs []build.Spec
	err   error
}

func (f *fakeBuilder) BuildAndPush(_ context.Context, s build.Spec) error {
	f.specs = append(f.specs, s)
	return f.err
}

type fakeLogs struct{ started []string }

func (f *fakeLogs) Start(deploymentID, containerID string) {
	f.started = append(f.started, deploymentID+"/"+containerID)
}

type env struct {
	st      *store.Store
	logBuf  *bytes.Buffer
	metrics *metrics.Worker
	acts    *Activities
	docker  *fakeDocker
	router  *fakeRouter
	builder *fakeBuilder
	logs    *fakeLogs
}

func newEnv(t *testing.T) env {
	st := store.New(testutil.NeedDB(t, pool))
	box, err := secrets.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	logBuf := &bytes.Buffer{}
	mw := metrics.NewWorker()
	e := env{st: st, logBuf: logBuf, metrics: mw, docker: &fakeDocker{port: 8080, volumes: map[string]bool{}, used: map[int]bool{}},
		router: &fakeRouter{}, builder: &fakeBuilder{}, logs: &fakeLogs{}}
	e.acts = &Activities{
		Store: st, Secrets: box, Docker: e.docker, Router: e.router, Builder: e.builder, Logs: e.logs,
		Metrics: mw,
		Log:     slog.New(slog.NewTextHandler(logBuf, nil)),
		Cfg: ActivityConfig{
			RegistryHost: "reg:5001", BuildWorkdir: "/tmp/cp-builds", DefaultContainerPort: 3080,
			PortStart: 20000, PortEnd: 20010, LegacyVolumeRoot: t.TempDir(),
			HealthTimeout: 50 * time.Millisecond, HealthInterval: time.Millisecond,
		},
	}
	return e
}

func (e env) run(t *testing.T, fn any, args ...any) (converter.EncodedValue, error) {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	ae := s.NewTestActivityEnvironment()
	ae.RegisterActivity(e.acts)
	return ae.ExecuteActivity(fn, args...)
}

func (e env) job(t *testing.T, serviceType string) store.Job {
	t.Helper()
	ctx := context.Background()
	f := testutil.Seed(t, e.st, serviceType)
	d, err := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.st.GetJob(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func isNonRetryable(err error) bool {
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.NonRetryable()
}

func isNonRetryableType(err error, typ string) bool {
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.NonRetryable() && appErr.Type() == typ
}

// scrape returns the worker metrics in Prometheus text format.
func scrape(t *testing.T, m *metrics.Worker) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestLoadJob(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	val, err := e.run(t, e.acts.LoadJob, j.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	var got store.Job
	val.Get(&got)
	if got.ProjectID != j.ProjectID || got.ServiceType != "nodejs" {
		t.Fatalf("got %+v", got)
	}
	if _, err := e.run(t, e.acts.LoadJob, "missing"); !isNonRetryable(err) {
		t.Fatalf("missing deployment must be non-retryable, got %v", err)
	}
}

func (e env) setRepoURL(t *testing.T, serviceID string, url *string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE "Service" SET "repoUrl" = $2 WHERE "id" = $1`, serviceID, url); err != nil {
		t.Fatal(err)
	}
}

func TestLoadJobOmitsRepoURL(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	secretURL := "https://ghp_SECRET9@example.com/x.git"
	e.setRepoURL(t, j.ServiceID, &secretURL)

	val, err := e.run(t, e.acts.LoadJob, j.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	var got store.Job
	val.Get(&got)
	if got.RepoURL != nil {
		t.Fatalf("RepoURL = %q, must be nil", *got.RepoURL)
	}
	payload, err := converter.GetDefaultDataConverter().ToPayload(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload.Data), "ghp_") {
		t.Fatalf("payload leaks the token: %s", payload.Data)
	}
}

func TestBuildImage(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	stored := "https://tok@example.com/x.git"
	e.setRepoURL(t, j.ServiceID, &stored)

	val, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: j.DeploymentID})
	if err != nil {
		t.Fatal(err)
	}
	var image string
	val.Get(&image)
	if image != "reg:5001/"+j.ServiceID+":main" {
		t.Fatalf("image = %s", image)
	}
	s := e.builder.specs[0]
	if s.WorkDir != filepath.Join("/tmp/cp-builds", j.DeploymentID) || s.ImageTag != image || s.ServiceType != "nodejs" ||
		s.RepoURL != stored || s.Branch != "main" || s.CommitSha != "main" {
		t.Fatalf("spec %+v", s)
	}
	if !strings.Contains(scrape(t, e.metrics), "cp_build_duration_seconds_count 1") {
		t.Fatal("build duration not observed")
	}

	if _, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: "missing"}); !isNonRetryableType(err, "NotFound") {
		t.Fatalf("unknown deployment must be non-retryable NotFound, got %v", err)
	}

	e.setRepoURL(t, j.ServiceID, nil)
	if _, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: j.DeploymentID}); !isNonRetryableType(err, "MissingRepo") {
		t.Fatalf("missing repo must be non-retryable, got %v", err)
	}
}

func TestBuildImageInvalidSourceIsNonRetryableWithoutCause(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	e.builder.err = fmt.Errorf("x: %w", gitsrc.ErrInvalid)

	_, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: j.DeploymentID})
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || !appErr.NonRetryable() || appErr.Type() != "InvalidSource" || appErr.Unwrap() != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildImagePassesOtherBuilderErrorsThrough(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	e.builder.err = errors.New("docker build: exit 1")

	_, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: j.DeploymentID})
	if err == nil || isNonRetryable(err) || !strings.Contains(err.Error(), "docker build: exit 1") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(scrape(t, e.metrics), "cp_build_duration_seconds_count 1") {
		t.Fatal("a failed build must not be observed as a build duration")
	}
}

func TestLegacyDir(t *testing.T) {
	e := newEnv(t)
	root := e.acts.Cfg.LegacyVolumeRoot
	def := filepath.Join(root, "svc1")
	for name, vp := range map[string]*string{
		"nil":            nil,
		"outside root":   ptr("/etc"),
		"dotdot escape":  ptr(root + "/../x"),
		"root itself":    ptr(root),
		"relative":       ptr("cp-vol-x"),
		"root with dots": ptr(root + "/s1/.."),
	} {
		if got := e.acts.legacyDir(store.Job{DeploymentID: "d1", ServiceID: "svc1", VolumePath: vp}); got != def {
			t.Errorf("%s: legacyDir = %q, want %q", name, got, def)
		}
	}
	used := filepath.Join(root, "s1")
	mustT(t, os.MkdirAll(used, 0o755))
	usedResolved, err := filepath.EvalSymlinks(used)
	mustT(t, err)
	if got := e.acts.legacyDir(store.Job{ServiceID: "svc1", VolumePath: &used}); got != usedResolved {
		t.Errorf("inside root: legacyDir = %q, want %q", got, usedResolved)
	}
	if log := e.logBuf.String(); !strings.Contains(log, "d1") || strings.Contains(log, "/etc") {
		t.Errorf("warning must name the deployment and not the path: %s", log)
	}
}

// SEC-2 (T23r1): a symlink inside the root that points outside must not be
// followed, whether it is the stored path or the default <root>/<serviceId>.
func TestLegacyDirRejectsSymlinkEscape(t *testing.T) {
	e := newEnv(t)
	root := e.acts.Cfg.LegacyVolumeRoot
	outside := t.TempDir()
	link := filepath.Join(root, "evil")
	mustT(t, os.Symlink(outside, link))
	def := filepath.Join(root, "svc1")
	if got := e.acts.legacyDir(store.Job{DeploymentID: "d1", ServiceID: "svc1", VolumePath: &link}); got != def {
		t.Errorf("stored symlink escape: legacyDir = %q, want %q", got, def)
	}
	mustT(t, os.Symlink(outside, filepath.Join(root, "svc2")))
	if got := e.acts.legacyDir(store.Job{DeploymentID: "d2", ServiceID: "svc2"}); dirExists(got) {
		t.Errorf("default symlink escape: legacyDir = %q must not resolve to a directory", got)
	}
	// A symlink that stays inside the root is fine.
	inner := filepath.Join(root, "real")
	mustT(t, os.MkdirAll(inner, 0o755))
	ok := filepath.Join(root, "alias")
	mustT(t, os.Symlink(inner, ok))
	innerResolved, err := filepath.EvalSymlinks(inner)
	mustT(t, err)
	// SEC-7 (T23r2): the resolved path is returned, so a later swap of the
	// symlink cannot redirect the bind mount.
	if got := e.acts.legacyDir(store.Job{ServiceID: "svc1", VolumePath: &ok}); got != innerResolved {
		t.Errorf("inside symlink: legacyDir = %q, want resolved %q", got, innerResolved)
	}
	// A path that does not exist counts as absent: the default is returned.
	missing := filepath.Join(root, "nope")
	if got := e.acts.legacyDir(store.Job{ServiceID: "svc1", VolumePath: &missing}); got != def {
		t.Errorf("missing: legacyDir = %q, want %q", got, def)
	}
}

func upsertErr(_ store.EnvVar, err error) error { return err }

func mustT(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

func decryptedEnv(t *testing.T, e env, serviceID string) map[string]string {
	t.Helper()
	raw, _ := e.st.EnvMap(context.Background(), serviceID)
	out := map[string]string{}
	for k, v := range raw {
		if !secrets.IsEncrypted(v) {
			t.Fatalf("%s stored unencrypted", k)
		}
		out[k], _ = e.acts.Secrets.Decrypt(v)
	}
	return out
}

func TestEnsureCredentialsGeneratesEncryptedPasswordOnce(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeRedis)
	if _, err := e.run(t, e.acts.EnsureCredentials, j); err != nil {
		t.Fatal(err)
	}
	first := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]
	if len(first) != 32 {
		t.Fatalf("password %q", first)
	}
	e.run(t, e.acts.EnsureCredentials, j)
	if again := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]; again != first {
		t.Fatal("existing password must not be replaced")
	}
}

// STAFF-2 (T23r1): concurrent first deploys must agree on one stored password.
func TestEnsureCredentialsConcurrentCallsStoreOneValue(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeRedis)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	seen := make(chan string, 20) // the value each caller observes right after its call
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := e.acts.EnsureCredentials(context.Background(), j)
			m, _ := e.st.EnvMap(context.Background(), j.ServiceID)
			errs <- err
			seen <- m["REDIS_PASSWORD"]
		}()
	}
	wg.Wait()
	close(errs)
	close(seen)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := e.st.EnvMap(context.Background(), j.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := e.st.ListEnvVars(context.Background(), j.ServiceID)
	if err != nil || len(vars) != 1 || vars[0].Value != raw["REDIS_PASSWORD"] {
		t.Fatalf("vars=%v err=%v", vars, err)
	}
	for v := range seen {
		if v != raw["REDIS_PASSWORD"] {
			t.Fatal("a caller observed a password that was later replaced")
		}
	}
}

// SEC-6 (T23r2): an empty stored password (raw or encrypted) is replaced; a
// non-empty one never is.
func TestEnsureCredentialsReplacesEmptyPassword(t *testing.T) {
	ctx := context.Background()
	for _, typ := range []string{store.TypeRedis, store.TypeMongoDB} {
		key := Managed[typ].PasswordEnv
		for name, stored := range map[string]func(e env) string{
			"raw empty":       func(e env) string { return "" },
			"encrypted empty": func(e env) string { v, _ := e.acts.Secrets.Encrypt(""); return v },
		} {
			e := newEnv(t)
			j := e.job(t, typ)
			mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, key, stored(e))))
			if err := e.acts.EnsureCredentials(ctx, j); err != nil {
				t.Fatal(err)
			}
			if got := decryptedEnv(t, e, j.ServiceID)[key]; len(got) != 32 {
				t.Fatalf("%s/%s: stored password %q, want a generated one", typ, name, got)
			}
		}
	}
}

func TestEnsureCredentialsKeepsUserPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeRedis)
	enc, _ := e.acts.Secrets.Encrypt("mine")
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "REDIS_PASSWORD", enc)))
	mustT(t, e.acts.EnsureCredentials(ctx, j))
	if got := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]; got != "mine" {
		t.Fatalf("got %q", got)
	}
}

func TestEnsureCredentialsConcurrentOverEmptyStoresOneValue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeRedis)
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "REDIS_PASSWORD", "")))
	var wg sync.WaitGroup
	seen := make(chan string, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.acts.EnsureCredentials(ctx, j); err != nil {
				t.Error(err)
			}
			m, _ := e.st.EnvMap(ctx, j.ServiceID)
			seen <- m["REDIS_PASSWORD"]
		}()
	}
	wg.Wait()
	close(seen)
	final := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]
	raw, _ := e.st.EnvMap(ctx, j.ServiceID)
	if len(final) != 32 {
		t.Fatalf("final %q", final)
	}
	for v := range seen {
		if v != raw["REDIS_PASSWORD"] {
			t.Fatal("a caller observed a password that was later replaced")
		}
	}
}

// Defence in depth: never start a managed DB without a password.
func TestRunContainerRefusesEmptyManagedPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeRedis)
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "REDIS_PASSWORD", "")))
	_, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n", Volume: "v"})
	if err == nil || !isNonRetryable(err) || len(e.docker.specs) != 0 {
		t.Fatalf("err=%v specs=%d", err, len(e.docker.specs))
	}
}

func TestEnsureCredentialsMongoIgnoresLegacyDir(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeMongoDB)
	os.MkdirAll(filepath.Join(e.acts.Cfg.LegacyVolumeRoot, j.ServiceID), 0o755)
	if _, err := e.run(t, e.acts.EnsureCredentials, j); err != nil {
		t.Fatal(err)
	}
	pw := decryptedEnv(t, e, j.ServiceID)["MONGO_INITDB_ROOT_PASSWORD"]
	if pw == "admin123" || len(pw) < 20 {
		t.Fatalf("Mongo must always get a random password, got %q", pw)
	}
}

func TestEnsureCredentialsSkipsBuiltApps(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	if _, err := e.run(t, e.acts.EnsureCredentials, j); err != nil {
		t.Fatal(err)
	}
	if got := decryptedEnv(t, e, j.ServiceID); len(got) != 0 {
		t.Fatalf("env = %v", got)
	}
}

func TestEnsureVolumeSkipsLegacyCopyForMongo(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeMongoDB)
	os.MkdirAll(filepath.Join(e.acts.Cfg.LegacyVolumeRoot, j.ServiceID), 0o755)

	if _, err := e.run(t, e.acts.EnsureVolume, j); err != nil {
		t.Fatal(err)
	}
	if want := []string{"create cp-vol-" + j.ServiceID}; !slices.Equal(e.docker.calls, want) {
		t.Fatalf("calls = %v, want %v", e.docker.calls, want)
	}
}

func TestEnsureVolumeMigratesLegacyDataIdempotently(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeRedis)
	os.MkdirAll(filepath.Join(e.acts.Cfg.LegacyVolumeRoot, j.ServiceID), 0o755)

	val, err := e.run(t, e.acts.EnsureVolume, j)
	if err != nil {
		t.Fatal(err)
	}
	var vol string
	val.Get(&vol)
	e.run(t, e.acts.EnsureVolume, j)
	vname := "cp-vol-" + j.ServiceID
	want := []string{"create " + vname, "copy " + vname, "copy " + vname}
	if vol != vname || !slices.Equal(e.docker.calls, want) {
		t.Fatalf("vol=%s calls=%v", vol, e.docker.calls)
	}
}

func TestRunContainerForBuiltApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	enc, err := e.acts.Secrets.Encrypt("bar")
	mustT(t, err)
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "FOO", enc)))
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "PLAIN", "legacy")))

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "cp-proj-" + j.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	s := e.docker.specs[0]
	if s.Name != "cp-"+j.DeploymentID || s.Network != "cp-proj-"+j.ProjectID || s.Aliases[0] != "svc-"+j.ServiceID ||
		s.Labels["cp.projectId"] != j.ProjectID || s.Env["NODE_ENV"] != "production" || s.Env["FOO"] != "bar" ||
		s.Env["PLAIN"] != "legacy" || s.ContainerPort != 8080 || s.PublishPort != 0 || s.Volume != "" ||
		s.VolumeTarget != "" || len(s.Tmpfs) != 0 || len(s.CapAdd) != 0 {
		t.Fatalf("spec %+v", s)
	}
	if res.HostPort != nil || res.ContainerName != s.Name || res.ContainerPort != 8080 {
		t.Fatalf("result %+v", res)
	}
}

// VER-2 (T23r1): only react static sites get the nginx master's three caps.
func TestRunContainerStaticSiteCapsOnlyForReact(t *testing.T) {
	for typ, want := range map[string][]string{
		store.TypeReact: StaticSiteCaps, store.TypeNodejs: nil, store.TypeNextjs: nil, store.TypeDocker: nil,
	} {
		e := newEnv(t)
		j := e.job(t, typ)
		image := "reg:5001/" + j.ServiceID + ":main"
		j.Image = &image
		if _, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"}); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if got := e.docker.specs[0].CapAdd; !slices.Equal(got, want) {
			t.Fatalf("%s: CapAdd %v, want %v", typ, got, want)
		}
	}
	if !slices.Equal(StaticSiteCaps, []string{"CHOWN", "SETUID", "SETGID"}) {
		t.Fatalf("StaticSiteCaps %v", StaticSiteCaps)
	}
}

// STAFF-3 (T23r1): a second RunContainer for the same deployment (activity
// retry) replaces the first container instead of failing.
func TestRunContainerTwiceForOneDeployment(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	for i := range 2 {
		if _, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"}); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if len(e.docker.specs) != 2 || e.docker.specs[0].Name != e.docker.specs[1].Name {
		t.Fatalf("specs %+v", e.docker.specs)
	}
}

// FWD-2 (T23r1): pulling and starting can be slow, so RunContainer heartbeats.
func TestRunContainerHeartbeats(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	var s testsuite.WorkflowTestSuite
	ae := s.NewTestActivityEnvironment()
	ae.RegisterActivity(e.acts)
	beats := 0
	ae.SetOnActivityHeartbeatListener(func(*activity.Info, converter.EncodedValues) { beats++ })
	if _, err := ae.ExecuteActivity(e.acts.RunContainer, RunInput{Job: j, Network: "n"}); err != nil {
		t.Fatal(err)
	}
	if beats == 0 {
		t.Fatal("RunContainer never heartbeated")
	}
}

func TestRunContainerForManagedRedis(t *testing.T) {
	e := newEnv(t)
	e.acts.Cfg.ExposeManagedPorts = true
	e.docker.used = map[int]bool{20000: true}
	j := e.job(t, store.TypeRedis)
	e.run(t, e.acts.EnsureCredentials, j)

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n", Volume: "cp-vol-" + j.ServiceID})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	s := e.docker.specs[0]
	if s.Image != "redis:7-alpine" || s.ContainerPort != 6379 || s.Volume != "cp-vol-"+j.ServiceID ||
		s.VolumeTarget != "/data" || len(s.Tmpfs) != 0 || !slices.Equal(s.CapAdd, []string{"DAC_OVERRIDE"}) ||
		!strings.Contains(strings.Join(s.Cmd, " "), `--requirepass "$REDIS_PASSWORD"`) || len(s.Env["REDIS_PASSWORD"]) != 32 {
		t.Fatalf("spec %+v", s)
	}
	if strings.Contains(strings.Join(s.Cmd, " "), "appendonly") {
		t.Fatalf("redis must run with RDB defaults (Node parity), cmd %v", s.Cmd)
	}
	if res.HostPort == nil || *res.HostPort != 20001 || s.PublishPort != 20001 {
		t.Fatalf("result %+v", res)
	}
}

func TestRunContainerForManagedMongo(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeMongoDB)
	e.run(t, e.acts.EnsureCredentials, j)

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n", Volume: "cp-vol-" + j.ServiceID})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	s := e.docker.specs[0]
	if s.Image != "mongo:7" || s.ContainerPort != 27017 || s.VolumeTarget != "/data/db" ||
		!slices.Equal(s.Tmpfs, []string{"/data/configdb"}) || !slices.Equal(s.CapAdd, []string{"CHOWN", "SETUID", "SETGID", "DAC_READ_SEARCH"}) ||
		s.Env["MONGO_INITDB_ROOT_USERNAME"] != "admin" || len(s.Env["MONGO_INITDB_ROOT_PASSWORD"]) != 32 {
		t.Fatalf("spec %+v", s)
	}
	if res.HostPort != nil {
		t.Fatalf("ports are not exposed by default: %+v", res)
	}
	// the shared Managed map must not be mutated by a run
	if len(Managed[store.TypeMongoDB].Env) != 1 {
		t.Fatalf("Managed mongo env mutated: %v", Managed[store.TypeMongoDB].Env)
	}
}

func TestRunContainerWithoutImageIsNonRetryable(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	if _, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"}); !isNonRetryableType(err, "ImageMissing") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunContainerInvalidSpecIsNonRetryableAndHidesValues(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	enc, err := e.acts.Secrets.Encrypt("SECRETBODY") // valid, so the early check passes and Run's own error is mapped
	mustT(t, err)
	mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, "PEM", enc)))
	e.docker.runErr = fmt.Errorf("%w: env var %q: value must be valid UTF-8 without line breaks", runtime.ErrInvalidSpec, "PEM")

	_, err = e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"})
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || !appErr.NonRetryable() || appErr.Type() != "InvalidSpec" || appErr.Unwrap() != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "SECRETBODY") || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("err = %v", err)
	}

	e.docker.runErr = errors.New("docker run: daemon unavailable")
	if _, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"}); err == nil || isNonRetryable(err) {
		t.Fatalf("other run errors stay retryable, got %v", err)
	}
}

func TestAllocatePortWarnsWhenDockerScanFails(t *testing.T) {
	e := newEnv(t)
	e.acts.Cfg.ExposeManagedPorts = true
	e.docker.usedErr = errors.New("docker down")
	j := e.job(t, store.TypeRedis)
	e.run(t, e.acts.EnsureCredentials, j)

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n", Volume: "v"})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	if res.HostPort == nil || *res.HostPort != 20000 {
		t.Fatalf("a failed scan falls back to the database view: %+v", res)
	}
	if log := e.logBuf.String(); !strings.Contains(log, "level=WARN") || !strings.Contains(log, "docker down") {
		t.Fatalf("expected a warning, log: %s", log)
	}
}

func TestWaitHealthy(t *testing.T) {
	e := newEnv(t)
	e.docker.probeFail = 2
	if _, err := e.run(t, e.acts.WaitHealthy, WaitInput{Network: "n", Host: "cp-d1", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	if len(e.docker.calls) != 3 {
		t.Fatalf("probes = %v", e.docker.calls)
	}

	e.docker.probeFail = -1
	_, err := e.run(t, e.acts.WaitHealthy, WaitInput{Network: "n", Host: "cp-d1", Port: 3000})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetirePreviousRemovesOlderContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	old, err := e.st.CreateDeployment(ctx, f.ServiceID, "v1")
	mustT(t, err)
	cur, err := e.st.CreateDeployment(ctx, f.ServiceID, "v2")
	mustT(t, err)
	for _, d := range []store.Deployment{old, cur} {
		mustT(t, e.st.MarkDeployed(ctx, d.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1}))
	}

	if _, err := e.run(t, e.acts.RetirePrevious, RetireInput{ServiceID: f.ServiceID, KeepDeploymentID: cur.ID}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.docker.calls, []string{"remove cp-" + old.ID}) {
		t.Fatalf("calls %v", e.docker.calls)
	}
	o, err := e.st.GetDeployment(ctx, old.ID)
	mustT(t, err)
	c, err := e.st.GetDeployment(ctx, cur.ID)
	mustT(t, err)
	if o.StoppedAt == nil || c.StoppedAt != nil {
		t.Fatalf("old stopped=%v cur stopped=%v", o.StoppedAt, c.StoppedAt)
	}
}

// STAFF-3 (T23r1): a Remove that fails midway leaves the work retryable, and
// the retry ends with every older row stopped.
func TestRetirePreviousRetriesAfterRemoveFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	var olds []store.Deployment
	for _, sha := range []string{"v1", "v2"} {
		d, err := e.st.CreateDeployment(ctx, f.ServiceID, sha)
		mustT(t, err)
		mustT(t, e.st.MarkDeployed(ctx, d.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1}))
		olds = append(olds, d)
	}
	cur, err := e.st.CreateDeployment(ctx, f.ServiceID, "v3")
	mustT(t, err)
	mustT(t, e.st.MarkDeployed(ctx, cur.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1}))
	e.docker.removeErr = map[string]int{ContainerName(olds[1].ID): 1}

	in := RetireInput{ServiceID: f.ServiceID, KeepDeploymentID: cur.ID}
	if _, err := e.run(t, e.acts.RetirePrevious, in); err == nil {
		t.Fatal("first attempt should surface the Remove failure")
	}
	if _, err := e.run(t, e.acts.RetirePrevious, in); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for _, d := range olds {
		got, err := e.st.GetDeployment(ctx, d.ID)
		mustT(t, err)
		if got.StoppedAt == nil {
			t.Fatalf("deployment %s not stopped after retry", d.ID)
		}
	}
	if c, err := e.st.GetDeployment(ctx, cur.ID); err != nil || c.StoppedAt != nil {
		t.Fatalf("current row must stay live: %+v %v", c, err)
	}
}

// FWD-1 (T23r1): the workflow ID is unique per deployment, so a claim that
// finds the row already in the target status is a retry of its own lost result.
func TestClaimIsIdempotentForOwnRetryOnly(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	claim := func(from, to string) bool {
		t.Helper()
		val, err := e.run(t, e.acts.Claim, ClaimInput{DeploymentID: j.DeploymentID, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		var won bool
		val.Get(&won)
		return won
	}
	if !claim(store.StatusQueued, store.StatusBuilding) {
		t.Fatal("first claim should win")
	}
	if !claim(store.StatusQueued, store.StatusBuilding) {
		t.Fatal("retry after a lost result must return true")
	}
	if claim(store.StatusQueued, store.StatusDeploying) {
		t.Fatal("a row in another status must not be claimed")
	}
}

// STAFF-2 (T24r1): the deploying claim is service-scoped. A second deployment
// of the service gets a retryable ServiceBusy until the first leaves deploying.
func TestClaimDeployActivity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	other, err := e.st.CreateDeployment(ctx, j.ServiceID, "v2")
	mustT(t, err)
	claim := func(id string) (bool, error) {
		t.Helper()
		val, err := e.run(t, e.acts.ClaimDeploy, id)
		if err != nil {
			return false, err
		}
		var won bool
		mustT(t, val.Get(&won))
		return won, nil
	}
	if won, err := claim(j.DeploymentID); err != nil || !won {
		t.Fatalf("first = %v, %v", won, err)
	}
	if won, err := claim(j.DeploymentID); err != nil || !won {
		t.Fatalf("own retry = %v, %v", won, err)
	}
	_, err = claim(other.ID)
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || appErr.Type() != "ServiceBusy" || appErr.NonRetryable() {
		t.Fatalf("busy must be a retryable ServiceBusy, got %v", err)
	}
	_, err = e.st.MarkFailed(ctx, j.DeploymentID)
	mustT(t, err)
	if won, err := claim(other.ID); err != nil || !won {
		t.Fatalf("after the first left deploying = %v, %v", won, err)
	}
	if won, err := claim(j.DeploymentID); err != nil || won {
		t.Fatalf("a failed row must lose: %v, %v", won, err)
	}
	if won, err := claim("missing"); err != nil || won {
		t.Fatalf("a missing row must lose: %v, %v", won, err)
	}
}

// T24r1 SEC-1: RevertRoute repoints to the newest other live deployment, or
// removes the route when there is none. It is idempotent.
func TestRevertRouteRepointsToNewestOtherDeployedOrRemoves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	mk := func(sha string) store.Deployment {
		d, err := e.st.CreateDeployment(ctx, f.ServiceID, sha)
		mustT(t, err)
		return d
	}
	deploy := func(d store.Deployment, port int) {
		mustT(t, e.st.MarkDeployed(ctx, d.ID, store.DeployedInfo{ContainerID: "c-" + d.ID, ContainerPort: port}))
	}
	revert := func(cur store.Deployment) error {
		_, err := e.run(t, e.acts.RevertRoute, RevertInput{ServiceID: f.ServiceID, DeploymentID: cur.ID})
		return err
	}
	cur := mk("new")

	// No other live deployment: the route is removed.
	if err := revert(cur); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.router.removed, []string{f.ServiceID}) || len(e.router.routes) != 0 {
		t.Fatalf("removed=%v routes=%v", e.router.removed, e.router.routes)
	}

	// Older deployed, a newer one stopped, a failed one with a container, and the current one:
	// only the newest deployed and not stopped row counts.
	older, newer, stopped, failed := mk("v1"), mk("v2"), mk("v3"), mk("v4")
	deploy(older, 3001)
	deploy(newer, 3002)
	deploy(stopped, 3003)
	mustT(t, e.st.MarkStopped(ctx, []string{stopped.ID}))
	_, err := pool.Exec(ctx, `UPDATE "Deployment" SET "containerId" = 'c', "containerPort" = 3004, "status" = 'failed' WHERE "id" = $1`, failed.ID)
	mustT(t, err)
	_, err = pool.Exec(ctx, `UPDATE "Deployment" SET "createdAt" = now() + (CASE "id" WHEN $1 THEN 1 WHEN $2 THEN 2 WHEN $3 THEN 3 ELSE 4 END) * interval '1 second'
	                         WHERE "id" IN ($1, $2, $3, $4)`, older.ID, newer.ID, stopped.ID, failed.ID)
	mustT(t, err)
	e.router.removed = nil
	for range 2 {
		if err := revert(cur); err != nil {
			t.Fatal(err)
		}
	}
	want := f.ServiceID + "->cp-" + newer.ID
	if !slices.Equal(e.router.routes, []string{want, want}) || len(e.router.removed) != 0 {
		t.Fatalf("routes=%v removed=%v", e.router.routes, e.router.removed)
	}
}

func TestRevertRoutePropagatesRouterErrors(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	e.router.err = errors.New("reload failed")
	_, err := e.run(t, e.acts.RevertRoute, RevertInput{ServiceID: j.ServiceID, DeploymentID: j.DeploymentID})
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusActivities(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)

	val, _ := e.run(t, e.acts.Claim, ClaimInput{DeploymentID: j.DeploymentID, From: store.StatusQueued, To: store.StatusBuilding})
	var won bool
	val.Get(&won)
	if !won {
		t.Fatal("claim should win")
	}
	if _, err := e.run(t, e.acts.MarkBuilt, MarkBuiltInput{DeploymentID: j.DeploymentID, Image: "img"}); err != nil {
		t.Fatal(err)
	}
	url := "http://x.localhost"
	if _, err := e.run(t, e.acts.MarkDeployed, DeployedInput{DeploymentID: j.DeploymentID, ContainerID: "c1", ContainerPort: 3000, RuntimeURL: &url}); err != nil {
		t.Fatal(err)
	}
	d, _ := e.st.GetDeployment(ctx, j.DeploymentID)
	if d.Status != store.StatusDeployed || *d.Image != "img" || *d.RuntimeURL != url {
		t.Fatalf("deployed %+v", d)
	}
	if !strings.Contains(scrape(t, e.metrics), `cp_deployments_total{outcome="deployed"} 1`) {
		t.Fatal("deployed outcome not counted")
	}

	val, _ = e.run(t, e.acts.SwitchRoute, RouteInput{ServiceID: "s1", ContainerName: "cp-d1", Port: 3000})
	var got string
	val.Get(&got)
	if got != "http://s1.localhost" || e.router.routes[0] != "s1->cp-d1" {
		t.Fatalf("url=%s routes=%v", got, e.router.routes)
	}
	e.run(t, e.acts.StartLogStream, StreamInput{DeploymentID: "d1", ContainerID: "c1"})
	if e.logs.started[0] != "d1/c1" {
		t.Fatalf("logs %v", e.logs.started)
	}
}
func TestMarkFailedLeavesDeployedRowAndCounter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	mustT(t, e.st.MarkDeployed(ctx, j.DeploymentID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1}))

	if _, err := e.run(t, e.acts.MarkFailed, j.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.st.GetDeployment(ctx, j.DeploymentID); d.Status != store.StatusDeployed {
		t.Fatalf("status %s", d.Status)
	}
	if strings.Contains(scrape(t, e.metrics), `cp_deployments_total{outcome="failed"} 1`) {
		t.Fatal("a deployed row must not count as failed")
	}
	if _, err := e.run(t, e.acts.MarkFailed, "unknown"); err != nil {
		t.Fatalf("unknown id is not an error: %v", err)
	}
	if strings.Contains(scrape(t, e.metrics), `outcome="failed"} 1`) {
		t.Fatal("an unknown id must not count as failed")
	}

	other := e.job(t, store.TypeNodejs)
	if ok, _ := e.st.ClaimDeployment(ctx, other.DeploymentID, store.StatusQueued, store.StatusDeploying); !ok {
		t.Fatal("claim")
	}
	if _, err := e.run(t, e.acts.MarkFailed, other.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.st.GetDeployment(ctx, other.DeploymentID); d.Status != store.StatusFailed {
		t.Fatalf("status %s", d.Status)
	}
	if !strings.Contains(scrape(t, e.metrics), `cp_deployments_total{outcome="failed"} 1`) {
		t.Fatal("a changed row counts once")
	}
}

func TestMarkBuiltAndMarkDeployedMissingRowAreNonRetryable(t *testing.T) {
	e := newEnv(t)
	if _, err := e.run(t, e.acts.MarkBuilt, MarkBuiltInput{DeploymentID: "missing", Image: "i"}); !isNonRetryableType(err, "NotFound") {
		t.Fatalf("MarkBuilt: %v", err)
	}
	_, err := e.run(t, e.acts.MarkDeployed, DeployedInput{DeploymentID: "missing", ContainerID: "c", ContainerPort: 1})
	if !isNonRetryableType(err, "NotFound") {
		t.Fatalf("MarkDeployed: %v", err)
	}
}

// Node-era EnvVar rows were never validated. A key or value the runtime would
// refuse must fail the deployment fast (non-retryable), name the key, never
// echo the value, and make no docker call at all (not even a pull).
func TestRunContainerNodeEraInvalidEnvFailsFastNamingKey(t *testing.T) {
	cases := []struct{ name, key, val, secret string }{
		{"space key", "A B", "hunter2-secret", "hunter2-secret"},
		{"equals key", "A=B", "eq-secret", "eq-secret"},
		{"hash key", "#X", "dot-secret", "dot-secret"},
		{"newline value", "PEM", "-----BEGIN-----\nSECRETBODY", "SECRETBODY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			j := e.job(t, store.TypeNodejs)
			image := "reg:5001/" + j.ServiceID + ":main"
			j.Image = &image
			enc, err := e.acts.Secrets.Encrypt(tc.val)
			mustT(t, err)
			mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, tc.key, enc)))

			_, err = e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"})
			if !isNonRetryableType(err, "InvalidSpec") {
				t.Fatalf("want non-retryable InvalidSpec, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("message must name the key and hide the value: %v", err)
			}
			if len(e.docker.calls) != 0 || len(e.docker.specs) != 0 {
				t.Fatalf("no docker call may happen: calls=%v", e.docker.calls)
			}
		})
	}
}

func TestRunContainerAcceptsLowerCaseAndDigitKeys(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	for _, k := range []string{"api_key2", "_x9", "Mixed_Case_1", "BAD-KEY", "my.key", "1ST"} {
		enc, err := e.acts.Secrets.Encrypt("v")
		mustT(t, err)
		mustT(t, upsertErr(e.st.UpsertEnvVar(ctx, j.ServiceID, k, enc)))
	}
	if _, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n"}); err != nil {
		t.Fatal(err)
	}
}
