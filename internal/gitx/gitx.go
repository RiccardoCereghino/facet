// Package gitx wraps the git CLI.
//
// facet shells out rather than using a pure-Go git library, deliberately: it
// needs Git-LFS, credential helpers, SSH-agent auth, and the --local hardlink
// clone optimisation, none of which go-git provides.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner runs git. The interface exists so callers can be tested against a fake.
type Runner interface {
	// Run executes git in dir (empty for the working directory) with extra
	// environment entries, returning trimmed stdout.
	Run(dir string, env []string, args ...string) (string, error)
}

// Git is the real implementation.
type Git struct{}

// Error carries git's stderr, which is where the useful part always is.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// Run executes git, optionally in dir and with extra env entries. Passing env
// per-command means GIT_LFS_SKIP_SMUDGE is scoped to the one clone that wants
// it, rather than mutated globally and unset in a defer.
func (Git) Run(dir string, env []string, args ...string) (string, error) {
	return run(context.Background(), dir, env, args...)
}

// RunTimeout behaves like Run but kills git if it has not finished within
// timeout. Local git operations return in milliseconds; a network operation
// (fetch) against a blackholed connection or captive portal does not return
// at all. Callers that must not hang forever on the latter use this instead
// of Run.
func (Git) RunTimeout(dir string, env []string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := run(ctx, dir, env, args...)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("git %s: timed out after %s", strings.Join(args, " "), timeout)
	}
	return out, err
}

// waitDelay bounds how long Wait() will wait for the stdout/stderr pipes to
// close once a context deadline kills the process. Killing `git fetch` does
// not kill an orphaned grandchild -- a remote helper (ssh, git-remote-ext,
// git-remote-https) that inherited the pipe and is still running -- so
// without this, Wait blocks until that grandchild exits on its own, which
// defeats the entire point of a bounded RunTimeout: a hung transport hangs
// the caller exactly as before, just with an extra unread context error.
const waitDelay = 2 * time.Second

func run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if _, ok := ctx.Deadline(); ok {
		cmd.WaitDelay = waitDelay
		configureCancel(cmd)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return strings.TrimSpace(stdout.String()), &Error{Args: args, ExitCode: code, Stderr: stderr.String()}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// IsRepo reports whether dir contains a git repository.
func IsRepo(dir string) bool {
	fi, err := os.Stat(dir + string(os.PathSeparator) + ".git")
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular()) // .git is a file in a worktree
}

// Origin returns dir's `origin` remote URL, or "" when dir is not a repo or has
// no origin.
func Origin(r Runner, dir string) string {
	if !IsRepo(dir) {
		return ""
	}
	out, err := r.Run(dir, nil, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// RemoteURL returns the URL of the named remote, and whether it exists.
func RemoteURL(r Runner, dir, remote string) (string, bool) {
	out, err := r.Run(dir, nil, "remote", "get-url", remote)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// CloneOptions controls Clone.
type CloneOptions struct {
	// SkipLFS fetches Git-LFS pointers instead of blobs.
	SkipLFS bool
	// SetOriginTo, when non-empty, repoints origin after cloning. Used when the
	// clone source is a local mirror but pushes must reach the forge.
	SetOriginTo string
}

// Clone clones src into dst, then checks out HEAD.
//
// Two details are load-bearing and were learned the hard way:
//
//   - core.longpaths, because some repos carry paths past Windows' 260-char
//     MAX_PATH. It is set on the command *and* persisted into the clone.
//   - --no-checkout followed by `reset --hard`, because git refuses to run an
//     active hook during clone (GIT_CLONE_PROTECTION_ACTIVE) and git-lfs installs
//     a post-checkout hook. Populating the tree afterwards sidesteps that without
//     weakening any git safety flag.
//
// When SetOriginTo is set, origin is repointed *before* the checkout, so that an
// LFS smudge pulls blobs from the forge rather than from a blob-less mirror.
func Clone(r Runner, src, dst string, opt CloneOptions) error {
	var env []string
	if opt.SkipLFS {
		env = append(env, "GIT_LFS_SKIP_SMUDGE=1")
	}
	if _, err := r.Run("", env,
		"-c", "core.longpaths=true", "clone", "--no-checkout",
		"-c", "core.longpaths=true", src, dst,
	); err != nil {
		return err
	}
	if opt.SetOriginTo != "" {
		if _, err := r.Run(dst, nil, "remote", "set-url", "origin", opt.SetOriginTo); err != nil {
			return err
		}
	}
	if _, err := r.Run(dst, env, "reset", "--hard", "--quiet", "HEAD"); err != nil {
		return err
	}
	return nil
}
