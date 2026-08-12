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
	// Read is the evidence this claim was derived from -- the labels actually
	// seen, the level actually assigned. IT IS WHAT MAKES A DEFECT FALSIFIABLE
	// AT THE POINT OF READING (facet#147).
	//
	// The incident behind it is worth the field on its own. Two defects were
	// reported against issues whose labels, checked minutes later, did not
	// match -- and an hour went into hunting a phantom before the label
	// timeline showed both issues HAD carried both labels for about fifty
	// seconds, while a repair was mid-flight. The report was RIGHT. Nobody
	// could tell, because it stated a conclusion and not what it had seen.
	Read string
}

func (d Defect) String() string {
	s := fmt.Sprintf("%s: %s", d.Ref, d.What)
	if d.Read != "" {
		s += "\n    read: " + d.Read
	}
	if d.Why != "" {
		s += "\n    why: " + d.Why
	}
	if d.Fix != "" {
		s += "\n    fix: " + d.Fix
	}
	return s
}

// Report is what one run of Doctor can say, and it SEPARATES TWO ANSWERS THAT
// ARE NOT THE SAME ANSWER.
//
// Defects are findings: something was read, and it is wrong. Unread is the
// third value -- a node whose read did not happen, so nothing is reported about
// it and nothing is ruled out beneath it.
//
// THEY WERE ONE LIST, AND THE COST WAS THAT `doctor` ANSWERED "I looked, and
// here is what is wrong" FOR A READ THAT NEVER HAPPENED. Measured live under a
// GraphQL exhaustion on 2026-08-11: a run against a node whose ancestry could
// not be read printed `1 defect(s)` and exited 1 -- the verb whose own --help
// documents exit 2 as *could NOT look*, and instructs callers not to read it as
// a finding, reporting a failure to look as a finding.
type Report struct {
	Defects []Defect
	Unread  []Defect
	// Acknowledged is the THIRD verdict (facet#159): a closed holder with no
	// children that carries acknowledgedLabel AND a reason on the record. It
	// is COUNTED AND PRINTED SEPARATELY, NEVER folded into Defects or hidden
	// -- a suppression that lowers the headline defect count is how an
	// instrument starts lying politely. Some legitimately ARE this shape: a
	// slate that was deferred, whose live blocks were correctly re-homed
	// elsewhere, should be childless, and GitHub allows exactly one parent
	// per issue -- so re-parenting the closed work back is not always
	// available, and where it is, doing it to a holder whose work is still
	// OPEN would produce a closed-parent-with-open-children defect on
	// purpose (facet#132's shape).
	Acknowledged []Defect
}

// Doctor reports every defect in the tree below root, root included, and
// separately everything it could not look at.
//
// IT IS DELIBERATELY TWO SETS OF CHECKS. The universal ones hold for any
// hierarchy anyone might build and run always. The structural ones exist only
// where a routing file declares levels, because the shape a tree ought to have
// is one organisation's contract -- so a tree with no structure configured is
// checked for the things that are wrong on their own terms, and for nothing
// else. In particular an issue with no parent is NEVER reported: that is the
// ordinary state of an issue, not a defect.
func Doctor(root *Node, route *routing.Routing) Report {
	return DoctorWithSource(root, route, nil)
}

