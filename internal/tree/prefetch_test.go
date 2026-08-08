package tree

import (
	"fmt"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// batchFake is a Source that ALSO reads many issues at once, built from the
// same scripted data the per-node fake serves. Its page limits are settable so
// every short-read case can be forced on a tiny fixture.
type batchFake struct {
	*fakeSource
	childPage   int // 0 means unlimited
	labelPage   int
	blockerPage int
	blockers    map[string][]ghx.Blocker
	fail        bool

	batchCalls    int
	widest        int
	childrenCalls int
}

func newBatchFake(f *fakeSource) *batchFake {
	return &batchFake{fakeSource: f, blockers: map[string][]ghx.Blocker{}}
}

func (b *batchFake) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	b.childrenCalls++
	return b.fakeSource.IssueChildren(repo, number)
}

func (b *batchFake) IssueNodes(refs []ghx.IssueRef) ([]ghx.NodeFields, error) {
	if b.fail {
		return nil, fmt.Errorf("the batch read is unavailable")
	}
	b.batchCalls++
	if len(refs) > b.widest {
		b.widest = len(refs)
	}

	out := make([]ghx.NodeFields, 0, len(refs))
	for _, r := range refs {
		k := key(r.OwnerRepo(), r.Number)
		iss, ok := b.issues[k]
		if !ok {
			out = append(out, ghx.NodeFields{Ref: r, Unreadable: true})
			continue
		}
		f := ghx.NodeFields{
			Ref: r, Title: iss.Title, State: iss.State,
			LabelsComplete: true, BlockersComplete: true, ChildrenComplete: true,
		}
		f.Labels = iss.LabelNames()
		if b.labelPage > 0 && len(f.Labels) > b.labelPage {
			f.Labels, f.LabelsComplete = f.Labels[:b.labelPage], false
		}
		f.BlockedBy = b.blockers[k]
		if b.blockerPage > 0 && len(f.BlockedBy) > b.blockerPage {
			f.BlockedBy, f.BlockersComplete = f.BlockedBy[:b.blockerPage], false
		}
		f.Children = b.children[k]
		if b.childPage > 0 && len(f.Children) > b.childPage {
			f.Children, f.ChildrenComplete = f.Children[:b.childPage], false
		}
		out = append(out, f)
	}
	return out, nil
}

// render flattens a walked tree to everything a report can read off it, so two
// trees are compared as a whole rather than field by field -- a comparison
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
// exercises the short-read paths zero times while passing, which is exactly
// the shape of a healthy-looking count that proved nothing.
func wide() *fakeSource {
	return &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: c1-structure", "OPEN"),
			"acme/doctrine#283": issue("seat: c1-other", "OPEN"),
			"acme/doctrine#284": issue("seat: c1-third", "OPEN"),
			"acme/lab#75":       issue("the commands", "OPEN", "complexity/3"),
			"acme/lab#76":       issue("the other bundle", "OPEN", "complexity/2"),
			"acme/harness#121":  issue("the work", "OPEN", "complexity/1", "area/ci", "fleet"),
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
}

// !! THE DIFFERENTIAL. !! The batched path must produce the tree the per-node
// walk produces -- same nodes, same order, same levels, same error states. A
// faster walk that quietly reports a different tree is the failure this whole
// change exists to avoid, and no timing check can catch it.
func TestBatchedAndPerNodeWalksAgree(t *testing.T) {
	route := routeWithStructure()
	fixtures := map[string]func() *fakeSource{"narrow": wellFormed, "wide": wide}

	for _, tt := range []struct {
		name                             string
		childPage, labelPage, blockerPag int
	}{
		{name: "everything complete"},
		{name: "children truncate at one", childPage: 1},
		{name: "children truncate at two", childPage: 2},
		{name: "labels truncate", labelPage: 1},
		{name: "blockers truncate", blockerPag: 1},
		{name: "everything truncates", childPage: 1, labelPage: 1, blockerPag: 1},
	} {
		for fname, fixture := range fixtures {
			t.Run(fmt.Sprintf("%s/%s", fname, tt.name), func(t *testing.T) {
				slow, err := Walk(fixture(), ref("acme", "lab", 46), -1, route)
				if err != nil {
					t.Fatalf("per-node walk: %v", err)
				}
				b := newBatchFake(fixture())
				b.childPage, b.labelPage, b.blockerPage = tt.childPage, tt.labelPage, tt.blockerPag
				fast, err := Walk(b, ref("acme", "lab", 46), -1, route)
				if err != nil {
					t.Fatalf("batched walk: %v", err)
				}
				if got, want := render(fast, 0), render(slow, 0); got != want {
					t.Errorf("the batched walk reports a different tree.\n--- per-node ---\n%s\n--- batched ---\n%s", want, got)
				}
				if b.batchCalls == 0 {
					t.Error("the batched path was never used, so this proved nothing")
				}
			})
		}
	}
}

