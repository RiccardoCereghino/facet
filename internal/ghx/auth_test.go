package ghx

import (
	"strings"
	"testing"
)

// healthy is the real `gh auth status` output from the mini on 2026-07-27,
// after the sculptor's swap to a classic PAT. gh masks the token itself.
const healthy = `github.com
  ✓ Logged in to github.com account RiccardoCereghino (/Users/cerre/.config/gh/hosts.yml)
  - Active account: true
  - Git operations protocol: ssh
  - Token: ghp_************************************
  - Token scopes: 'read:org', 'repo', 'workflow'
`

// preIncident is the shape the credential had before the outage: an OAuth-App
// token, which any other `gh auth login` anywhere invalidates.
const preIncident = `github.com
  ✓ Logged in to github.com account RiccardoCereghino (keyring)
  - Active account: true
  - Git operations protocol: ssh
  - Token: gho_************************************
  - Token scopes: 'admin:public_key', 'gist', 'read:org', 'repo'
`

// httpsFlip is what `gh auth login --with-token` leaves behind: the same
// credential, with the host protocol silently switched to https.
const httpsFlip = `github.com
  ✓ Logged in to github.com account RiccardoCereghino (/Users/cerre/.config/gh/hosts.yml)
  - Active account: true
  - Git operations protocol: https
  - Token: ghp_************************************
  - Token scopes: 'read:org', 'repo', 'workflow'
`

const loggedOut = "You are not logged into any GitHub hosts. To log in, run: gh auth login\n"

// unconfirmed is the shape gh ACTUALLY produced during the 2026-07-27 outage,
// and — captured live on 2026-07-27 with a known-good credential and github.com
// made unreachable — the shape it produces on a network fault too. gh prints
// the same thing for both and does not distinguish them.
//
// This fixture was missing, and its absence is why the collapse of "cannot
// confirm" into "no credential" survived to review. Note gh's own last two
// lines: following them during a network blip destroys a working credential.
const unconfirmed = `github.com
  X Failed to log in to github.com account RiccardoCereghino (/Users/cerre/.config/gh/hosts.yml)
  - Active account: true
  - The token in /Users/cerre/.config/gh/hosts.yml is invalid.
  - To re-authenticate, run: gh auth login -h github.com
  - To forget about this account, run: gh auth logout -h github.com -u RiccardoCereghino
`

func TestParseAuthStatusHealthy(t *testing.T) {
	st, err := parseAuthStatus(healthy)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if !st.LoggedIn {
		t.Error("LoggedIn = false, want true")
	}
	if st.Host != "github.com" {
		t.Errorf("Host = %q, want github.com", st.Host)
	}
	if st.Account != "RiccardoCereghino" {
		t.Errorf("Account = %q, want RiccardoCereghino", st.Account)
	}
	if !st.Active {
		t.Error("Active = false, want true")
	}
	if st.TokenType != "ghp_" {
		t.Errorf("TokenType = %q, want ghp_", st.TokenType)
	}
	if st.GitProtocol != "ssh" {
		t.Errorf("GitProtocol = %q, want ssh", st.GitProtocol)
	}
	if st.ConfigSource != "/Users/cerre/.config/gh/hosts.yml" {
		t.Errorf("ConfigSource = %q", st.ConfigSource)
	}
	if got := strings.Join(st.Scopes, ","); got != "read:org,repo,workflow" {
		t.Errorf("Scopes = %q", got)
	}
}

func TestParseAuthStatusLoggedOut(t *testing.T) {
	st, err := parseAuthStatus(loggedOut)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if st.LoggedIn {
		t.Error("LoggedIn = true, want false")
	}
	if st.TokenType != "" || len(st.Scopes) != 0 {
		t.Errorf("logged out status carried credential fields: %+v", st)
	}
}