// DoctorWithSource is [Doctor] with the acknowledgment check ACTIVE: src is
// used to fetch the body of a candidate closed-empty holder (only when it
// already carries acknowledgedLabel) and look for a reason recorded there.
//
// src IS OPTIONAL, and nil is not a degraded mode to apologise for --
// "automation arrives as a setting and it starts off" applies here exactly as
// it does everywhere else in the bottega: with src == nil the acknowledged
// label is READ but cannot be VERIFIED, so it changes nothing and the node
// still reports as a defect. A label nobody can check is not a suppression.
func DoctorWithSource(root *Node, route *routing.Routing, src Source) Report {
	var rep Report
	nodes := append([]*Node{root}, root.Descendants()...)

	for _, n := range nodes {
		d, unread := universal(n)
		rep.Defects = append(rep.Defects, d...)
		rep.Unread = append(rep.Unread, unread...)
	}
	if route == nil || route.Structure == nil {
		return rep
	}
	for _, n := range nodes {
		d, unread, ack := structural(n, route.Structure, src)
		rep.Defects = append(rep.Defects, d...)
		rep.Unread = append(rep.Unread, unread...)
		rep.Acknowledged = append(rep.Acknowledged, ack...)
		if n.Err == nil && n.Assigned {
			rep.Defects = append(rep.Defects,
				levelLabel(n, route.Structure, route.KeyForRepo(n.Ref.OwnerRepo()))...)
		}
	}
	return rep
}

// universal holds whatever the tree is meant to be. It returns findings and
// could-not-looks separately, because one list cannot carry both.
func universal(n *Node) (defects, unread []Defect) {
	var out []Defect

	if n.Err != nil {
		// A CYCLE IS A FINDING; EVERYTHING ELSE HERE IS NOT. Both stop the walk
		// at this node and both arrive in Err, and they are opposite facts: the
		// cycle was fully read and the record really is malformed, while an
		// unreadable node is a read that did not happen and asserts nothing.
		var cyc *SelfAncestorError
		if errors.As(n.Err, &cyc) {
			return []Defect{{
				Ref:  n.Ref,
				What: "could not be read: " + n.Err.Error(),
				Read: "reached during the descent while already on the path from the root",
				Why:  "an issue that is its own ancestor makes any walk of its ancestry run forever, so nothing below it can be judged",
				Fix:  fmt.Sprintf("break it at the closing edge: re-parent %s away from the ancestor it repeats", n.Ref),
			}}, nil
		}
		// NOT A FINDING. The node was not read, so this says nothing about
		// whether anything is wrong with it -- and the report is silent about
		// whatever hangs below it. It is still REPORTED, and naming it is the
		// whole point: one inaccessible issue must not blank out the rest of a
		// tree, and a partial answer that says which part is missing beats no
		// answer.
		return nil, []Defect{{
			Ref:  n.Ref,
			What: "could not be read: " + n.Err.Error(),
			Why:  "a node that cannot be read is not a node with nothing wrong -- the rest of this report is silent about whatever hangs below it",
			Fix:  "check the issue exists and this credential can see its repository",
		}}
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
				Read: fmt.Sprintf("state %s, %d children read, open: %s",
					n.State, len(n.Children), strings.Join(open, ", ")),
				Why: "the parent says the work is finished and its own children say otherwise; whichever is right, one of them is misleading everyone reading the tree",
				Fix: "reopen the parent, or close or re-parent the children that are still live",
			})
		}
	}
	return out, nil
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
	want, declared := s.LabelFor(n.Level, repoKey, n.Title, n.Labels)
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

	// The evidence, on every branch: the labels actually read and the level
	// actually assigned. A claim about labels that does not say which labels it
	// saw cannot be checked by the person reading it -- and when one was
	// doubted, it took a label-event timeline to establish that the tool had
	// been right all along (facet#147).
	evidence := fmt.Sprintf("labels %s; assigned level %q, whose label is %s",
		labelList(n.Labels), levelNameAt(s, n.Level), want)

	switch {
	case len(has) == 1 && has[0] == want:
		return nil
	case len(has) == 0:
		return []Defect{{
			Ref:  n.Ref,
			What: fmt.Sprintf("sits at level %q and does not record it: %s is missing", levelNameAt(s, n.Level), want),
			Read: evidence,
			Why:  "the level is knowable only by parsing the title, so every actor that is not facet must reimplement that parse -- and a retitled issue silently changes level",
			Fix:  fmt.Sprintf("gh issue edit %d --repo %s --add-label %s", n.Ref.Number, n.Ref.OwnerRepo(), want),
		}}
	default:
		// THE FIX MUST NOT REMOVE THE LABEL IT IS ABOUT TO ADD. `has` contains
		// every structure label the node carries, and when the node carries the
		// RIGHT one alongside a wrong one -- which is what a half-finished
		// repair looks like -- the correct label was in the removal list too,
		// producing `--remove-label type/work --add-label type/work`.
		//
		// That no-op was the ONLY signal available that anything was odd, and
		// it pointed the wrong way: it made a CORRECT finding look incoherent,
		// and an hour went into hunting a phantom that was a true report. An
		// instrument was disbelieved for being right.
		var remove []string
		for _, l := range has {
			if l != want {
				remove = append(remove, l)
			}
		}
		return []Defect{{
			Ref: n.Ref,
			What: fmt.Sprintf("records %s but sits at level %q, which is %s",
				strings.Join(has, ", "), levelNameAt(s, n.Level), want),
			Read: evidence,
			Why:  "the label and the tree disagree about what this is; two sources of truth is the defect, and a reader has no way to tell which one is stale",
			Fix: fmt.Sprintf("decide which is right, then either re-parent it or: gh issue edit %d --repo %s --remove-label %s --add-label %s",
				n.Ref.Number, n.Ref.OwnerRepo(), strings.Join(remove, " --remove-label "), want),
		}}
	}
}

