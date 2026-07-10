package routing

import (
	"strings"
	"testing"
)

func conv() *Conventions {
	return &Conventions{
		TitlePattern: `^[^:\n]{2,60}: .+`,
		RequireOneOf: map[string][]string{
			"priority":   {"P0-critical", "P1-high", "P2-medium", "P3-low"},
			"complexity": {"complexity/1", "complexity/2", "complexity/3"},
		},
		RequirePrefix: map[string]string{"area": "area/"},
	}
}

func TestConventionsCheck(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		labels []string
		// want is a substring of the error; empty means the issue is acceptable.
		want string
	}{
		{
			name:   "a conforming issue",
			title:  "gateway: last_login_at is never written",
			labels: []string{"P1-high", "area/security", "complexity/2"},
		},
		{
			name:   "no colon in the title",
			title:  "fix the login bug",
			labels: []string{"P1-high", "area/security", "complexity/2"},
			want:   "title",
		},
		{
			name:   "nothing after the colon",
			title:  "gateway-admin: ",
			labels: []string{"P1-high", "area/security", "complexity/2"},
			want:   "title",
		},
		{
			name:   "missing a priority",
			title:  "gateway: last_login_at is never written",
			labels: []string{"area/security", "complexity/2"},
			want:   "priority: expected one of",
		},
		{
			name:   "two priorities",
			title:  "gateway: last_login_at is never written",
			labels: []string{"P1-high", "P2-medium", "area/security", "complexity/2"},
			want:   "priority: expected exactly one",
		},
		{
			name:   "missing an area",
			title:  "gateway: last_login_at is never written",
			labels: []string{"P1-high", "complexity/2"},
			want:   "area: expected at least one label starting with \"area/\"",
		},
		{
			// Several areas are fine -- the board picks a primary by risk.
			name:   "several areas",
			title:  "gateway: last_login_at is never written",
			labels: []string{"P1-high", "area/security", "area/backups", "complexity/2"},
		},
		{
			// Every violation at once, so the filer fixes them in one pass rather
			// than discovering them one at a time.
			name:   "all violations are reported together",
			title:  "no colon here",
			labels: nil,
			want:   "title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conv().Check(tt.title, tt.labels)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// Every violation must be reported at once.
func TestConventionsCheckReportsEverything(t *testing.T) {
	err := conv().Check("no colon here", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"title", "priority", "complexity", "area"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %q:\n%v", want, err)
		}
	}
}

// Errors must be stable: the groups live in a map, and Go randomises map order.
func TestConventionsCheckIsDeterministic(t *testing.T) {
	first := conv().Check("bad", nil).Error()
	for i := 0; i < 20; i++ {
		if got := conv().Check("bad", nil).Error(); got != first {
			t.Fatalf("error text varies between runs:\n%s\n%s", first, got)
		}
	}
}

// A routing file with no conventions block enforces nothing: facet knows nothing
// about any particular organisation.
func TestNilConventionsAllowAnything(t *testing.T) {
	var c *Conventions
	if err := c.Check("anything at all", nil); err != nil {
		t.Fatalf("nil conventions rejected an issue: %v", err)
	}
}

// A title pattern that does not compile must fail at load, not at file time.
func TestValidateConventions(t *testing.T) {
	r := &Routing{
		Repos:       map[string]Repo{"a": {Dir: "a", URL: "u"}},
		Conventions: &Conventions{TitlePattern: "([unclosed"},
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected an invalid titlePattern to fail validation")
	}
	r2 := &Routing{
		Repos:       map[string]Repo{"a": {Dir: "a", URL: "u"}},
		Conventions: &Conventions{RequireOneOf: map[string][]string{"priority": nil}},
	}
	if err := r2.Validate(); err == nil {
		t.Fatal("expected an empty requireOneOf group to fail validation")
	}
}

func TestSearchTerms(t *testing.T) {
	tests := map[string]string{
		// The component prefix is dropped: a dozen issues share it.
		"gateway: last_login_at is never written": "last_login_at never written",
		// Case is preserved; short words go, because search ignores them anyway.
		"widgetapi prod: replace the baked test signingKey": "replace baked test signingKey",
		// No colon: search the whole title.
		"no colon at all here": "colon here",
	}
	for title, want := range tests {
		if got := SearchTerms(title); got != want {
			t.Errorf("SearchTerms(%q) = %q, want %q", title, got, want)
		}
	}
}
