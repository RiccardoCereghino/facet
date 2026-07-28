package mux

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// LiveRoots reports tmux panes, in any session, and processes -- via lsof --
// whose current working directory is rooted inside dir. Live only checks the
// one session named after the workspace; a pane moved elsewhere (tmux's
// link-window does exactly this) would otherwise still hold a process open on
// a directory reap is about to delete, unnoticed.
//
// Both probes are a convenience layered on top of the git-based checks in
// package workspace: a tool that is missing, or a server that is not running,
// is not a failure -- it means there is nothing to report, the same way
// Available and Kill degrade elsewhere in this package.
func (Tmux) LiveRoots(dir string) ([]string, error) {
	var live []string
	live = append(live, tmuxPanesUnder(dir, "")...)
	live = append(live, lsofProcessesUnder(dir)...)
	return live, nil
}

// tmuxPanesUnder lists, as human-readable descriptions naming the target and
// pid, every pane across every session whose pane_current_path is dir or a
// directory beneath it. socket selects an alternate server via `tmux -L
// <socket>`, so tests can target an isolated server; production callers pass
// "" for the default one.
func tmuxPanesUnder(dir, socket string) []string {
	if !(Tmux{}).Available() {
		return nil
	}
	dir = canonicalPath(dir)
	argv := []string{"list-panes", "-a", "-F",
		"#{session_name}:#{window_index}.#{pane_index}\t#{pane_pid}\t#{pane_current_path}"}
	if socket != "" {
		argv = append([]string{"-L", socket}, argv...)
	}
	raw, err := tmuxOutput(argv...)
	if err != nil && len(raw) == 0 {
		// No server running exits non-zero with nothing on stdout -- that is
		// "no panes", not a failure worth surfacing.
		return nil
	}
	var live []string
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		target, pid, path := f[0], f[1], f[2]
		if underRoot(dir, path) {
			live = append(live, "tmux pane "+target+" (pid "+pid+") at "+path)
		}
	}
	return live
}

// lsofProcessesUnder lists, as human-readable descriptions naming the command
// and pid, every process whose current working directory (lsof's "cwd" file
// descriptor) is rooted inside dir. It is the fallback for a live process that
// tmux does not know about at all -- one started outside any pane -- and the
// only probe available on a host with no tmux. It naturally does nothing on
// native Windows, where lsof is not on PATH.
func lsofProcessesUnder(dir string) []string {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil
	}
	// -a ANDs the selections together. Without it, lsof ORs "-d cwd" (every
	// process's cwd, system-wide) with "+D dir" (every open file under dir),
	// which reports every process on the host instead of just the ones rooted
	// in dir.
	raw, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fpcn", "+D", canonicalPath(dir)).Output()
	if err != nil && len(raw) == 0 {
		// lsof exits non-zero with nothing on stdout when nothing matches --
		// that is "no processes", not a failure worth surfacing.
		return nil
	}
	var live []string
	pid, cmdName := "", ""
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = line[1:]
		case 'c':
			cmdName = line[1:]
		case 'n':
			live = append(live, "process "+cmdName+" (pid "+pid+") at "+line[1:])
		}
	}
	return live
}

// underRoot reports whether path is root itself or lies beneath it.
func underRoot(root, path string) bool {
	root, path = canonicalPath(root), canonicalPath(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// canonicalPath resolves symlinks so a directory reached through one (macOS
// routes /tmp and /var through /private) still matches what tmux or lsof
// report, which is already the resolved form. A path that cannot be resolved
// (already gone, or one lsof reported that this process cannot stat) falls
// back to a plain Clean.
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
