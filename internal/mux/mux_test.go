package mux

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKdlPathEscapesWindowsSeparators(t *testing.T) {
	// A backslash is KDL's escape character, so an unescaped Windows path makes
	// the layout unparseable -- and zellij's error would blame the layout, not us.
	tests := map[string]string{
		`C:\Users\me\ws`:     `C:\\Users\\me\\ws`,
		`/home/me/ws`:        `/home/me/ws`,
		`C:\a "quoted" \dir`: `C:\\a \"quoted\" \\dir`,
	}
	for in, want := range tests {
		if got := kdlPath(in); got != want {
			t.Errorf("kdlPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteLayoutSubstitutes(t *testing.T) {
	ws := t.TempDir()
	s := Session{
		Name: "iss-x-1", Workspace: ws, HomeDir: filepath.Join(ws, "repo"),
		Number: 42, Agent: "claude",
	}
	path, _, err := writeLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, ph := range []string{"__CWD__", "__WS__", "__AGENT_CMD__", "__AGENT_ARGS__", "__NUM__"} {
		if strings.Contains(out, ph) {
			t.Errorf("placeholder %s survived rendering", ph)
		}
	}
	// The agent is an argument to a shell, never the executable itself.
	if !strings.Contains(out, "claude") || strings.Contains(out, `command "claude"`) {
		t.Errorf("agent must be passed to a shell, not exec'd:\n%s", out)
	}
	if !strings.Contains(out, `name="#42"`) {
		t.Errorf("issue number missing:\n%s", out)
	}
	// It must land under .facet/, so reap removes it with everything else.
	if filepath.Dir(path) != filepath.Join(ws, ".facet") {
		t.Errorf("layout written to %s", path)
	}
	if runtime.GOOS == "windows" && strings.Contains(out, `cwd "C:\U`) {
		t.Error("Windows path was not escaped for KDL")
	}
}

func TestWriteLayoutHonoursOverride(t *testing.T) {
	ws := t.TempDir()
	override := filepath.Join(ws, "custom.kdl")
	if err := os.WriteFile(override, []byte("layout { cwd \"__CWD__\" }\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	path, _, err := writeLayout(Session{Workspace: ws, HomeDir: ws, Override: override})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "tab name") {
		t.Errorf("built-in layout was used despite an override:\n%s", b)
	}
}

// An unreadable override must fall back to the built-in layout, not fail a spawn.
func TestWriteLayoutIgnoresMissingOverride(t *testing.T) {
	ws := t.TempDir()
	path, _, err := writeLayout(Session{Workspace: ws, HomeDir: ws, Override: filepath.Join(ws, "nope.kdl")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "tab name") {
		t.Error("did not fall back to the built-in layout")
	}
}

func TestWriteLayoutDefaultsToAShell(t *testing.T) {
	ws := t.TempDir()
	path, _, _ := writeLayout(Session{Workspace: ws, HomeDir: ws})
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "__AGENT__") || !strings.Contains(string(b), "command \"") {
		t.Errorf("no shell substituted:\n%s", b)
	}
}

func TestByName(t *testing.T) {
	if ByName("none") != nil || ByName("off") != nil {
		t.Error("none/off must select no launcher")
	}
	if ByName("nonsense") != nil {
		t.Error("an unknown launcher must be nil, not a default")
	}
}

// zellij must actually accept what we generate. Skipped where zellij is absent.
func TestZellijAcceptsGeneratedLayout(t *testing.T) {
	z := Zellij{}
	if !z.Available() {
		t.Skip("zellij not installed")
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "repo"), 0o777); err != nil {
		t.Fatal(err)
	}
	path, _, err := writeLayout(Session{
		Name: "facet-layout-probe", Workspace: ws,
		HomeDir: filepath.Join(ws, "repo"), Number: 1, Agent: "pwsh",
	})
	if err != nil {
		t.Fatal(err)
	}
	// `setup --check` parses config, not layouts. There is no `zellij --validate`,
	// so the cheapest real check is that the file is well-formed KDL with the
	// braces balanced and no stray placeholder. A malformed layout only surfaces
	// when a session starts, which a test must not do.
	b, _ := os.ReadFile(path)
	src := string(b)
	if strings.Count(src, "{") != strings.Count(src, "}") {
		t.Errorf("unbalanced braces in generated layout:\n%s", src)
	}
	if strings.Contains(src, "__") {
		t.Errorf("unsubstituted placeholder in generated layout:\n%s", src)
	}
}

// zellij sessions do not nest. Attaching from inside one takes over the client,
// and against a dead session it resurrects it -- which is how a colleague's
// long-exited session came back to life while this was being written.
func TestPlan(t *testing.T) {
	const name, layout = "iss-repo-67-x", "/tmp/layout.kdl"
	tests := []struct {
		name                   string
		inSession, live, asTab bool
		wantArgv               []string
		wantGuidance           bool
	}{
		{"outside, session exists: attach",
			false, true, false, []string{"attach", name}, false},

		// --new-session-with-layout, NOT --layout: with --session, --layout means
		// "add tabs to the named session" and dies with "Session not found".
		{"outside, session missing: create from layout",
			false, false, false, []string{"--session", name, "--new-session-with-layout", layout}, false},

		// `--session <this one> --layout <file>` adds the tabs and returns at once.
		{"inside, as tabs: add them to THIS session, named explicitly",
			true, false, true, []string{"--session", "quadratic-cymbal", "--layout", layout}, false},

		// A live session for the workspace wins over adding tabs: rejoin it rather
		// than duplicating its tabs into whichever session we happen to be sitting in.
		{"inside, workspace session exists: rejoin it, do not duplicate its tabs",
			true, true, true, []string{"action", "switch-session", name}, false},
		{"inside, --session, workspace session exists: switch to it",
			true, true, false, []string{"action", "switch-session", name}, false},

		// The one case with nothing safe to run.
		{"inside, session missing: guide, do not nest",
			true, false, false, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ZELLIJ_SESSION_NAME", "quadratic-cymbal")
			argv, guidance := plan(name, layout, tt.inSession, tt.live, tt.asTab)
			if tt.wantGuidance {
				if guidance == "" {
					t.Fatalf("expected guidance, got argv %v", argv)
				}
				if argv != nil {
					t.Errorf("guidance case must run nothing, got %v", argv)
				}
				for _, want := range []string{"do not nest", "detach", "--session"} {
					if !strings.Contains(guidance, want) {
						t.Errorf("guidance omits %q:\n%s", want, guidance)
					}
				}
				return
			}
			if guidance != "" {
				t.Fatalf("unexpected guidance: %s", guidance)
			}
			if len(argv) != len(tt.wantArgv) {
				t.Fatalf("argv = %v, want %v", argv, tt.wantArgv)
			}
			for i := range argv {
				if argv[i] != tt.wantArgv[i] {
					t.Fatalf("argv = %v, want %v", argv, tt.wantArgv)
				}
			}
		})
	}
}

// `zellij attach` must never be reached from inside a session: that is the call
// that resurrects a dead session and steals the client.
func TestPlanNeverAttachesFromInsideASession(t *testing.T) {
	for _, live := range []bool{true, false} {
		for _, asTab := range []bool{true, false} {
			argv, _ := plan("n", "l", true, live, asTab)
			if len(argv) > 0 && argv[0] == "attach" {
				t.Errorf("plan(inSession=true, live=%v, asTab=%v) = %v; must not attach", live, asTab, argv)
			}
		}
	}
}

func TestInSessionReadsZellijEnv(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	if InSession() {
		t.Error("empty ZELLIJ must not count as being in a session")
	}
	t.Setenv("ZELLIJ", "0")
	t.Setenv("ZELLIJ_SESSION_NAME", "quadratic-cymbal")
	if !InSession() {
		t.Error(`ZELLIJ="0" still means we are inside a session -- zellij sets it to 0`)
	}
	if SessionName() != "quadratic-cymbal" {
		t.Errorf("SessionName = %q", SessionName())
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
// only when the workspace can arrive as tabs beside what you are already doing.
func TestAutoOpen(t *testing.T) {
	zj := fakeLauncher{"zellij"}
	wt := fakeLauncher{"windows-terminal"}

	tests := []struct {
		name       string
		l          Launcher
		inSession  bool
		ownSession bool
		wantOpen   bool
		wantAsTab  bool
	}{
		{"inside zellij: open as tabs, unprompted", zj, true, false, true, true},
		{"inside zellij, --session: nothing safe to do automatically", zj, true, true, true, false},

		// Outside a session, opening would seize the terminal. Stay opt-in.
		{"outside zellij: do not open", zj, false, false, false, false},

		// Windows Terminal spawns a window; never do that unasked.
		{"windows-terminal, even inside zellij: do not open", wt, true, false, false, false},

		{"no launcher: do not open", nil, true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.inSession {
				t.Setenv("ZELLIJ", "0")
			} else {
				t.Setenv("ZELLIJ", "")
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
	t.Setenv("ZELLIJ", "")
	if open, _ := AutoOpen(fakeLauncher{"zellij"}, false); open {
		t.Error("outside a session, opening seizes the terminal; it must stay opt-in")
	}
}

// zellij's Windows backend calls CreateProcessW with no shell. It cannot start a
// `#!/bin/sh` script or a .cmd shim -- and `claude`, installed by npm, is both.
// When the spawn fails the fork PANICS and the whole session dies. It did.
//
// So the layout must never name the agent directly: it names a real executable
// and passes the agent as an argument.
func TestLayoutNeverExecsTheAgentDirectly(t *testing.T) {
	ws := t.TempDir()
	path, _, err := writeLayout(Session{
		Name: "iss-x-67", Workspace: ws, HomeDir: filepath.Join(ws, "repo"),
		Number: 67, Agent: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	src := string(b)

	if strings.Contains(src, `command "claude"`) {
		t.Fatalf("layout execs the agent directly; CreateProcessW cannot start it:\n%s", src)
	}
	if !strings.Contains(src, "args ") || !strings.Contains(src, "claude") {
		t.Errorf("agent is not passed as an argument to a shell:\n%s", src)
	}
	// Every `command` in the layout must be an executable that exists.
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "command ") {
			continue
		}
		raw := strings.Trim(strings.TrimPrefix(line, "command "), `"`)
		p := strings.ReplaceAll(raw, `\`, `\`) // undo KDL escaping
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(p), ".exe") {
			t.Errorf("layout command %q is not a PE executable", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("layout command %q does not exist: %v", p, err)
		}
	}
}

// A top-level cwd is ignored when a layout is added as tabs to an existing
// session: zellij used facet's own working directory instead. Every pane carries
// its own cwd now.
func TestLayoutSetsCwdOnEveryPane(t *testing.T) {
	ws := t.TempDir()
	home := filepath.Join(ws, "repo")
	path, _, err := writeLayout(Session{Name: "n", Workspace: ws, HomeDir: home, Number: 1, Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	src := string(b)

	var panes, panesWithCwd int
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pane") || strings.HasPrefix(line, "pane split_direction") {
			continue
		}
		panes++
		if strings.Contains(line, "cwd=") {
			panesWithCwd++
		}
	}
	if panes == 0 {
		t.Fatalf("no panes found:\n%s", src)
	}
	if panes != panesWithCwd {
		t.Errorf("%d of %d panes carry a cwd; a top-level cwd is ignored for added tabs:\n%s",
			panesWithCwd, panes, src)
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
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(exe), ".exe") {
		t.Errorf("exe = %q; must be a PE image", exe)
	}
	if !filepath.IsAbs(exe) {
		t.Errorf("exe = %q; must be an absolute path so zellij need not search PATH", exe)
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

func TestFindExecutableRejectsNonPEOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	// `claude` resolves to an extensionless sh script; it must never be chosen.
	if p := findExecutable("claude"); p != "" {
		t.Errorf("findExecutable(claude) = %q; an npm shim is not a PE image", p)
	}
	if p := findExecutable("pwsh"); p == "" || !strings.EqualFold(filepath.Ext(p), ".exe") {
		t.Errorf("findExecutable(pwsh) = %q; want a .exe", p)
	}
}

// A layout override is copied once and then goes stale, silently, defeating every
// fix shipped after the copy. That is precisely what happened: an override seeded
// from an older template still said `command "__AGENT__"`, so zellij was handed a
// command that does not exist.
func TestStaleOverrideIsRejectedAndFallsBack(t *testing.T) {
	ws := t.TempDir()
	override := filepath.Join(ws, "stale.kdl")
	stale := "layout {\n    cwd \"__CWD__\"\n    tab name=\"#__NUM__\" {\n" +
		"        pane { command \"__AGENT__\" }\n    }\n}\n"
	if err := os.WriteFile(override, []byte(stale), 0o666); err != nil {
		t.Fatal(err)
	}
	path, warn, err := writeLayout(Session{
		Name: "n", Workspace: ws, HomeDir: filepath.Join(ws, "repo"),
		Number: 67, Agent: "claude", Override: override,
	})
	if err != nil {
		t.Fatal(err)
	}
	if warn == "" {
		t.Error("a stale override must be reported, not used silently")
	}
	if !strings.Contains(warn, "__AGENT__") {
		t.Errorf("warning should name the leftover placeholder: %q", warn)
	}
	b, _ := os.ReadFile(path)
	src := string(b)
	if strings.Contains(src, "__AGENT__") {
		t.Fatalf("stale override was written to disk:\n%s", src)
	}
	if !strings.Contains(src, `name="agent"`) {
		t.Errorf("did not fall back to the built-in layout:\n%s", src)
	}
	if p := layoutProblem(src); p != "" {
		t.Errorf("fallback layout is itself unsafe: %s", p)
	}
}

// An override naming a command that cannot be spawned is the other way to panic
// zellij. Reject that too.
func TestOverrideWithUnrunnableCommandIsRejected(t *testing.T) {
	ws := t.TempDir()
	override := filepath.Join(ws, "bad.kdl")
	if err := os.WriteFile(override,
		[]byte("layout {\n    pane { command \"definitely-not-a-real-binary-xyz\" }\n}\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	_, warn, err := writeLayout(Session{Name: "n", Workspace: ws, HomeDir: ws, Override: override})
	if err != nil {
		t.Fatal(err)
	}
	if warn == "" || !strings.Contains(warn, "not on PATH") {
		t.Errorf("warn = %q; an unrunnable command must be rejected", warn)
	}
}

func TestLayoutProblem(t *testing.T) {
	shell := findExecutable(shellCandidates()...)
	if shell == "" {
		t.Skip("no shell")
	}
	esc := kdlPath(shell)

	tests := map[string]struct {
		src  string
		want string // substring, "" = safe
	}{
		"safe, absolute command": {"layout { pane { command \"" + esc + "\" } }", ""},
		// KDL also spells it as a property. Both must be checked.
		"safe, property form":  {"layout { pane command=\"" + esc + "\" }", ""},
		"safe, no command":     {"layout { pane }", ""},
		"leftover placeholder": {"layout { cwd \"__CWD__\" }", "unsubstituted"},
		"missing absolute file": {
			"layout {\n    pane { command \"" + kdlPath(missingAbs(t)) + "\" }\n}",
			"does not exist"},
		"not on PATH":   {"layout { pane { command \"definitely-not-a-real-binary-xyz\" } }", "not on PATH"},
		"empty command": {"layout {\n    pane { command \"\" }\n}", "empty command"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := layoutProblem(tt.src)
			if tt.want == "" {
				if got != "" {
					t.Errorf("layoutProblem = %q; want safe", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("layoutProblem = %q; want it to mention %q", got, tt.want)
			}
		})
	}
}

// The built-in layout must always be safe. If it is not, that is a facet bug and
// writeLayout returns an error rather than handing it to zellij.
func TestBuiltInLayoutIsAlwaysSafe(t *testing.T) {
	ws := t.TempDir()
	path, warn, err := writeLayout(Session{Name: "n", Workspace: ws, HomeDir: ws, Number: 1, Agent: "claude"})
	if err != nil {
		t.Fatalf("built-in layout is unsafe: %v", err)
	}
	if warn != "" {
		t.Errorf("unexpected warning: %s", warn)
	}
	b, _ := os.ReadFile(path)
	if p := layoutProblem(string(b)); p != "" {
		t.Errorf("built-in layout %s", p)
	}
}

// missingAbs returns an absolute path that does not exist. On Windows a leading
// separator is not absolute -- it needs a drive letter -- so derive it from a
// real temp dir.
func missingAbs(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "no-such-binary.exe")
	if !filepath.IsAbs(p) {
		t.Fatalf("%q is not absolute", p)
	}
	return p
}

// Adding tabs must never steal focus. The layout is applied to a session the user
// is already working in; `focus=true` yanks them out of the pane they are typing
// in and drops them into a freshly started agent. It did.
func TestLayoutNeverStealsFocus(t *testing.T) {
	ws := t.TempDir()
	path, _, err := writeLayout(Session{
		Name: "iss-x-67", Workspace: ws, HomeDir: filepath.Join(ws, "repo"),
		Number: 67, Agent: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "focus=true") {
		t.Errorf("layout steals focus:\n%s", b)
	}
}

// The layout is applied in full on every attach, so a second tab is duplicated
// each time. Exactly one.
func TestLayoutHasExactlyOneTab(t *testing.T) {
	ws := t.TempDir()
	path, _, err := writeLayout(Session{
		Name: "iss-x-67", Workspace: ws, HomeDir: filepath.Join(ws, "repo"), Number: 67,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var tabs int
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "tab ") {
			tabs++
		}
	}
	if tabs != 1 {
		t.Errorf("layout declares %d tabs; every attach would add them all:\n%s", tabs, b)
	}
}

// The two panes must land in different places: the agent in the home clone, the
// shell at the workspace root, so both are reachable without cd.
func TestLayoutPanesUseBothDirectories(t *testing.T) {
	ws := t.TempDir()
	home := filepath.Join(ws, "repo")
	path, _, err := writeLayout(Session{Name: "n", Workspace: ws, HomeDir: home, Number: 1, Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	src := string(b)
	if !strings.Contains(src, kdlPath(home)) {
		t.Error("agent pane is not in the home clone")
	}
	if !strings.Contains(src, `cwd="`+kdlPath(ws)+`"`) {
		t.Errorf("shell pane is not at the workspace root:\n%s", src)
	}
}
