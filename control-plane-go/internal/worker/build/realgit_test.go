package build

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"infrapilot/internal/runtime"
)

// realGit skips unless the real git is available and -short is off.
func realGit(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("real git probe skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

// twoCommitRepo creates a throwaway repo (no network) and returns its path and
// the sha of the first commit; HEAD is on the second.
func twoCommitRepo(t *testing.T) (dir, first string) {
	t.Helper()
	ctx := context.Background()
	dir = filepath.Join(t.TempDir(), "src")
	var r runtime.ExecRunner
	run := func(args ...string) string {
		t.Helper()
		out, err := r.Run(ctx, dir, "git", args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	if _, err := r.Run(ctx, "", "git", "init", "-q", "-b", "main", dir); err != nil {
		t.Fatal(err)
	}
	run("commit", "-q", "--allow-empty", "-m", "one")
	first = run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "two")
	if run("rev-parse", "HEAD") == first {
		t.Fatal("expected two commits")
	}
	return dir, first
}

// Probe of the real git: the switch form the builder uses moves HEAD, and the
// hardened flags refuse a file:// clone.
func TestRealGitSwitchDetach(t *testing.T) {
	realGit(t)
	ctx := context.Background()
	dir, first := twoCommitRepo(t)
	b := Builder{R: runtime.ExecRunner{}, Log: slog.New(slog.DiscardHandler)}

	// Full sha and an abbreviated one, through checkoutRef (fetch fails: no origin, ignored).
	for _, ref := range []string{first, first[:10]} {
		if _, err := b.git(ctx, dir, "git switch", "", "switch", "--detach", "--end-of-options", "main"); err != nil {
			t.Fatal(err)
		}
		if err := b.checkoutRef(ctx, dir, Spec{Branch: "main", CommitSha: ref}); err != nil {
			t.Fatalf("checkoutRef(%q): %v", ref, err)
		}
		head, err := b.git(ctx, dir, "git rev-parse", "", "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) != first {
			t.Fatalf("HEAD = %q err=%v, want %s", head, err, first)
		}
	}
	// A ref that is not a commit fails the switch.
	if err := b.checkoutRef(ctx, dir, Spec{Branch: "main", CommitSha: strings.Repeat("0", 40)}); err == nil {
		t.Fatal("switch to a missing commit must fail")
	}
}

func TestRealGitHardenedFlagsRefuseFileClone(t *testing.T) {
	realGit(t)
	ctx := context.Background()
	dir, _ := twoCommitRepo(t)
	dst := filepath.Join(t.TempDir(), "dst")
	_, err := runtime.ExecRunner{}.Run(ctx, "", "git", gitArgs("clone", "--depth", "1", "--single-branch", "--no-tags", "--", "file://"+dir, dst)...)
	if err == nil {
		t.Fatal("file:// clone must be refused by protocol.allow=never")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not allowed") || !strings.HasPrefix(err.Error(), "git clone:") {
		t.Fatalf("err = %v", err)
	}
}
