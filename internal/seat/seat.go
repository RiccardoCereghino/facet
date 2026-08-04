// Package seat writes the two files that say who a workspace belongs to and
// what it is allowed to work on: .seat, one line naming the seat, and .scope,
// one issue reference per line.
//
// The load-bearing decision is who writes them. Both are written by whoever
// *creates* the workspace and never by the agent that then works in it:
// attribution has to be derived and checkable rather than asserted by the thing
// being attributed. The mechanism this replaces was an environment variable,
// which every session could set to any value and thereby sign as anyone.
//
// Be clear about the ceiling. Files on disk defeat accidents -- a stale export,
// a typo, a command pasted from another workspace -- and they do not defeat
// deliberate tampering, because the agent runs as the same user and can rewrite
// them or work from somewhere else entirely. That is the honest limit of any
// scheme where the writer and the subject share a uid. The next step up is a
// per-seat credential, which composes with this rather than replacing it: the
// file becomes the claim and the credential becomes the proof.
//
// The on-disk format is fixed by files that already exist and by a separate
// reader being written against them: .seat is the name plus a newline, .scope is
// one owner/repo#n per line, each newline-terminated. Do not change it here.
package seat

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// NameFile, ScopeFile and SeatIssueFile are the file names, relative to a
// workspace root.
//
// SeatIssueFile names the issue that describes the SEAT rather than the work:
// its bundle and order, its per-issue tiers, the orchestration notes, and the
// channel it escalates on. It is the third member of the family and it is
// deliberately the same mechanism as the other two — one line, written by
// whoever creates the workspace, never by the agent that then works in it.
//
// facet writes the pointer and nothing else. It does not create the issue, does
// not read it, and does not know what is in it: what a seat issue CONTAINS is
// gad's and stele's.
const (
	NameFile      = ".seat"
	ScopeFile     = ".scope"
	SeatIssueFile = ".seat-issue"
)

// ValidateName rejects a seat name that cannot be used as written.
//
// The '.' rule is the one with a story: a multiplexer target address uses ':'
// for a window and '.' for a pane, so a seat called "w-example-m7.1" addresses
// pane 1 of "w-example-m7" and every command aimed at it lands somewhere else,
// or nowhere. It has been worked around by hand at least once by renaming the
// seat after the fact.
//
// The rest follow from a name being passed to other tools and printed in
// records: a path separator would let a name reach outside anything it is
// composed into, and whitespace or control characters make a name that looks
// identical to a different one.
func ValidateName(name string) error {
	fix := "fix: pass --seat with a name of letters, digits and dashes, e.g. --seat w-example-12"
	if name == "" {
		return fmt.Errorf("a seat name is required and must not be empty\n%s", fix)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("seat name %q contains '.': a multiplexer reads '.' as the pane separator, "+
			"so %q addresses a pane rather than a session\n"+
			"fix: spell it without the dot, e.g. --seat %s",
			name, name, strings.ReplaceAll(name, ".", ""))
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("seat name %q contains a path separator\n%s", name, fix)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return fmt.Errorf("seat name %q contains whitespace or a non-printing character, "+
				"which makes two different names look alike\n%s", name, fix)
		}
	}
	return nil
}

// Ref is one issue a workspace legitimately covers, in the owner/repo#n form the
// files on disk already use -- or, when Landing is true, a repo whose PULL
// REQUESTS the workspace's work lands in without claiming any issue there.
//
// Landing exists for facet#97: a seat's issues are sometimes filed in one repo
// while its work (and so its PRs) land in another, and there was no honest way
// to write that -- naming some unrelated issue in the landing repo admitted
// every PR there while asserting the seat covers work it does not. A landing
// entry says the true thing directly, and Number is meaningless when it is set
// (there is no issue to number).
type Ref struct {
	Repo    string // owner/name
	Number  int
	Landing bool
}

// String renders the canonical form: owner/repo#n for an issue, or
// landing:owner/repo for a landing-only entry. Both round-trip through
// ParseRef.
func (r Ref) String() string {
	if r.Landing {
		return "landing:" + r.Repo
	}
	return fmt.Sprintf("%s#%d", r.Repo, r.Number)
}

// refFix is the fix line every ParseRef refusal ends with.
const refFix = "fix: write it as owner/repo#123, or landing:owner/repo for a repo with no issue of its own"

