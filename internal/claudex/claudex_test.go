package claudex

import (
	"strings"
	"testing"
)

// THE REGRESSION THIS GUARDS: --remote-control takes an OPTIONAL value, so the
// positional `--rc <name>` form becomes ambiguous the moment anything else
// follows it -- an initial prompt is read as the session name, or the name as
// the prompt. The `=` form cannot be misread.
func TestArgsAttachTheSessionNameWithEquals(t *testing.T) {
	got := Args(Options{RemoteControl: true, SessionName: "iss-facet-27"})
	want := []string{"--remote-control=iss-facet-27"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Args = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "iss-facet-27" {
			t.Errorf("Args = %v; the session name must never stand as its own word", got)
		}
	}
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want []string
	}{
		{"no remote control: no flags at all", Options{}, nil},
		{"no remote control, name ignored", Options{SessionName: "x"}, nil},
		{"remote control, no name: bare flag", Options{RemoteControl: true}, []string{"--remote-control"}},
		{"remote control with a name", Options{RemoteControl: true, SessionName: "x"}, []string{"--remote-control=x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Args(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("Args = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Args = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestShellCommand(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want string
	}{
		{"plain", Options{}, "claude"},
		{"remote control", Options{RemoteControl: true, SessionName: "iss-x"}, "claude --remote-control=iss-x"},
		{
			"prefix is exported for the command, not the shell",
			Options{RemoteControl: true, SessionName: "iss-x", SessionNamePrefix: "mini"},
			"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=mini claude --remote-control=iss-x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellCommand(tt.in); got != tt.want {
				t.Errorf("ShellCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

// A workspace name is derived from an issue title, so it reaches a pane's
// command line as text somebody else wrote.
func TestShellCommandQuotesTheSessionName(t *testing.T) {
	got := ShellCommand(Options{RemoteControl: true, SessionName: "x; rm -rf /"})
	want := `claude '--remote-control=x; rm -rf /'`
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
	// The shell must see one word after `claude`, not a second command.
	if strings.Contains(strings.TrimSuffix(got, "'"), "; rm") &&
		!strings.Contains(got, "'--remote-control=x; rm -rf /'") {
		t.Errorf("ShellCommand = %q; the metacharacter escaped its quotes", got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},
		{"iss-facet-27", "iss-facet-27"},
		{"--remote-control=iss-x", "--remote-control=iss-x"},
		{"/Users/me/Work spaces", "'/Users/me/Work spaces'"},
		{"a;b", "'a;b'"},
		{"it's", `'it'\''s'`},
	}
	for _, tt := range tests {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFindSessionURL(t *testing.T) {
	const url = "https://claude.ai/code/session_01ACfiHoTWSGhCKabYtHsW9r"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nothing there", "welcome to claude\n> ", ""},
		{"in a banner", "  Remote Control: " + url + "\n", url},
		{"trailing punctuation is not part of it", "see " + url + ".\n", url},
		{
			"an unrelated claude.ai link is not a session",
			"docs: https://claude.ai/code/settings\n",
			"",
		},
		{
			// A pane that has been through an agent restart holds every
			// session it ever ran; the live one is the most recent.
			"after a restart, the last one wins",
			"first https://claude.ai/code/session_aaa\nthen " + url + "\n",
			url,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FindSessionURL(tt.in); got != tt.want {
				t.Errorf("FindSessionURL = %q, want %q", got, tt.want)
			}
		})
	}
}
