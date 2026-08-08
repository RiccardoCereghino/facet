package tree

import (
	"strconv"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// BatchSource is an OPTIONAL capability: a Source that can read many issues in
// one request. A Source that does not provide it is simply not asked, and the
// walk runs exactly as it always has -- which is what keeps the per-node path
// alive as the fallback, and as the thing a differential test checks the
// batched path against.
type BatchSource interface {
	IssueNodes(refs []ghx.IssueRef) ([]ghx.NodeFields, error)
}

// prefetchRounds bounds the level-by-level expansion. Each round descends one
// rung, so this permits a tree far deeper than any real one while making a
// pathological shape terminate rather than spin.
const prefetchRounds = 24

// prefetch answers IssueChildren from a batch already read, and falls through
// to the real source for anything the batch did not completely cover.
//
// IT WRAPS Source RATHER THAN REPLACING THE WALK. descend keeps deciding
// order, cycles, depth limits and how an unreadable node is reported, so the
// batched path cannot drift from the per-node one in any of the places that
// are easy to get subtly wrong. The only thing that changes is where a child
// list comes from.
//
// ONLY COMPLETE ANSWERS ARE CACHED. A node whose children, labels or blockers
// came back short is left out, so asking for it falls through to the paged
// read that has always been correct. Nothing here can shorten a list: it can
// only avoid a request.
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

// newPrefetch reads the tree below root a LEVEL AT A TIME, batching every node
// of a level into one request.
//
// Level-by-level rather than nested, because GitHub bills a query on the nodes
// it COULD return, and those multiply down nesting while they only add across
// aliases. Nesting four rungs deep at a page size of 25 is billed ~179,000
// nodes to return 89; the same tree read this way is billed roughly its own
// size. The measurements are in internal/ghx/batch.go.
//
// A failed batch is NOT an error. Every node it could not cover falls through
// to the per-node path, so the worst case is the cost the walk had before this
// existed -- never a wrong answer, and never a short one.
func newPrefetch(src Source, root ghx.IssueRef, maxDepth int) Source {
	bs, ok := src.(BatchSource)
	if !ok {
		return src
	}
	p := &prefetch{
		Source:   src,
		children: map[string][]ghx.SubIssue{},
		blockers: map[string][]ghx.Blocker{},
	}

	// fields holds every node read so far, so a child's title, state and
	// labels come from its OWN read rather than from its parent's. That is
	// what keeps a child's labels off the parent's query, where the page size
	// would multiply them.
	fields := map[string]ghx.NodeFields{}
	frontier := []ghx.IssueRef{root}
	seen := map[string]bool{root.String(): true}

	depth := 0
	for round := 0; round < prefetchRounds && len(frontier) > 0; round++ {
		got, err := bs.IssueNodes(frontier)
		if err != nil {
			break
		}
		var next []ghx.IssueRef
		for _, f := range got {
			fields[refKey(f.Ref.OwnerRepo(), f.Ref.Number)] = f
			if f.Unreadable {
				continue
			}
			if f.BlockersComplete {
				p.blockers[refKey(f.Ref.OwnerRepo(), f.Ref.Number)] = f.BlockedBy
			}
			if !f.ChildrenComplete {
				// Its child list is short. Leave it uncached so the paged read
				// answers for it, and do not descend from a partial list.
				continue
			}
			for _, c := range f.Children {
				if seen[c.String()] {
					continue
				}
				seen[c.String()] = true
				next = append(next, c)
			}
		}
		frontier = next

		depth++
		if maxDepth >= 0 && depth > maxDepth {
			// The walk will not descend past here, so reading further is a
			// request bought for nothing.
			break
		}
	}

	// Every node has now been read in its own right, so a parent's child list
	// can be assembled with each child's real title, state and labels.
	for k, f := range fields {
		if f.Unreadable || !f.ChildrenComplete {
			continue
		}
		kids := make([]ghx.SubIssue, 0, len(f.Children))
		complete := true
		for _, c := range f.Children {
			cf, ok := fields[refKey(c.OwnerRepo(), c.Number)]
			if !ok || cf.Unreadable {
				// Never read, or unreadable. The per-node path names it in the
				// one place that can.
				complete = false
				break
			}
			if !cf.LabelsComplete {
				// A TRUNCATED LABEL LIST SILENTLY CHANGES A NODE'S LEVEL,
				// because the level is derived from a type/* label that may be
				// the one that fell off the end. Refuse the whole list rather
				// than cache an entry that reads fine and is wrong.
				complete = false
				break
			}
			kids = append(kids, ghx.SubIssue{
				Ref: c, Title: cf.Title, State: cf.State, Labels: cf.Labels,
			})
		}
		if !complete {
			continue
		}
		p.children[k] = kids
	}
	return p
}
