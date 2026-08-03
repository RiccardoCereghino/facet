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
	ViewIssue(repo string, number int) (*ghx.Issue, error)
	IssueChildren(repo string, number int) ([]ghx.IssueRef, error)
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

	Children []*Node

	// Err records a node that could not be read. The walk keeps going: one
	// inaccessible issue must not blank out the rest of a tree, and a partial
	// answer that says which part is missing beats no answer.
	Err error
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
		level, ok, err := LevelOf(src, route, ref)
		if err != nil {
			return nil, err
		}
		root.Level, root.Assigned, root.LevelKnown = level, ok, true
	}
	path := map[string]bool{ref.String(): true}
	descend(src, root, maxDepth, route, path)
	return root, nil
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
		key := k.String()
		if path[key] {
			// Self-ancestry. Record it as a childless node so the report can
			// name it, and do not follow it.
			parent.Children = append(parent.Children, &Node{
				Ref: k, Depth: parent.Depth + 1,
				Err: fmt.Errorf("cycle: %s is its own ancestor", key),
			})
			continue
		}
		child, err := node(src, k, parent.Depth+1, route)
		if err != nil {
			child = &Node{Ref: k, Depth: parent.Depth + 1, Err: err}
			parent.Children = append(parent.Children, child)
			continue
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
	child.Level, child.Assigned = route.Structure.Assign(parent.Level, key, child.Title)
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
func LevelOf(src Source, route *routing.Routing, ref ghx.IssueRef) (int, bool, error) {
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
			return 0, false, fmt.Errorf("cycle above %s: %s is its own ancestor", ref, parent)
		}
		seen[parent.String()] = true
		chain = append(chain, parent)
		at = parent
	}

	roots := s.ChildLevels(-1)
	if len(roots) == 0 {
		return 0, false, nil
	}
	level := roots[0]
	for i := len(chain) - 2; i >= 0; i-- {
		n := chain[i]
		iss, err := src.ViewIssue(n.OwnerRepo(), n.Number)
		if err != nil {
			return 0, false, err
		}
		if iss == nil {
			return 0, false, fmt.Errorf("%s: no such issue", n)
		}
		next, ok := s.Assign(level, route.KeyForRepo(n.OwnerRepo()), iss.Title)
		if !ok {
			return 0, false, nil
		}
		level = next
	}
	return level, true, nil
}
