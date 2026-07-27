package ghx

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestCheckUnconfirmedIsFatalButBlamesTheRightThing.
//
// Still fatal -- the foreman's all-fatal ruling stands, and if github.com is
// unreachable then spawn cannot clone or fetch either, so refusing is correct.
// What is asserted here is the MESSAGE, because a gate that fires on the right
// condition for the wrong stated reason is a broken gate, not a strict one.
//
// The hazard is concrete: told "you have no credential", a reasonable operator
// regenerates the token -- a `gh auth logout`, which is precisely how this fleet
// got a ~72-second no-credential window. gh's own printed advice says to do it.
func TestCheckUnconfirmedIsFatalButBlamesTheRightThing(t *testing.T) {
	probs := checkFixture(t, unconfirmed)

	if len(probs) != 1 {
		t.Fatalf("want exactly one finding, not a pile of 'none reported': %s", names(probs))
	}
	p := probs[0]
	if p.Check == "logged in" {
		t.Fatal("a configured-but-unconfirmable credential must NOT be reported as absent")
	}
	if p.Check != "credential confirmed" {
		t.Fatalf("Check = %q", p.Check)
	}
	// The three things that stop this message causing the damage.
	for _, want := range []string{"DO NOT REGENERATE", "cannot reach", "gh auth logout"} {
		if !strings.Contains(p.Why, want) {
			t.Errorf("Why must contain %q, so the operator does not destroy a working credential: %q", want, p.Why)
		}
	}
	// And it must quote gh rather than paraphrase it.
	if !strings.Contains(p.Got, "is invalid") {
		t.Errorf("Got must carry gh's own words: %q", p.Got)
	}
}

// TestCheckUnconfirmedIsStillFatal states the ruling as an assertion, so a
// later reader cannot mistake the friendlier message for a softened gate.
func TestCheckUnconfirmedIsStillFatal(t *testing.T) {
	if len(checkFixture(t, unconfirmed)) == 0 {
		t.Fatal("an unconfirmable credential must still fail the preflight")
	}
}

// TestUnrecognisedOutputKeepsTheWarning is the guard on the parser's DEFAULT.
//
// The do-not-regenerate warning hangs off the unconfirmed branch alone. If an
// unrecognised output fell through to "absent", the operator would instead read
// "got none" -- and the natural response to that is to reissue the token, which
// is the ~72-second bare-machine window. The fall-through would bypass the
// protection precisely when the parse is least reliable.
//
// So this asserts on THE WARNING, not merely on the state: the warning is the
// thing being protected.
func TestUnrecognisedOutputKeepsTheWarning(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"a wording gh has not shipped yet", futureShape},
		{"no output at all", ""},
		{"garbage", "totally unexpected\nnonsense from somewhere\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probs := checkFixture(t, tc.out)
			if len(probs) != 1 {
				t.Fatalf("want exactly one finding, got: %s", names(probs))
			}
			p := probs[0]
			if p.Check == "logged in" {
				t.Fatal("an unrecognised output must NOT be reported as 'no credential' — " +
					"that tells the operator to go and issue one, and issuing is the " +
					"dangerous step")
			}
			if p.Check != "credential confirmed" {
				t.Fatalf("Check = %q, want the cautious branch", p.Check)
			}
			if !strings.Contains(p.Why, "DO NOT REGENERATE") {
				t.Errorf("the warning is the thing being protected and it is missing: %q", p.Why)
			}
		})
	}
}

// TestUnrecognisedOutputBlamesTheParser: when the shape is unknown, the report
// must say the PARSER did not recognise it, not imply a verdict on the
// credential it never reached.
func TestUnrecognisedOutputBlamesTheParser(t *testing.T) {
	probs := checkFixture(t, futureShape)
	if len(probs) != 1 {
		t.Fatalf("want one finding, got: %s", names(probs))
	}
	if !strings.Contains(probs[0].Got, "matched no status shape") {
		t.Errorf("Got must name the parse gap: %q", probs[0].Got)
	}
}

// TestNilStatusIsCautious: absence of evidence is not evidence of absence. A
// nil status must take the same suppressing branch.
func TestNilStatusIsCautious(t *testing.T) {
	probs := Check(nil, req())
	if len(probs) != 1 || probs[0].Check != "credential confirmed" {
		t.Fatalf("a nil status must land cautious, got: %s", names(probs))
	}
	if !strings.Contains(probs[0].Why, "DO NOT REGENERATE") {
		t.Error("the warning must survive a nil status too")
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

// TestCheckSSHKeyExistence covers the platform-neutral half. It must hold
// everywhere, Windows included.
func TestCheckSSHKeyExistence(t *testing.T) {
	dir := t.TempDir()

	probs, note := CheckSSHKey("")
	if probs != nil || note != "" {
		t.Errorf("an empty path skips the check, got: %s / %q", names(probs), note)
	}

	key := filepath.Join(dir, "id_ed25519")
	probs, note = CheckSSHKey(key)
	if len(probs) != 1 || probs[0].Check != "ssh key" {
		t.Fatalf("a missing key must be reported on every platform, got: %s", names(probs))
	}
	if !strings.Contains(probs[0].Why, "API does not accept SSH") {
		t.Errorf("Why must say SSH cannot replace the token: %q", probs[0].Why)
	}
	if note != "" {
		t.Errorf("a missing key is the finding; permissions are moot, so no note: %q", note)
	}

	if err := os.WriteFile(key, []byte("not a real key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if probs, _ = CheckSSHKey(key); len(probs) != 0 {
		t.Errorf("a present, 0600 key must produce no problem anywhere, got: %s", names(probs))
	}
}

// TestCheckSSHKeyPermissions asserts the DOCUMENTED behaviour of the permission
// half on each platform. It is deliberately not a skip: on Windows it asserts
// that the check declares itself not applicable and explains why, which is a
// real assertion about a real contract. Weakening either branch to make both
// pass would be the thing Law 4 forbids -- a green tick standing in for a check
// that did not happen.
func TestCheckSSHKeyPermissions(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(key, []byte("not a real key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	probs, note := CheckSSHKey(key)

	if runtime.GOOS == "windows" {
		// Windows has no POSIX permission bits: os.FileMode does not represent
		// NTFS ACLs, so a mode test would pass or fail for reasons unrelated to
		// who can read the key.
		if len(probs) != 0 {
			t.Errorf("no mode-based verdict may be reached on Windows, got: %s", names(probs))
		}
		if note == "" {
			t.Fatal("Windows MUST report that the permission check did not run — " +
				"a silent pass would let an operator believe it was verified")
		}
		if !strings.Contains(note, "NOT checked") {
			t.Errorf("the note must be unmistakable, not a hedge: %q", note)
		}
		if !strings.Contains(note, "icacls") {
			t.Errorf("the note must tell the operator how to check it themselves: %q", note)
		}
		return
	}

	// Every other platform: this is a real check and it fires.
	if len(probs) != 1 || probs[0].Check != "ssh key permissions" {
		t.Fatalf("a group/world-readable key must be reported, got: %s", names(probs))
	}
	if probs[0].Got != "0644" {
		t.Errorf("Got must name the offending mode, got %q", probs[0].Got)
	}
	if note != "" {
		t.Errorf("the check applies on %s, so nothing may be reported as skipped: %q",
			runtime.GOOS, note)
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
