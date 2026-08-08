package tree

import (
	"fmt"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// batchedSource is a Source that ALSO answers whole subtrees, built from the
// same scripted data the per-node fake serves. Its page size and reach are
// deliberately small so both incomplete cases happen on a tiny fixture:
// connections truncate, and nodes sit below what one query reaches.
type batchedSource struct {
	*fakeSource
	page  int
	reach int

	subtreeCalls  int
	childrenCalls int
}

func (b *batchedSource) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	b.childrenCalls++
	return b.fakeSource.IssueChildren(repo, number)
}

func (b *batchedSource) IssueSubtree(repo string, number, depth int) (*ghx.SubTree, error) {
	b.subtreeCalls++
	if depth > b.reach {
		depth = b.reach
	}
	owner, name, _ := strings.Cut(repo, "/")
	ref := ghx.IssueRef{Owner: owner, Repo: name, Number: number}
	st := b.build(ref, depth)
	if st == nil {
		return nil, fmt.Errorf("%s#%d: no such issue", repo, number)
	}
	return st, nil
}

func (b *batchedSource) build(ref ghx.IssueRef, depth int) *ghx.SubTree {
	k := key(ref.OwnerRepo(), ref.Number)
	iss, ok := b.issues[k]
	if !ok {
		return nil
	}
	st := &ghx.SubTree{
		Ref: ref, Title: iss.Title, State: iss.State, Labels: iss.LabelNames(),
	}
	kids := b.children[k]
	if depth <= 1 {
		// The query ran out of rungs here: the connection is absent entirely,
		// and only the summary says whether anything is below.
		st.MoreChildren = len(kids) > 0
		return st
	}
	st.ConnectionSeen = true
	for i, c := range kids {
		if i >= b.page {
			st.MoreChildren = true
			break
		}
		if child := b.build(c, depth-1); child != nil {
			st.Children = append(st.Children, *child)
		} else {
			// Unreadable, carried the way the real query carries it: no state.
			st.Children = append(st.Children, ghx.SubTree{Ref: c})
		}
	}
	return st
}

// render flattens a walked tree to everything a report can read off it, so two
// trees can be compared as a whole rather than field by field -- a comparison
// that checks three fields passes while a fourth silently diverges.
func render(n *Node, depth int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s%s title=%q state=%q labels=%v depth=%d level=%d assigned=%v known=%v parentLevel=%d hasParent=%v err=%v levelErr=%v\n",
		strings.Repeat("  ", depth), n.Ref, n.Title, n.State, n.Labels, n.Depth,
		n.Level, n.Assigned, n.LevelKnown, n.ParentLevel, n.HasParent, n.Err, n.LevelErr)
	for _, c := range n.Children {
		sb.WriteString(render(c, depth+1))
	}
	return sb.String()
}

// wide is a tree BROAD as well as deep. wellFormed has at most one child per
// node, so no page size can ever truncate it -- a differential run against it
// exercises the truncation path zero times while passing, which is the shape
// of a healthy-looking count that proved nothing.
func wide() *fakeSource {
	f := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: c1-structure", "OPEN"),
			"acme/doctrine#283": issue("seat: c1-other", "OPEN"),
			"acme/doctrine#284": issue("seat: c1-third", "OPEN"),
			"acme/lab#75":       issue("the commands", "OPEN", "complexity/3"),
			"acme/lab#76":       issue("the other bundle", "OPEN", "complexity/2"),
			"acme/harness#121":  issue("the work", "OPEN", "complexity/1"),
			"acme/harness#122":  issue("more work", "OPEN", "complexity/1"),
			"acme/harness#123":  issue("yet more work", "OPEN", "complexity/1"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {
				ref("acme", "doctrine", 282),
				ref("acme", "doctrine", 283),
				ref("acme", "doctrine", 284),
			},
			"acme/doctrine#282": {ref("acme", "lab", 75), ref("acme", "lab", 76)},
			"acme/lab#75": {
				ref("acme", "harness", 121),
				ref("acme", "harness", 122),
				ref("acme", "harness", 123),
			},
		},
	}
	return f
}

// !! THE DIFFERENTIAL. !! The batched path must produce the tree the per-node
// walk produces -- same nodes, same order, same levels, same error states. A
// faster walk that quietly reports a different tree is the failure this whole
// change has to avoid, and it is not one a timing check can catch.
func TestBatchedAndPerNodeWalksAgree(t *testing.T) {
	route := routeWithStructure()

	fixtures := map[string]func() *fakeSource{"narrow": wellFormed, "wide": wide}
	for _, tt := range []struct{ page, reach int }{
		{page: 100, reach: 100}, // everything in one answer
		{page: 2, reach: 100},   // connections truncate
		{page: 1, reach: 100},   // every connection truncates
		{page: 100, reach: 2},   // nothing nests far enough
		{page: 1, reach: 1},     // both at once, the worst case
	} {
		for fname, fixture := range fixtures {
			t.Run(fmt.Sprintf("%s/page=%d/reach=%d", fname, tt.page, tt.reach), func(t *testing.T) {
				slow, err := Walk(fixture(), ref("acme", "lab", 46), -1, route)
				if err != nil {
					t.Fatalf("per-node walk: %v", err)
				}
				b := &batchedSource{fakeSource: fixture(), page: tt.page, reach: tt.reach}
				fast, err := Walk(b, ref("acme", "lab", 46), -1, route)
				if err != nil {
					t.Fatalf("batched walk: %v", err)
				}
				if got, want := render(fast, 0), render(slow, 0); got != want {
					t.Errorf("the batched walk reports a different tree.\n--- per-node ---\n%s\n--- batched ---\n%s", want, got)
				}
				if b.subtreeCalls == 0 {
					t.Error("the batched path was never used, so this proved nothing")
				}
			})
		}
	}
}

