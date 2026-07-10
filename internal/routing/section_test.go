package routing

import "testing"

// UpsertScope records the repo set a human confirmed at spawn time, so the next
// spawn does not have to guess again. It rewrites one section of someone's issue
// body, which is unforgiving: the neighbouring sections must come back byte for
// byte, and running it twice must change nothing the second time.
func TestUpsertScope(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		repos []string
		want  string
		// changed reports whether the body should differ from the input.
		changed bool
	}{
		{
			name:    "replaces an existing section, leaves its neighbours alone",
			body:    "### Summary\n\nx\n\n### Repos in scope\n\nplatform\n\n### Acceptance\n\ny\n",
			repos:   []string{"platform", "gateway"},
			want:    "### Summary\n\nx\n\n### Repos in scope\n\nplatform, gateway\n\n### Acceptance\n\ny\n",
			changed: true,
		},
		{
			name:    "idempotent: the same set twice is a no-op",
			body:    "### Repos in scope\n\nplatform, gateway\n",
			repos:   []string{"platform", "gateway"},
			want:    "### Repos in scope\n\nplatform, gateway\n",
			changed: false,
		},
		{
			name:    "appends when the section is absent",
			body:    "## Problem\n\nThe backup has never been restored.\n",
			repos:   []string{"platform", "infra"},
			want:    "## Problem\n\nThe backup has never been restored.\n\n## Repos in scope\n\nplatform, infra\n",
			changed: true,
		},
		{
			name:    "a form's checkbox rendering is replaced by the confirmed set",
			body:    "### Repos in scope\n\n- [x] platform\n- [ ] infra\n\n### Blocked by\n\n_No response_\n",
			repos:   []string{"platform"},
			want:    "### Repos in scope\n\nplatform\n\n### Blocked by\n\n_No response_\n",
			changed: true,
		},
		{
			name:    "an empty repo set never rewrites the body",
			body:    "### Repos in scope\n\nplatform\n",
			repos:   nil,
			want:    "### Repos in scope\n\nplatform\n",
			changed: false,
		},
		{
			// The heading level is whatever the body already uses: forms emit ###,
			// hand-written issues use ##. Do not "normalise" someone's markdown.
			name:    "an existing heading keeps its level",
			body:    "## Repos in scope\n\nplatform\n",
			repos:   []string{"platform", "infra"},
			want:    "## Repos in scope\n\nplatform, infra\n",
			changed: true,
		},
		{
			name:    "an empty body gets a section",
			body:    "",
			repos:   []string{"platform"},
			want:    "## Repos in scope\n\nplatform\n",
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := UpsertScope(tt.body, tt.repos)
			if got != tt.want {
				t.Errorf("body:\n%q\nwant:\n%q", got, tt.want)
			}
			if changed != tt.changed {
				t.Errorf("changed = %v, want %v", changed, tt.changed)
			}
			// Running it again must be a no-op, whatever the first pass did.
			again, changedAgain := UpsertScope(got, tt.repos)
			if changedAgain {
				t.Errorf("second pass reported a change")
			}
			if again != got {
				t.Errorf("second pass rewrote the body:\n%q", again)
			}
		})
	}
}

// The whole point is that a rewritten body still routes to the same repos.
func TestUpsertScopeRoundTrips(t *testing.T) {
	r := load(t)
	body := "### Summary\n\nx\n\n### Repos in scope\n\n- [x] gateway\n- [ ] infra-core\n\n### Acceptance\n\ny\n"
	out, changed := UpsertScope(body, []string{"platform", "gateway"})
	if !changed {
		t.Fatal("expected a rewrite")
	}
	got := r.scopeField(out)
	if len(got) != 2 || got[0] != "platform" || got[1] != "gateway" {
		t.Fatalf("rewritten body parses to %v", got)
	}
}
