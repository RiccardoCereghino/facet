package tree

import (
	"strconv"
	"sync"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// prefetchWidth is how many child reads are in flight at once.
//
// Concurrency is the right tool HERE and was the wrong one before, and the
// difference is which resource is scarce. Against GraphQL the budget is points
// and running ten queries at once spends them ten times faster -- so the answer
// there was fewer calls. Against conditional REST an unchanged read costs
// NOTHING, so the only remaining cost is latency, and latency is exactly what
// running them together removes.
//
// Modest on purpose: enough to turn a minute of waiting into seconds, not
// enough to look like a burst to anyone on the other end. Measured: a 160-node
// walk goes from ~60s one at a time to ~9s at 8, and ~4s here.
const prefetchWidth = 24

// prefetchRounds bounds the descent. Each round goes one rung deeper, so this
// permits a tree far deeper than any real one while making a pathological
// shape terminate rather than spin.
const prefetchRounds = 32

// prefetch answers IssueChildren from a read already done, and falls through to
// the real source for anything it did not cover.
//
// IT WRAPS Source RATHER THAN REPLACING THE WALK. descend keeps deciding
// order, cycles, depth limits and how an unreadable node is reported, so the
// prefetched path cannot drift from the plain one in any of the places that are
// easy to get subtly wrong. The only thing that changes is WHEN a child list is
// read -- a level at a time, together -- and never what it says.
//
// It needs no capability interface and no cooperation from the source: it calls
// the same method the walk would have called, just earlier and in parallel. So
// every existing fake exercises it unchanged, and a differential test compares
// two paths that cannot disagree by construction.
type prefetch struct {
	Source
	mu       sync.Mutex
	children map[string][]ghx.SubIssue
}

func (p *prefetch) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	p.mu.Lock()
	kids, ok := p.children[refKey(repo, number)]
	p.mu.Unlock()
	if ok {
		return kids, nil
	}
	return p.Source.IssueChildren(repo, number)
}

func refKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

// newPrefetch reads the tree below root a LEVEL AT A TIME, with the reads of
// one level running together.
//
// A failed read is NOT recorded. It is left out, so the walk asks for it again
// in the ordinary way and reports whatever happens there -- where the failure
// can be named against the node it belongs to, rather than here where it cannot.
func newPrefetch(src Source, root ghx.IssueRef, maxDepth int) Source {
	if maxDepth == 0 {
		// Nothing below the root will be read, so there is nothing to warm.
		return src
	}
	p := &prefetch{Source: src, children: map[string][]ghx.SubIssue{}}

	frontier := []ghx.IssueRef{root}
	seen := map[string]bool{root.String(): true}

	for round := 0; round < prefetchRounds && len(frontier) > 0; round++ {
		results := p.readLevel(src, frontier)

		var next []ghx.IssueRef
		for _, r := range results {
			if r.err != nil {
				continue
			}
			p.children[refKey(r.ref.OwnerRepo(), r.ref.Number)] = r.kids
			for _, k := range r.kids {
				if seen[k.Ref.String()] {
					continue
				}
				seen[k.Ref.String()] = true
				next = append(next, k.Ref)
			}
		}
		frontier = next

		if maxDepth >= 0 && round+1 >= maxDepth {
			// The walk will not descend past here, so reading further is
			// work bought for nothing.
			break
		}
	}
	return p
}

type levelResult struct {
	ref  ghx.IssueRef
	kids []ghx.SubIssue
	err  error
}

// readLevel reads every node of one level, prefetchWidth at a time.
func (p *prefetch) readLevel(src Source, refs []ghx.IssueRef) []levelResult {
	out := make([]levelResult, len(refs))
	sem := make(chan struct{}, prefetchWidth)
	var wg sync.WaitGroup

	for i, r := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			kids, err := src.IssueChildren(r.OwnerRepo(), r.Number)
			out[i] = levelResult{ref: r, kids: kids, err: err}
		}()
	}
	wg.Wait()
	return out
}
