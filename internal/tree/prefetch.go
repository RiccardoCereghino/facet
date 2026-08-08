package tree

import (
	"strconv"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// SubtreeSource is an OPTIONAL capability: a Source that can answer a whole
// subtree in one read. A Source that does not provide it is simply not asked,
// and the walk runs exactly as it always has -- which is what keeps the
// per-node path alive as the fallback and as the thing a differential test
// checks the fast path against.
type SubtreeSource interface {
	IssueSubtree(repo string, number, depth int) (*ghx.SubTree, error)
}

// subtreeDepth is how many rungs one query asks for. Four covers the declared
// shape of the trees this was built for; anything below it is reached by a
// follow-up rather than by nesting deeper, because the node budget is a
// product across levels and a fifth rung is rejected outright.
const subtreeDepth = 4

// prefetchRounds bounds the follow-up loop. Each round reaches subtreeDepth
// rungs further down, so this permits a tree far deeper than any real one
// while making a pathological or cyclic shape terminate rather than spin.
const prefetchRounds = 8

// prefetch answers IssueChildren from a batch already read, and falls through
// to the real source for anything the batch did not completely cover.
//
// IT DELIBERATELY WRAPS Source RATHER THAN REPLACING THE WALK. descend keeps
// deciding order, cycles, depth limits and how an unreadable node is reported
// -- so the fast path cannot drift from the slow one in any of the places that
// are easy to get subtly wrong. The only thing that changes is where a child
// list comes from.
//
// ONLY COMPLETE ANSWERS ARE CACHED. A node whose children were truncated, or
// which sat below what the query reached, is left out of the map entirely, so
// asking for it falls through to the paged per-node read that has always been
// correct. Nothing here can shorten a child list: it can only avoid a request.
type prefetch struct {
	Source
	children map[string][]ghx.SubIssue
	blockers map[string][]ghx.Blocker
}

func (p *prefetch) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	if kids, ok := p.children[refKey(repo, number)]; ok {
		return kids, nil
	}
	return p.Source.IssueChildren(repo, number)
}

// Blockers reports the blocking edges read alongside the tree, with each
// blocker's own state, and whether this node was covered at all.
func (p *prefetch) Blockers(repo string, number int) ([]ghx.Blocker, bool) {
	if p == nil {
		return nil, false
	}
	b, ok := p.blockers[refKey(repo, number)]
	return b, ok
}

func refKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

// newPrefetch reads the tree below root in as few requests as it can, and
// returns a Source that answers from what it got.
//
// A failed batch is NOT an error. Every node it could not cover simply falls
// through to the per-node path, so the worst case is the cost the walk had
// before this existed -- never a wrong answer, and never a short one.
func newPrefetch(src Source, root ghx.IssueRef, maxDepth int) Source {
	ss, ok := src.(SubtreeSource)
	if !ok {
		return src
	}
	p := &prefetch{
		Source:   src,
		children: map[string][]ghx.SubIssue{},
		blockers: map[string][]ghx.Blocker{},
	}

	frontier := []ghx.IssueRef{root}
	seen := map[string]bool{root.String(): true}
	for round := 0; round < prefetchRounds && len(frontier) > 0; round++ {
		var next []ghx.IssueRef
		for _, ref := range frontier {
			st, err := ss.IssueSubtree(ref.OwnerRepo(), ref.Number, subtreeDepth)
			if err != nil || st == nil {
				// Leave it uncached; the walk will read it the old way and
				// report whatever it finds in the ordinary place.
				continue
			}
			next = append(next, absorb(p, st, seen)...)
		}
		frontier = next
	}
	_ = maxDepth // the walk still enforces it; over-reading is only wasted work
	return p
}

// absorb records everything a batch answered completely, and returns the nodes
// to ask about next.
//
// The two incomplete cases are finished differently, and getting that wrong is
// how this path either loops or quietly falls back to the walk it replaced. A
// TRUNCATED connection is finished by paging that one node: re-asking for its
// subtree returns the same first page every time. An UNREACHED node is
// finished by asking for its subtree, which is the whole point -- paging it
// would buy one rung where a query buys four.
func absorb(p *prefetch, st *ghx.SubTree, seen map[string]bool) []ghx.IssueRef {
	var next []ghx.IssueRef
	queue := func(r ghx.IssueRef) {
		if seen[r.String()] {
			return
		}
		seen[r.String()] = true
		next = append(next, r)
	}

	var rec func(n *ghx.SubTree)
	rec = func(n *ghx.SubTree) {
		key := refKey(n.Ref.OwnerRepo(), n.Ref.Number)
		if _, done := p.blockers[key]; !done {
			p.blockers[key] = n.BlockedBy
		}

		// Whatever DID come back below this node is still worth keeping, even
		// when the node's own list is short.
		for i := range n.Children {
			rec(&n.Children[i])
		}

		switch {
		case !n.MoreChildren:
			kids := make([]ghx.SubIssue, 0, len(n.Children))
			for i := range n.Children {
				c := &n.Children[i]
				kids = append(kids, ghx.SubIssue{
					Ref: c.Ref, Title: c.Title, State: c.State, Labels: c.Labels,
				})
			}
			p.children[key] = kids

		case !n.ConnectionSeen:
			// The query ran out of rungs here. One more query, rooted here,
			// reaches four more.
			queue(n.Ref)

		default:
			// Truncated. Page it the way that has always been correct, then
			// ask about each child that the batch did not already cover.
			kids, err := p.Source.IssueChildren(n.Ref.OwnerRepo(), n.Ref.Number)
			if err != nil {
				// Leave it uncached. The walk reads it again and reports the
				// failure in the ordinary place, rather than here where
				// nothing can say which subtree it belonged to.
				return
			}
			p.children[key] = kids
			for _, k := range kids {
				if _, have := p.children[refKey(k.Ref.OwnerRepo(), k.Ref.Number)]; have {
					continue
				}
				queue(k.Ref)
			}
		}
	}
	rec(st)
	return next
}
