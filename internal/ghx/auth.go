package ghx

import (
	"os/exec"
	"regexp"
	"strings"
)

// AuthStatus is what `gh auth status` reports about one host: whether there is a
// credential, whose it is, what kind it is, what it can do, and how git will talk
// to the forge.
//
// It deliberately carries no token value and cannot be made to. See tokenType.
type AuthStatus struct {
	// Host is the forge, e.g. "github.com". Empty when logged out.
	Host string
	// LoggedIn is true when gh reports a credential for Host at all -- that is,
	// one is configured. It says nothing about whether that credential works.
	LoggedIn bool
	// Verified is true only when gh CONFIRMED the credential with the forge.
	//
	// The distinction is load-bearing. `gh auth status` makes a network call,
	// and when it cannot reach GitHub it prints "X Failed to log in ... The
	// token ... is invalid" for a perfectly good credential. It prints exactly
	// the same thing for a genuinely revoked one. gh does not distinguish the
	// two, so neither may we: LoggedIn && !Verified means "a credential is
	// configured and could not be confirmed", which is a THIRD state, not a
	// flavour of "no credential".
	//
	// Collapsing it into !LoggedIn is dangerous, not merely imprecise -- see
	// Problem's text in preflight.go for what an operator does when told they
	// have no credential.
	Verified bool
	// VerifyFailure is gh's own stated reason when Verified is false.
	VerifyFailure string
	// Account is the login gh says the credential belongs to.
	Account string
	// Active is gh's "Active account" line: with several accounts logged in,
	// only one is used.
	Active bool
	// TokenType is the credential's prefix and nothing else -- "ghp_", "gho_",
	// "github_pat_". This is the whole point of the type: gho_ is an OAuth-App
	// token, one per (app, user), so any other `gh auth login` anywhere
	// invalidates it. That is the fault this package exists for.
	TokenType string
	// Scopes are the OAuth scopes gh reports, e.g. "repo", "read:org".
	Scopes []string
	// GitProtocol is the HOST-level git protocol, "ssh" or "https".
	//
	// Read it from here and never from `gh config get git_protocol`: that key
	// returns the GLOBAL default, which is "https" on this machine while the
	// host-level value is "ssh". A check built on the global key false-alarms
	// forever.
	GitProtocol string
	// ConfigSource is the path gh names as the credential's source, e.g.
	// "/Users/cerre/.config/gh/hosts.yml" or "keyring". Reported, never read.
	ConfigSource string
	// Raw is gh's output, kept for error messages. gh masks the token itself
	// before printing, so this holds asterisks where the secret would be.
	Raw string
}

// tokenPrefix matches only the leading type marker of a GitHub credential. The
// capture group stops at the underscore on purpose: no rule in this package may
// widen it to include the secret. `gh auth status` masks the value anyway, but
// this type must be unable to hold one even if a future gh stops masking.
var tokenPrefix = regexp.MustCompile(`\b(gh[pousr]_|github_pat_)`)

