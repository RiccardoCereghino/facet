package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// fakeGH is a scripted ghx.Client. It records every IssueID/AddBlockedBy call
// so tests can assert exactly which edges runFile attempted, without ever
// shelling out to gh.
type fakeGH struct {
	createURL string

	issueIDs  map[string]int64 // "owner/repo#n" -> id
	issueErrs map[string]error // "owner/repo#n" -> error from IssueID

	addBlockedByCalls []string // "repo#number<-id" per call
	addBlockedByErr   error

	// auth is what Auth returns. Nil means "a sound credential": tests that are
	// not about the preflight should not have to script one, and the zero value
	// of a scripted status is logged out, which would fail every one of them.
	auth *ghx.AuthStatus

	// The issue graph. Keys are "owner/repo#n" throughout.
	parents    map[string]ghx.IssueRef   // a miss means "no parent", not an error
	parentErrs map[string]error          // "could not tell", which is a third state
	children   map[string][]ghx.IssueRef // eventually consistent in reality
	childErrs  map[string]error

	addSubIssueCalls []string // "repo#number<-childID" per call
	addSubIssueErr   error

	removeSubIssueCalls []string // "repo#number<-childID" per call
	removeSubIssueErr   error

	// The dependency graph, which is not the issue graph.
	blockedBy map[string][]ghx.IssueRef
	blocking  map[string][]ghx.IssueRef

	comments       map[string][]ghx.Comment
	postedComments []string // "repo#number: body" per call
	editedComments []string // "commentID: body" per call

	statuses    map[string]string // "owner/repo#n" -> board status
	statusesErr error
}

// Auth returns the scripted status, defaulting to a credential that satisfies
// ghx.DefaultRequirements so unrelated tests are unaffected by the preflight.
func (f *fakeGH) Auth() (*ghx.AuthStatus, error) {
	if f.auth != nil {
		return f.auth, nil
	}
	return &ghx.AuthStatus{
		Host: "github.com", State: ghx.StateConfirmed,
		Account: "RiccardoCereghino", Active: true,
		TokenType: "ghp_", Scopes: []string{"read:org", "repo", "workflow"},
		GitProtocol: "ssh", ConfigSource: "/dev/null",
	}, nil
}

