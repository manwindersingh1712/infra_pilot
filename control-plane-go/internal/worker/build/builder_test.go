package build

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/testutil"
)

// fakeSha is what the fake rev-parse prints for the "abc1234" ref.
const fakeSha = "abc1234000000000000000000000000000000000\n"

// gitSub returns the git subcommand, skipping the leading "-c k=v" pairs.
func gitSub(args []string) string {
	i := 0
	for i+1 < len(args) && args[i] == "-c" {
		i += 2
	}
	if i < len(args) {
		return args[i]
	}
	return ""
}

// fakeDocker simulates git clone (creates the repo dir with files), an
// existing or missing buildx builder, and captures the Dockerfile at build time.
func fakeDocker(files map[string]string, builderExists bool, dockerfile *string) *testutil.FakeRunner {
	return &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch {
		case c.Name == "git" && gitSub(c.Args) == "clone":
			dir := c.Args[len(c.Args)-1]
			_ = os.MkdirAll(dir, 0o750)
			for name, body := range files {
				_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
			}
		case c.Name == "git" && gitSub(c.Args) == "fetch":
			return "", errors.New("shallow fetch failed") // best effort: logged and ignored
		case c.Name == "git" && gitSub(c.Args) == "rev-parse":
			return fakeSha, nil
		case c.Name == "docker" && c.Args[0] == "buildx" && c.Args[1] == "inspect" && !builderExists:
			return "", errors.New("no builder \"cp-builder\" found")
		case c.Name == "docker" && c.Args[0] == "buildx" && c.Args[1] == "build":
			b, _ := os.ReadFile(filepath.Join(c.Dir, "Dockerfile"))
			*dockerfile = string(b)
		}
		return "", nil
	}}
}

func newBuilder(r *testutil.FakeRunner) Builder {
	return Builder{R: r, Name: "cp-builder", Memory: "2g", CPUs: 2, Log: slog.New(slog.DiscardHandler)}
}

