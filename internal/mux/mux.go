// Package mux drives the terminal multiplexer a spawned workspace opens in.
//
// The multiplexer is a convenience, never a dependency: a workspace is fully
// created -- clones, branch, CLAUDE.md -- before anything here is attempted, and
// every failure below degrades to printing the command you could have run.
//
// The default launcher is tmux. WindowsTerminal is a degraded fallback for
// hosts where tmux is not available (notably native Windows): its tabs cannot
// be re-attached once closed, which is exactly the property tmux is wanted for.
package mux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Session describes the multiplexer session for one issue workspace.
type Session struct {
	Name      string // the session name, and the workspace directory name
	Workspace string // absolute path to the workspace
	HomeDir   string // absolute path to the clone holding the issue branch
	Number    int
	// Agent is the command the first pane runs. Empty means an ordinary shell.
	Agent string
	// Override is an executable layout script to use instead of the built-in
	// argv construction. Ignored when empty, unreadable, or non-executable; a
	// non-zero exit warns and falls back to the built-in path.
	Override string
	// AsTab opens the workspace as a new window in the tmux session we are
	// already inside, rather than in a session of its own.
	AsTab bool
	// Switch moves this client to the workspace's own session, when it has one.
	// It is the ONLY way facet will take you out of the session you are sitting
	// in, and it must come from an explicit flag -- never inferred.
	Switch bool
	// Focus leaves the new window focused. `tmux new-window` always focuses what
	// it creates, so when Focus is false the previously focused window is
	// restored afterwards. Adding a window beside someone who is mid-sentence
	// must not move them.
	Focus bool
}

// InSession reports whether this process is already inside a tmux session.
// tmux sets $TMUX in every child it spawns; its value is
// <socket_path>,<server_pid>,<session_id>, but presence alone is the signal.
func InSession() bool { return os.Getenv("TMUX") != "" }

