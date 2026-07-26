package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RiccardoCereghino/facet/internal/claudex"
	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/spf13/cobra"
)

// agentOpts says what the first pane should run. Zero value means a plain login
// shell -- callers that want the default must say so explicitly.
type agentOpts struct {
	// Claude runs the CLI in the pane. False leaves a plain login shell.
	Claude bool
	// Remote adds Remote Control to that invocation. Meaningless without Claude.
	Remote bool
	// SessionName names the Remote Control session: the workspace name, so a
	// session on claude.ai is identifiable as the issue it belongs to.
	SessionName string
	// SessionNamePrefix comes from routing, and distinguishes hosts.
	SessionNamePrefix string
}

// agentFlags is the --claude/--remote surface, registered identically by `spawn`
// and `attach` so the two cannot drift.
//
// --rc/--no-rc are the names the first Remote Control launch shipped under, kept
// working as aliases. They collapse the two axes into one, which is why they are
// not the canonical spelling: there is no way to say "claude, but no Remote
// Control" with them.
type agentFlags struct {
	claude, remote, rc, noRC bool
}

func (a *agentFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&a.claude, "claude", true, "run claude in the pane (--claude=false leaves a plain login shell)")
	f.BoolVar(&a.remote, "remote", true, "give claude Remote Control, so the session is reachable off this host (--remote=false runs it without)")
	f.BoolVar(&a.rc, "rc", false, "deprecated alias for --claude --remote")
	f.BoolVar(&a.noRC, "no-rc", false, "deprecated alias for --claude=false")
	_ = f.MarkDeprecated("rc", "use --claude --remote")
	_ = f.MarkDeprecated("no-rc", "use --claude=false")
}

// agentChoice is a resolved agentOpts plus whether the caller actually asked.
type agentChoice struct {
	agentOpts
	// Explicit is true when the caller named one of the launch flags rather than
	// leaving the default. `spawn` needs the distinction: in a pane, running
	// claude is the default, but seizing THIS terminal with a blocking claude is
	// not something a default may decide.
	Explicit bool
}

// resolve folds the deprecated aliases into the canonical pair. An explicitly
// given --claude or --remote always wins: an alias only speaks for a flag the
// caller did not name.
func (a agentFlags) resolve(cmd *cobra.Command) agentChoice {
	f := cmd.Flags()
	c := agentChoice{
		agentOpts: agentOpts{Claude: a.claude, Remote: a.remote},
		Explicit:  f.Changed("claude") || a.rc || a.noRC,
	}
	if !f.Changed("claude") {
		// --no-rc wins over --rc, as it did when they were the only names.
		if a.noRC {
			c.Claude = false
		} else if a.rc {
			c.Claude = true
		}
	}
	if !f.Changed("remote") && a.rc && !a.noRC {
		c.Remote = true
	}
	return c
}

// agentCommand is what the first pane runs.
//
// FACET_AGENT wins outright: when it is set, neither --claude nor --remote
// applies and the pane runs exactly what it says. It is the escape hatch for
// anyone not driving claude, and it predates the flags -- making it lose to a
// flag's default would have silently stopped honouring it the day they landed.
// An env var and a defaulted flag cannot be ranked by "who was more explicit",
// so the env var is simply given the whole decision.
func agentCommand(o agentOpts) string {
	if v := os.Getenv("FACET_AGENT"); v != "" {
		return v
	}
	if !o.Claude {
		return ""
	}
	return claudex.ShellCommand(claudex.Options{
		RemoteControl:     o.Remote,
		SessionName:       o.SessionName,
		SessionNamePrefix: o.SessionNamePrefix,
	})
}

// sessionFor describes a workspace to the multiplexer. Both the layout renderer
// and the session opener build it here, so the session facet builds at spawn
// time is byte-for-byte the one it would have opened.
func sessionFor(ws, name, homeDir string, number int, agent agentOpts) mux.Session {
	if agent.SessionName == "" {
		agent.SessionName = name
	}
	s := mux.Session{
		Name:      name,
		Workspace: ws,
		HomeDir:   filepath.Join(ws, homeDir),
		Number:    number,
		Agent:     agentCommand(agent),
		Override:  mux.LayoutOverride(roots.Workspaces),
	}
	if _, err := os.Stat(s.Override); err != nil {
		s.Override = ""
	}
	return s
}

// openOpts is how a caller wants the workspace opened.
type openOpts struct {
	Mux                  string
	AsTab, Focus, Switch bool
	Agent                agentOpts
}

// rcURLWait is how long to watch a fresh pane for the Remote Control URL. Long
// enough for a cold `claude` start on a loaded box, short enough that a pane
// which is never going to print one does not hold the command open.
const rcURLWait = 20 * time.Second

// openSession starts or rejoins the multiplexer session for a workspace.
//
// Only ever reached through an explicit --attach. Its failure is never fatal:
// the workspace, its clones, its branch and its CLAUDE.md all exist by now.
// The worst case is that you are told what to type.
func openSession(ws, name, homeDir string, number int, o openOpts) error {
	var l mux.Launcher
	if o.Mux != "" {
		l = mux.ByName(o.Mux)
	} else {
		l = mux.Pick()
	}
	if l == nil {
		fmt.Printf("\nNo multiplexer available. Work in %s\n", filepath.Join(ws, homeDir))
		return nil
	}
	s := sessionFor(ws, name, homeDir, number, o.Agent)
	s.AsTab, s.Switch, s.Focus = o.AsTab, o.Switch, o.Focus
	target, err := l.Start(s)
	if err == nil {
		reportRCURL(l, target, o.Agent)
		return nil
	}
	// Being inside a session with nothing safe to run is not a failure; it is
	// a fork in the road, and the human picks.
	var g *mux.ErrGuidance
	if errors.As(err, &g) {
		fmt.Fprintf(os.Stderr, "\n%s\n", g.Msg)
		return nil
	}
	fmt.Fprintf(os.Stderr, "\n%s failed to start (%v)\n", l.Name(), err)
	fmt.Fprintf(os.Stderr, "the workspace is intact. Join it yourself:\n  %s\n", l.AttachCommand(name))
	return nil
}

// reportRCURL prints the Remote Control session URL once the pane has printed
// it. Reaching the session from elsewhere is the entire reason to enable Remote
// Control, and the CLI writes the URL only into its own pane -- so a caller who
// opened that pane in the background would otherwise have to go and read it off
// the window by hand.
//
// Best-effort throughout: no pane to read, no Remote Control asked for, a
// launcher that cannot read panes, or a pane that never prints one, all mean
// silence rather than an error. Nothing here can fail the open.
func reportRCURL(l mux.Launcher, target string, agent agentOpts) {
	if target == "" || !agent.Claude || !agent.Remote {
		return
	}
	if os.Getenv("FACET_AGENT") != "" {
		return // not our invocation; we cannot say what it prints
	}
	r, ok := l.(mux.PaneReader)
	if !ok {
		return
	}
	deadline := time.Now().Add(rcURLWait)
	for {
		if out, err := r.CapturePane(target, 200); err == nil {
			if u := claudex.FindSessionURL(out); u != "" {
				fmt.Printf("\nremote:     %s\n", u)
				return
			}
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
