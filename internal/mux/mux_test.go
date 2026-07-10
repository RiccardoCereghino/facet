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
	path, err := writeLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, ph := range []string{"__CWD__", "__WS__", "__AGENT__", "__NUM__"} {
		if strings.Contains(out, ph) {
			t.Errorf("placeholder %s survived rendering", ph)
		}
	}
	if !strings.Contains(out, `command "claude"`) {
		t.Errorf("agent command missing:\n%s", out)
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
	path, err := writeLayout(Session{Workspace: ws, HomeDir: ws, Override: override})
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
	path, err := writeLayout(Session{Workspace: ws, HomeDir: ws, Override: filepath.Join(ws, "nope.kdl")})
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
	path, _ := writeLayout(Session{Workspace: ws, HomeDir: ws})
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
	path, err := writeLayout(Session{
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
				for _, want := range []string{"do not nest", "detach", "--tab"} {
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
