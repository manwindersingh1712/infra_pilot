package build

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/runtime"
)

type Spec struct {
	RepoURL     string
	Branch      string
	CommitSha   string
	ImageTag    string // <registry>/<serviceId>:<commitSha>
	WorkDir     string // <BUILD_WORKDIR>/<deploymentId>, removed afterwards
	ServiceType string
}

// Builder builds images in an isolated buildx builder (BuildKit in its own
// container with CPU/memory limits) instead of on the host daemon.
type Builder struct {
	R      runtime.Runner
	Name   string // buildx builder name, e.g. "cp-builder"
	Memory string // BuildKit container memory limit, e.g. "2g"
	CPUs   int    // BuildKit container CPU limit
	Log    *slog.Logger

	gitStepTimeout time.Duration // per git step; zero means defaultGitStepTimeout
}

const (
	defaultGitStepTimeout = 5 * time.Minute
	pushTimeout           = 10 * time.Minute
)

func (b Builder) stepTimeout() time.Duration {
	if b.gitStepTimeout > 0 {
		return b.gitStepTimeout
	}
	return defaultGitStepTimeout
}

// gitSafe restricts git to https and disables credential helpers.
var gitSafe = []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "credential.helper=",
	"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false"}

func gitArgs(a ...string) []string { return append(slices.Clone(gitSafe), a...) }

// EnsureBuilder creates the builder on first use.
func (b Builder) EnsureBuilder(ctx context.Context) error {
	if _, err := b.R.Run(ctx, "", "docker", "buildx", "inspect", b.Name); err == nil {
		return nil
	}
	b.Log.Info("creating buildx builder", "name", b.Name)
	_, err := b.R.Run(ctx, "", "docker", "buildx", "create", "--name", b.Name, "--driver", "docker-container",
		"--driver-opt", "memory="+b.Memory,
		"--driver-opt", "cpu-period=100000",
		"--driver-opt", "cpu-quota="+strconv.Itoa(b.CPUs*100000),
		"--bootstrap")
	return err
}

// writeDockerfile creates a generated Dockerfile inside the clone. Any
// existing entry (including a dangling symlink) means the repo ships its own,
// so nothing is written. Only Lstat and O_EXCL are used: neither follows a
// symlink nor blocks on a FIFO.
func (b Builder) writeDockerfile(repo, content, serviceType string) error {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	defer root.Close()
	if _, err := root.Lstat("Dockerfile"); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("check Dockerfile: %w", err)
		}
		return nil
	}
	b.Log.Info("generating Dockerfile", "serviceType", serviceType)
	f, err := root.OpenFile("Dockerfile", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302 G304 G306 -- BuildKit must read it; generated, not secret; path is a constant inside an os.Root
	if err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	return nil
}

// fail wraps err for the caller with credentials scrubbed. A cancelled or
// expired ctx wins so callers can match context.Canceled.
func (b Builder) fail(ctx context.Context, op string, err error, repoURL string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%s: %s", op, gitsrc.Scrub(err.Error(), repoURL))
}

// git runs one git step under its own timeout. A caller cancellation returns
// the caller's ctx error; a step timeout returns an error naming the step.
func (b Builder) git(ctx context.Context, dir, op, repoURL string, args ...string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, b.stepTimeout())
	defer cancel()
	out, err := b.R.Run(sctx, dir, "git", gitArgs(args...)...)
	if err == nil {
		return out, nil
	}
	if ctx.Err() == nil && sctx.Err() != nil {
		return out, fmt.Errorf("%s: timed out after %s", op, b.stepTimeout())
	}
	return out, b.fail(ctx, op, err, repoURL)
}

// legacyDefaultRef is the Node API placeholder (control-plane/apps/api
// serviceController.ts defaults commitSha to main); remove after the Plan 2 API
// cutover (backlog).
const legacyDefaultRef = "main"

var hexRefRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// checkoutRef moves the clone to s.CommitSha. The branch tip and HEAD need no
// work, and neither does the legacy default ref. Any other ref is fetched (best effort, the object may already be
// present) and then switched to; a failing switch fails the build, because the
// image is pushed under that ref's tag.
func (b Builder) checkoutRef(ctx context.Context, repo string, s Spec) error {
	ref := s.CommitSha
	if ref == s.Branch || ref == "HEAD" {
		return nil
	}
	if ref == legacyDefaultRef {
		b.Log.Warn("legacy default ref \"main\" treated as tip of branch", "branch", s.Branch, "ref", ref)
		return nil
	}
	if _, err := b.git(ctx, repo, "git fetch", s.RepoURL, "fetch", "--depth", "50", "--no-tags", "--end-of-options", "origin", ref); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.Log.Warn("git fetch failed, continuing", "error", err.Error())
	}
	if _, err := b.git(ctx, repo, "git switch", s.RepoURL, "switch", "--detach", "--end-of-options", ref); err != nil {
		return err
	}
	if !hexRefRe.MatchString(ref) {
		return nil
	}
	out, err := b.git(ctx, repo, "git rev-parse", s.RepoURL, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(out)), strings.ToLower(ref)) {
		return errors.New("git rev-parse: checked-out commit does not match the requested commit")
	}
	return nil
}

// BuildAndPush clones the repo, writes a Dockerfile if the type needs one and
// the repo has none, builds in the isolated builder and pushes the image.
func (b Builder) BuildAndPush(ctx context.Context, s Spec) error {
	if err := gitsrc.Validate(s.RepoURL, s.Branch, s.CommitSha); err != nil {
		return err
	}
	if err := gitsrc.ValidImageTag(s.ImageTag, s.CommitSha); err != nil {
		return err
	}
	if err := os.RemoveAll(s.WorkDir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.WorkDir, 0o750); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(s.WorkDir) }()
	repo := filepath.Join(s.WorkDir, "repo")

	b.Log.Info("cloning", "repo", gitsrc.RedactURL(s.RepoURL), "branch", s.Branch)
	if _, err := b.git(ctx, "", "git clone", s.RepoURL, "clone", "--depth", "1", "--single-branch", "--no-tags", "--branch", s.Branch, "--", s.RepoURL, repo); err != nil {
		return err
	}
	if err := b.checkoutRef(ctx, repo, s); err != nil {
		return err
	}

	if content := GenerateDockerfile(s.ServiceType, repo); content != "" {
		if err := b.writeDockerfile(repo, content, s.ServiceType); err != nil {
			return err
		}
	}

	if err := b.EnsureBuilder(ctx); err != nil {
		return b.fail(ctx, "docker buildx create", err, s.RepoURL)
	}
	b.Log.Info("building image", "image", s.ImageTag)
	if _, err := b.R.Run(ctx, repo, "docker", "buildx", "build", "--builder", b.Name, "--load", "-t", s.ImageTag, "."); err != nil {
		return b.fail(ctx, "docker buildx build", err, s.RepoURL)
	}
	b.Log.Info("pushing image", "image", s.ImageTag)
	pctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	if _, err := b.R.Run(pctx, "", "docker", "push", s.ImageTag); err != nil {
		if ctx.Err() == nil && pctx.Err() != nil {
			return fmt.Errorf("docker push: timed out after %s", pushTimeout)
		}
		return b.fail(ctx, "docker push", err, s.RepoURL)
	}
	return nil
}
