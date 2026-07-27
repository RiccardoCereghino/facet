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

	// A nil status is not evidence of an absent credential -- it is evidence of
	// nothing at all, so it takes the cautious branch with the rest.
	if st != nil && st.State == StateAbsent {
		add("logged in", "a credential for github.com", "none",
			"this is the fault itself: on 2026-07-27 the fleet lost GitHub access "+
				"mid-teardown and found out by trying to use it. gh reports this "+
				"without needing a working token, so it can be checked first.")
		// Everything below reads fields that are meaningless when logged out.
		return probs
	}

	if st == nil || st.State != StateConfirmed {
		// STILL FATAL. If the forge is unreachable, spawn cannot clone or fetch
		// either, so refusing is right. What must not happen is refusing for the
		// WRONG STATED REASON -- a gate that fires on the right condition and
		// blames the wrong thing is a broken gate, not a strict one.
		add("credential confirmed", "gh can confirm the credential with github.com",
			"gh could NOT confirm it"+withReason(verifyFailure(st)),
			"DO NOT REGENERATE THE TOKEN ON THE STRENGTH OF THIS MESSAGE. gh "+
				"prints exactly this when the token is dead AND when it simply "+
				"cannot reach github.com, and it does not distinguish the two -- so "+
				"neither can this check. Your credential may be perfectly fine. "+
				"gh's own advice here ('To re-authenticate, run: gh auth login' / "+
				"'To forget about this account, run: gh auth logout') will DESTROY a "+
				"working credential if the real problem is the network, and this "+
				"fleet has already had a ~72-second no-credential window from "+
				"exactly that move (stele#55). Check connectivity FIRST -- `curl -sS "+
				"-o /dev/null -w '%{http_code}' https://api.github.com` -- and only "+
				"reissue if the network is fine.")
		// The X-shaped output carries no Token or scopes lines at all, so every
		// check below would report "none reported" and bury the one finding that
		// matters.
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

// sshKeyWhy is the reason attached to every SSH-key problem.
const sshKeyWhy = "the token authenticates the API; this key authenticates every push " +
	"the fleet makes. Nothing checked it before stele#55, so half the credential " +
	"surface was unmonitored. SSH cannot replace the token -- GitHub's API does " +
	"not accept SSH authentication -- so both must hold, independently."

// CheckSSHKey reports whether the key git actually pushes with is present and
// private. It returns the problems found and, as a second value, a non-empty
// string when a check could NOT be applied on this platform.
//
// The caller must surface that string. A preflight that prints a clean pass on
// a platform where half the check did not run is a lie, and a worse one than a
// red: the operator reads "green" and believes their key permissions were
// verified. The difference between a platform carve-out and a lie is whether
// the output says which one happened.
//
// # Two halves, with different portability
//
// The EXISTENCE half is platform-neutral and runs everywhere.
//
// The PERMISSION half is Unix-only, deliberately and explicitly. Go's
// os.FileMode does not represent NTFS ACLs, so on Windows the mode bits are
// synthesised and "is this key 0600 or stricter" cannot mean there what it
// means on Unix -- a mode test would pass or fail for reasons unrelated to who
// can actually read the file. Win32-OpenSSH does enforce an ACL rule of its
// own, so a real check IS expressible there; it needs DACL enumeration via
// golang.org/x/sys/windows, which is a new dependency and a body of code that
// cannot be exercised on the machine this fleet runs on. Rather than ship a
// mode test that is meaningless on Windows, or drop the check to get a green
// tick, the permission half declares itself not applicable and says why. See
// keyPermission, which has one implementation per platform.
//
// # Local-only, deliberately
//
// No network, no agent, no `ssh -T`. The preflight must run when the machine is
// offline or the forge is down, and a gate that cannot be skipped must not be
// able to go red for a reason that is not about this machine's credentials. The
// cost is declared rather than hidden: this proves the key EXISTS and, on Unix,
// that it is not group- or world-readable. It does not prove GitHub still
// accepts it. A live probe belongs elsewhere.
func CheckSSHKey(path string) (probs []Problem, notApplicable string) {
	if path == "" {
		return nil, ""
	}
	info, err := os.Stat(path)
	if err != nil {
		// Permissions are moot when there is no key: the missing key is the
		// finding, and reporting a platform caveat next to it would only
		// dilute it.
		return []Problem{{Check: "ssh key", Want: "a private key at " + path,
			Got: "missing (" + err.Error() + ")", Why: sshKeyWhy}}, ""
	}
	prob, why := keyPermission(path, info.Mode().Perm())
	if prob != nil {
		return []Problem{*prob}, ""
	}
	return nil, why
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

// verifyFailure is st's stated reason, tolerating a nil status.
func verifyFailure(st *AuthStatus) string {
	if st == nil {
		return "gh's status could not be read at all"
	}
	return st.VerifyFailure
}

// withReason appends gh's own words when it gave any, so the report quotes the
// tool rather than paraphrasing it.
func withReason(s string) string {
	if s == "" {
		return ""
	}
	return " (gh says: " + s + ")"
}

func orNone(s string) string {
	if s == "" {
		return "none reported"
	}
	return s
}
