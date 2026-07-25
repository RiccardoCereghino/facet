package mux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestByName(t *testing.T) {
	if ByName("none") != nil || ByName("off") != nil {
		t.Error("none/off must select no launcher")
	}
	if ByName("nonsense") != nil {
		t.Error("an unknown launcher must be nil, not a default")
	}
	// The old "zellij" alias must not resolve any more; the parked port is gone.
	if ByName("zellij") != nil {
		t.Error("zellij must no longer resolve; the port is tmux now")
	}
}

// tmux sessions do not nest. Adding a workspace from inside a session must add
// it as a window, never attach a client to another session (which would take
// over the terminal). Switching happens only when explicitly asked for.
func TestPlan(t *testing.T) {
	const name, home = "iss-repo-67-x", "/tmp/home"
	const winName = "#67"
	base := planInput{
		Name: name, HomeDir: home, Number: 67,
		AgentExe: "/bin/bash", AgentArgs: []string{"-lc", "claude"},
	}

	tests := []struct {
		name         string
		mut          func(*planInput)
		wantFirstCmd string   // the tmux subcommand of the first argv
		wantContains []string // substrings that must appear across the flattened argvs
		wantMissing  []string // substrings that must not appear
		wantGuidance bool
	}{
		{
			name:         "outside, session exists: attach",
			mut:          func(in *planInput) { in.InSession = false; in.Live = true },
			wantFirstCmd: "attach-session",
			wantContains: []string{"=" + name},
		},
		{
			name:         "outside, session missing: create detached, attach",
			mut:          func(in *planInput) { in.InSession = false; in.Live = false },
			wantFirstCmd: "new-session",
			wantContains: []string{"-d", "-s", name, winName, "attach-session", "-c", home, "-lc", "claude"},
			wantMissing:  []string{"split-window"},
		},
		{
			name:         "inside, as window: new-window on current session, no client attach",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer"; in.AsTab = true },
			wantFirstCmd: "new-window",
			wantContains: []string{"=outer:", winName, "-c", home, "-lc", "claude"},
			wantMissing:  []string{"attach-session", "switch-client", "split-window"},
		},
		{
			// THE REGRESSION THIS GUARDS: adding as tab must win even when a
			// live session exists, never move the client silently.
			name:         "inside, workspace session exists: still add as window, never move the client",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer"; in.AsTab = true; in.Live = true },
			wantFirstCmd: "new-window",
			wantMissing:  []string{"switch-client", "attach-session", "split-window"},
		},
		{
			name:         "inside, --switch, workspace session exists: switch-client",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer"; in.Switch = true; in.Live = true },
			wantFirstCmd: "switch-client",
			wantContains: []string{"=" + name},
		},
		{
			name:         "inside, --switch, workspace session missing: guide",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer"; in.Switch = true; in.Live = false },
			wantGuidance: true,
		},
		{
			name:         "inside, --session (asTab=false, switch=false): guide",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer" },
			wantGuidance: true,
		},
		{
			name:         "inside, --session, workspace session exists: still guide, do not move silently",
			mut:          func(in *planInput) { in.InSession = true; in.CurrentSess = "outer"; in.Live = true },
			wantGuidance: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.mut(&in)
			argvs, guidance := plan(in)
			if tt.wantGuidance {
				if guidance == "" {
					t.Fatalf("expected guidance, got argvs %v", argvs)
				}
				if argvs != nil {
					t.Errorf("guidance case must run nothing, got %v", argvs)
				}
				for _, want := range []string{"do not nest", "detach"} {
					if !strings.Contains(guidance, want) {
						t.Errorf("guidance omits %q:\n%s", want, guidance)
					}
				}
				return
			}
			if guidance != "" {
				t.Fatalf("unexpected guidance: %s", guidance)
			}
			if len(argvs) == 0 {
				t.Fatalf("expected argvs, got none")
			}
			if len(argvs[0]) == 0 || argvs[0][0] != tt.wantFirstCmd {
				t.Fatalf("first argv = %v, want it to start with %q", argvs[0], tt.wantFirstCmd)
			}
			joined := flatten(argvs)
			for _, w := range tt.wantContains {
				if !containsToken(joined, w) {
					t.Errorf("argvs %v missing %q (joined: %q)", argvs, w, strings.Join(joined, " "))
				}
			}
			for _, w := range tt.wantMissing {
				if containsToken(joined, w) {
					t.Errorf("argvs %v must not contain %q", argvs, w)
				}
			}
		})
	}
}

