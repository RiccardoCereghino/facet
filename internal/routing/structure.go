package routing

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Structure describes the levels an issue tree is expected to have -- a
// commission holding seats holding blocks holding work, or whatever shape a
// particular routing file declares.
//
// IT IS ENTIRELY OPTIONAL, AND ITS ABSENCE IS NOT A DEGRADED STATE. facet
// spawns workspaces from issues; it has no opinion about whether those issues
// are arranged in a hierarchy. A routing file with no structure block gets no
// structure checks at all -- not lenient ones. Anyone adopting facet must be
// able to file an issue with no parent and never be told it is wrong, because
// the hierarchy is one organisation's contract rather than anything facet
// means by "issue".
//
// This is the same separation the rest of this file already makes: facet knows
// that *some* labels are required, never which ones.
type Structure struct {
	// Levels are ordered from the root down. Levels[0] describes the root of a
	// tree, Levels[1] its children, and so on.
	Levels []Level `json:"levels"`
}

// Level is one rung. A level with no Accepts admits anything, which is the
// right default for the rungs whose membership is obvious from position --
// there is nothing to check about "the work" beyond it being at the bottom.
type Level struct {
	// Name is what this rung is called in reports. Required.
	Name string `json:"name"`
	// Accepts lists alternative shapes a node at this level may take. A node
	// satisfies the level if it matches ANY of them.
	Accepts []LevelMatch `json:"accepts,omitempty"`
	// Optional lets a tree skip this rung: its parent's children may sit at
	// this level or at the next one down. It exists because a real hierarchy
	// has a rung that is sometimes not needed -- a bundle of one is just the
	// work -- and forcing an empty node in to satisfy a schema teaches people
	// to file placeholder issues.
	Optional bool `json:"optional,omitempty"`
	// Label is the label that RECORDS this level on the issue, e.g.
	// "type/block". Optional: a structure that declares none keeps working
	// exactly as before, because the level stays derivable from the title --
	// it just stays underivable by anything that is not facet.
	//
	// It exists because a level parsed out of a title prefix is invisible to
	// every other tool, and a retitled issue silently changes level. A label
	// is read by `gh issue list --label`, by a GraphQL filter, and by an actor
	// with no title-parsing rules at all.
	Label string `json:"label,omitempty"`
	// RequiresChildren marks a rung whose whole purpose is to hold others, so
	// that one closed with none is reported. That is a real loss rather than a
	// tidiness point: a record of who did some work, carrying nothing, means
	// the work it accounted for can no longer be attributed to it.
	RequiresChildren bool `json:"requiresChildren,omitempty"`
}

// LevelMatch is one accepted shape. An empty field does not constrain, so
// {"repo": "x"} means "anything in x" and {} means "anything at all".
type LevelMatch struct {
	// Repo is a key in the routing table's repos map, not an owner/name.
	Repo string `json:"repo,omitempty"`
	// TitlePattern is a Go regexp the issue title must match.
	TitlePattern string `json:"titlePattern,omitempty"`
	// Label overrides the level's own Label when THIS shape is what matched.
	//
	// It is needed because one rung can be spelled differently per repo: the
	// same "seat" level is `seat: …` in stele and `maquette: …` in
	// lab-workspaces, and those are recorded as type/seat and type/maquette.
	// Deriving the label from the level NAME could not express that.
	//
	// A LABEL WITH NO TitlePattern BESIDE IT IS THE TEST, NOT JUST THE RECORD.
	// With a title pattern present the two are alternatives -- either satisfies
	// the shape. With none, there is no other test left, and reading the label
	// as merely decorative would make the shape admit everything: an
	// unconstrained shape is how a rung silently absorbs the one below it.
	// Repo still scopes the shape either way.
	Label string `json:"label,omitempty"`
	// ChildMustBe names the one level a child may occupy when THIS shape is
	// what matched, overriding the optional-skipping ChildLevels would
	// otherwise allow. Empty means no narrowing, which is every shape that
	// does not ask for it.
	//
	// It exists because two shapes sharing a rung can legitimately want
	// different things below them. A rung holding either a live seat or a
	// triage grouping is the case that produced it: a seat may hold work
	// directly -- a bundle of one is just the work -- while a grouping filed
	// for later must have its work grouped into a bundle first, because
	// grouping is the whole reason that shape exists. Position alone cannot
	// tell the two apart, since position is exactly what they share.
	//
	// The narrowing must name a level the rung's children could already
	// occupy; validate refuses anything else, so this can only ever remove a
	// candidate and never introduce one.
	ChildMustBe string `json:"childMustBe,omitempty"`
}

