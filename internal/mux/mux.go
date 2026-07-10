// Package mux drives the terminal multiplexer a spawned workspace opens in.
//
// The multiplexer is a convenience, never a dependency: a workspace is fully
// created -- clones, branch, CLAUDE.md -- before anything here is attempted, and
// every failure below degrades to printing the command you could have run.
// zellij on Windows is a community fork, so this assumes nothing about it that
// has not been checked with Available().
package mux

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed layout.kdl.tmpl
var defaultLayout string

// Session describes the multiplexer session for one issue workspace.
type Session struct {
	Name      string // the session name, and the workspace directory name
	Workspace string // absolute path to the workspace
	HomeDir   string // absolute path to the clone holding the issue branch
	Number    int
	// Agent is the command the first pane runs. Empty means an ordinary shell.
	Agent string
	// Override is a layout file to use instead of the built-in one. Ignored when
	// empty or unreadable.
	Override string
}

// defaultShell is what a pane runs when no agent command is configured.
func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "pwsh"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "bash"
}

// Launcher is one multiplexer.
type Launcher interface {
	// Name is what to call it in output.
	Name() string
	// Available reports whether it is installed and functioning.
	Available() bool
	// Live reports whether a session of that name is currently running.
	Live(session string) bool
	// Start creates the session and attaches. It blocks until you detach.
	Start(s Session) error
	// Attach joins an existing session.
	Attach(name string) error
	// Kill removes the session. A missing session is not an error.
	Kill(name string) error
	// AttachCommand is what a human would type to join.
	AttachCommand(name string) string
}

// Pick returns the best available launcher, or nil.
func Pick() Launcher {
	for _, l := range []Launcher{Zellij{}, WindowsTerminal{}} {
		if l.Available() {
			return l
		}
	}
	return nil
}

// ByName returns a specific launcher, or nil when it is unavailable.
func ByName(name string) Launcher {
	switch strings.ToLower(name) {
	case "zellij":
		if l := (Zellij{}); l.Available() {
			return l
		}
	case "wt", "windows-terminal":
		if l := (WindowsTerminal{}); l.Available() {
			return l
		}
	case "none", "off":
		return nil
	}
	return nil
}

// -----------------------------------------------------------------------------

// Zellij drives the zellij multiplexer. One session per issue: `zellij
// list-sessions` is then the dashboard of running agents, attaching joins
// exactly the one you want, and reaping one touches no other.
type Zellij struct{}

func (Zellij) Name() string { return "zellij" }

func (Zellij) Available() bool {
	return exec.Command("zellij", "--version").Run() == nil
}

// Live reports whether the named session exists and has not exited. zellij keeps
// exited sessions listed, marked "(EXITED ...)", so presence alone is not enough.
func (Zellij) Live(session string) bool {
	out, err := exec.Command("zellij", "list-sessions", "--no-formatting").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != session {
			continue
		}
		return !strings.Contains(line, "EXITED")
	}
	return false
}

// Start attaches to the session, creating it from a layout if it does not exist.
// It blocks: zellij takes over the terminal until you detach.
func (z Zellij) Start(s Session) error {
	if z.Live(s.Name) {
		return z.Attach(s.Name)
	}
	layout, err := writeLayout(s)
	if err != nil {
		return err
	}
	// --new-session-with-layout, not --layout. With --session, `--layout` means
	// "add these tabs to the named session", so it tries to ATTACH and fails with
	// "Session not found" when the session is new. Verified against 0.43.1-win32.
	//
	// There is also no way to create a *background* session from a layout --
	// `attach --create-background` takes no layout -- so this runs in the
	// foreground and holds the terminal until you detach.
	return passthrough("zellij", "--session", s.Name, "--new-session-with-layout", layout)
}

func (Zellij) Attach(name string) error {
	return passthrough("zellij", "attach", name)
}

func (Zellij) Kill(name string) error {
	// -f kills a running session before deleting it. A session that never existed
	// makes this fail, which is not an error worth surfacing.
	_ = exec.Command("zellij", "delete-session", "--force", name).Run()
	return nil
}

func (Zellij) AttachCommand(name string) string { return "zellij attach " + name }

// -----------------------------------------------------------------------------

// WindowsTerminal is the fallback when zellij is unavailable. Its tabs cannot be
// re-attached once closed, which is exactly the property zellij is wanted for --
// so this is a degraded mode, not an equal one.
type WindowsTerminal struct{}

func (WindowsTerminal) Name() string { return "windows-terminal" }

func (WindowsTerminal) Available() bool {
	_, err := exec.LookPath("wt")
	return err == nil
}

// Live always reports false: a Windows Terminal tab is not a session and cannot
// be discovered after the fact.
func (WindowsTerminal) Live(string) bool { return false }

func (WindowsTerminal) Start(s Session) error {
	args := []string{"-w", "facet", "nt", "--title", s.Name, "-d", s.HomeDir}
	if s.Agent != "" {
		args = append(args, s.Agent)
	}
	return exec.Command("wt", args...).Start()
}

func (WindowsTerminal) Attach(string) error {
	return fmt.Errorf("windows-terminal tabs cannot be re-attached once closed")
}

func (WindowsTerminal) Kill(string) error { return nil }

func (WindowsTerminal) AttachCommand(name string) string {
	return "(windows-terminal: reopen manually; tabs cannot be re-attached)"
}

// -----------------------------------------------------------------------------

// passthrough runs cmd wired to this process's terminal.
func passthrough(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// LayoutOverride is the path facet prefers over its built-in layout, when it
// exists. It lives with the other project data, not in the binary.
func LayoutOverride(workspacesRoot string) string {
	return filepath.Join(workspacesRoot, ".tools", "issue-layout.kdl")
}

// writeLayout renders the KDL layout into the workspace and returns its path.
// It lands under .facet/ so `facet reap` removes it with everything else.
func writeLayout(s Session) (string, error) {
	tmpl := defaultLayout
	if s.Override != "" {
		if b, err := os.ReadFile(s.Override); err == nil {
			tmpl = string(b)
		}
	}
	agent := s.Agent
	if agent == "" {
		agent = defaultShell()
	}
	r := strings.NewReplacer(
		"__CWD__", kdlPath(s.HomeDir),
		"__WS__", kdlPath(s.Workspace),
		"__AGENT__", agent,
		"__NUM__", fmt.Sprint(s.Number),
	)
	dir := filepath.Join(s.Workspace, ".facet")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "layout.kdl")
	if err := os.WriteFile(path, []byte(r.Replace(tmpl)), 0o666); err != nil {
		return "", err
	}
	return path, nil
}

// kdlPath escapes a filesystem path for a KDL string. Windows separators are
// escape characters in KDL, so C:\a\b must be written C:\\a\\b.
func kdlPath(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, `\`, `\\`), `"`, `\"`)
}