func flatten(argvs [][]string) []string {
	var out []string
	for _, a := range argvs {
		out = append(out, a...)
	}
	return out
}

func containsToken(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// `tmux attach-session` must never be reached from inside a session: that call
// takes over the client.
func TestPlanNeverAttachesFromInsideASession(t *testing.T) {
	base := planInput{
		Name: "n", HomeDir: "/h", Number: 1,
		AgentExe: "/bin/bash", AgentArgs: []string{"-lc", "x"},
		InSession: true, CurrentSess: "outer",
	}
	for _, live := range []bool{true, false} {
		for _, asTab := range []bool{true, false} {
			for _, switchTo := range []bool{true, false} {
				in := base
				in.Live = live
				in.AsTab = asTab
				in.Switch = switchTo
				argvs, _ := plan(in)
				for _, argv := range argvs {
					if len(argv) > 0 && argv[0] == "attach-session" {
						t.Errorf("plan(inSession=true, live=%v, asTab=%v, switch=%v) = %v; must not attach",
							live, asTab, switchTo, argvs)
					}
				}
			}
		}
	}
}

// Being moved out of the session you are working in is never a default. It has
// to be asked for, every time. This is the whole point of the --switch flag.
func TestPlanNeverSwitchesUnlessAsked(t *testing.T) {
	base := planInput{
		Name: "n", HomeDir: "/h", Number: 1,
		AgentExe: "/bin/bash", AgentArgs: []string{"-lc", "x"},
		InSession: true, CurrentSess: "outer",
	}
	for _, live := range []bool{true, false} {
		for _, asTab := range []bool{true, false} {
			in := base
			in.Live = live
			in.AsTab = asTab
			argvs, _ := plan(in)
			for _, argv := range argvs {
				for _, a := range argv {
					if a == "switch-client" {
						t.Errorf("plan(inSession=true, live=%v, asTab=%v, switch=false) = %v; "+
							"switched without being asked", live, asTab, argvs)
					}
				}
			}
		}
	}
}

// The inverse: --switch against a live session is the one case that moves you.
func TestPlanSwitchesWhenAsked(t *testing.T) {
	argvs, guidance := plan(planInput{
		Name: "n", InSession: true, CurrentSess: "outer", Live: true, Switch: true,
	})
	if guidance != "" {
		t.Fatalf("unexpected guidance: %s", guidance)
	}
	if len(argvs) != 1 {
		t.Fatalf("argvs = %v; want one command", argvs)
	}
	got := argvs[0]
	want := []string{"switch-client", "-t", "=n"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v; want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("argv = %v; want %v", got, want)
		}
	}
}

func TestInSessionReadsTmuxEnv(t *testing.T) {
	t.Setenv("TMUX", "")
	if InSession() {
		t.Error("empty TMUX must not count as being in a session")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	if !InSession() {
		t.Error("TMUX being set means we are inside a session")
	}
}

// SessionName goes through tmux; the test patches sessionNameFn to keep the
// unit test hermetic. That is the only reason sessionNameFn exists as a var.
func TestSessionNameIsRead(t *testing.T) {
	orig := sessionNameFn
	t.Cleanup(func() { sessionNameFn = orig })
	sessionNameFn = func() string { return "quadratic-cymbal" }
	if got := SessionName(); got != "quadratic-cymbal" {
		t.Errorf("SessionName = %q", got)
	}
}

// fakeLauncher lets AutoOpen be tested without a multiplexer installed.
type fakeLauncher struct{ name string }

func (f fakeLauncher) Name() string                { return f.name }
func (fakeLauncher) Available() bool               { return true }
func (fakeLauncher) Live(string) bool              { return false }
func (fakeLauncher) Start(Session) error           { return nil }
func (fakeLauncher) Attach(string) error           { return nil }
func (fakeLauncher) Kill(string) error             { return nil }
func (fakeLauncher) AttachCommand(s string) string { return s }

// The rule that decides whether facet seizes your terminal: open automatically
// only when the workspace can arrive as a window beside what you are already
// doing.
func TestAutoOpen(t *testing.T) {
	tm := fakeLauncher{"tmux"}
	wt := fakeLauncher{"windows-terminal"}

	tests := []struct {
		name       string
		l          Launcher
		inSession  bool
		ownSession bool
		wantOpen   bool
		wantAsTab  bool
	}{
		{"inside tmux: open as window, unprompted", tm, true, false, true, true},
		{"inside tmux, --session: nothing safe to do automatically", tm, true, true, true, false},

		// Outside a session, opening would seize the terminal. Stay opt-in.
		{"outside tmux: do not open", tm, false, false, false, false},

		// Windows Terminal spawns a window; never do that unasked.
		{"windows-terminal, even inside tmux: do not open", wt, true, false, false, false},

		{"no launcher: do not open", nil, true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.inSession {
				t.Setenv("TMUX", "/tmp/tmux/default,1,0")
			} else {
				t.Setenv("TMUX", "")
			}
			open, asTab := AutoOpen(tt.l, tt.ownSession)
			if open != tt.wantOpen || asTab != tt.wantAsTab {
				t.Errorf("AutoOpen = (open=%v, asTab=%v), want (open=%v, asTab=%v)", open, asTab, tt.wantOpen, tt.wantAsTab)
			}
		})
	}
}

