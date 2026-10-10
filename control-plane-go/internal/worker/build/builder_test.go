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

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/testutil"
)

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
			return "", errors.New("shallow fetch failed") // ignored, like Node
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
	safe := "-c protocol.allow=never -c protocol.https.allow=always -c credential.helper="
	want := []string{
		"git " + safe + " clone --depth 1 --branch main -- https://example.com/r.git " + repo,
		"git " + safe + " fetch --depth 50 --end-of-options origin abc1234",
		"git checkout --end-of-options abc1234",
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
