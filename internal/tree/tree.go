// Package tree walks GitHub's sub-issue graph and reports on its shape.
//
// The graph is cross-repository: a child routinely lives somewhere other than
// its parent, so every node is identified by owner, repo and number rather
// than by a number alone.
//
// What counts as a well-formed shape is NOT decided here. This package walks,
// counts and reports; the levels come from the routing file, and a caller with
// no structure configured gets a tree with no opinion attached to it.
package tree

import (
	"fmt"
	"sort"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// Source is the narrow slice of GitHub a walk needs. It is declared here
// rather than reusing the full client so a test can script two methods instead
// of twenty.
type Source interface {
	// ViewIssue is called exactly once per walk, for the root -- every other
	// node's title/state/labels comes from IssueChildren directly (facet#105).
	ViewIssue(repo string, number int) (*ghx.Issue, error)
	IssueChildren(repo string, number int) ([]ghx.SubIssue, error)
	// IssueParent is needed to establish what the node a walk STARTS at
	// actually is. Without it a walk can only assume its argument is a root,
	// and that assumption is wrong for every subtree.
	IssueParent(repo string, number int) (ghx.IssueRef, bool, error)
}

// Node is one issue in the tree.
type Node struct {
	Ref    ghx.IssueRef
	Title  string
	State  string // OPEN or CLOSED
	Labels []string
	Depth  int

	// Level indexes the routing file's levels. Assigned is false when no
	// structure is configured -- the common case -- and also when the node
	// matched no level the structure allows here, which is the defect a shape
	// report exists to find. The two are told apart by LevelKnown.
	Level      int
	Assigned   bool
	LevelKnown bool

	// ParentLevel is the level this node's parent was assigned, which is what
	// its own candidates were derived from. Reported separately from Depth
	// because the two diverge the moment a level is skipped -- and a message
	// that derives the expectation from depth while the assignment came from
	// the parent's level says things like "expects issue" about a node it just
	// rejected for not being an issue.
	ParentLevel int
	HasParent   bool

	// candidates is the exact set of levels this node was judged against,
	// recorded rather than re-derived. It can be narrower than
	// ChildLevels(ParentLevel) when the parent's own shape permits less below
	// it than its rung alone would, and a report that re-derived the set from
	// the rung would then name expectations the node was never judged by --
	// the same class of mistake as deriving them from Depth, one level along.
	candidates []int

	Children []*Node

	// LevelErr records that the node's POSITION could not be established --
	// the node itself read fine, but the ancestry its level is derived from
	// did not. Distinct from Err, which means the node is unreadable: these
	// are different facts and a report that gives them one sentence is the
	// inherited-message defect again.
	LevelErr error

	// Err records a node that could not be read. The walk keeps going: one
	// inaccessible issue must not blank out the rest of a tree, and a partial
	// answer that says which part is missing beats no answer.
	Err error

	// BlockedBy is what must land before this node can start, carried with
	// each blocker's own state, when the source could answer it alongside the
	// tree. BlockersKnown tells "none" from "not read" -- and they are
	// opposite answers, since only one of them means ready.
	//
	// Reading them here rather than per node afterwards is what stops the
	// second N+1: a blocker holding ten issues was fetched ten times, because
	// the edge and the state came from different requests.
	BlockedBy     []ghx.Blocker
	BlockersKnown bool
}

// IsClosed reports whether the issue is closed.
func (n *Node) IsClosed() bool { return strings.EqualFold(n.State, "CLOSED") }

// Tier extracts the complexity label as "cN", plus every complexity label
// found so a caller can say why it could not settle on one.
//
// A parent's tier is NEVER an input to a child's. It is an at-a-glance worst
// case for a grouping and nothing more: inheriting it would silently move
// merge authority for work that has not changed.
func (n *Node) Tier() (tier string, found []string) {
	for _, l := range n.Labels {
		if rest, ok := strings.CutPrefix(l, "complexity/"); ok {
			found = append(found, l)
			tier = "c" + rest
		}
	}
	if len(found) != 1 {
		return "", found
	}
	return tier, found
}

// Walk reads the tree rooted at ref, to maxDepth levels below the root
// (negative for unlimited).
//
// Cycles are detected on the path rather than globally: an issue legitimately
// appears twice in one report if two branches reach it, but an issue that is
// its own ancestor would spin forever.
func Walk(src Source, ref ghx.IssueRef, maxDepth int, route *routing.Routing) (*Node, error) {
	// Read the tree in bulk first, where the source can. Everything below is
	// unchanged: this only decides where a child list comes from, so the
	// order, the cycle handling, the depth limit and every error message stay
	// the walk's. A source without the capability is not asked and nothing
	// here notices.
	src = newPrefetch(src, ref, maxDepth)

	root, err := node(src, ref, 0, route)
	if err != nil {
		return nil, err
	}
	// ESTABLISH WHAT THE STARTING NODE ACTUALLY IS. Assuming it is a root is
	// wrong for every subtree, and wrong in the direction that matters: a walk
	// begun at a node three rungs down would call it a root, then report every
	// correctly-placed node beneath it as misplaced and prescribe re-parenting
	// a tree that was right. A doctor's false positive that tells someone to
	// break a correct tree is worse than a missed defect.
	if route != nil && route.Structure != nil {
		level, ok, err := LevelOf(src, route, ref, root.Labels)
		switch {
		case err != nil:
			// CARRY IT, DO NOT BLANK THE REPORT. The same principle the child
			// direction has always held: one inaccessible issue must not erase
			// the rest of a tree, and a partial answer that says which part is
			// missing beats no answer. The climb only entered the walk with the
			// start-node fix, and the principle did not come with it -- so a
			// parent in another repository this credential cannot read, or one
			// transient failure, turned the whole report into nothing.
			root.LevelErr = err
		default:
			root.Level, root.Assigned, root.LevelKnown = level, ok, true
		}
	}
	path := map[string]bool{ref.String(): true}
	descend(src, root, maxDepth, route, path)
	if p, ok := src.(*prefetch); ok {
		attachBlockers(root, p)
	}
	return root, nil
}

// attachBlockers hands each node the blocking edges the bulk read already
// returned. A node the batch did not cover is left BlockersKnown false rather
// than empty, because "nothing blocks this" and "nobody asked" are the two
// answers a readiness report must never confuse.
func attachBlockers(root *Node, p *prefetch) {
	for _, n := range append([]*Node{root}, root.Descendants()...) {
		if b, ok := p.Blockers(n.Ref.OwnerRepo(), n.Ref.Number); ok {
			n.BlockedBy, n.BlockersKnown = b, true
		}
	}
}

func descend(src Source, parent *Node, maxDepth int, route *routing.Routing, path map[string]bool) {
	if maxDepth >= 0 && parent.Depth >= maxDepth {
		return
	}
	kids, err := src.IssueChildren(parent.Ref.OwnerRepo(), parent.Ref.Number)
	if err != nil {
		parent.Err = err
		return
	}
	for _, k := range kids {
		key := k.Ref.String()
		if path[key] {
			// Self-ancestry. Record it as a childless node so the report can
			// name it, and do not follow it.
			parent.Children = append(parent.Children, &Node{
				Ref: k.Ref, Depth: parent.Depth + 1,
				Err: fmt.Errorf("cycle: %s is its own ancestor", key),
			})
			continue
		}
		if k.State == "" {
			// The child's fields did not come back with the parent's
			// IssueChildren read -- GraphQL can return a list entry it could
			// not resolve. Same fact ViewIssue failing for a child used to
			// carry: the node is known to exist (it has a ref) but could not
			// be read, so it is reported and not descended into.
			parent.Children = append(parent.Children, &Node{
				Ref: k.Ref, Depth: parent.Depth + 1,
				Err: fmt.Errorf("%s: its title/state/labels did not come back from the parent's children read", k.Ref),
			})
			continue
		}
		child := &Node{
			Ref: k.Ref, Title: k.Title, State: k.State,
			Labels: k.Labels, Depth: parent.Depth + 1,
		}
		assign(child, parent, route)
		parent.Children = append(parent.Children, child)

		path[key] = true
		descend(src, child, maxDepth, route, path)
		delete(path, key)
	}
}

// assign settles a child's level from its parent's. With no structure
// configured nothing is assigned and nothing is judged.
//
// A parent that sits at no declared level stops the assignment here rather
// than passing its zero Level down: everything below a misplaced node would
// otherwise be judged against the wrong rung and reported as misplaced too,
// turning one defect into a cascade of them. The defect is the ancestor's, and
// it is already reported as such.
func assign(child, parent *Node, route *routing.Routing) {
	if route == nil || route.Structure == nil || !parent.LevelKnown || !parent.Assigned {
		return
	}
	child.LevelKnown = true
	child.ParentLevel, child.HasParent = parent.Level, true
	key := route.KeyForRepo(child.Ref.OwnerRepo())
	// The parent's own shape, not just its rung, decides what may sit below it.
	// Every field that answers this is already on the parent node, so narrowing
	// here costs no read.
	parentKey := route.KeyForRepo(parent.Ref.OwnerRepo())
	within := route.Structure.ChildLevelsFor(parent.Level, parentKey, parent.Title, parent.Labels)
	child.candidates = within
	child.Level, child.Assigned = route.Structure.AssignWithin(within, key, child.Title, child.Labels)
}

func node(src Source, ref ghx.IssueRef, depth int, _ *routing.Routing) (*Node, error) {
	iss, err := src.ViewIssue(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return nil, err
	}
	if iss == nil {
		return nil, fmt.Errorf("%s: no such issue", ref)
	}
	return &Node{
		Ref: ref, Title: iss.Title, State: iss.State,
		Labels: iss.LabelNames(), Depth: depth,
	}, nil
}

// Descendants returns every node below the root, root excluded, depth-first.
func (n *Node) Descendants() []*Node {
	var out []*Node
	var rec func(*Node)
	rec = func(p *Node) {
		for _, c := range p.Children {
			out = append(out, c)
			rec(c)
		}
	}
	rec(n)
	return out
}

// Counts tallies board statuses over a set of nodes.
//
// Unknown counts nodes the board has no item for, and it is reported rather
// than folded into any bucket: "not on the board" is a different fact from
// "not started", and merging them would quietly turn an unconfigured tree into
// one that looks untouched.
type Counts struct {
	Total    int
	ByStatus map[string]int
	Unknown  int
	Closed   int
}

// Tally counts nodes by their board status.
func Tally(nodes []*Node, statuses map[string]string) Counts {
	c := Counts{ByStatus: map[string]int{}}
	for _, n := range nodes {
		c.Total++
		if n.IsClosed() {
			c.Closed++
		}
		if s, ok := statuses[n.Ref.String()]; ok && s != "" {
			c.ByStatus[s]++
			continue
		}
		c.Unknown++
	}
	return c
}

// StatusNames lists the statuses seen, in a stable order.
func (c Counts) StatusNames() []string {
	out := make([]string, 0, len(c.ByStatus))
	for k := range c.ByStatus {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParentCycleError reports an issue that is its own ancestor. It is a typed
// error rather than a plain one because a cycle is a DEFECT IN THE RECORD,
// while a failed read is a transient or permission problem -- the caller
// reports them differently, and only a type lets it tell them apart.
// It names the CLOSING EDGE rather than just the node asked about. "X is its
// own ancestor" is true and useless: it repeats the node the caller already
// gave and names nothing else in the loop, so there is no edge to go and break.
type ParentCycleError struct {
	At       ghx.IssueRef // where the walk started
	Child    ghx.IssueRef // the node whose parent closes the loop
	Ancestor ghx.IssueRef // the parent that was already in the ancestry
}

func (e *ParentCycleError) Error() string {
	return fmt.Sprintf("cycle in the ancestry of %s: %s's parent is %s, which is already above it",
		e.At, e.Child, e.Ancestor)
}

// LevelOf resolves which declared level an issue occupies.
//
// IT IS NOT A DEPTH. A level is assigned by matching a node's shape against
// the rungs its parent's level permits, so a tree that skips an optional rung
// has nodes whose level exceeds their ancestor count. The count is only ever a
// coincidence that holds until the first skip.
//
// It climbs to the root -- the child->parent direction, which is the
// immediately consistent one, unlike listing children -- and then assigns back
// down through the same Structure.Assign the walk uses. Both the walk and any
// caller judging a single edge go through this one function, so they cannot
// disagree about the same tree by construction rather than by having been
// fixed to the same answer twice.
//
// ok is false when the node, or some ancestor of it, sits at no declared level.
// That is a real finding about the tree rather than an error, and the caller
// decides what to do with it: a walk reports the node it started at, while an
// edge check refuses and points at the tree above.
//
// refLabels is ref's own labels, when the caller already has them (every
// caller does -- a walk from node(), a wire check from the issue it already
// fetched to build its refusal). Passing them avoids a second ViewIssue call
// for ref in the common case where ref turns out to be its own root
// (facet#105: a walk costs exactly one ViewIssue call, for the root). nil is
// fine when the caller has nothing on hand; it only costs a fetch when ref
// actually has no parent.
func LevelOf(src Source, route *routing.Routing, ref ghx.IssueRef, refLabels []string) (int, bool, error) {
	if route == nil || route.Structure == nil {
		return 0, false, nil
	}
	s := route.Structure

	// Climb, collecting the chain with ref first and the root last.
	chain := []ghx.IssueRef{ref}
	seen := map[string]bool{ref.String(): true}
	at := ref
	for {
		parent, ok, err := src.IssueParent(at.OwnerRepo(), at.Number)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			break
		}
		if seen[parent.String()] {
			return 0, false, &ParentCycleError{At: ref, Child: at, Ancestor: parent}
		}
		seen[parent.String()] = true
		chain = append(chain, parent)
		at = parent
	}

	roots := s.ChildLevels(-1)
	if len(roots) == 0 {
		return 0, false, nil
	}

	// A root's own level is not just "the shallowest candidate" -- it is
	// whatever the root's own type/* label asserts, when it asserts one.
	// ChildLevels(-1) can only ever name the sole non-optional level at
	// position 0 (in practice "commission"), so position alone could never
	// let a root be reported as, say, "block" -- only reading the label can.
	rootRef := chain[len(chain)-1]
	var rootLabels []string
	// rootTitle is only ever read by the descent below, which runs when the
	// chain is longer than one -- and that is exactly the case where the root
	// is a different issue than ref and so is genuinely fetched. When ref IS
	// the root there is nothing to descend and no title is needed.
	var rootTitle string
	if rootRef == ref {
		rootLabels = refLabels
	} else {
		rootIss, err := src.ViewIssue(rootRef.OwnerRepo(), rootRef.Number)
		if err != nil {
			return 0, false, err
		}
		if rootIss == nil {
			return 0, false, fmt.Errorf("%s: no such issue", rootRef)
		}
		rootLabels = rootIss.LabelNames()
		rootTitle = rootIss.Title
	}

	level := roots[0]
	if lvl, ok, ambiguous := s.LevelForLabels(route.KeyForRepo(rootRef.OwnerRepo()), rootLabels); ambiguous {
		return 0, false, fmt.Errorf(
			"%s carries labels for more than one declared level; the tree cannot tell which is authoritative",
			rootRef)
	} else if ok {
		level = lvl
	}

	// The descent carries the parent's identity, not just its level, because
	// what may sit below a rung can depend on which shape the parent matched.
	parentKey, parentTitle, parentLabels := route.KeyForRepo(rootRef.OwnerRepo()), rootTitle, rootLabels
	for i := len(chain) - 2; i >= 0; i-- {
		n := chain[i]
		iss, err := src.ViewIssue(n.OwnerRepo(), n.Number)
		if err != nil {
			return 0, false, err
		}
		if iss == nil {
			return 0, false, fmt.Errorf("%s: no such issue", n)
		}
		key := route.KeyForRepo(n.OwnerRepo())
		within := s.ChildLevelsFor(level, parentKey, parentTitle, parentLabels)
		next, ok := s.AssignWithin(within, key, iss.Title, iss.LabelNames())
		if !ok {
			return 0, false, nil
		}
		level = next
		parentKey, parentTitle, parentLabels = key, iss.Title, iss.LabelNames()
	}
	return level, true, nil
}