// The combination that must never silently seize the terminal.
func TestAutoOpenNeverStealsTheTerminal(t *testing.T) {
	t.Setenv("TMUX", "")
	if open, _ := AutoOpen(fakeLauncher{"tmux"}, false); open {
		t.Error("outside a session, opening seizes the terminal; it must stay opt-in")
	}
}

func TestAgentInvocationRunsThroughAShell(t *testing.T) {
	exe, args := agentInvocation("claude")
	if exe == "" {
		t.Skip("no shell found")
	}
	if strings.Contains(strings.ToLower(exe), "claude") {
		t.Errorf("exe = %q; the agent must not be the executable", exe)
	}
	if !filepath.IsAbs(exe) {
		t.Errorf("exe = %q; must be an absolute path so tmux need not search PATH", exe)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "claude") {
		t.Errorf("args = %v; agent not passed through", args)
	}
}

// With no agent, the pane is just a shell and takes no args.
func TestAgentInvocationEmptyAgent(t *testing.T) {
	exe, args := agentInvocation("")
	if exe == "" {
		t.Skip("no shell found")
	}
	if len(args) != 0 {
		t.Errorf("args = %v; a bare shell needs none", args)
	}
}

// The single pane lands at HomeDir so the agent starts where the code is.
func TestBuildSessionSetsCwdOnAgentPane(t *testing.T) {
	in := planInput{
		Name: "n", HomeDir: "/h", Number: 1,
		AgentExe: "/bin/bash", AgentArgs: []string{"-lc", "claude"},
	}
	argvs, _ := plan(in)
	if len(argvs) == 0 {
		t.Fatal("expected at least one argv")
	}
	if !argvContains(argvs[0], "-c", "/h") {
		t.Errorf("agent pane not rooted at HomeDir: %v", argvs[0])
	}
}

// Agent argv is passed directly to new-session/new-window as the pane's
// command, not through `send-keys` (which would leak into shell history) and
// not as the executable (which would fail for npm shims).
func TestBuildSessionAgentPaneRunsThroughShell(t *testing.T) {
	argvs, _ := plan(planInput{
		Name: "n", HomeDir: "/h", Number: 5,
		AgentExe: "/bin/bash", AgentArgs: []string{"-lc", "claude"},
	})
	// No send-keys anywhere.
	for _, argv := range argvs {
		for _, a := range argv {
			if a == "send-keys" {
				t.Errorf("agent runs through send-keys (leaks into shell history): %v", argvs)
			}
		}
	}
	// new-session names bash (not "claude") as the pane executable.
	if len(argvs) == 0 || len(argvs[0]) < 2 {
		t.Fatalf("no new-session argv")
	}
	tail := argvs[0][len(argvs[0])-3:] // exe -lc claude
	if tail[0] != "/bin/bash" || tail[1] != "-lc" || tail[2] != "claude" {
		t.Errorf("agent invocation not shell-wrapped: %v", tail)
	}
}

func TestBuildSessionWindowNameIsIssueNumber(t *testing.T) {
	argvs, _ := plan(planInput{
		Name: "n", HomeDir: "/h", Number: 42,
		AgentExe: "/bin/bash",
	})
	joined := flatten(argvs)
	if !containsToken(joined, "#42") {
		t.Errorf("window name not #%d in %v", 42, argvs)
	}
}