func TestBuildAndPushCreatesBuilderAndGeneratesDockerfile(t *testing.T) {
	var dockerfile string
	r := fakeDocker(map[string]string{"package.json": `{"scripts":{"start":"node app.js"}}`}, false, &dockerfile)
	wd := filepath.Join(t.TempDir(), "dep1")

	err := newBuilder(r).BuildAndPush(context.Background(), Spec{
		RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "abc1234",
		ImageTag: "localhost:5001/svc:abc1234", WorkDir: wd, ServiceType: "nodejs",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(wd, "repo")
	safe := "-c protocol.allow=never -c protocol.https.allow=always -c credential.helper= -c core.hooksPath=/dev/null -c submodule.recurse=false"
	want := []string{
		"git " + safe + " clone --depth 1 --single-branch --no-tags --branch main -- https://example.com/r.git " + repo,
		"git " + safe + " fetch --depth 50 --no-tags --end-of-options origin abc1234",
		"git " + safe + " switch --detach --end-of-options abc1234",
		"git " + safe + " rev-parse HEAD",
		"docker buildx inspect cp-builder",
		"docker buildx create --name cp-builder --driver docker-container --driver-opt memory=2g --driver-opt cpu-period=100000 --driver-opt cpu-quota=200000 --bootstrap",
		"docker buildx build --builder cp-builder --load -t localhost:5001/svc:abc1234 .",
		"docker push localhost:5001/svc:abc1234",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(dockerfile, `CMD ["sh", "-c", "npm start"]`) {
		t.Fatalf("generated Dockerfile:\n%s", dockerfile)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatal("work dir should be removed")
	}
}

func TestBuildAndPushReusesBuilderAndKeepsDockerfile(t *testing.T) {
	var dockerfile string
	r := fakeDocker(map[string]string{"Dockerfile": "FROM custom"}, true, &dockerfile)
	err := newBuilder(r).BuildAndPush(context.Background(), Spec{
		RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "main", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "nodejs",
	})
	if err != nil || dockerfile != "FROM custom" {
		t.Fatalf("err=%v dockerfile=%q", err, dockerfile)
	}
	for _, c := range r.Commands() {
		if strings.HasPrefix(c, "docker buildx create") {
			t.Fatal("existing builder must be reused")
		}
	}
}

func TestBuildAndPushStopsOnBuildFailure(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Name == "git" && gitSub(c.Args) == "rev-parse" {
			return fakeSha, nil
		}
		if c.Name == "docker" && len(c.Args) > 1 && c.Args[1] == "build" {
			return "", errors.New("build broke")
		}
		return "", nil
	}}
	err := newBuilder(r).BuildAndPush(context.Background(), Spec{RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "abc1234", ImageTag: "i", WorkDir: t.TempDir() + "/w", ServiceType: "docker"})
	if err == nil || !strings.Contains(err.Error(), "build broke") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.Commands() {
		if strings.HasPrefix(c, "docker push") {
			t.Fatal("must not push after failed build")
		}
	}
}

// SEC-1: a repo that ships a dangling Dockerfile symlink must not make the
// worker write through it.
func TestBuildAndPushDoesNotWriteThroughDanglingDockerfileSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "victim")
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Name == "git" && gitSub(c.Args) == "rev-parse" {
			return fakeSha, nil
		}
		if c.Name == "git" && gitSub(c.Args) == "clone" {
			dir := c.Args[len(c.Args)-1]
			_ = os.MkdirAll(dir, 0o750)
			_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0o600)
			if err := os.Symlink(outside, filepath.Join(dir, "Dockerfile")); err != nil {
				t.Fatal(err)
			}
		}
		return "", nil
	}}
	err := newBuilder(r).BuildAndPush(context.Background(), Spec{
		RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "abc1234", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "nodejs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

var rejectedSources = []Spec{
	{RepoURL: "file:///etc", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "ext::sh -c id", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "ssh://git@h/x", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "git@github.com:x/y.git", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "http://h/x", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "https://-oProxyCommand=x/y", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "https://h/x y", Branch: "main", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "--upload-pack=x", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "-b", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "a..b", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "a/", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "x.lock", CommitSha: "abc1234"},
	{RepoURL: "https://h/x", Branch: "main", CommitSha: "--upload-pack=touch /tmp/pwn"},
	{RepoURL: "https://h/x", Branch: "main", CommitSha: "-x12"},
	{RepoURL: "https://h/x", Branch: "main", CommitSha: "ab"},
	{RepoURL: "https://h/x", Branch: "main", CommitSha: "a..bc"},
	{RepoURL: "https://h/x", Branch: "main", CommitSha: "feature/x"},
}

func TestBuildAndPushRejectsUnsafeSource(t *testing.T) {
	for _, s := range rejectedSources {
		r := &testutil.FakeRunner{}
		s.ImageTag, s.WorkDir, s.ServiceType = "img", t.TempDir()+"/w", "docker"
		err := newBuilder(r).BuildAndPush(context.Background(), s)
		if !errors.Is(err, gitsrc.ErrInvalid) {
			t.Errorf("%+v: err = %v", s, err)
		}
		if cmds := r.Commands(); len(cmds) != 0 {
			t.Errorf("%+v: ran commands %v", s, cmds)
		}
		if _, err := os.Stat(s.WorkDir); !os.IsNotExist(err) {
			t.Errorf("%+v: work dir was touched", s)
		}
	}
}

func TestBuildAndPushHidesRepoCredentials(t *testing.T) {
	for _, url := range []string{"https://user:ghp_SECRET1@example.com/r.git", "https://ghp_TOKEN2@example.com/r.git"} {
		var logs bytes.Buffer
		r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
			if c.Name == "git" && gitSub(c.Args) == "clone" {
				return "", errors.New("fatal: unable to access '" + url + "/': 403")
			}
			return "", nil
		}}
		b := newBuilder(r)
		b.Log = slog.New(slog.NewJSONHandler(&logs, nil))
		err := b.BuildAndPush(context.Background(), Spec{RepoURL: url, Branch: "main", CommitSha: "abc1234", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "docker"})
		if err == nil || strings.Contains(err.Error(), "ghp_") || strings.Contains(logs.String(), "ghp_") {
			t.Fatalf("leak: err=%v logs=%s", err, logs.String())
		}

		// success run: logs carry the redacted form only
		logs.Reset()
		var df string
		b = newBuilder(fakeDocker(nil, true, &df))
		b.Log = slog.New(slog.NewJSONHandler(&logs, nil))
		if err := b.BuildAndPush(context.Background(), Spec{RepoURL: url, Branch: "main", CommitSha: "abc1234", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "docker"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.String(), "ghp_") || !strings.Contains(logs.String(), "****@example.com") {
			t.Fatalf("logs: %s", logs.String())
		}
	}
}

func TestBuildAndPushReturnsContextErrorOnCloneFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		cancel()
		return "", errors.New("signal: killed")
	}}
	err := newBuilder(r).BuildAndPush(ctx, Spec{RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "abc1234", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "docker"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func gitSpec(ref string) Spec {
	return Spec{RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: ref, ImageTag: "img", ServiceType: "docker"}
}

func countSub(cmds []string, sub string) int {
	n := 0
	for _, c := range cmds {
		if strings.Contains(c, " "+sub+" ") || strings.HasSuffix(c, " "+sub) {
			n++
		}
	}
	return n
}

func TestBuildAndPushSwitchFailureFails(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Name == "git" && gitSub(c.Args) == "switch" {
			return "", errors.New("fatal: invalid reference: abc1234 for https://u:ghp_SW@example.com/r.git")
		}
		return "", nil
	}}
	s := gitSpec("abc1234")
	s.RepoURL = "https://u:ghp_SW@example.com/r.git"
	s.WorkDir = t.TempDir() + "/w"
	err := newBuilder(r).BuildAndPush(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "git switch") || strings.Contains(err.Error(), "ghp_") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.Commands() {
		if strings.HasPrefix(c, "docker") {
			t.Fatalf("docker ran after failed switch: %s", c)
		}
	}
}

func TestBuildAndPushSwitchFailureReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if gitSub(c.Args) == "switch" {
			cancel()
			return "", errors.New("signal: killed")
		}
		return "", nil
	}}
	s := gitSpec("abc1234")
	s.WorkDir = t.TempDir() + "/w"
	if err := newBuilder(r).BuildAndPush(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildAndPushVerifiesSha(t *testing.T) {
	run := func(ref, revParse string) (*testutil.FakeRunner, error) {
		r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
			if gitSub(c.Args) == "rev-parse" {
				return revParse + "\n", nil
			}
			return "", nil
		}}
		s := gitSpec(ref)
		s.WorkDir = t.TempDir() + "/w"
		return r, newBuilder(r).BuildAndPush(context.Background(), s)
	}
	if _, err := run("abc1234", "ffffffff00000000000000000000000000000000"); err == nil || !strings.Contains(err.Error(), "rev-parse") {
		t.Fatalf("mismatch must fail, err = %v", err)
	}
	if _, err := run("ABC1234", "abc1234000000000000000000000000000000000"); err != nil {
		t.Fatalf("case-insensitive match: %v", err)
	}
	if r, err := run("v1.2.3", ""); err != nil || countSub(r.Commands(), "rev-parse") != 0 || countSub(r.Commands(), "switch") != 1 {
		t.Fatalf("tag ref: err=%v cmds=%v", err, r.Commands())
	}
	for _, ref := range []string{"main", "HEAD"} {
		r, err := run(ref, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, sub := range []string{"fetch", "switch", "rev-parse"} {
			if n := countSub(r.Commands(), sub); n != 0 {
				t.Errorf("ref %s ran %s", ref, sub)
			}
		}
	}
}

