package tree

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// countingSource records how a walk reads, and how widely, so a test can
// assert the reads of one level actually ran together rather than in turn.
type countingSource struct {
	*fakeSource

	mu       sync.Mutex
	calls    int
	inFlight int
	widest   int
	// linger holds each read open long enough that siblings starting together
	// overlap observably. A gate cannot be used: it would block the ROOT's
	// read, and the level with siblings on it is never reached.
	linger time.Duration
}

func (c *countingSource) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	c.mu.Lock()
	c.calls++
	c.inFlight++
	if c.inFlight > c.widest {
		c.widest = c.inFlight
	}
	c.mu.Unlock()

	if c.linger > 0 {
		time.Sleep(c.linger)
	}
	kids, err := c.fakeSource.IssueChildren(repo, number)

	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return kids, err
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
// node, so a level of it is one read and running a level together proves
// nothing at all.
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

// !! THE DIFFERENTIAL. !! Reading a level at a time must produce exactly the
// tree reading one node at a time produces -- same nodes, same order, same
// levels, same error states. A faster walk that quietly reports a different
// tree is the failure this change has to avoid, and no timing check catches it.
//
// The two paths call the SAME method on the same source, so they cannot
// disagree by construction; this holds that property rather than assuming it,
// because the ordering and the cycle handling are what a rewrite gets wrong.
func TestPrefetchedAndPlainWalksAgree(t *testing.T) {
	route := routeWithStructure()
	for name, fixture := range map[string]func() *fakeSource{
		"narrow": wellFormed, "wide": wide, "unreadable": withUnreadableChild,
	} {
		t.Run(name, func(t *testing.T) {
			plain, err := walkFrom(fixture(), ref("acme", "lab", 46), -1, route)
			if err != nil {
				t.Fatalf("plain walk: %v", err)
			}
			fast, err := Walk(fixture(), ref("acme", "lab", 46), -1, route)
			if err != nil {
				t.Fatalf("prefetched walk: %v", err)
			}
			if got, want := render(fast, 0), render(plain, 0); got != want {
				t.Errorf("the prefetched walk reports a different tree.\n--- plain ---\n%s\n--- prefetched ---\n%s", want, got)
			}
		})
	}
}

// withUnreadableChild scripts a child whose read fails, so the differential
// covers the shape where the two paths could most easily disagree.
func withUnreadableChild() *fakeSource {
	f := wide()
	f.errs = map[string]error{"acme/lab#75": fmt.Errorf("cannot read this one")}
	return f
}

// A LEVEL IS READ TOGETHER, which is the entire point: against conditional
// REST an unchanged read costs nothing, so latency is the only cost left and
// running them at once is what removes it.
func TestALevelIsReadTogether(t *testing.T) {
	c := &countingSource{fakeSource: wide(), linger: 25 * time.Millisecond}
	if _, err := Walk(c, ref("acme", "lab", 46), -1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.widest < 3 {
		t.Errorf("at most %d read(s) were ever in flight at once; the three siblings on a level "+
			"must be read together, or the walk is still paying one round trip per node", c.widest)
	}
}

// A READ THAT FAILS IS NOT RECORDED. It falls through to the ordinary path, so
// the failure is reported against the node it belongs to rather than swallowed
// here where nothing can name it.
func TestAFailedPrefetchFallsBackRatherThanCaching(t *testing.T) {
	src := wellFormed()
	src.errs = map[string]error{"acme/doctrine#282": fmt.Errorf("boom")}

	plain, err := walkFrom(src, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("plain walk: %v", err)
	}
	src2 := wellFormed()
	src2.errs = map[string]error{"acme/doctrine#282": fmt.Errorf("boom")}
	fast, err := Walk(src2, ref("acme", "lab", 46), -1, routeWithStructure())
	if err != nil {
		t.Fatalf("prefetched walk: %v", err)
	}
	if got, want := render(fast, 0), render(plain, 0); got != want {
		t.Errorf("a failed read changed the tree.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// A depth-limited walk must not read past the depth it will report on.
func TestADepthLimitedWalkDoesNotReadDeeper(t *testing.T) {
	c := &countingSource{fakeSource: wide()}
	if _, err := Walk(c, ref("acme", "lab", 46), 1, routeWithStructure()); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	// The root, and nothing below the first level.
	if c.calls > 1 {
		t.Errorf("a depth-1 walk made %d child reads, want 1 -- it read past what it reports", c.calls)
	}
}
