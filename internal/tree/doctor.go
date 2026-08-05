package tree

import (
	"errors"
	"fmt"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// Defect is one problem found in a tree, phrased so it explains itself without
// anyone having to find the incident it came from. Why is not optional: a check
// whose reason is not written down is a check the next person deletes.
type Defect struct {
	Ref  ghx.IssueRef
	What string // what is wrong
	Why  string // the loss it represents
	Fix  string // what to do about it
}

func (d Defect) String() string {
	s := fmt.Sprintf("%s: %s", d.Ref, d.What)
	if d.Why != "" {
		s += "\n    why: " + d.Why
	}
	if d.Fix != "" {
		s += "\n    fix: " + d.Fix
	}
	return s
}

// Doctor reports every defect in the tree below root, root included.
//
// IT IS DELIBERATELY TWO SETS OF CHECKS. The universal ones hold for any
// hierarchy anyone might build and run always. The structural ones exist only
// where a routing file declares levels, because the shape a tree ought to have
// is one organisation's contract -- so a tree with no structure configured is
// checked for the things that are wrong on their own terms, and for nothing
// else. In particular an issue with no parent is NEVER reported: that is the
// ordinary state of an issue, not a defect.
func Doctor(root *Node, route *routing.Routing) []Defect {
	var out []Defect
	nodes := append([]*Node{root}, root.Descendants()...)

	for _, n := range nodes {
		out = append(out, universal(n)...)
	}
	if route == nil || route.Structure == nil {
		return out
	}
	for _, n := range nodes {
		out = append(out, structural(n, route.Structure)...)
		if n.Err == nil && n.Assigned {
			out = append(out, levelLabel(n, route.Structure, route.KeyForRepo(n.Ref.OwnerRepo()))...)
		}
	}
	return out
}

// universal holds whatever the tree is meant to be.
func universal(n *Node) []Defect {
	var out []Defect

	if n.Err != nil {
		out = append(out, Defect{
			Ref:  n.Ref,
			What: "could not be read: " + n.Err.Error(),
			Why:  "a node that cannot be read is not a node with nothing wrong -- the rest of this report is silent about whatever hangs below it",
			Fix:  "check the issue exists and this credential can see its repository",
		})
		return out
	}

	// A closed parent with open children. True of any hierarchy: whatever the
	// parent stood for was declared finished while part of it demonstrably was
	// not.
	if n.IsClosed() {
		var open []string
		for _, c := range n.Children {
			if c.Err == nil && !c.IsClosed() {
				open = append(open, c.Ref.String())
			}
		}
		if len(open) > 0 {
			out = append(out, Defect{
				Ref:  n.Ref,
				What: fmt.Sprintf("is closed, but %d of its children are open: %s", len(open), strings.Join(open, ", ")),
				Why:  "the parent says the work is finished and its own children say otherwise; whichever is right, one of them is misleading everyone reading the tree",
				Fix:  "reopen the parent, or close or re-parent the children that are still live",
			})
		}
	}
	return out
}

func levelNameAt(s *routing.Structure, i int) string {
	if i < 0 || i >= len(s.Levels) {
		return "node"
	}
	return s.Levels[i].Name
}

// levelLabel reports whether the node RECORDS the level it occupies, and it is
// the check argano#7's whole block depends on.
//
// Three outcomes, and the middle one is why this is not just a presence check:
//
//   - the right label is present -- nothing to say;
//   - NO structure label at all -- the level exists only in the title, so
//     anything that is not facet must parse a prefix to find it;
//   - a DIFFERENT structure label than the level this node actually occupies --
//     two sources of truth, which is the defect rather than the fix, so it is
//     reported loudly and never silently corrected.
func levelLabel(n *Node, s *routing.Structure, repoKey string) []Defect {
	want, declared := s.LabelFor(n.Level, repoKey, n.Title)
	if !declared {
		return nil
	}

	known := map[string]bool{}
	for _, l := range s.Labels() {
		known[l] = true
	}
	var has []string
	for _, l := range n.Labels {
		if known[l] {
			has = append(has, l)
		}
	}

	switch {
	case len(has) == 1 && has[0] == want:
		return nil
	case len(has) == 0:
		return []Defect{{
			Ref:  n.Ref,
			What: fmt.Sprintf("sits at level %q and does not record it: %s is missing", levelNameAt(s, n.Level), want),
			Why:  "the level is knowable only by parsing the title, so every actor that is not facet must reimplement that parse -- and a retitled issue silently changes level",
			Fix:  fmt.Sprintf("gh issue edit %d --repo %s --add-label %s", n.Ref.Number, n.Ref.OwnerRepo(), want),
		}}
	default:
		return []Defect{{
			Ref: n.Ref,
			What: fmt.Sprintf("records %s but sits at level %q, which is %s",
				strings.Join(has, ", "), levelNameAt(s, n.Level), want),
			Why: "the label and the tree disagree about what this is; two sources of truth is the defect, and a reader has no way to tell which one is stale",
			Fix: fmt.Sprintf("decide which is right, then either re-parent it or: gh issue edit %d --repo %s --remove-label %s --add-label %s",
				n.Ref.Number, n.Ref.OwnerRepo(), strings.Join(has, " --remove-label "), want),
		}}
	}
}

// structural holds only where levels are declared.
func structural(n *Node, s *routing.Structure) []Defect {
	var out []Defect
	if n.Err != nil {
		return out
	}

	// The node read fine and its POSITION could not be established. Two
	// different facts hide here and they get two messages, because one sentence
	// covering both is how a report ends up asserting something false about
	// whichever case it was not written for.
	if n.LevelErr != nil {
		var cyc *ParentCycleError
		if errors.As(n.LevelErr, &cyc) {
			// A record defect, not a failure. It is fully known and nameable.
			return []Defect{{
				Ref: n.Ref,
				What: fmt.Sprintf("is inside a parent cycle: %s's parent is %s, which is already above it",
					cyc.Child, cyc.Ancestor),
				Why: "an issue that is its own ancestor has no level, and any walk of its ancestry runs forever -- so nothing above it can be judged, by this report or any other",
				Fix: fmt.Sprintf("break it at the closing edge: re-parent %s away from %s",
					cyc.Child, cyc.Ancestor),
			}}
		}
		// A read that did not answer. Nothing is known, and saying more than
		// that would be inventing it.
		return []Defect{{
			Ref:  n.Ref,
			What: "its position in the tree could not be established: " + n.LevelErr.Error(),
			Why:  "the node itself was read; its ancestry was not, and that is where a level comes from -- so the tree below is shown but nothing here says whether it sits in the right place",
			Fix:  "retry, or check this credential can read the repositories its parents live in -- a parent routinely lives in another repo",
		}}
	}

	if !n.LevelKnown {
		return out
	}

	// The defect the whole feature exists for: something at a depth where it
	// does not belong, which is how levels collapse one reasonable-looking
	// edge at a time.
	if !n.Assigned {
		// THE NODE THE WALK STARTED AT, which has no parent in this report --
		// unplaceable because it, or something above it, matches no declared
		// level. Whatever is at fault may be an ancestor this walk never
		// visited, so nothing here can name it, and the child wording below
		// would blame this node for its ancestor's defect and tell someone to
		// re-parent a node that may be perfectly well formed.
		if !n.HasParent {
			out = append(out, Defect{
				Ref:  n.Ref,
				What: "could not be placed: it, or something above it, sits at no level this structure declares",
				Why: "this walk began here, so the misplaced node may be an ancestor it never visited -- " +
					"naming this one as the defect would send someone to re-parent a node that may be correct",
				Fix: "run doctor from the root of the tree, which can see the ancestors this report cannot",
			})
			return out
		}

		// Derive the expectation from the parent's LEVEL, which is what the
		// assignment actually used. Deriving it from depth instead produces a
		// message naming the very level the node was just rejected for not
		// matching, whenever a rung above has been skipped.
		var want []string
		for _, i := range s.ChildLevels(n.ParentLevel) {
			want = append(want, s.Levels[i].Describe())
		}
		what := fmt.Sprintf("sits below %q, which may only hold %s",
			levelNameAt(s, n.ParentLevel), strings.Join(want, " or "))
		if len(want) == 0 {
			what = fmt.Sprintf("sits below %q, the deepest declared level -- nothing may hang under it",
				levelNameAt(s, n.ParentLevel))
		}
		out = append(out, Defect{
			Ref:  n.Ref,
			What: what,
			Why:  "a node at the wrong level collapses the ones above it, and every individual edge still looks reasonable while it happens",
			Fix:  "re-parent it, correct its title or repo, or declare the level it belongs to",
		})
		return out
	}

	// A rung whose purpose is to hold others, closed holding nothing.
	if lvl := s.Levels[n.Level]; lvl.RequiresChildren && n.IsClosed() && len(n.Children) == 0 {
		out = append(out, Defect{
			Ref:  n.Ref,
			What: fmt.Sprintf("is a closed %s with no children", lvl.Name),
			Why:  "this level exists to hold the work it accounts for; closed and empty, whatever it covered can no longer be attributed to it",
			Fix:  "wire its work under it before closing, or record why there was none",
		})
	}
	return out
}

// PlanBackfill records the level on every node missing it, and reports what it
// applied and what it could not reach.
//
// It lives here, beside the check it repairs, and takes the write as a
// function so it can be tested without touching a live issue. That is not
// ceremony: this is the only code in the level work that MUTATES the record,
// and it was the one path with no test until an audit said so.
//
// It applies ONLY the unambiguous case. A node whose label CONTRADICTS its
// position is left exactly as it is and reported by Doctor instead: two
// sources of truth is the defect, and quietly picking one destroys the
// evidence of which was wrong. A node whose level could not be established is
// skipped rather than guessed at, and COUNTED -- a tally of successes alone
// reads as complete coverage.
func PlanBackfill(route *routing.Routing, nodes []*Node, apply func(repo string, number int, label string) error) (applied, skipped int) {
	if route == nil || route.Structure == nil {
		return 0, 0
	}
	known := map[string]bool{}
	for _, l := range route.Structure.Labels() {
		known[l] = true
	}
	for _, n := range nodes {
		if n.Err != nil || !n.Assigned {
			skipped++
			continue
		}
		want, ok := route.Structure.LabelFor(n.Level, route.KeyForRepo(n.Ref.OwnerRepo()), n.Title)
		if !ok {
			continue
		}
		has, conflict := false, false
		for _, l := range n.Labels {
			switch {
			case l == want:
				has = true
			case known[l]:
				conflict = true
			}
		}
		if has || conflict {
			continue
		}
		if err := apply(n.Ref.OwnerRepo(), n.Ref.Number, want); err != nil {
			skipped++
			continue
		}
		applied++
	}
	return applied, skipped
}
