// Package gitxtest is one configurable fake gitx.Runner, replacing three
// independent ad hoc fakes (in internal/mirror and internal/workspace) that
// differed only in which call they made fail.
//
// It is nested under gitx rather than exported from it, mirroring
// net/http/httptest under net/http: an exported non-test helper living in
// gitx itself would compile into every binary that imports gitx, including
// the production one, just to serve callers who will never fake anything.
package gitxtest

import (
	"time"

	"github.com/RiccardoCereghino/facet/internal/gitx"
)

// timeoutRunner is gitx.Git's real RunTimeout, checked for at runtime so a
// Runner wrapping the real implementation preserves genuine timeout
// enforcement rather than silently downgrading it to Run.
type timeoutRunner interface {
	RunTimeout(dir string, env []string, timeout time.Duration, args ...string) (string, error)
}

// Runner is a configurable fake gitx.Runner. The zero value succeeds on
// every call and records it in Calls.
type Runner struct {
	// Real, when set, is delegated to for any call Fail does not match --
	// models a fake that is real except for the one path under test.
	Real gitx.Runner
	// Fail, when it returns true for a call's argv, makes that call return
	// Err instead of succeeding or delegating to Real.
	Fail func(args []string) bool
	// Err is returned for a call Fail matches.
	Err error
	// OnCall, when set, runs for every call before Fail is checked -- used to
	// fabricate whatever files the code under test expects a real git
	// invocation to have produced (a clone's HEAD, a stamp file).
	OnCall func(dir string, args []string)

	// Calls records every call's argv, in order.
	Calls [][]string
}

func (r *Runner) record(dir string, args []string) (failed bool, err error) {
	r.Calls = append(r.Calls, args)
	if r.OnCall != nil {
		r.OnCall(dir, args)
	}
	if r.Fail != nil && r.Fail(args) {
		return true, r.Err
	}
	return false, nil
}

// Run implements gitx.Runner.
func (r *Runner) Run(dir string, env []string, args ...string) (string, error) {
	if failed, err := r.record(dir, args); failed {
		return "", err
	}
	if r.Real != nil {
		return r.Real.Run(dir, env, args...)
	}
	return "", nil
}

// RunTimeout implements the timeoutRunner interface some callers duck-type
// for, so a Runner can stand in wherever a real gitx.Git could.
func (r *Runner) RunTimeout(dir string, env []string, timeout time.Duration, args ...string) (string, error) {
	if failed, err := r.record(dir, args); failed {
		return "", err
	}
	if tr, ok := r.Real.(timeoutRunner); ok {
		return tr.RunTimeout(dir, env, timeout, args...)
	}
	if r.Real != nil {
		return r.Real.Run(dir, env, args...)
	}
	return "", nil
}

// Call returns the first recorded call whose argv satisfies match, or nil.
func (r *Runner) Call(match func(args []string) bool) []string {
	for _, c := range r.Calls {
		if match(c) {
			return c
		}
	}
	return nil
}
