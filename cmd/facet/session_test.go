package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// The four rows of the behaviour matrix, at the point where facet decides what
// the pane's command is. mux wraps whatever comes out of here in the pane's
// login shell; internal/mux's TestPaneMatrix covers that half.
func TestAgentCommandMatrix(t *testing.T) {
	tests := []struct {
		name       string
		facetAgent string
		in         agentOpts
		want       string
	}{
		{
			name: "defaults: claude with Remote Control, named for the workspace",
			in:   agentOpts{Claude: true, Remote: true, SessionName: "iss-facet-27"},
			want: "claude --remote-control=iss-facet-27",
		},
		{
			name: "--remote=false: claude, no Remote Control",
			in:   agentOpts{Claude: true, SessionName: "iss-facet-27"},
			want: "claude",
		},
		{
			name: "--claude=false: nothing, so the pane is a plain login shell",
			in:   agentOpts{Remote: true, SessionName: "iss-facet-27"},
			want: "",
		},
		{
			name: "--claude=false --remote=true: still nothing",
			in:   agentOpts{Claude: false, Remote: true, SessionName: "iss-facet-27"},
			want: "",
		},
		{
			name:       "FACET_AGENT wins over the defaults",
			facetAgent: "nvim .",
			in:         agentOpts{Claude: true, Remote: true, SessionName: "iss-facet-27"},
			want:       "nvim .",
		},
		{
			// THE REGRESSION THIS GUARDS: FACET_AGENT predates the flags, so
			// it must not start losing to a flag's default value.
			name:       "FACET_AGENT wins over --claude=false too",
			facetAgent: "nvim .",
			in:         agentOpts{},
			want:       "nvim .",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FACET_AGENT", tt.facetAgent)
			if got := agentCommand(tt.in); got != tt.want {
				t.Errorf("agentCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentCommandCarriesTheSessionNamePrefix(t *testing.T) {
	t.Setenv("FACET_AGENT", "")
	got := agentCommand(agentOpts{
		Claude: true, Remote: true, SessionName: "iss-facet-27", SessionNamePrefix: "mini",
	})
	want := "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=mini claude --remote-control=iss-facet-27"
	if got != want {
		t.Errorf("agentCommand = %q, want %q", got, want)
	}
}

// resolveFlags runs the flag surface the way cobra would, so the Changed()
// bookkeeping the resolution depends on is real.
func resolveFlags(t *testing.T, argv ...string) agentChoice {
	t.Helper()
	var a agentFlags
	cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.SetArgs(argv)
	cmd.SetOut(discard{})
	cmd.SetErr(discard{})
	a.register(cmd)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("parsing %v: %v", argv, err)
	}
	return a.resolve(cmd)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestAgentFlagsResolve(t *testing.T) {
	tests := []struct {
		name        string
		argv        []string
		wantClaude  bool
		wantRemote  bool
		wantExplict bool
	}{
		{"no flags: claude with Remote Control, but nobody asked", nil, true, true, false},
		{"--claude=false", []string{"--claude=false"}, false, true, true},
		{"--remote=false", []string{"--remote=false"}, true, false, false},
		{"--claude=false --remote=false", []string{"--claude=false", "--remote=false"}, false, false, true},
		{"--claude explicitly on", []string{"--claude=true"}, true, true, true},

		// The deprecated aliases.
		{"--rc means both", []string{"--rc"}, true, true, true},
		{"--no-rc means neither", []string{"--no-rc"}, false, true, true},
		{"--no-rc beats --rc, as it always did", []string{"--rc", "--no-rc"}, false, true, true},

		// A named canonical flag outranks an alias speaking for it.
		{"--claude=false beats --rc", []string{"--rc", "--claude=false"}, false, true, true},
		{"--remote=false beats --rc", []string{"--rc", "--remote=false"}, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFlags(t, tt.argv...)
			if got.Claude != tt.wantClaude || got.Remote != tt.wantRemote {
				t.Errorf("resolve(%v) = {Claude:%v Remote:%v}, want {Claude:%v Remote:%v}",
					tt.argv, got.Claude, got.Remote, tt.wantClaude, tt.wantRemote)
			}
			if got.Explicit != tt.wantExplict {
				t.Errorf("resolve(%v).Explicit = %v, want %v", tt.argv, got.Explicit, tt.wantExplict)
			}
		})
	}
}

// THE REGRESSION THIS GUARDS: --claude defaults to true because a PANE should
// run claude. Left alone, that default must not also start a blocking claude in
// the terminal `facet spawn` was run from -- seizing the caller's terminal is
// not something a default may decide.
func TestBareSpawnDoesNotSeizeTheTerminal(t *testing.T) {
	if got := resolveFlags(t); got.Explicit {
		t.Fatal("no flags given, yet the launch reads as explicitly asked for")
	}
	for _, argv := range [][]string{{"--remote=false"}, nil} {
		if resolveFlags(t, argv...).Explicit {
			t.Errorf("resolve(%v).Explicit = true; only naming a launch flag counts as asking", argv)
		}
	}
}