// TestParseAuthStatusUnconfirmed: a configured-but-unconfirmable credential is
// its own state. Reporting it as "logged out" would tell an operator whose
// credential is fine that they have none.
func TestParseAuthStatusUnconfirmed(t *testing.T) {
	st, err := parseAuthStatus(unconfirmed)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if !st.LoggedIn {
		t.Error("a credential IS configured here — gh names the account and the file")
	}
	if st.Verified {
		t.Error("gh did not confirm it, so Verified must be false")
	}
	if st.Account != "RiccardoCereghino" {
		t.Errorf("Account = %q, want RiccardoCereghino", st.Account)
	}
	if st.Host != "github.com" {
		t.Errorf("Host = %q, want github.com", st.Host)
	}
	if st.ConfigSource != "/Users/cerre/.config/gh/hosts.yml" {
		t.Errorf("ConfigSource = %q", st.ConfigSource)
	}
	if !strings.Contains(st.VerifyFailure, "is invalid") {
		t.Errorf("VerifyFailure must quote gh's own words, got %q", st.VerifyFailure)
	}
}

// TestVerifiedOnlyOnConfirmation pins the three states apart, which is the
// whole point: two of them are failures and they need different responses.
func TestVerifiedOnlyOnConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		out              string
		loggedIn, verify bool
	}{
		{"confirmed", healthy, true, true},
		{"configured but unconfirmable", unconfirmed, true, false},
		{"no credential at all", loggedOut, false, false},
	} {
		st, _ := parseAuthStatus(tc.out)
		if st.LoggedIn != tc.loggedIn || st.Verified != tc.verify {
			t.Errorf("%s: LoggedIn=%v Verified=%v, want %v/%v",
				tc.name, st.LoggedIn, st.Verified, tc.loggedIn, tc.verify)
		}
	}
}

func TestParseAuthStatusOAuthToken(t *testing.T) {
	st, _ := parseAuthStatus(preIncident)
	if st.TokenType != "gho_" {
		t.Errorf("TokenType = %q, want gho_", st.TokenType)
	}
	if st.ConfigSource != "keyring" {
		t.Errorf("ConfigSource = %q, want keyring", st.ConfigSource)
	}
}

// TestTokenValueIsUnrepresentable is the load-bearing test of this package. gh
// masks the token today; if it ever stops, the struct must still be incapable
// of holding the secret. The assertion is on the TYPE's behaviour, not on gh's.
func TestTokenValueIsUnrepresentable(t *testing.T) {
	const secret = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	unmasked := strings.Replace(healthy,
		"ghp_************************************", secret, 1)

	st, err := parseAuthStatus(unmasked)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if st.TokenType != "ghp_" {
		t.Fatalf("TokenType = %q, want the prefix alone", st.TokenType)
	}
	for _, field := range []struct{ name, val string }{
		{"TokenType", st.TokenType},
		{"Host", st.Host},
		{"Account", st.Account},
		{"GitProtocol", st.GitProtocol},
		{"ConfigSource", st.ConfigSource},
		{"Scopes", strings.Join(st.Scopes, ",")},
	} {
		if strings.Contains(field.val, "0123456789") {
			t.Errorf("%s retained token material: %q", field.name, field.val)
		}
	}
	// Raw is gh's own output and is documented as such; it is the one field
	// that reflects whatever gh printed, and it exists for error messages.
	// Nothing else may carry the value.
	if !strings.Contains(st.Raw, secret) {
		t.Log("Raw did not contain the value; that is fine, but the contract is " +
			"that Raw is gh's output verbatim and callers must not print it")
	}
}

func TestParseAuthStatusHTTPSFlip(t *testing.T) {
	st, _ := parseAuthStatus(httpsFlip)
	if st.GitProtocol != "https" {
		t.Errorf("GitProtocol = %q, want https", st.GitProtocol)
	}
}

func TestTokenType(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"  - Token: ghp_****", "ghp_"},
		{"  - Token: gho_****", "gho_"},
		{"  - Token: github_pat_11AB****", "github_pat_"},
		{"  - Token: ghs_****", "ghs_"},
		{"  - Token: ", ""},
	} {
		if got := tokenType(tc.line); got != tc.want {
			t.Errorf("tokenType(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}