// ChildLevels returns the level indices a child of a node at parentLevel may
// legitimately occupy: the next rung down, plus any rung reachable by skipping
// optional ones. Passing -1 asks what a root may be.
//
// The walk stops at the first REQUIRED level, because a required rung cannot
// be skipped -- that is what makes it required, and it is the property that
// catches a bundle filed directly under a commission with no seat between.
func (s *Structure) ChildLevels(parentLevel int) []int {
	if s == nil {
		return nil
	}
	var out []int
	for i := parentLevel + 1; i < len(s.Levels); i++ {
		out = append(out, i)
		if !s.Levels[i].Optional {
			break
		}
	}
	return out
}

// Assign works out which level a node sits at, given its parent's level. It
// returns the first candidate rung whose shape the node matches.
//
// A node that matches no candidate is the defect this whole type exists to
// catch: something at a depth where it does not belong. ok is false then, and
// the caller reports it against the candidates -- naming what was expected
// rather than only what was found.
//
// THE SHALLOWEST MATCH WINS, WHICH MATTERS WHEN A SKIPPABLE RUNG IS
// UNCONSTRAINED. A level with no Accepts admits anything, so if it is also
// Optional it absorbs everything that could have belonged to the rung below:
// work hanging directly off the rung above is named for the skippable level
// rather than the one under it. That is not a guess being made badly -- the
// declared structure genuinely does not distinguish the two, and facet will
// not invent a distinction its configuration does not express. It costs
// nothing in correctness (no defect is reported either way, and the rungs
// below stay right) and only shows up as a label in a report. Give the level
// an Accepts if the difference matters.
func (s *Structure) Assign(parentLevel int, repoKey, title string, labels []string) (level int, ok bool) {
	if s == nil {
		return 0, false
	}
	return s.AssignWithin(s.ChildLevels(parentLevel), repoKey, title, labels)
}

// AssignWithin is Assign against a candidate set the caller has already
// narrowed, which is what a caller holding the PARENT's identity can do and
// Assign cannot: ChildLevels knows only a rung index, so it cannot see that one
// shape sharing that rung permits less below it than another does.
//
// Assign remains the answer whenever the parent is just a level.
func (s *Structure) AssignWithin(candidates []int, repoKey, title string, labels []string) (level int, ok bool) {
	if s == nil {
		return 0, false
	}
	have := make(map[string]bool, len(labels))
	for _, l := range labels {
		have[l] = true
	}
	for _, i := range candidates {
		if i < 0 || i >= len(s.Levels) {
			continue
		}
		lvl := s.Levels[i]
		// The level's own label asserts the level directly, for a rung that
		// declares one without spelling out shapes.
		if lvl.Label != "" && have[lvl.Label] {
			return i, true
		}
		// Otherwise a shape decides. A label declared on a shape is scoped
		// exactly as tightly as that shape's title pattern -- the seat level's
		// type/seat is stele-only and type/maquette is lab-workspaces-only, the
		// same way "^seat: " only ever matched a stele title -- which is why
		// the repo check lives inside the shape rather than out here.
		if lvl.accepts(repoKey, title, have) {
			return i, true
		}
	}
	return 0, false
}

