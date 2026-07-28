package main

import (
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// resolveLaunch governs the path with NO pane: claude would take over the
// terminal `facet spawn` was run from. That is why it stays off unless routing
// or a named flag turns it on, even though the same --claude flag defaults to
// true for a pane.
func TestResolveLaunch(t *testing.T) {
	on := &routing.Routing{Spawn: &routing.Spawn{RC: true}}
	off := &routing.Routing{}

	tests := []struct {
		name  string
		argv  []string
		route *routing.Routing
		want  bool
	}{
		{"no flags, routing silent: launch nothing", nil, off, false},
		{"no flags, no routing at all: launch nothing", nil, nil, false},
		{"no flags, routing says rc: launch", nil, on, true},

		{"--claude, routing silent: launch", []string{"--claude=true"}, off, true},
		{"--rc, routing silent: launch", []string{"--rc"}, off, true},

		{"--claude=false beats routing", []string{"--claude=false"}, on, false},
		{"--no-rc beats routing", []string{"--no-rc"}, on, false},
		{"--no-rc beats --rc and routing", []string{"--rc", "--no-rc"}, on, false},

		// --remote only says HOW to launch, never WHETHER.
		{"--remote=false alone launches nothing", []string{"--remote=false"}, off, false},
		{"--remote=false still launches when routing says rc", []string{"--remote=false"}, on, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := spawnOpts{Agent: resolveFlags(t, tt.argv...)}
			if got := resolveLaunch(o, tt.route); got != tt.want {
				t.Errorf("resolveLaunch(%v, rc=%v) = %v, want %v",
					tt.argv, tt.route.SpawnRC(), got, tt.want)
			}
		})
	}
}

// offlineGH fails the test if anything reaches the forge. The seat checks are
// meant to happen before the credential gate and before the issue lookup, and
// "it refuses" and "it refuses without having done anything first" are different
// claims -- only the second one means a mistyped seat name cannot leave a branch
// behind on the forge.
type offlineGH struct {
	fakeGH
	t *testing.T
}

func (o *offlineGH) Auth() (*ghx.AuthStatus, error) {
	o.t.Error("spawn reached the credential check before validating the seat")
	return o.fakeGH.Auth()
}

func (o *offlineGH) ViewIssue(repo string, number int) (*ghx.Issue, error) {
	o.t.Errorf("spawn looked up %s#%d before validating the seat", repo, number)
	return o.fakeGH.ViewIssue(repo, number)
}

func TestSpawnRefusesABadSeatBeforeTouchingTheForge(t *testing.T) {
	tests := []struct {
		name    string
		opts    spawnOpts
		wantMsg string
	}{
		{
			// No default is derived from anything. A seat name nobody chose is a
			// name nobody can be held to, which is the whole point of the file.
			name:    "no seat at all",
			opts:    spawnOpts{Number: 12, Repo: "owner/repo"},
			wantMsg: "--seat",
		},
		{
			// A multiplexer target reads '.' as the pane separator, so this name
			// addresses a pane of a differently-named session.
			name:    "dotted seat name",
			opts:    spawnOpts{Number: 12, Repo: "owner/repo", Seat: "w-example-m7.1"},
			wantMsg: "w-example-m71",
		},
		{
			name:    "unqualified scope entry",
			opts:    spawnOpts{Number: 12, Repo: "owner/repo", Seat: "w-example-12", Scope: []string{"repo#7"}},
			wantMsg: "--scope",
		},
		{
			name:    "scope entry with no issue number",
			opts:    spawnOpts{Number: 12, Repo: "owner/repo", Seat: "w-example-12", Scope: []string{"owner/repo"}},
			wantMsg: "--scope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := gh
			gh = &offlineGH{t: t}
			t.Cleanup(func() { gh = prev })

			err := runSpawn(tt.opts)
			if err == nil {
				t.Fatal("spawn accepted it")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
			if !strings.Contains(err.Error(), "fix:") {
				t.Errorf("error %q does not tell the reader how to fix it", err)
			}
		})
	}
}

func TestIssueBranchNameCarriesFeaturePrefix(t *testing.T) {
	got := issueBranchName(42, "some-slug")
	want := "feature/42-some-slug"
	if got != want {
		t.Errorf("issueBranchName(42, %q) = %q, want %q", "some-slug", got, want)
	}
}
