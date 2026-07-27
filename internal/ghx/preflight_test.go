package ghx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// req is FleetRequirements without the SSH key, which CheckSSHKey covers
// separately and which would otherwise make these tests depend on the machine.
func req() Requirements {
	r := FleetRequirements()
	r.SSHKey = ""
	return r
}

// checkFixture parses gh output and runs the fleet requirements over it.
func checkFixture(t *testing.T, out string) []Problem {
	t.Helper()
	st, err := parseAuthStatus(out)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	return Check(st, req())
}

func names(probs []Problem) string {
	var s []string
	for _, p := range probs {
		s = append(s, p.Check)
	}
	return strings.Join(s, "; ")
}

func TestCheckHealthyIsGreen(t *testing.T) {
	if probs := checkFixture(t, healthy); len(probs) != 0 {
		t.Errorf("the real, current credential must pass: %s", names(probs))
	}
}

// TestCheckLoggedOut is the red path the whole issue is about.
func TestCheckLoggedOut(t *testing.T) {
	probs := checkFixture(t, loggedOut)
	if len(probs) != 1 || probs[0].Check != "logged in" {
		t.Fatalf("want exactly the logged-in problem, got: %s", names(probs))
	}
	// Everything below "logged in" reads fields that are meaningless when
	// logged out. Reporting them too would bury the one finding that matters.
	if probs[0].Why == "" {
		t.Error("a problem with no Why is a check the next operator deletes")
	}
}

// TestCheckOAuthToken: valid, active, right account, ample scopes -- and still
// wrong, because the token type is the failure mode.
func TestCheckOAuthToken(t *testing.T) {
	probs := checkFixture(t, preIncident)
	var found bool
	for _, p := range probs {
		if p.Check == "token type" {
			found = true
			if !strings.Contains(p.Why, "OAuth-App") {
				t.Errorf("token-type Why must name the root cause: %q", p.Why)
			}
		}
	}
	if !found {
		t.Fatalf("gho_ must be refused even while it works; got: %s", names(probs))
	}
	// It is also scope-short: the old token had no workflow scope.
	if !strings.Contains(names(probs), "token scopes") {
		t.Errorf("want a scopes problem too (no workflow); got: %s", names(probs))
	}
}

// TestCheckHTTPSFlip covers trap (c): --with-token silently switches the host
// protocol, and an https remote plus a scope gap fails late.
func TestCheckHTTPSFlip(t *testing.T) {
	probs := checkFixture(t, httpsFlip)
	if len(probs) != 1 || probs[0].Check != "git protocol" {
		t.Fatalf("want exactly the git-protocol problem, got: %s", names(probs))
	}
	if probs[0].Got != "https" || probs[0].Want != "ssh" {
		t.Errorf("want ssh/got https, got want=%q got=%q", probs[0].Want, probs[0].Got)
	}
}

func TestCheckMissingWorkflowScope(t *testing.T) {
	out := strings.Replace(healthy,
		"'read:org', 'repo', 'workflow'", "'read:org', 'repo'", 1)
	probs := checkFixture(t, out)
	if len(probs) != 1 || probs[0].Check != "token scopes" {
		t.Fatalf("want exactly the scopes problem, got: %s", names(probs))
	}
	if !strings.Contains(probs[0].Why, "workflow") {
		t.Errorf("Why must name the missing scope: %q", probs[0].Why)
	}
}

func TestCheckWrongAccount(t *testing.T) {
	out := strings.Replace(healthy, "account RiccardoCereghino", "account someone-else", 1)
	probs := checkFixture(t, out)
	if len(probs) != 1 || probs[0].Check != "account" {
		t.Fatalf("want exactly the account problem, got: %s", names(probs))
	}
}

func TestCheckInactiveAccount(t *testing.T) {
	out := strings.Replace(healthy, "Active account: true", "Active account: false", 1)
	probs := checkFixture(t, out)
	if len(probs) != 1 || probs[0].Check != "active account" {
		t.Fatalf("want exactly the active-account problem, got: %s", names(probs))
	}
}

// TestProblemsCarryReasons: the format is the deliverable. A red at 3am has to
// explain itself without anyone finding the incident it came from.
func TestProblemsCarryReasons(t *testing.T) {
	for _, out := range []string{loggedOut, preIncident, httpsFlip} {
		for _, p := range checkFixture(t, out) {
			if p.Check == "" || p.Want == "" || p.Got == "" || p.Why == "" {
				t.Errorf("incomplete problem: %+v", p)
			}
			if !strings.Contains(p.String(), "why:") {
				t.Errorf("Problem.String must surface the reason: %q", p.String())
			}
		}
	}
}

func TestCheckSSHKey(t *testing.T) {
	dir := t.TempDir()

	if probs := CheckSSHKey(""); probs != nil {
		t.Errorf("an empty path skips the check, got: %s", names(probs))
	}

	missing := filepath.Join(dir, "id_ed25519")
	probs := CheckSSHKey(missing)
	if len(probs) != 1 || probs[0].Check != "ssh key" {
		t.Fatalf("a missing key must be reported, got: %s", names(probs))
	}
	if !strings.Contains(probs[0].Why, "API does not accept SSH") {
		t.Errorf("Why must say SSH cannot replace the token: %q", probs[0].Why)
	}

	if err := os.WriteFile(missing, []byte("not a real key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if probs := CheckSSHKey(missing); len(probs) != 0 {
		t.Errorf("a 0600 key must pass, got: %s", names(probs))
	}

	if err := os.Chmod(missing, 0o644); err != nil {
		t.Fatal(err)
	}
	probs = CheckSSHKey(missing)
	if len(probs) != 1 || probs[0].Check != "ssh key permissions" {
		t.Fatalf("a world-readable key must be reported, got: %s", names(probs))
	}
}

func TestMissingScopesIsCaseInsensitive(t *testing.T) {
	if got := missingScopes([]string{"REPO", " Read:Org ", "workflow"},
		[]string{"repo", "read:org", "workflow"}); len(got) != 0 {
		t.Errorf("missingScopes = %v, want none", got)
	}
	if got := missingScopes([]string{"repo"}, []string{"repo", "workflow"}); len(got) != 1 || got[0] != "workflow" {
		t.Errorf("missingScopes = %v, want [workflow]", got)
	}
}