// ChildLevelsFor is ChildLevels narrowed by the shape the parent itself
// matched. A caller that knows only the parent's rung must use ChildLevels; a
// caller holding the parent's repo, title and labels should use this, because
// two shapes on one rung may permit different things below them.
//
// THE RESULT IS ALWAYS A SUBSET OF ChildLevels, and may be empty. It can only
// ever remove a candidate, never introduce one -- so a narrowing cannot smuggle
// in a rung the structure does not permit, and that holds without relying on
// validate() having run. validate() refuses a narrowing that names a rung the
// children may not occupy, so the empty case is unreachable through Load; it
// stays expressible here because failing closed is the only answer that keeps
// the promise this comment makes.
func (s *Structure) ChildLevelsFor(parentLevel int, repoKey, title string, labels []string) []int {
	if s == nil {
		return nil
	}
	out := s.ChildLevels(parentLevel)
	m, ok := s.matchedShape(parentLevel, repoKey, title, labels)
	if !ok || m.ChildMustBe == "" {
		return out
	}
	for _, i := range out {
		if s.Levels[i].Name == m.ChildMustBe {
			return []int{i}
		}
	}
	// The narrowing names a rung these children may not occupy. validate()
	// refuses that structure, so this is unreachable through Load -- but a
	// hand-built one must not be answered by WIDENING back to out, and must
	// not be answered by searching the whole ladder either: both would hand
	// back a rung this parent never permitted, which is the one thing the
	// invariant above promises cannot happen. Nothing is permitted instead.
	return nil
}

// matchedShape reports which of a level's accepted shapes a node satisfies, so
// a caller can read the properties that shape carries rather than the level's.
//
// A LABEL MATCH DECIDES BEFORE A TITLE MATCH, which is the precedence Assign
// already uses: a label names its shape exactly, where a title pattern is a
// guess about spelling. A level with no Accepts has no shape to report.
func (s *Structure) matchedShape(level int, repoKey, title string, labels []string) (LevelMatch, bool) {
	if s == nil || level < 0 || level >= len(s.Levels) {
		return LevelMatch{}, false
	}
	l := s.Levels[level]
	have := make(map[string]bool, len(labels))
	for _, v := range labels {
		have[v] = true
	}
	for _, m := range l.Accepts {
		if m.Repo != "" && m.Repo != repoKey {
			continue
		}
		if m.Label != "" && have[m.Label] {
			return m, true
		}
	}
	for _, m := range l.Accepts {
		if m.satisfiedBy(repoKey, title, have) {
			return m, true
		}
	}
	return LevelMatch{}, false
}

// LevelForLabels reports the level a node's own labels assert, independent of
// position or title. It exists for the case ChildLevels can never reach: a
// root's candidate set is always just the shallowest non-optional level (in
// practice "commission"), because that is the only rung reachable by walking
// forward from a hypothetical parent at -1 -- so no title convention, and no
// change to ChildLevels, could ever let a node like "block" be a root. A node
// carrying that level's own Label (or one of its Accepts' Labels) asserts it
// directly, out of band from position.
//
// repoKey scopes an Accepts entry's label exactly as tightly as its title
// pattern already is -- the seat level's type/seat is stele-only and
// type/maquette is lab-workspaces-only, and a label match must honour that
// the same way title matching always has.
//
// ok is false when no declared level's label is present. ambiguous is true
// when labels for more than one DIFFERENT level are present at once -- a real
// data conflict the caller must not silently resolve by picking one.
func (s *Structure) LevelForLabels(repoKey string, labels []string) (level int, ok bool, ambiguous bool) {
	if s == nil {
		return 0, false, false
	}
	have := make(map[string]bool, len(labels))
	for _, l := range labels {
		have[l] = true
	}
	matched := -1
	for i, lvl := range s.Levels {
		hit := lvl.Label != "" && have[lvl.Label]
		if !hit {
			for _, m := range lvl.Accepts {
				if m.Repo != "" && m.Repo != repoKey {
					continue
				}
				if m.Label != "" && have[m.Label] {
					hit = true
					break
				}
			}
		}
		if !hit {
			continue
		}
		if matched == -1 {
			matched = i
		} else if matched != i {
			return 0, false, true
		}
	}
	if matched == -1 {
		return 0, false, false
	}
	return matched, true, false
}

