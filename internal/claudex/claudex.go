// Package claudex launches the Claude Code CLI, the same way ghx/gitx wrap gh
// and git: facet shells out rather than embedding anything.
//
// facet is a scaffolder; opening an agent has always been left to the operator.
// This package exists for one deliberate exception: launching a session with
// Remote Control enabled, so an agent on the Mini is reachable over Anthropic's
// relay independent of the tailnet. Remote Control needs the CLI signed in via
// claude.ai OAuth (Pro/Max/Team, not an API key) and the workspace trusted once;
// a launch failure here is surfaced but never fatal to a spawn.
package claudex

import (
	"os"
	"os/exec"
)

// LaunchRC runs `claude --rc [sessionName]` interactively in dir, enabling
// Remote Control. If sessionNamePrefix is non-empty it is passed through as
// CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX. The call blocks for the life of the
// session, wired to the current stdio, and returns whatever the CLI exits with.
func LaunchRC(dir, sessionName, sessionNamePrefix string) error {
	args := []string{"--rc"}
	if sessionName != "" {
		args = append(args, sessionName)
	}
	cmd := exec.Command("claude", args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if sessionNamePrefix != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX="+sessionNamePrefix)
	}
	return cmd.Run()
}