func (f *fakeGH) key(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

func (f *fakeGH) ViewIssue(_ string, _ int) (*ghx.Issue, error) { return nil, nil }
func (f *fakeGH) DevelopBranch(_ string, _ int, _, _ string) (string, error) {
	return "", nil
}
func (f *fakeGH) BranchesFor(_ string, _ int) ([]string, error) { return nil, nil }
func (f *fakeGH) ViewPR(_, _ string) (*ghx.PR, error)           { return nil, nil }
func (f *fakeGH) MergedPRForSHA(_, _ string) (*ghx.PRForCommit, error) {
	return nil, nil
}
func (f *fakeGH) SetIssueStatus(_ ghx.ProjectTarget, _ string) error { return nil }
func (f *fakeGH) SetIssueBody(_ string, _ int, _ string) error       { return nil }
func (f *fakeGH) SearchIssues(_, _ string) ([]ghx.Issue, error)      { return nil, nil }
func (f *fakeGH) CreateIssue(_, _, _ string, _ []string) (string, error) {
	return f.createURL, nil
}

// IssueID errors on a map miss, matching the real CLI's behaviour on a
// nonexistent or inaccessible issue (a 404 via `gh api`) -- a test relying on
// "an unscripted issue is skipped" must see the same failure mode the real
// client produces, not a fake id of 0 that happens to also look like an error
// path today but wouldn't catch a caller that started treating 0 as valid.
func (f *fakeGH) IssueID(repo string, number int) (int64, error) {
	k := f.key(repo, number)
	if err, ok := f.issueErrs[k]; ok {
		return 0, err
	}
	id, ok := f.issueIDs[k]
	if !ok {
		return 0, fmt.Errorf("fakeGH.IssueID: no issue id scripted for %s", k)
	}
	return id, nil
}

func (f *fakeGH) AddBlockedBy(repo string, number int, blockingID int64) error {
	f.addBlockedByCalls = append(f.addBlockedByCalls, f.key(repo, number)+"<-"+strconv.FormatInt(blockingID, 10))
	return f.addBlockedByErr
}

// --- the issue graph -------------------------------------------------------
//
// Scripted separately from the dependency edges above because they are
// different graphs: parents say what a thing is part of, blockers say what must
// land first. A fake that conflated them would let a test pass that only works
// because the two happen to agree.

// IssueParent returns the scripted parent. A miss is "asked, and there is
// none" -- NOT an error -- because that is the honest majority case and the
// one the no-parent-is-valid rule turns on. An unreadable parent is scripted
// explicitly via parentErrs.
func (f *fakeGH) IssueParent(repo string, number int) (ghx.IssueRef, bool, error) {
	k := f.key(repo, number)
	if err, ok := f.parentErrs[k]; ok {
		return ghx.IssueRef{}, false, err
	}
	p, ok := f.parents[k]
	return p, ok, nil
}

func (f *fakeGH) IssueChildren(repo string, number int) ([]ghx.IssueRef, error) {
	k := f.key(repo, number)
	if err, ok := f.childErrs[k]; ok {
		return nil, err
	}
	return f.children[k], nil
}

func (f *fakeGH) AddSubIssue(repo string, number int, childID int64) error {
	f.addSubIssueCalls = append(f.addSubIssueCalls,
		f.key(repo, number)+"<-"+strconv.FormatInt(childID, 10))
	return f.addSubIssueErr
}

func (f *fakeGH) RemoveSubIssue(repo string, number int, childID int64) error {
	f.removeSubIssueCalls = append(f.removeSubIssueCalls,
		f.key(repo, number)+"<-"+strconv.FormatInt(childID, 10))
	return f.removeSubIssueErr
}

func (f *fakeGH) BlockedBy(repo string, number int) ([]ghx.IssueRef, error) {
	return f.blockedBy[f.key(repo, number)], nil
}

func (f *fakeGH) Blocking(repo string, number int) ([]ghx.IssueRef, error) {
	return f.blocking[f.key(repo, number)], nil
}

func (f *fakeGH) IssueComments(repo string, number int) ([]ghx.Comment, error) {
	return f.comments[f.key(repo, number)], nil
}

func (f *fakeGH) PostComment(repo string, number int, body string) (string, error) {
	f.postedComments = append(f.postedComments, f.key(repo, number)+": "+body)
	return "https://github.com/" + f.key(repo, number) + "-comment", nil
}

func (f *fakeGH) EditComment(_ string, commentID int64, body string) (string, error) {
	f.editedComments = append(f.editedComments,
		strconv.FormatInt(commentID, 10)+": "+body)
	return "https://github.com/edited-comment", nil
}

func (f *fakeGH) ProjectStatuses(_ string, _ int, _ string) (map[string]string, error) {
	return f.statuses, f.statusesErr
}

var _ ghx.Client = (*fakeGH)(nil)

func withTempRouting(t *testing.T, repo string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	contents := `{
		"version": 1,
		"repos": {"home": {"dir": "home", "url": "https://example.invalid/home.git"}},
		"ownerRepoToKey": {"` + repo + `": "home"},
		"aliases": {},
		"areaMap": {},
		"knowledgeByArea": {},
		"pathHints": {}
	}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := roots
	roots = config.Roots{Routing: path}
	t.Cleanup(func() { roots = prev })
}

// runFile creates blocked-by edges for a bare `#n`, and for a same-owner
// owner/repo#n, but skips a cross-owner ref -- and none of that stops the
// issue from being filed.
func TestRunFile_BlockedByEdges_Mixed(t *testing.T) {
	withTempRouting(t, "acme/gateway")
	fake := &fakeGH{
		createURL: "https://github.com/acme/gateway/issues/42",
		issueIDs: map[string]int64{
			"acme/gateway#5":     1005,
			"acme/infra-core#41": 1041,
			// Scripted deliberately, so the cross-owner guard's coverage does
			// not depend on IssueID failing on a miss: if the guard were
			// removed, this id would produce a real (and wrong) `<-1009`
			// call the assertion below would catch.
			"other/thing#9": 1009,
		},
	}
	prevGH := gh
	gh = fake
	t.Cleanup(func() { gh = prevGH })

	body := "### Blocked by / waiting on\n\n" +
		"#5, acme/infra-core#41, other/thing#9, account creation (operator)\n"

	err := runFile(fileOpts{
		Repo:  "acme/gateway",
		Title: "gateway: fix the thing",
		Body:  body,
	})
	if err != nil {
		t.Fatalf("runFile: %v", err)
	}

	want := []string{"acme/gateway#42<-1005", "acme/gateway#42<-1041"}
	if !equalSlices(fake.addBlockedByCalls, want) {
		t.Errorf("AddBlockedBy calls = %v, want %v", fake.addBlockedByCalls, want)
	}
}

// An unresolvable ref (IssueID errors) is reported and skipped; filing still
// succeeds and the other refs in the same section still get their edges.
func TestRunFile_BlockedByEdges_UnresolvableRefSkipped(t *testing.T) {
	withTempRouting(t, "acme/gateway")
	fake := &fakeGH{
		createURL: "https://github.com/acme/gateway/issues/7",
		issueIDs: map[string]int64{
			"acme/gateway#3": 1003,
		},
		issueErrs: map[string]error{
			"acme/gateway#404": errIssueNotFound,
		},
	}
	prevGH := gh
	gh = fake
	t.Cleanup(func() { gh = prevGH })

	body := "### Blocked by / waiting on\n\n#404, #3\n"

	if err := runFile(fileOpts{
		Repo:  "acme/gateway",
		Title: "gateway: fix the thing",
		Body:  body,
	}); err != nil {
		t.Fatalf("runFile: %v", err)
	}

	want := []string{"acme/gateway#7<-1003"}
	if !equalSlices(fake.addBlockedByCalls, want) {
		t.Errorf("AddBlockedBy calls = %v, want %v", fake.addBlockedByCalls, want)
	}
}

// Two spellings of the same issue -- a bare `#n` and its `owner/repo#n`
// equivalent in the filing repo -- resolve to one identity and must produce
// exactly one edge, not two.
func TestRunFile_BlockedByEdges_DedupesResolvedIdentity(t *testing.T) {
	withTempRouting(t, "acme/gateway")
	fake := &fakeGH{
		createURL: "https://github.com/acme/gateway/issues/10",
		issueIDs: map[string]int64{
			"acme/gateway#5": 1005,
		},
	}
	prevGH := gh
	gh = fake
	t.Cleanup(func() { gh = prevGH })

	body := "### Blocked by / waiting on\n\n#5, acme/gateway#5\n"

	if err := runFile(fileOpts{
		Repo:  "acme/gateway",
		Title: "gateway: fix the thing",
		Body:  body,
	}); err != nil {
		t.Fatalf("runFile: %v", err)
	}

	want := []string{"acme/gateway#10<-1005"}
	if !equalSlices(fake.addBlockedByCalls, want) {
		t.Errorf("AddBlockedBy calls = %v, want %v", fake.addBlockedByCalls, want)
	}
}

var errIssueNotFound = &fileTestError{"issue not found"}

type fileTestError struct{ msg string }

func (e *fileTestError) Error() string { return e.msg }

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