// sessionNameFn is patched by tests. In production it shells out to tmux.
var sessionNameFn = func() string {
	if !InSession() {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#S").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SessionName is the tmux session we are inside, if any. tmux does not export
// the session name as an env var, so it is read live from the server.
func SessionName() string { return sessionNameFn() }

// ErrGuidance is returned when there is nothing safe to run and the human has to
// act first. Its message says what to do.
type ErrGuidance struct{ Msg string }

func (e *ErrGuidance) Error() string { return e.Msg }

// AutoOpen decides whether to open a freshly spawned workspace straight away,
// and whether to open it as a new window.
//
// The rule: open automatically only when doing so cannot steal the terminal.
// Inside a tmux session a new window appears alongside what you are already
// doing, so that is safe and is what you almost always want. Starting a
// *session*, by contrast, seizes the terminal until you detach -- so outside
// tmux, opening stays opt-in via --attach.
//
// ownSession forces a separate session even from inside one, which cannot be
// done without detaching first; plan() then returns guidance rather than acting.
func AutoOpen(l Launcher, ownSession bool) (open, asTab bool) {
	if l == nil || l.Name() != "tmux" || !InSession() {
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

// agentInvocation returns the executable and arguments that start the agent in
// a pane. The agent is launched THROUGH a shell so that `#!/bin/sh` scripts and
// npm shims (like `claude`) can be started reliably.
//
// The pane outlives the agent. Without the trailing `exec`, tmux closes the pane
// -- and with it the window -- the moment the agent exits, so restarting one
// means recreating the window and losing its scrollback. Dropping into an
// interactive login shell in the same directory keeps the window; `exec`
// replaces the `-lc` shell rather than leaving a second one nested under it.
//
// An empty exe means we could not find a shell to trust; the caller should open
// a plain pane rather than risk the spawn.
func agentInvocation(agent string) (exe string, args []string) {
	exe = findExecutable(shellCandidates()...)
	if exe == "" {
		return "", nil
	}
	if runtime.GOOS == "windows" {
		// pwsh -NoExit already leaves the tab on an interactive prompt.
		if agent == "" {
			return exe, nil
		}
		return exe, []string{"-NoLogo", "-NoExit", "-Command", agent}
	}
	fallback := "exec " + singleQuote(exe) + " -il"
	if agent == "" {
		return exe, []string{"-lc", fallback}
	}
	return exe, []string{"-lc", agent + "; " + fallback}
}

// singleQuote makes s one POSIX shell word. Only the shell's own path goes
// through here, but a home directory with a space in it is ordinary enough.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pinWindowName returns the tmux commands that stop the program in a window from
// renaming it. An agent writes its own terminal title within seconds of
// starting, and tmux's automatic-rename copies that over the name facet gave the
// window -- after which `-t <session>:<name>` targets nothing, and the window is
// no longer identifiable as the workspace's.
func pinWindowName(target string) [][]string {
	return [][]string{
		{"set-window-option", "-t", target, "automatic-rename", "off"},
		{"set-window-option", "-t", target, "allow-rename", "off"},
	}
}

// creates reports whether a tmux argv makes a new window we can address
// afterwards. Those are run with `-P -F #{window_id}` so their id comes back on
// stdout: a window id is stable, where a name can be overwritten and an index
// shifts when a neighbour closes.
func creates(argv []string) bool {
	return len(argv) > 0 && (argv[0] == "new-session" || argv[0] == "new-window")
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

// findExecutable returns the absolute path of the first candidate that the OS
// can actually execute. tmux itself does not run on native Windows, but this
// helper is Windows-aware for the WindowsTerminal path and future launchers.
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
	//
	// target is a handle on the window it created -- a tmux window id -- or ""
	// when it created none (it attached, switched, or the launcher has no such
	// notion). The caller needs it to read back what the pane printed.
	Start(s Session) (target string, err error)
	// Attach joins an existing session.
	Attach(name string) error
	// Kill removes the session. A missing session is not an error.
	Kill(name string) error
	// AttachCommand is what a human would type to join.
	AttachCommand(name string) string
}

// PaneReader is implemented by launchers that can read back what a window has
// printed. Optional: a launcher without it is simply not asked, and the caller
// does without. It stays deliberately ignorant of what the text means -- the
// agent's output is the agent package's business, not the multiplexer's.
type PaneReader interface {
	// CapturePane returns the last lines of the target window's scrollback.
	CapturePane(target string, lines int) (string, error)
}

// Pick returns the best available launcher, or nil.
func Pick() Launcher {
	for _, l := range []Launcher{Tmux{}, WindowsTerminal{}} {
		if l.Available() {
			return l
		}
	}
	return nil
}

// ByName returns a specific launcher, or nil when it is unavailable.
func ByName(name string) Launcher {
	switch strings.ToLower(name) {
	case "tmux":
		if l := (Tmux{}); l.Available() {
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

// Tmux drives the tmux multiplexer. One session per issue: `tmux list-sessions`
// is then the dashboard of running agents, attaching joins exactly the one you
// want, and reaping one touches no other.
//
// Every session name is targeted with an exact-match prefix (`-t "=<name>"`).
// Without it tmux does prefix-matching, and `iss-repo-6` would happily attach
// to `iss-repo-67`.
type Tmux struct{}

func (Tmux) Name() string { return "tmux" }

// Available reports whether tmux is installed. Native Windows has no tmux;
// WSL binaries on PATH cannot own a Windows terminal either, so refuse.
func (Tmux) Available() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return exec.Command("tmux", "-V").Run() == nil
}

// Live reports whether the named session exists on the tmux server. tmux has
// no "exited" state -- a session is either present or it is not.
func (Tmux) Live(session string) bool {
	return exec.Command("tmux", "has-session", "-t", "="+session).Run() == nil
}

// planInput carries everything plan() needs to compose a session or window.
type planInput struct {
	Name        string
	HomeDir     string
	Number      int
	AgentExe    string
	AgentArgs   []string
	CurrentSess string // the tmux session we are inside, when InSession
	InSession   bool
	Live        bool
	AsTab       bool
	Switch      bool
}

// plan works out how to open a workspace, given where we are standing. It is
// pure so the decision can be tested without a multiplexer. argv is one or more
// tmux invocations to run in order; a non-empty guidance means run nothing and
// tell the human instead.
//
// tmux sessions do not nest (well, they can, but the outer client gets confused
// and the inner reads $TMUX from the outer). From inside a session the only
// safe moves are to add the workspace as a new window, or -- when explicitly
// asked -- to switch to a live session of its own.
//
// THE RULE: from inside a session, facet never moves you unless you asked.
func plan(in planInput) (argvs [][]string, guidance string) {
	winName := fmt.Sprintf("#%d", in.Number)

	switch {
	case !in.InSession && in.Live:
		return [][]string{
			{"attach-session", "-t", "=" + in.Name},
		}, ""

	case !in.InSession && !in.Live:
		// Detached create then attach. Detached-first is the only safe idiom
		// outside tmux: a signal between the two cannot leave a half-built
		// session sitting there attached. Layout is one pane, the agent, at
		// HomeDir -- users who want a split shell can add one with prefix + %.
		create := []string{"new-session", "-d", "-s", in.Name, "-c", in.HomeDir, "-n", winName, "-P", "-F", "#{window_id}"}
		if in.AgentExe != "" {
			create = append(create, in.AgentExe)
			create = append(create, in.AgentArgs...)
		}
		return [][]string{
			create,
			{"attach-session", "-t", "=" + in.Name},
		}, ""

	case in.InSession && in.Switch && in.Live:
		return [][]string{
			{"switch-client", "-t", "=" + in.Name},
		}, ""

	case in.InSession && in.Switch && !in.Live:
		return nil, "you asked to switch to " + in.Name + ", but it is not running.\n" +
			"  tmux sessions do not nest, so it cannot be created from in here. Either:\n" +
			"    detach first -- Ctrl+b then d, then re-run with --session\n" +
			"    or drop --switch, to open it here as a window"

	case in.InSession && in.AsTab:
		// Adding a window to the CURRENT session. This wins even when the
		// workspace has a live session of its own: duplicated windows are
		// cheap and undoable, being yanked out of the session you are typing
		// in is neither.
		//
		// One pane, the agent, at HomeDir. Target the current session by name
		// for defensive clarity; `-t ":"` would work but reads worse in logs.
		add := []string{"new-window", "-t", "=" + in.CurrentSess + ":", "-n", winName, "-c", in.HomeDir, "-P", "-F", "#{window_id}"}
		if in.AgentExe != "" {
			add = append(add, in.AgentExe)
			add = append(add, in.AgentArgs...)
		}
		return [][]string{add}, ""

	default: // in.InSession, and --session was asked for
		return nil, "you are inside tmux session " + in.CurrentSess + ", so " + in.Name +
			" cannot have a session of its own.\n" +
			"  tmux sessions do not nest. Either:\n" +
			"    detach first -- Ctrl+b then d, then re-run this command\n" +
			"    or drop --session, to open it here as a window"
	}
}

// Start opens the workspace: attaching, switching, or adding a window as the
// situation allows. It blocks while tmux holds the terminal.
func (z Tmux) Start(s Session) (string, error) {
	inSession := InSession()
	currentSess := ""
	if inSession {
		currentSess = SessionName()
		// Already sitting in the workspace's own session: adding a window for
		// it again would just duplicate, and switching to it is a no-op.
		if currentSess == s.Name {
			return "", &ErrGuidance{Msg: "you are already inside tmux session " + s.Name + "."}
		}
	}

	// Try the override script first (if it exists and is executable). A
	// non-zero exit is treated as "override failed"; warn and fall back to the
	// built-in path so a stale script does not block a spawn.
	if s.Override != "" {
		if err := runOverride(s); err == nil {
			// The override built the session. All that is left is what would
			// happen after the built-in path: attach from outside, or nothing
			// (the window is already there) from inside.
			if !inSession {
				return "", passthrough("tmux", "attach-session", "-t", "="+s.Name)
			}
			return "", nil
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: layout override %s failed (%v); using the built-in layout instead.\n",
				s.Override, err)
		}
	}

	exe, args := agentInvocation(s.Agent)
	if exe == "" {
		// No trustworthy shell. A pane with no command is better than risking
		// a broken spawn -- tmux would leave the window in a "[exited]" state
		// but the session survives.
		exe = defaultShell()
		args = nil
	}
	argvs, guidance := plan(planInput{
		Name:        s.Name,
		HomeDir:     s.HomeDir,
		Number:      s.Number,
		AgentExe:    exe,
		AgentArgs:   args,
		CurrentSess: currentSess,
		InSession:   inSession,
		Live:        z.Live(s.Name),
		AsTab:       s.AsTab,
		Switch:      s.Switch,
	})
	if guidance != "" {
		return "", &ErrGuidance{Msg: guidance}
	}

	// `tmux new-window` always focuses the window it creates. Note where we
	// were, so the caller who did not ask to be moved can be put back.
	restore := ""
	if inSession && s.AsTab && !s.Focus {
		restore = focusedWindowIndex(currentSess)
	}

	// The final argv (attach-session, when we're building a fresh session from
	// outside) needs to be run under passthrough because it blocks the
	// terminal. Everything before that is a tmux command whose exit code we
	// want to observe without seizing the terminal.
	target := ""
	for i, argv := range argvs {
		last := i == len(argvs)-1
		if last && len(argv) > 0 && argv[0] == "attach-session" {
			return target, passthrough("tmux", argv...)
		}
		if creates(argv) {
			out, err := exec.Command("tmux", argv...).Output()
			if err != nil {
				return "", fmt.Errorf("tmux %s: %w", strings.Join(argv, " "), err)
			}
			target = strings.TrimSpace(string(out))
			// Pin the name before the agent has had time to overwrite it. A
			// failure here costs targeting, not the window, so it is not fatal.
			for _, opt := range pinWindowName(target) {
				_ = exec.Command("tmux", opt...).Run()
			}
			continue
		}
		if err := exec.Command("tmux", argv...).Run(); err != nil {
			return "", fmt.Errorf("tmux %s: %w", strings.Join(argv, " "), err)
		}
	}

	if restore != "" {
		_ = exec.Command("tmux", "select-window", "-t", "="+currentSess+":"+restore).Run()
	}
	return target, nil
}

// CapturePane returns the last lines of the target window's scrollback,
// implementing PaneReader.
func (Tmux) CapturePane(target string, lines int) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-"+strconv.Itoa(lines), "-t", target).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// focusedWindowIndex returns the current session's active window index, or ""
// when it cannot be read. Empty string means "do not attempt to restore".
func focusedWindowIndex(session string) string {
	if session == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", "="+session+":", "-F", "#{window_index}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runOverride executes the layout script. Args: session name, home dir,
// workspace, issue number, agent exe, agent args...
//
// Returns os.ErrNotExist when the file is not present (a signal to the caller
// to fall through to the built-in path without warning).
func runOverride(s Session) error {
	if _, err := os.Stat(s.Override); err != nil {
		return os.ErrNotExist
	}
	exe, args := agentInvocation(s.Agent)
	scriptArgs := []string{s.Override, s.Name, s.HomeDir, s.Workspace, fmt.Sprint(s.Number), exe}
	scriptArgs = append(scriptArgs, args...)
	cmd := exec.Command("bash", scriptArgs...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// LayoutOverride is the path facet prefers over its built-in layout, when it
// exists and is executable. It lives with the other project data, not in the
// binary.
func LayoutOverride(workspacesRoot string) string {
	return filepath.Join(workspacesRoot, ".tools", "issue-layout.sh")
}

func (Tmux) Attach(name string) error {
	return passthrough("tmux", "attach-session", "-t", "="+name)
}

func (Tmux) Kill(name string) error {
	// Killing a session that never existed makes tmux exit non-zero, which is
	// not an error worth surfacing.
	_ = exec.Command("tmux", "kill-session", "-t", "="+name).Run()
	return nil
}

func (Tmux) AttachCommand(name string) string { return "tmux attach -t =" + name }

// -----------------------------------------------------------------------------

// WindowsTerminal is the fallback when tmux is unavailable. Its tabs cannot be
// re-attached once closed, which is exactly the property tmux is wanted for --
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

// Start opens a tab. It returns no target: a Windows Terminal tab has no handle
// facet can address afterwards, so nothing can be read back out of it.
func (WindowsTerminal) Start(s Session) (string, error) {
	args := []string{"-w", "facet", "nt", "--title", s.Name, "-d", s.HomeDir}
	if s.Agent != "" {
		args = append(args, s.Agent)
	}
	return "", exec.Command("wt", args...).Start()
}

func (WindowsTerminal) Attach(string) error {
	return fmt.Errorf("windows-terminal tabs cannot be re-attached once closed")
}

func (WindowsTerminal) Kill(string) error { return nil }

func (WindowsTerminal) AttachCommand(string) string {
	return "(windows-terminal: reopen manually; tabs cannot be re-attached)"
}

// -----------------------------------------------------------------------------

// passthrough runs cmd wired to this process's terminal.
func passthrough(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