// A TRUNCATED LABEL LIST IS THE DANGEROUS ONE, and it is why the label page
// size is generous rather than cheap. A node's level is derived from its
// type/* label, so a label list cut short does not fail -- it assigns a
// DIFFERENT LEVEL, and the report looks perfectly well formed afterwards.
func TestATruncatedLabelListNeverReachesTheTree(t *testing.T) {
	route := routeWithStructure()
	slow, err := Walk(wide(), ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	b := newBatchFake(wide())
	b.labelPage = 1 // acme/harness#121 carries three labels
	fast, err := Walk(b, ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got, want := render(fast, 0), render(slow, 0); got != want {
		t.Errorf("a short label list reached the tree.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
	if b.childrenCalls == 0 {
		t.Error("nothing fell back to the per-node read, so the short list was simply used")
	}
}

// The point of the change: a LEVEL costs one request, not one per node.
func TestALevelCostsOneRequest(t *testing.T) {
	b := newBatchFake(wide())
	if _, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	// Four rungs deep, so four rounds read all nine nodes.
	if b.batchCalls > 4 {
		t.Errorf("%d batch requests for a 4-rung tree, want at most 4", b.batchCalls)
	}
	if b.widest < 3 {
		t.Errorf("the widest batch carried %d issue(s); a level of three siblings must ride together", b.widest)
	}
	if b.childrenCalls != 0 {
		t.Errorf("IssueChildren called %d time(s) despite complete batches, want 0", b.childrenCalls)
	}
}

// A source with no batching capability is not asked, and the walk is exactly
// what it always was. This keeps the per-node path a real fallback rather than
// dead code nobody would notice rotting.
func TestASourceWithoutTheCapabilityIsUntouched(t *testing.T) {
	src := wellFormed()
	if _, err := Walk(src, ref("acme", "lab", 46), -1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if src.viewIssueCalls != 1 {
		t.Errorf("ViewIssue called %d time(s), want exactly 1 -- the root only", src.viewIssueCalls)
	}
}

// A batch that FAILS must cost correctness nothing: everything falls through
// to the read that has always worked.
func TestAFailedBatchFallsBackToThePerNodeWalk(t *testing.T) {
	b := newBatchFake(wide())
	b.fail = true
	fast, err := Walk(b, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("a failed batch broke the walk: %v", err)
	}
	slow, err := Walk(wide(), ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got, want := render(fast, 0), render(slow, 0); got != want {
		t.Errorf("a failed batch changed the tree.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Blockers arrive with the tree, carrying each blocker's own state -- and a
// TRUNCATED list reports not-known rather than none, because answering from
// the edges that fitted on one page is how a node with more blockers than a
// page holds comes to read as ready.
func TestBlockersArriveWithTheTreeAndAShortListIsNotNone(t *testing.T) {
	full := newBatchFake(wide())
	full.blockers["acme/lab#46"] = []ghx.Blocker{
		{Ref: ref("acme", "harness", 9), State: "OPEN"},
		{Ref: ref("acme", "harness", 10), State: "CLOSED"},
	}
	root, err := Walk(full, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !root.BlockersKnown {
		t.Fatal("blockers were read with the tree and reported unknown")
	}
	if len(root.BlockedBy) != 2 || root.BlockedBy[0].State != "OPEN" {
		t.Fatalf("blockers = %+v, want both, carrying their own states", root.BlockedBy)
	}

	cut := newBatchFake(wide())
	cut.blockers["acme/lab#46"] = full.blockers["acme/lab#46"]
	cut.blockerPage = 1
	root2, err := Walk(cut, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if root2.BlockersKnown {
		t.Error("a truncated blocker list was reported as known -- a later open blocker would read as ready")
	}

	none := newBatchFake(wide())
	root3, err := Walk(none, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(root3.BlockedBy) != 0 {
		t.Fatalf("blockers appeared from nowhere: %+v", root3.BlockedBy)
	}
}