// blockingRunner blocks every git call until its ctx is done, like a hung clone.
type blockingRunner struct {
	gitStep string // subcommand that blocks
	calls   []string
}

func (r *blockingRunner) Run(ctx context.Context, _ string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name)
	if name == "git" && gitSub(args) == r.gitStep {
		<-ctx.Done()
		return "", errors.New("signal: killed")
	}
	if name == "git" && gitSub(args) == "rev-parse" {
		return fakeSha, nil
	}
	return "", nil
}

func TestBuildAndPushGitStepTimesOut(t *testing.T) {
	for _, step := range []string{"clone", "switch"} {
		r := &blockingRunner{gitStep: step}
		b := Builder{R: r, Name: "cp-builder", Memory: "2g", CPUs: 2, Log: slog.New(slog.DiscardHandler), gitStepTimeout: 50 * time.Millisecond}
		s := gitSpec("abc1234")
		s.WorkDir = t.TempDir() + "/w"
		done := make(chan error, 1)
		go func() { done <- b.BuildAndPush(context.Background(), s) }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "git "+step) || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("%s: err = %v", step, err)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatalf("%s: step timeout must not look like caller cancellation", step)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: step did not time out", step)
		}
		for _, c := range r.calls {
			if c == "docker" {
				t.Fatalf("%s: docker ran after timeout", step)
			}
		}
	}
}

func TestStepTimeoutDefaults(t *testing.T) {
	if (Builder{}).stepTimeout() != 5*time.Minute || pushTimeout != 10*time.Minute {
		t.Fatal("default git step timeout is 5 min, push 10 min")
	}
}

// SEC-6: every error BuildAndPush returns is scrubbed, not just clone.
func TestBuildAndPushScrubsEveryReturnedError(t *testing.T) {
	url := "https://user:ghp_SECRET1@example.com/r.git?token=ghp_QTOK"
	for _, failing := range []string{"fetch-ignored", "buildx-inspect-create", "build", "push"} {
		r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
			leak := errors.New("boom " + url + " ghp_SECRET1 ghp_QTOK")
			switch {
			case c.Name == "git" && gitSub(c.Args) == "rev-parse":
				return fakeSha, nil
			case failing == "buildx-inspect-create" && c.Name == "docker" && c.Args[1] == "inspect":
				return "", errors.New("no builder")
			case failing == "buildx-inspect-create" && c.Name == "docker" && c.Args[1] == "create":
				return "", leak
			case failing == "build" && c.Name == "docker" && c.Args[1] == "build":
				return "", leak
			case failing == "push" && c.Name == "docker" && c.Args[0] == "push":
				return "", leak
			}
			return "", nil
		}}
		s := gitSpec("abc1234")
		s.RepoURL, s.WorkDir = url, t.TempDir()+"/w"
		err := newBuilder(r).BuildAndPush(context.Background(), s)
		if failing == "fetch-ignored" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil || strings.Contains(err.Error(), "ghp_") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("%s: err = %v", failing, err)
		}
	}
}

func TestBuildAndPushScrubsFetchWarning(t *testing.T) {
	var logs bytes.Buffer
	url := "https://user:ghp_SECRET1@example.com/r.git"
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch gitSub(c.Args) {
		case "fetch":
			return "", errors.New("fatal: unable to access '" + url + "/': 403")
		case "rev-parse":
			return fakeSha, nil
		}
		return "", nil
	}}
	b := newBuilder(r)
	b.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	s := gitSpec("abc1234")
	s.RepoURL, s.WorkDir = url, t.TempDir()+"/w"
	if err := b.BuildAndPush(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "ghp_") || !strings.Contains(logs.String(), "git fetch failed") {
		t.Fatalf("logs: %s", logs.String())
	}
}