// labelList renders a node's labels for an evidence line, saying "none" rather
// than printing an empty string -- because "read: labels " and "read: labels
// (nothing came back)" are the two cases this whole change exists to keep
// apart.
func labelList(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

// structural holds only where levels are declared. Like universal it returns
// findings and could-not-looks separately.
func structural(n *Node, s *routing.Structure, src Source) (defects, unread, acknowledged []Defect) {
	var out []Defect
	if n.Err != nil {
		return nil, nil, nil // universal already reported it as unread
	}

	// The node read fine and its POSITION could not be established. Two
	// different facts hide here and they get two messages, because one sentence
	// covering both is how a report ends up asserting something false about
	// whichever case it was not written for.
	if n.LevelErr != nil {
		var cyc *ParentCycleError
		if errors.As(n.LevelErr, &cyc) {
			// A record defect, not a failure. It is fully known and nameable,
			// so it STAYS A FINDING: the walk read everything it needed and the
			// tree really is malformed.
			return []Defect{{
				Ref: n.Ref,
				What: fmt.Sprintf("is inside a parent cycle: %s's parent is %s, which is already above it",
					cyc.Child, cyc.Ancestor),
				Read: fmt.Sprintf("climbed the ancestry of %s and reached %s twice", cyc.At, cyc.Ancestor),
				Why:  "an issue that is its own ancestor has no level, and any walk of its ancestry runs forever -- so nothing above it can be judged, by this report or any other",
				Fix: fmt.Sprintf("break it at the closing edge: re-parent %s away from %s",
					cyc.Child, cyc.Ancestor),
			}}, nil, nil
		}
		// A read that did not answer. Nothing is known, and saying more than
		// that would be inventing it -- WHICH IS WHY IT IS NOT A FINDING. This
		// is the case measured live under GraphQL exhaustion: an issue's parent
		// has no REST endpoint, so the level climb is the one GraphQL call left
		// in a walk, and it is the first thing an exhausted budget takes away.
		return nil, []Defect{{
			Ref:  n.Ref,
			What: "its position in the tree could not be established: " + n.LevelErr.Error(),
			Why:  "the node itself was read; its ancestry was not, and that is where a level comes from -- so the tree below is shown but nothing here says whether it sits in the right place",
			Fix:  "retry, or check this credential can read the repositories its parents live in -- a parent routinely lives in another repo",
		}}, nil
	}

	if !n.LevelKnown {
		return out, nil, nil
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
			// DELIBERATELY STILL A FINDING, and the boundary is worth stating.
			// Everything WAS read here; what is inconclusive is the judgement,
			// because the walk began below whatever is misplaced. That is a
			// different fact from a read that did not happen, and widening the
			// third value to cover it would be a second change wearing this
			// one's clothes.
			return out, nil, nil
		}

		// Report the expectation the assignment ACTUALLY USED, recorded on the
		// node at walk time. Re-deriving it here -- from depth, or even from
		// the parent's rung -- produces a message naming levels the node was
		// never judged against, whenever a rung above was skipped or the
		// parent's shape narrowed what may sit below it.
		cands := n.candidates
		if cands == nil {
			cands = s.ChildLevels(n.ParentLevel)
		}
		var want []string
		for _, i := range cands {
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
			Read: fmt.Sprintf("labels %s; parent sits at %q, judged against %s",
				labelList(n.Labels), levelNameAt(s, n.ParentLevel), describeLevels(s, cands)),
			Why: "a node at the wrong level collapses the ones above it, and every individual edge still looks reasonable while it happens",
			Fix: "re-parent it, correct its title or repo, or declare the level it belongs to",
		})
		return out, nil, nil
	}

	// A rung whose purpose is to hold others, holding nothing.
	//
	// THE OPEN CASE IS THE EXPENSIVE ONE AND WAS NEVER CHECKED. A closed empty
	// node is a loss already taken. An OPEN empty node is offered by every
	// planner and every readiness query as if it were work, so it costs
	// somebody's attention every time the board is read -- and `facet tree wire`
	// manufactures them by correct behaviour, since the level is assigned by
	// POSITION: a leaf wired straight onto a holder is recorded as the rung
	// between them, and nobody chooses that (facet#146).
	if lvl := s.Levels[n.Level]; lvl.RequiresChildren.Demands(n.IsClosed()) && len(n.Children) == 0 {
		what := fmt.Sprintf("is an open %s with no children", lvl.Name)
		why := "this level exists to hold work, and an empty one is a level label with nothing under it -- offered by every planner as if it were work"
		fix := "wire its work under it, or close it if there is none"
		if n.IsClosed() {
			what = fmt.Sprintf("is a closed %s with no children", lvl.Name)
			why = "this level exists to hold the work it accounts for; closed and empty, whatever it covered can no longer be attributed to it"
			fix = "wire its work under it before closing, or record why there was none"
		}
		read := fmt.Sprintf("state %s, %d children read, level %q requires children %s",
			n.State, len(n.Children), lvl.Name, requirementPhrase(lvl.RequiresChildren))

		// THE THIRD VERDICT (facet#159): a closed holder with no children can
		// be the TRUTH -- a slate that was deferred, whose live blocks were
		// correctly re-homed elsewhere, should end up childless, and GitHub's
		// one-parent-per-issue limit means that is not always fixable by
		// re-parenting. acknowledgedLabel alone is not enough: a bare label is
		// a claim nobody re-checks, so it is verified against a reason
		// recorded ON THE ISSUE, fetched only for this one candidate.
		if n.IsClosed() && hasLabel(n.Labels, acknowledgedLabel) {
			switch reason, err := acknowledgedReason(n.Ref, src); {
			case err != nil:
				// COULD NOT VERIFY IS NOT ACKNOWLEDGED. A label that cannot be
				// checked must not suppress anything -- that would let a read
				// failure quietly turn into "no defect here", the same
				// pass-by-absence class this whole report exists to refuse.
				out = append(out, Defect{
					Ref:  n.Ref,
					What: what + fmt.Sprintf(" and carries %s, but its reason could not be verified: %s", acknowledgedLabel, err),
					Read: read,
					Why:  why + " -- and a suppression that cannot be checked is not a suppression",
					Fix:  fmt.Sprintf("retry, or check this credential can read %s", n.Ref),
				})
			case reason != "":
				return out, nil, []Defect{{
					Ref:  n.Ref,
					What: what,
					Read: read + fmt.Sprintf("; %s present, reason: %q", acknowledgedLabel, reason),
					Why:  "acknowledged with a reason on the record -- a legitimate terminal state, not a loss",
					Fix:  "",
				}}
			default:
				// The label is set but no reason section was found. Reported
				// as its OWN defect, distinct from the ordinary childless
				// one, so the fix a reader gets is "write the reason", not
				// "wire work under it" -- the label already says there is
				// none to wire.
				out = append(out, Defect{
					Ref:  n.Ref,
					What: what + fmt.Sprintf(" and carries %s with NO reason recorded", acknowledgedLabel),
					Read: read,
					Why:  "a bare acknowledged label is a claim nobody re-checks, and this report requires a reason before it will suppress anything",
					Fix:  fmt.Sprintf("add a %q section to the issue body naming why it is legitimately childless", acknowledgedHeading),
				})
			}
			return out, nil, nil
		}

		out = append(out, Defect{
			Ref:  n.Ref,
			What: what,
			// The evidence carries WHICH requirement fired, because "0 children"
			// alone does not say whether this rung is checked once closed or at
			// all times -- and those are the two states a reader has to tell
			// apart to know whether the report is about a loss or about work
			// standing empty on the board.
			Read: read,
			Why:  why,
			Fix:  fix,
		})
	}
	return out, nil, nil
}