// ParseRef reads one scope entry. It is strict about the form because the file
// is a wire contract with a separate reader: an entry that is nearly right is
// worse than one that is refused, since the reader will simply not match it and
// the workspace will look out of scope for work it was created to do.
func ParseRef(s string) (Ref, error) {
	t := strings.TrimSpace(s)

	if repo, ok := strings.CutPrefix(t, "landing:"); ok {
		return parseLandingRef(s, repo)
	}

	repo, num, ok := strings.Cut(t, "#")
	if !ok {
		return Ref{}, fmt.Errorf("scope entry %q has no '#'\n%s", s, refFix)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return Ref{}, fmt.Errorf("scope entry %q does not name owner/repo before the '#'\n%s", s, refFix)
	}
	if strings.ContainsAny(repo, " \t") {
		return Ref{}, fmt.Errorf("scope entry %q has whitespace inside owner/repo\n%s", s, refFix)
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return Ref{}, fmt.Errorf("scope entry %q: %q is not an issue number\n%s", s, num, refFix)
	}
	if n < 1 {
		return Ref{}, fmt.Errorf("scope entry %q: issue numbers start at 1\n%s", s, refFix)
	}
	return Ref{Repo: repo, Number: n}, nil
}

// parseLandingRef reads the owner/repo half of a landing:owner/repo entry.
// original is the whole entry, as given, for the error messages -- repo is
// already the part after the prefix.
func parseLandingRef(original, repo string) (Ref, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return Ref{}, fmt.Errorf("scope entry %q does not name owner/repo after 'landing:'\n%s", original, refFix)
	}
	if strings.ContainsAny(repo, " \t") {
		return Ref{}, fmt.Errorf("scope entry %q has whitespace inside owner/repo\n%s", original, refFix)
	}
	return Ref{Repo: repo, Landing: true}, nil
}

