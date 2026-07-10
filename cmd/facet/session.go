package main

import (
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

// openSession starts or rejoins the multiplexer session for a workspace.
//
// It is always the last thing spawn does, and its failure is never fatal: the
// workspace, its clones, its branch and its CLAUDE.md all exist by now. The worst
// case is that you are told what to type.
func openSession(ws, name, homeDir string, number int, launcherName string) error {
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
	if err := l.Start(s); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s failed to start (%v)\n", l.Name(), err)
		fmt.Fprintf(os.Stderr, "the workspace is intact. Join it yourself:\n  %s\n", l.AttachCommand(name))
		return nil
	}
	return nil
}