// LabelFor returns the label that records a node's level, and whether one is
// declared at all.
//
// The MATCHED shape decides: a level whose accepts carry their own labels is
// how one rung gets a different name per repo. A level with no accepts, or an
// accept with no label, falls back to the level's own.
func (s *Structure) LabelFor(level int, repoKey, title string, labels []string) (string, bool) {
	if s == nil || level < 0 || level >= len(s.Levels) {
		return "", false
	}
	l := s.Levels[level]
	if m, ok := s.matchedShape(level, repoKey, title, labels); ok && m.Label != "" {
		return m.Label, true
	}
	return l.Label, l.Label != ""
}

// LabelsFor returns the labels that can actually be applied to a node in
// repoKey -- which is a NARROWER question than [Structure.Labels], and the
// difference matters to anything that asks a repository to have them.
//
// A label declared on an accepted shape is scoped exactly as tightly as that
// shape: `{"repo": "stele", "label": "type/seat"}` means type/seat is
// reachable in stele and NOWHERE ELSE, because matchedShape skips a shape
// whose repo does not match. A level's OWN label carries no such scope and is
// reachable everywhere.
//
// Labels() is the RECOGNITION set: it answers "is this one of ours?", which is
// repo-independent by nature. This is the REQUIREMENT set: it answers "could a
// wire here ever need this?", which is not. Asking the recognition set the
// requirement question demands labels of a repository that the same structure
// forbids ever applying there.
//
// An empty repoKey -- a repository the routing table does not map -- yields
// only the unscoped labels, matching what a wire there could reach.
func (s *Structure) LabelsFor(repoKey string) []string {
	if s == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, l := range s.Levels {
		add(l.Label)
		for _, m := range l.Accepts {
			if m.Repo != "" && m.Repo != repoKey {
				continue
			}
			add(m.Label)
		}
	}
	return out
}

// Labels returns every label the structure can apply ANYWHERE, so a caller can
// tell a level label apart from any other label an issue happens to carry.
// Without it, "the type/* label disagrees" could not be distinguished from
// "this issue has a label facet has never heard of".
//
// It is the recognition set and is repo-independent. A caller asking what a
// particular repository must DEFINE wants [Structure.LabelsFor] instead.
func (s *Structure) Labels() []string {
	if s == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, l := range s.Levels {
		add(l.Label)
		for _, m := range l.Accepts {
			add(m.Label)
		}
	}
	return out
}

// accepts reports whether a node could be this level. Patterns are compiled
// here rather than cached because validate() has already proved every one of
// them compiles, and a tree walk is bounded by an API budget long before it is
// bounded by regexp compilation.
func (l Level) accepts(repoKey, title string, have map[string]bool) bool {
	if len(l.Accepts) == 0 {
		return true
	}
	for _, m := range l.Accepts {
		if !m.satisfiedBy(repoKey, title, have) {
			continue
		}
		return true
	}
	return false
}

// satisfiedBy reports whether one accepted shape admits a node.
//
// Repo scopes the shape. Then, of the two remaining tests, a shape uses
// whichever it declares: with both a title pattern and a label they are
// ALTERNATIVES, either sufficient; with only a label the label is the test;
// with neither the shape admits anything in scope, which is the documented
// meaning of {} and of {"repo": "x"}.
func (m LevelMatch) satisfiedBy(repoKey, title string, have map[string]bool) bool {
	if m.Repo != "" && m.Repo != repoKey {
		return false
	}
	if m.Label != "" && have[m.Label] {
		return true
	}
	if m.TitlePattern != "" {
		re, err := regexp.Compile(m.TitlePattern)
		// An uncompilable pattern cannot match. It is unreachable via Load,
		// which validates first, but a hand-built Structure in a test must not
		// silently match everything.
		return err == nil && re.MatchString(title)
	}
	// No title test left. A label declared with none is the test itself --
	// otherwise the shape would admit everything and the rung would absorb the
	// one below it.
	return m.Label == ""
}