// ParseRefs reads several entries, reporting the first that is wrong. Duplicates
// are dropped, keeping the first occurrence: the order is meaningful (the issue
// a workspace was created for comes first) and repeating an entry is a slip
// rather than a request for two lines.
func ParseRefs(in []string) ([]Ref, error) {
	var out []Ref
	for _, s := range in {
		r, err := ParseRef(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return Dedupe(out), nil
}

// Dedupe keeps the first occurrence of each reference. Order is meaningful --
// the issue a workspace was created for leads -- so this cannot sort.
func Dedupe(in []Ref) []Ref {
	var out []Ref
	seen := map[string]bool{}
	for _, r := range in {
		if seen[r.String()] {
			continue
		}
		seen[r.String()] = true
		out = append(out, r)
	}
	return out
}

// Join renders references for a person to read on one line.
func Join(refs []Ref) string {
	s := make([]string, len(refs))
	for i, r := range refs {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

// Write records the seat's name, and the issues it covers if there are any.
//
// An empty scope writes no file at all rather than an empty one. Absent means
// "nothing to check here", which is a state a real workspace is in -- the one an
// operator drives from covers no single issue -- and it must keep working
// without needing an exemption. An empty file would be a hole with a name on it.
func Write(workspaceDir, name string, scope []Ref) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := writeAndVerify(filepath.Join(workspaceDir, NameFile), []byte(name+"\n")); err != nil {
		return err
	}
	if len(scope) == 0 {
		return nil
	}
	return writeAndVerify(filepath.Join(workspaceDir, ScopeFile), marshalScope(scope))
}

// marshalScope renders the file: one reference per line, every line terminated.
// A terminated last line is what is on disk and what a line-at-a-time reader
// expects; it also makes appending a matter of writing more bytes.
func marshalScope(scope []Ref) []byte {
	var b bytes.Buffer
	for _, r := range scope {
		fmt.Fprintf(&b, "%s\n", r)
	}
	return b.Bytes()
}

// writeAndVerify writes the file and then reads it back, failing when the bytes
// on disk are not the bytes that were asked for.
//
// This is not defensive padding. A write that reports success and does not land
// is the failure this codebase has hit most often, in enough different tools
// that treating a nil error as evidence is no longer reasonable: a full disk, a
// path that resolved somewhere unexpected, another process writing the same
// file. The check costs one read of a file measured in bytes.
func writeAndVerify(path string, want []byte) error {
	if err := os.WriteFile(path, want, 0o666); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return verify(path, want)
}

// verify is the half of writeAndVerify that can be shown failing without having
// to arrange a filesystem that lies, which is why it is its own function.
func verify(path string, want []byte) error {
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read back %s: %w", path, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s read back as %q after writing %q; the write reported success and did not land\n"+
			"fix: check the filesystem this workspace is on, then create it again", path, got, want)
	}
	return nil
}

// ReadName returns the seat's name, and "" when the workspace has none.
func ReadName(workspaceDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(workspaceDir, NameFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadScope returns the issues a workspace covers. No file means no scope
// recorded, which is not an error -- see Write.
func ReadScope(workspaceDir string) ([]Ref, error) {
	b, err := os.ReadFile(filepath.Join(workspaceDir, ScopeFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Ref
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r, err := ParseRef(line)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", filepath.Join(workspaceDir, ScopeFile), i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// WriteSeatIssue records the issue that describes this seat.
//
// Separate from Write rather than a fourth parameter to it: a workspace can be
// created without a seat issue — every one before this existed was, and an
// operator's own workspace has none — and threading an optional value through
// the function that writes the two mandatory ones would make the common case
// carry the rare one.
//
// Read back like everything else here, for the reason writeAndVerify exists: a
// write that reports success and does not land is the failure this codebase has
// hit most often.
func WriteSeatIssue(workspaceDir string, ref Ref) error {
	return writeAndVerify(filepath.Join(workspaceDir, SeatIssueFile), []byte(ref.String()+"\n"))
}

// ReadSeatIssue returns the issue that describes this seat, and whether one is
// recorded at all.
//
// **Missing and empty are different, and that is the whole point.** No file
// means no seat issue recorded, which is not an error: it is the state of every
// workspace created before this file existed, and of any workspace an operator
// drives directly. A file that is *present but names nothing* is an error,
// because "this seat has no record" and "the spawner meant to write one and did
// not" are both defensible readings of it — the same argument parseScope already
// makes for an empty .scope, and the same reason it is refused rather than
// guessed at.
//
// A note may follow the ref on later lines, as .seat allows beneath the name.
func ReadSeatIssue(workspaceDir string) (Ref, bool, error) {
	path := filepath.Join(workspaceDir, SeatIssueFile)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Ref{}, false, nil
	}
	if err != nil {
		return Ref{}, false, err
	}

	for _, line := range strings.Split(string(b), "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		r, err := ParseRef(text)
		if err != nil {
			return Ref{}, false, fmt.Errorf("%s: %w", path, err)
		}
		return r, true, nil
	}

	return Ref{}, false, fmt.Errorf("%s is present but names no issue\n"+
		"fix: write one issue as owner/repo#123, or delete the file if this workspace has no seat issue — "+
		"an empty file is ambiguous between 'no seat issue' and 'the spawner meant to write one and did not', "+
		"which is why it is refused rather than guessed at", path)
}

// AppendScope adds issues to a workspace's scope, and reports which were new. It
// exists because a seat is sometimes handed a second issue after its workspace
// was created, and the alternative to recording that is the seat asserting it.
//
// Adding a reference already present is a no-op rather than a second line, so
// running it twice is safe.
func AppendScope(workspaceDir string, add []Ref) ([]Ref, error) {
	have, err := ReadScope(workspaceDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range have {
		seen[r.String()] = true
	}
	var added []Ref
	for _, r := range add {
		if seen[r.String()] {
			continue
		}
		seen[r.String()] = true
		have = append(have, r)
		added = append(added, r)
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := writeAndVerify(filepath.Join(workspaceDir, ScopeFile), marshalScope(have)); err != nil {
		return nil, err
	}
	return added, nil
}

// writeScope replaces .scope's contents wholesale, deleting the file rather
// than writing an empty one -- the same rule Write follows, for the same
// reason: absent means "nothing to check here", a real state a workspace can
// be in, and an empty file would be a hole with a name on it that ReadScope
// cannot tell apart from "the writer meant to record something and did not".
func writeScope(workspaceDir string, scope []Ref) error {
	path := filepath.Join(workspaceDir, ScopeFile)
	if len(scope) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	return writeAndVerify(path, marshalScope(scope))
}

// RemoveScope drops entries from a workspace's scope and reports what is left.
// Removing an entry not present is a no-op for that entry, matching AppendScope's
// idempotence in the other direction -- running it twice, or against a scope
// that never had the entry, is safe.
//
// This is the verb `add` had no opposite for (facet#112): a boundary that can
// only widen is a ratchet, and correcting a wrong entry required editing
// .scope by hand -- exactly what these tools exist to make unnecessary.
func RemoveScope(workspaceDir string, remove []Ref) (removed, remaining []Ref, err error) {
	have, err := ReadScope(workspaceDir)
	if err != nil {
		return nil, nil, err
	}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[r.String()] = true
	}
	remaining = have[:0:0] // never alias `have`'s backing array with the shrunk result
	for _, r := range have {
		if drop[r.String()] {
			removed = append(removed, r)
			continue
		}
		remaining = append(remaining, r)
	}
	if len(removed) == 0 {
		return nil, have, nil
	}
	if err := writeScope(workspaceDir, remaining); err != nil {
		return nil, nil, err
	}
	return removed, remaining, nil
}

// SetScope replaces a workspace's scope wholesale and reports what it
// replaced, the way tree wire reports the parent an edge moved a child away
// from -- so the previous state is not simply gone the moment the new one is
// written.
//
// refs is deduplicated the same way AppendScope's input is; SetScope always
// writes (even to the same value) so it is not idempotent in the sense
// AppendScope is -- it is an assignment, not a merge, and the previous value
// is returned specifically so a caller can tell whether anything changed.
func SetScope(workspaceDir string, refs []Ref) (previous []Ref, err error) {
	previous, err = ReadScope(workspaceDir)
	if err != nil {
		return nil, err
	}
	if err := writeScope(workspaceDir, Dedupe(refs)); err != nil {
		return nil, err
	}
	return previous, nil
}
