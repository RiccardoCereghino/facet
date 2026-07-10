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
