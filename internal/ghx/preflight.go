package ghx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Requirements is what the fleet needs to be true of its GitHub credential
// before any command that depends on it runs.
type Requirements struct {
	// Scopes must all be present.
	Scopes []string
	// GitProtocol is the host-level protocol git must be configured to use.
	GitProtocol string
	// Account is the login the credential must belong to.
	Account string
	// ForbiddenTokenTypes are credential prefixes that must not be in use, even
	// when they are currently valid.
	ForbiddenTokenTypes []string
	// SSHKey is the private key path git uses to push. Empty skips the check.
	SSHKey string
}

// Problem is one failed check, phrased so that a red at 3am explains itself
// without anyone having to find the incident it came from. Why is not optional:
// a check whose reason is not written down is a check the next operator deletes.
type Problem struct {
	Check string // what was checked
	Want  string // what the fleet requires
	Got   string // what is actually there
	Why   string // the incident or invariant the check exists for
}

func (p Problem) String() string {
	return fmt.Sprintf("%s: want %s, got %s\n    why: %s", p.Check, p.Want, p.Got, p.Why)
}

// FleetRequirements is the standing requirement set for this fleet, as ruled on
// stele#55. Each entry's reasoning is carried in the Problem it produces.
func FleetRequirements() Requirements {
	return Requirements{
		Scopes:              []string{"repo", "read:org", "workflow"},
		GitProtocol:         "ssh",
		Account:             "RiccardoCereghino",
		ForbiddenTokenTypes: []string{"gho_"},
		SSHKey:              DefaultSSHKey(),
	}
}

// DefaultSSHKey is the key every push the fleet makes authenticates with. It
// returns "" when the home directory cannot be determined, which skips the
// check rather than inventing a path.
func DefaultSSHKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "id_ed25519")
}

// Check reports every way st falls short of req. An empty result means the
// credential surface is sound.
//
// Nothing here is advisory and nothing here can be skipped. A gate with an
// escape hatch is not a gate: the whole reason this exists is that the fault it
// catches failed silently and late, and was found mid-operation by trying to
// use the credential.
func Check(st *AuthStatus, req Requirements) []Problem {
	var probs []Problem
	add := func(check, want, got, why string) {
		probs = append(probs, Problem{Check: check, Want: want, Got: got, Why: why})
	}

	if st == nil || !st.LoggedIn {
		add("logged in", "a credential for github.com", "none",
			"this is the fault itself: on 2026-07-27 the fleet lost GitHub access "+
				"mid-teardown and found out by trying to use it. gh reports this "+
				"without needing a working token, so it can be checked first.")
		// Everything below reads fields that are meaningless when logged out.
		return probs
	}

	if !st.Active {
		add("active account", "the credential is the active one", "inactive",
			"gh can hold several logins at once; an inactive one is present in the "+
				"config and used by nothing.")
	}

	if req.Account != "" && !strings.EqualFold(st.Account, req.Account) {
		add("account", req.Account, orNone(st.Account),
			"a valid credential for the wrong identity passes every other check and "+
				"writes to the wrong account.")
	}

	for _, bad := range req.ForbiddenTokenTypes {
		if st.TokenType == bad {
			add("token type", "not "+bad, st.TokenType,
				"gho_ is an OAuth-App token and GitHub issues one per (app, user) "+
					"pair, so ANY other `gh auth login` anywhere -- another machine, a "+
					"container, a Codespace -- silently invalidates this one. That is "+
					"the root cause of the 2026-07-27 outage. A classic or fine-grained "+
					"PAT is a distinct credential and cannot be rotated out from under "+
					"the fleet by someone else's login.")
		}
	}
	if st.TokenType == "" {
		add("token type", "a recognisable credential prefix", "none reported",
			"gh named no token type, so the credential's kind -- and therefore its "+
				"rotation exposure -- cannot be established.")
	}

	if missing := missingScopes(st.Scopes, req.Scopes); len(missing) > 0 {
		add("token scopes", strings.Join(req.Scopes, ", "), scopesGot(st.Scopes),
			"missing: "+strings.Join(missing, ", ")+". read:org is REQUIRED by "+
				"`gh auth login --with-token` on a classic token, not an optional "+
				"grant. repo carries every issue, PR and search call facet and prism "+
				"make. workflow is needed for any gh-mediated workflow-file operation; "+
				"pushes are SSH so they never surfaced the gap. A valid but "+
				"scope-short token is the hardest failure to attribute, because "+
				"everything else looks green.")
	}

	if req.GitProtocol != "" && st.GitProtocol != req.GitProtocol {
		add("git protocol", req.GitProtocol, orNone(st.GitProtocol),
			"`gh auth login --with-token` SILENTLY flips the host protocol to https. "+
				"It did on 2026-07-27. An https remote plus a scope gap is a push "+
				"failure that surfaces late, at the moment work would otherwise land. "+
				"Note this is the HOST-level value, which is what gh auth status "+
				"reports; `gh config get git_protocol` returns the global default and "+
				"is the wrong source.")
	}

	return probs
}

// CheckSSHKey reports whether the key git actually pushes with is present and
// private.
//
// It is deliberately local-only: no network, no agent, no `ssh -T`. The
// preflight must run when the machine is offline or the forge is down, and a
// gate that cannot be skipped must not be able to go red for a reason that is
// not about this machine's credentials. The cost is declared rather than
// hidden: this proves the key EXISTS and is not world-readable, not that GitHub
// still accepts it. A live probe belongs elsewhere.
func CheckSSHKey(path string) []Problem {
	if path == "" {
		return nil
	}
	const why = "the token authenticates the API; this key authenticates every push " +
		"the fleet makes. Nothing checked it before stele#55, so half the credential " +
		"surface was unmonitored. SSH cannot replace the token -- GitHub's API does " +
		"not accept SSH authentication -- so both must hold, independently."

	info, err := os.Stat(path)
	if err != nil {
		return []Problem{{Check: "ssh key", Want: "a private key at " + path,
			Got: "missing (" + err.Error() + ")", Why: why}}
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return []Problem{{Check: "ssh key permissions", Want: "0600 at " + path,
			Got: fmt.Sprintf("%#o", mode),
			Why: why + " ssh refuses to use a key others can read."}}
	}
	return nil
}

// missingScopes returns the required scopes absent from have.
func missingScopes(have, want []string) []string {
	set := make(map[string]bool, len(have))
	for _, s := range have {
		set[strings.ToLower(strings.TrimSpace(s))] = true
	}
	var missing []string
	for _, w := range want {
		if !set[strings.ToLower(w)] {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	return missing
}

func scopesGot(scopes []string) string {
	if len(scopes) == 0 {
		return "none reported"
	}
	return strings.Join(scopes, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none reported"
	}
	return s
}