// Every -t target starts with `=`. Without it tmux does prefix-matching and
// `iss-facet-6` would target `iss-facet-67`.
func TestBuildSessionExactMatchTargets(t *testing.T) {
	cases := []planInput{
		{Name: "iss-x-6", HomeDir: "/h", Number: 6, AgentExe: "/bin/bash"},
		{Name: "iss-x-6", HomeDir: "/h", Number: 6, AgentExe: "/bin/bash", InSession: true, CurrentSess: "outer", AsTab: true},
		{Name: "iss-x-6", InSession: true, CurrentSess: "outer", Switch: true, Live: true},
		{Name: "iss-x-6", Live: true},
	}
	for _, in := range cases {
		argvs, _ := plan(in)
		for _, argv := range argvs {
			for i, a := range argv {
				if a != "-t" {
					continue
				}
				if i+1 >= len(argv) {
					t.Errorf("-t with no value in %v", argv)
					continue
				}
				val := argv[i+1]
				if !strings.HasPrefix(val, "=") {
					t.Errorf("-t %q lacks '=' exact-match prefix (%v)", val, argv)
				}
			}
		}
	}
}

// Inside-tmux add-as-tab must emit exactly one new-window (never a create).
func TestBuildSessionSingleWindow(t *testing.T) {
	argvs, _ := plan(planInput{
		Name: "n", HomeDir: "/h", Number: 1,
		AgentExe: "/bin/bash", InSession: true, CurrentSess: "outer", AsTab: true,
	})
	var newWindow, newSession int
	for _, argv := range argvs {
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "new-window":
			newWindow++
		case "new-session":
			newSession++
		}
	}
	if newWindow != 1 {
		t.Errorf("expected exactly 1 new-window, got %d in %v", newWindow, argvs)
	}
	if newSession != 0 {
		t.Errorf("adding a window must not create a session, got %d in %v", newSession, argvs)
	}
}

// An override script that exits non-zero must warn and fall back to the
// built-in path -- exactly like the parked KDL layer treated a stale layout.
func TestOverrideScriptFallsBackOnNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell override path is POSIX-only")
	}
	ws := t.TempDir()
	override := filepath.Join(ws, "issue-layout.sh")
	if err := os.WriteFile(override, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runOverride(Session{
		Name: "n", Workspace: ws, HomeDir: ws, Number: 1, Override: override,
	})
	if err == nil {
		t.Fatal("runOverride should surface a non-zero exit")
	}
	if os.IsNotExist(err) {
		t.Errorf("non-zero exit must be distinguishable from missing script")
	}
}

// A missing override must be silently reported via os.ErrNotExist so Start can
// fall through to the built-in path without warning.
func TestRunOverrideMissingReportsErrNotExist(t *testing.T) {
	err := runOverride(Session{Override: "/definitely/not/a/real/path.sh"})
	if !os.IsNotExist(err) {
		t.Errorf("runOverride(missing) = %v; want os.ErrNotExist", err)
	}
}

func TestLayoutOverridePath(t *testing.T) {
	got := LayoutOverride("/home/x/Workspaces")
	want := filepath.Join("/home/x/Workspaces", ".tools", "issue-layout.sh")
	if got != want {
		t.Errorf("LayoutOverride = %q; want %q", got, want)
	}
}

// TestTmuxAcceptsGeneratedLayout is the real-tmux integration test. It uses a
// temp socket (`tmux -L <socket>`) that never touches the user's default
// server, and cleans up with `kill-server` on the same socket.
func TestTmuxAcceptsGeneratedLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux does not run on native Windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("facet-test-%d", os.Getpid())
	session := fmt.Sprintf("facet-probe-%d", os.Getpid())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	// Compose the argvs plan() would produce for a fresh session, then run
	// them on the isolated socket. Drop the final attach-session (which would
	// block the test).
	in := planInput{
		Name: session, HomeDir: t.TempDir(), Number: 1,
		AgentExe: "/bin/sh", AgentArgs: []string{"-c", "sleep 30"},
	}
	argvs, guidance := plan(in)
	if guidance != "" {
		t.Fatalf("unexpected guidance: %s", guidance)
	}
	for _, argv := range argvs {
		if len(argv) > 0 && argv[0] == "attach-session" {
			continue // would block the test
		}
		full := append([]string{"-L", socket}, argv...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v failed: %v\n%s", full, err, out)
		}
	}
	// The session should now exist on the isolated socket.
	if err := exec.Command("tmux", "-L", socket, "has-session", "-t", "="+session).Run(); err != nil {
		t.Errorf("has-session after build: %v", err)
	}
}

// argvContains reports whether argv contains a "-flag value" pair.
func argvContains(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}
