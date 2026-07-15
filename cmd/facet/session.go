package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/RiccardoCereghino/facet/internal/mux"
)

// agentCommand is what the first pane runs. Overridable, because not everyone
// drives the same agent.
func agentCommand() string {
	if v := os.Getenv("FACET_AGENT"); v != "" {
		return v
	}
	return "claude"
}

// sessionFor describes a workspace to the multiplexer. Both the layout renderer
// and the session opener build it here, so the layout facet writes at spawn time
// is byte-for-byte the one it would have opened.
func sessionFor(ws, name, homeDir string, number int) mux.Session {
	s := mux.Session{
		Name:      name,
		Workspace: ws,
		HomeDir:   filepath.Join(ws, homeDir),
		Number:    number,
		Agent:     agentCommand(),
		Override:  mux.LayoutOverride(roots.Workspaces),
	}
	if _, err := os.Stat(s.Override); err != nil {
		s.Override = ""
	}
	return s
}

// openSession starts or rejoins the multiplexer session for a workspace.
//
// Only ever reached through an explicit --attach. Its failure is never fatal: the
// workspace, its clones, its branch and its CLAUDE.md all exist by now. The worst
// case is that you are told what to type.
func openSession(ws, name, homeDir string, number int, launcherName string, asTab, focus, switchTo bool) error {
	var l mux.Launcher
	if launcherName != "" {
		l = mux.ByName(launcherName)
	} else {
		l = mux.Pick()
	}
	if l == nil {
		fmt.Printf("\nNo multiplexer available. Work in %s\n", filepath.Join(ws, homeDir))
		return nil
	}
	s := sessionFor(ws, name, homeDir, number)
	s.AsTab, s.Switch, s.Focus = asTab, switchTo, focus
	err := l.Start(s)
	if err == nil {
		return nil
	}
	// Being inside a session with nothing safe to run is not a failure; it is a
	// fork in the road, and the human picks.
	var g *mux.ErrGuidance
	if errors.As(err, &g) {
		fmt.Fprintf(os.Stderr, "\n%s\n", g.Msg)
		return nil
	}
	fmt.Fprintf(os.Stderr, "\n%s failed to start (%v)\n", l.Name(), err)
	fmt.Fprintf(os.Stderr, "the workspace is intact. Join it yourself:\n  %s\n", l.AttachCommand(name))
	return nil
}