// tokenType returns the credential's prefix, or "" if the line names none.
func tokenType(line string) string {
	if m := tokenPrefix.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// Auth reports the current `gh auth status`.
//
// It is the one call in this package that must work when nothing else does. gh
// exits non-zero when logged out, so the output is parsed regardless of exit
// code: a non-zero exit is a finding to report, not a reason to stop parsing.
// CombinedOutput is used for the same reason -- the logged-out message has
// landed on both streams across gh versions, and losing it would turn a
// diagnosable red into "gh failed".
//
// This function, and this package, NEVER call `gh auth login`, `gh auth logout`
// or `gh auth refresh`, and never read ~/.config/gh/hosts.yml. Both rules are
// load-bearing:
//
//   - `gh auth status` reports invalidity WITHOUT a working token, which is
//     exactly the property a check on the credential needs. Anything that
//     required a valid credential to run would go blind during the fault it
//     exists to catch.
//
//     It does NOT work offline, and an earlier version of this comment claimed
//     it did. `gh auth status` makes a network call: with the forge unreachable
//     it prints "X Failed to log in ... The token ... is invalid" for a
//     credential that is fine. So the correct claim is that this check needs no
//     VALID token, not that it needs no network -- see AuthStatus.Verified.
//
//   - A repair path that can leave the machine with no credential is the same
//     class of fault as the incident. The 2026-07-27 swap script's
//     install-failure fallback ran `gh auth logout` and left the mini with no
//     credential for about 72 seconds. Its probe folds in here; its fallback
//     does not, and must not be reintroduced.
func (CLI) Auth() (*AuthStatus, error) { return parseAuthStatus(ghAuthStatus()) }

// ghAuthStatus shells the command and returns whatever it printed. The error is
// swallowed deliberately -- see Auth.
func ghAuthStatus() string {
	out, _ := exec.Command("gh", "auth", "status").CombinedOutput()
	return string(out)
}

// parseAuthStatus reads gh's human-readable status. There is no --json for it,
// so this parses prose; every field is optional and a missing one leaves its
// zero value, which the preflight then reports as a problem rather than
// guessing.
func parseAuthStatus(out string) (*AuthStatus, error) {
	st := &AuthStatus{Raw: out}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.Contains(line, "not logged into any GitHub host"):
			return st, nil
		case strings.HasPrefix(line, "✓ Logged in to"):
			st.LoggedIn = true
			st.Verified = true
			st.Host, st.Account, st.ConfigSource = parseLoginLine(line)
		case strings.HasPrefix(line, "X Failed to log in to"),
			strings.HasPrefix(line, "✗ Failed to log in to"):
			// A credential IS configured -- gh names the account and the file --
			// and gh could not confirm it. Either the token is genuinely dead or
			// the forge was unreachable; gh reports both identically.
			st.LoggedIn = true
			st.Verified = false
			st.Host, st.Account, st.ConfigSource = parseLoginLine(line)
		case strings.HasPrefix(line, "- The token in ") && strings.HasSuffix(line, "is invalid."),
			strings.Contains(line, "invalid token"):
			st.VerifyFailure = strings.TrimPrefix(line, "- ")
		case strings.HasPrefix(line, "- Active account:"):
			st.Active = strings.EqualFold(fieldValue(line), "true")
		case strings.HasPrefix(line, "- Git operations protocol:"):
			st.GitProtocol = strings.ToLower(fieldValue(line))
		case strings.HasPrefix(line, "- Token scopes:"):
			st.Scopes = parseScopes(fieldValue(line))
		case strings.HasPrefix(line, "- Token:"):
			// Only the prefix is retained. Never the rest of the line.
			st.TokenType = tokenType(line)
		case st.Host == "" && !strings.HasPrefix(line, "-") && !strings.ContainsAny(line, " \t"):
			// The bare host line gh prints above each block.
			st.Host = line
		}
	}
	return st, nil
}

// fieldValue returns what follows the first colon.
func fieldValue(line string) string {
	_, v, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// parseLoginLine pulls the host, account and config source out of either
// "✓ Logged in to github.com account RiccardoCereghino (/path/to/hosts.yml)" or
// "X Failed to log in to github.com account RiccardoCereghino (/path/…)".
// Cutting at "in to " covers both without a second parser.
func parseLoginLine(line string) (host, account, source string) {
	if _, rest, ok := strings.Cut(line, "in to "); ok {
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			host = fields[0]
		}
		for i, f := range fields {
			if f == "account" && i+1 < len(fields) {
				account = fields[i+1]
				break
			}
		}
	}
	if open := strings.LastIndex(line, "("); open != -1 {
		if close := strings.LastIndex(line, ")"); close > open {
			source = line[open+1 : close]
		}
	}
	return host, account, source
}

// parseScopes turns "'read:org', 'repo', 'workflow'" into the bare names.
func parseScopes(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.Trim(strings.TrimSpace(s), "'\""); s != "" {
			out = append(out, s)
		}
	}
	return out
}