// acknowledgedLabel is the label [DoctorWithSource] honours for the third
// verdict (facet#159). The prefix matches the shape this repo's other level
// labels already use (type/block, type/work); the name is this feature's own
// choice.
const acknowledgedLabel = "tree/acknowledged"

// acknowledgedHeading is the section a reason must be written under. Level
// and case insensitive, like every other section this bottega's tools parse.
const acknowledgedHeading = "acknowledged"

// hasLabel reports whether name is among labels.
func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

// acknowledgedReason fetches ref's body (only ever called for a node already
// carrying acknowledgedLabel) and returns the text under an "Acknowledged"
// heading. Empty, nil means the label is set but no reason was written --
// distinct from a non-nil error, which means the check could not run at all.
//
// src == nil is not an error: it means the caller did not wire a source
// ([Doctor] itself never does), and an unverifiable label is reported exactly
// like an unfound reason -- the difference between the two is stated in the
// caller's own two branches, not folded together here.
func acknowledgedReason(ref ghx.IssueRef, src Source) (string, error) {
	if src == nil {
		return "", nil
	}
	issue, err := src.ViewIssue(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return "", err
	}
	return sectionBody(issue.Body, acknowledgedHeading), nil
}

// sectionBody returns the non-empty text under a markdown heading matching
// name (case insensitive), up to the next heading of any level or the end of
// the body. Whitespace-only content between headings reads as no reason,
// which is the same rule [structural]'s caller acts on: a section present but
// empty is not a reason recorded.
func sectionBody(body, name string) string {
	lines := strings.Split(body, "\n")
	var found bool
	var b strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			if found {
				break // the next heading of any level ends the section
			}
			if heading == strings.ToLower(name) {
				found = true
			}
			continue
		}
		if found {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// requirementPhrase renders a level's child requirement for an evidence line,
// so a reader can tell "this rung is checked once closed" from "this rung is
// checked at all times" without opening the routing file.
func requirementPhrase(c routing.ChildRequirement) string {
	if c == routing.ChildrenRequiredAlways {
		return "at all times"
	}
	return "once closed"
}

// describeLevels names the rungs a node was judged against, for an evidence
// line. "nothing" rather than an empty string: a parent that permits nothing
// below it is a real answer and must not print as a blank.
func describeLevels(s *routing.Structure, levels []int) string {
	if len(levels) == 0 {
		return "nothing -- that rung permits no children"
	}
	out := make([]string, 0, len(levels))
	for _, i := range levels {
		out = append(out, levelNameAt(s, i))
	}
	return strings.Join(out, " or ")
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
		want, ok := route.Structure.LabelFor(n.Level, route.KeyForRepo(n.Ref.OwnerRepo()), n.Title, n.Labels)
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