// Describe renders what a level expects, for a refusal that names the fix
// rather than only the failure.
func (l Level) Describe() string {
	if len(l.Accepts) == 0 {
		return l.Name + " (anything)"
	}
	parts := make([]string, 0, len(l.Accepts))
	for _, m := range l.Accepts {
		// A shape with a label and NO title pattern is satisfied ONLY by that
		// label, so describing it as "anything" is a refusal telling the reader
		// the opposite of the rule it just enforced. With a pattern beside it
		// the two are alternatives, and "or labelled" is then the truth.
		labelIsTheTest := m.Label != "" && m.TitlePattern == ""
		var part string
		switch {
		case labelIsTheTest && m.Repo != "":
			part = fmt.Sprintf("labelled %s, in %s", m.Label, m.Repo)
		case labelIsTheTest:
			part = "labelled " + m.Label
		case m.Repo != "" && m.TitlePattern != "":
			part = fmt.Sprintf("%s matching %s", m.Repo, m.TitlePattern)
		case m.Repo != "":
			part = "anything in " + m.Repo
		case m.TitlePattern != "":
			part = "a title matching " + m.TitlePattern
		default:
			part = "anything"
		}
		if m.Label != "" && !labelIsTheTest {
			part += fmt.Sprintf(" (or labelled %s)", m.Label)
		}
		parts = append(parts, part)
	}
	return l.Name + " (" + strings.Join(parts, ", or ") + ")"
}

// validate is called from Routing.Validate. Like Conventions.validate it
// reports every problem at once: someone editing a routing file by hand should
// see the whole list, not rediscover one rule per attempt.
func (s *Structure) validate(repos map[string]Repo) error {
	if s == nil {
		return nil
	}
	var errs []error
	if len(s.Levels) == 0 {
		errs = append(errs, errors.New("structure: levels is empty; omit the block entirely to disable structure checks"))
	}
	for i, l := range s.Levels {
		if strings.TrimSpace(l.Name) == "" {
			errs = append(errs, fmt.Errorf("structure: levels[%d] has no name", i))
		}
		for j, m := range l.Accepts {
			if m.Repo != "" {
				if _, ok := repos[m.Repo]; !ok {
					errs = append(errs, fmt.Errorf(
						"structure: levels[%d].accepts[%d] names repo %q, which is not in repos", i, j, m.Repo))
				}
			}
			if m.TitlePattern != "" {
				if _, err := regexp.Compile(m.TitlePattern); err != nil {
					errs = append(errs, fmt.Errorf(
						"structure: levels[%d].accepts[%d] titlePattern %q: %w", i, j, m.TitlePattern, err))
				}
			}
			if m.ChildMustBe != "" {
				// A narrowing may only remove a candidate, never add one, so it
				// has to name a rung this level's children could already
				// occupy. Naming one they could not is the mistake that would
				// otherwise permit a shape the structure does not declare.
				var permitted []string
				found := false
				for _, c := range s.ChildLevels(i) {
					permitted = append(permitted, s.Levels[c].Name)
					if s.Levels[c].Name == m.ChildMustBe {
						found = true
					}
				}
				if !found {
					where := strings.Join(permitted, ", ")
					if where == "" {
						where = "nothing -- it is the deepest level"
					}
					errs = append(errs, fmt.Errorf(
						"structure: levels[%d].accepts[%d] childMustBe is %q, which a child of %q may not be; it may be: %s",
						i, j, m.ChildMustBe, l.Name, where))
				}
			}
		}
	}
	// A trailing optional level cannot be skipped onto anything, so it is
	// almost certainly a mistake rather than a permission.
	if n := len(s.Levels); n > 0 && s.Levels[n-1].Optional {
		errs = append(errs, fmt.Errorf(
			"structure: the last level %q is optional, which permits nothing -- there is no rung below it to skip to",
			s.Levels[n-1].Name))
	}
	return errors.Join(errs...)
}