// The point of the whole change: when one answer covers the tree, the walk
// stops asking per node.
func TestOneAnswerCostsOneRequest(t *testing.T) {
	b := &batchedSource{fakeSource: wellFormed(), page: 100, reach: 100}
	if _, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if b.subtreeCalls != 1 {
		t.Errorf("IssueSubtree called %d time(s), want exactly 1", b.subtreeCalls)
	}
	if b.childrenCalls != 0 {
		t.Errorf("IssueChildren called %d time(s) despite a complete batch, want 0", b.childrenCalls)
	}
}

// A truncated connection is finished by PAGING that node, not by asking for
// its subtree again -- which would return the same first page forever and
// leave the rest of the tree unread while looking fast.
func TestATruncatedConnectionIsPagedAndNothingIsLost(t *testing.T) {
	b := &batchedSource{fakeSource: wide(), page: 1, reach: 100}
	fast, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	slow, err := Walk(wide(), ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got, want := len(fast.Descendants()), len(slow.Descendants()); got != want {
		t.Errorf("the batched walk found %d descendants, the per-node walk %d -- truncation lost nodes", got, want)
	}
	if b.childrenCalls == 0 {
		t.Error("a truncated connection was never paged, so nothing completed it")
	}
}

// A source with no batching capability is not asked, and the walk is exactly
// what it always was. This is what keeps the per-node path a real fallback
// rather than dead code nobody would notice rotting.
func TestASourceWithoutTheCapabilityIsUntouched(t *testing.T) {
	src := wellFormed()
	if _, err := Walk(src, ref("acme", "lab", 46), -1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if src.viewIssueCalls != 1 {
		t.Errorf("ViewIssue called %d time(s), want exactly 1 -- the root only", src.viewIssueCalls)
	}
}

// A batch that FAILS must cost correctness nothing: every node it did not
// cover falls through to the read that has always worked.
func TestAFailedBatchFallsBackToThePerNodeWalk(t *testing.T) {
	b := &failingBatch{fakeSource: wellFormed()}
	fast, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("a failed batch broke the walk: %v", err)
	}
	slow, err := Walk(wellFormed(), ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got, want := render(fast, 0), render(slow, 0); got != want {
		t.Errorf("a failed batch changed the tree.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

type failingBatch struct{ *fakeSource }

func (f *failingBatch) IssueSubtree(string, int, int) (*ghx.SubTree, error) {
	return nil, fmt.Errorf("the bulk read is unavailable")
}

// Blockers arrive with the tree, carrying each blocker's own state -- and a
// node the batch did not cover reports NOT KNOWN rather than none, because
// only one of those two means ready.
func TestBlockersArriveWithTheTreeAndAbsenceIsNotNone(t *testing.T) {
	b := &blockerBatch{fakeSource: wellFormed()}
	root, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !root.BlockersKnown {
		t.Fatal("the root's blockers were read with the tree and reported unknown")
	}
	if len(root.BlockedBy) != 1 || root.BlockedBy[0].State != "OPEN" {
		t.Fatalf("root blockers = %+v, want one carrying its own state", root.BlockedBy)
	}

	plain := &batchedSource{fakeSource: wellFormed(), page: 100, reach: 100}
	root2, err := Walk(plain, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(root2.BlockedBy) != 0 {
		t.Fatalf("blockers appeared from nowhere: %+v", root2.BlockedBy)
	}
}

type blockerBatch struct{ *fakeSource }

func (b *blockerBatch) IssueSubtree(repo string, number, _ int) (*ghx.SubTree, error) {
	owner, name, _ := strings.Cut(repo, "/")
	r := ghx.IssueRef{Owner: owner, Repo: name, Number: number}
	iss := b.issues[key(repo, number)]
	if iss == nil {
		return nil, fmt.Errorf("%s#%d: no such issue", repo, number)
	}
	st := &ghx.SubTree{
		Ref: r, Title: iss.Title, State: iss.State, Labels: iss.LabelNames(),
		ConnectionSeen: true,
		BlockedBy: []ghx.Blocker{
			{Ref: ghx.IssueRef{Owner: "acme", Repo: "harness", Number: 9}, State: "OPEN"},
		},
	}
	// Children left unlisted and unmarked: a leaf as far as this batch says.
	return st, nil
}
