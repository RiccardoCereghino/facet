// Package gitx wraps the git CLI.
//
// facet shells out rather than using a pure-Go git library, deliberately: it
// needs Git-LFS, credential helpers, SSH-agent auth, and the --local hardlink
// clone optimisation, none of which go-git provides.
package gitx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		}
		return strings.TrimSpace(stdout.String()), &Error{Args: args, ExitCode: code, Stderr: stderr.String()}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
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
