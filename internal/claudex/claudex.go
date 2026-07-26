// Package claudex launches the Claude Code CLI, the same way ghx/gitx wrap gh
// and git: facet shells out rather than embedding anything.
//
// facet is a scaffolder; opening an agent has always been left to the operator.
// This package exists for one deliberate exception: launching a session with
// Remote Control enabled, so an agent on the Mini is reachable over Anthropic's
// relay independent of the tailnet. Remote Control needs the CLI signed in via
// claude.ai OAuth (Pro/Max/Team, not an API key) and the workspace trusted once;
// a launch failure here is surfaced but never fatal to a spawn.
//
// There are two ways facet starts the CLI and ONE place that decides what the
// invocation looks like: Args. LaunchRC runs it here, blocking, wired to this
// terminal -- what `spawn` does when it is opening nothing else. ShellCommand
// renders the same invocation as a command line for a multiplexer pane to run,
// which is what `attach` and `spawn --attach` do. A second, independently
// written launcher would drift from this one on the first flag change.
package claudex

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Exe is the CLI facet starts. Not configurable here: FACET_AGENT replaces the
// whole invocation upstream rather than substituting a binary into this one.
const Exe = "claude"

// Options describes one claude invocation.
type Options struct {
	// RemoteControl enables Remote Control for the session.
	RemoteControl bool
	// SessionName names the Remote Control session. Ignored when RemoteControl
	// is false; empty means let the CLI name the session itself.
	SessionName string
	// SessionNamePrefix, when set, is exported as
	// CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX so sessions are named per host.
	SessionNamePrefix string
}

// Args returns the arguments that follow Exe.
//
// The session name is attached with `--remote-control=<name>`, never as the
// positional `--rc <name>`. The value is optional, so a positional name is
// ambiguous the moment anything else follows it on the command line: an initial
// prompt is read as the session name, or the name as the prompt, depending on
// which way the CLI resolves it. The `=` form cannot be misread, and costs
// nothing when there is no prompt.
func Args(o Options) []string {
	if !o.RemoteControl {
		return nil
	}
	if o.SessionName == "" {
		return []string{"--remote-control"}
	}
	return []string{"--remote-control=" + o.SessionName}
}

// ShellCommand renders the invocation as a single POSIX shell command line,
// including the session-name-prefix assignment when there is one. It is what a
// multiplexer pane runs; nothing here blocks or touches this process's stdio.
func ShellCommand(o Options) string {
	var b strings.Builder
	if o.SessionNamePrefix != "" {
		b.WriteString("CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=")
		b.WriteString(ShellQuote(o.SessionNamePrefix))
		b.WriteString(" ")
	}
	b.WriteString(Exe)
	for _, a := range Args(o) {
		b.WriteString(" ")
		b.WriteString(ShellQuote(a))
	}
	return b.String()
}

// ShellQuote makes s a single POSIX shell word. Anything outside a conservative
// safe set is single-quoted, and an embedded single quote is closed, escaped and
// reopened -- the only way to quote one inside single quotes.
//
// A workspace name reaches a pane's command line through here, and a workspace
// name comes from an issue title.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsFunc(s, needsQuoting) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuoting(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("-_./=:@,+", r)
}

// sessionURLRe matches the Remote Control session URL the CLI prints when it
// starts. Anchored on the session_ prefix so an unrelated claude.ai link in the
// same scrollback -- a docs page, a link the agent itself printed -- is not
// mistaken for one.
var sessionURLRe = regexp.MustCompile(`https://claude\.ai/code/session_[A-Za-z0-9]+`)

// FindSessionURL returns the Remote Control session URL in s, or "".
//
// The URL is the whole point of Remote Control -- it is how the session is
// reached from a phone -- and the CLI prints it only into its own pane. Whoever
// launched the pane is far better placed to surface it than the operator, who
// would otherwise have to go and read it off the window.
//
// The LAST match wins: a pane that has been through an agent restart holds every
// session it has ever run, and the live one is the most recent.
func FindSessionURL(s string) string {
	m := sessionURLRe.FindAllString(s, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// Launch runs claude interactively in dir. The call blocks for the life of the
// session, wired to the current stdio, and returns whatever the CLI exits with.
//
// This is the path `spawn` takes when it is opening no multiplexer pane. When
// there IS a pane, the pane runs ShellCommand's output instead: a second,
// blocking claude in this terminal underneath it would fight the pane for stdin.
func Launch(dir string, o Options) error {
	cmd := exec.Command(Exe, Args(o)...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if o.SessionNamePrefix != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX="+o.SessionNamePrefix)
	}
	return cmd.Run()
}
