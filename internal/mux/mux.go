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
	// AsTab opens the workspace as tabs in the multiplexer session we are already
	// inside, rather than in a session of its own.
	AsTab bool
}

// InSession reports whether this process is already inside a zellij session.
// zellij exports ZELLIJ into everything it spawns.
func InSession() bool { return os.Getenv("ZELLIJ") != "" }

// SessionName is the zellij session we are inside, if any.
func SessionName() string { return os.Getenv("ZELLIJ_SESSION_NAME") }

// ErrGuidance is returned when there is nothing safe to run and the human has to
// act first. Its message says what to do.
type ErrGuidance struct{ Msg string }

func (e *ErrGuidance) Error() string { return e.Msg }

// AutoOpen decides whether to open a freshly spawned workspace straight away,
// and whether to open it as tabs.
//
// The rule: open automatically only when doing so cannot steal the terminal.
// Inside a zellij session new tabs appear alongside what you are already doing,
// so that is safe and is what you almost always want. Starting a *session*, by
// contrast, seizes the terminal until you detach -- so outside zellij, opening
// stays opt-in via --attach.
//
// ownSession forces a separate session even from inside one, which cannot be
// done without detaching first; plan() then returns guidance rather than acting.
func AutoOpen(l Launcher, ownSession bool) (open, asTab bool) {
	if l == nil || l.Name() != "zellij" || !InSession() {
		return false, false
	}
	return true, !ownSession
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

// agentInvocation returns the executable and arguments that start the agent in a
// pane.
//
// The agent is always launched THROUGH a shell, never directly. zellij's Windows
// backend calls CreateProcessW with no shell, so it cannot start a `#!/bin/sh`
// script or a `.cmd` shim -- and `claude`, installed by npm, is exactly that.
// Worse, when the spawn fails the fork PANICS and takes the whole session down.
// So we hand it a real executable and let that run the agent.
//
// An empty exe means we could not find a shell to trust; the caller should open
// a plain pane rather than risk the spawn.
func agentInvocation(agent string) (exe string, args []string) {
	exe = findExecutable(shellCandidates()...)
	if exe == "" {
		return "", nil
	}
	if agent == "" {
		return exe, nil
	}
	if runtime.GOOS == "windows" {
		// -NoExit keeps the pane usable after the agent exits.
		return exe, []string{"-NoLogo", "-NoExit", "-Command", agent}
	}
	return exe, []string{"-lc", agent}
}

func shellCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"pwsh", "powershell", "cmd"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh, "bash", "sh"}
	}
	return []string{"bash", "sh"}
}

// findExecutable returns the absolute path of the first candidate that the OS can
// actually execute. On Windows that means a PE image: LookPath honours PATHEXT,
// but an extensionless npm shim can still win, so the extension is checked.
func findExecutable(candidates ...string) string {
	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(p), ".exe") {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
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

// plan works out how to open a workspace, given where we are standing. It is
// pure so the decision can be tested without a multiplexer; argv is what to run,
// and a non-empty guidance means run nothing and tell the human instead.
//
// zellij sessions do not nest. Attaching from inside one is not a no-op: it
// takes over the client, and if the target is a dead session it resurrects it.
// So from inside a session the only safe moves are to switch to a live one, or
// to pull the workspace in as tabs.
func plan(name, layout string, inSession, live, asTab bool) (argv []string, guidance string) {
	switch {
	case !inSession && live:
		return []string{"attach", name}, ""

	case !inSession && !live:
		// --new-session-with-layout, not --layout. With --session, `--layout`
		// means "add these tabs to the named session", so it tries to attach and
		// dies with "Session not found" when the session is new.
		return []string{"--session", name, "--new-session-with-layout", layout}, ""

	case inSession && live:
		// The workspace already has a session of its own. Rejoin it rather than
		// duplicating its tabs here -- this wins over asTab deliberately.
		return []string{"action", "switch-session", name}, ""

	case inSession && asTab:
		// `--session <existing> --layout <file>` adds the layout's tabs to that
		// session and returns immediately. Naming the session explicitly beats
		// relying on a bare --layout picking up ZELLIJ from the environment, and
		// it is the form that was actually exercised against a live session.
		return []string{"--session", SessionName(), "--layout", layout}, ""

	default: // inSession && !live, and --session was asked for
		return nil, "you are inside zellij session " + SessionName() + ", and " + name +
			" does not exist yet.\n" +
			"  zellij sessions do not nest. Either:\n" +
			"    detach first -- Ctrl+o then d, then re-run this command\n" +
			"    or drop --session, to open it here as tabs"
	}
}

// Start opens the workspace: attaching, switching, or adding tabs as the
// situation allows. It blocks while zellij holds the terminal.
func (z Zellij) Start(s Session) error {
	layout, err := writeLayout(s)
	if err != nil {
		return err
	}
	argv, guidance := plan(s.Name, layout, InSession(), z.Live(s.Name), s.AsTab)
	if guidance != "" {
		return &ErrGuidance{Msg: guidance}
	}
	return passthrough("zellij", argv...)
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
	exe, args := agentInvocation(s.Agent)
	if exe == "" {
		// No trustworthy executable. A pane with no command is a plain shell,
		// which is infinitely better than panicking the user's session.
		exe = defaultShell()
		args = nil
	}
	var argsLine string
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = `"` + kdlPath(a) + `"`
		}
		argsLine = "args " + strings.Join(quoted, " ")
	}
	r := strings.NewReplacer(
		"__CWD__", kdlPath(s.HomeDir),
		"__WS__", kdlPath(s.Workspace),
		"__AGENT_CMD__", kdlPath(exe),
		"__AGENT_ARGS__", argsLine,
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
