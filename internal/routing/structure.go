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
	Label string `json:"label,omitempty"`
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
	have := make(map[string]bool, len(labels))
	for _, l := range labels {
		have[l] = true
	}
	for _, i := range s.ChildLevels(parentLevel) {
		lvl := s.Levels[i]
		labelled := lvl.Label != "" && have[lvl.Label]
		if !labelled {
			for _, m := range lvl.Accepts {
				if m.Label != "" && have[m.Label] {
					labelled = true
					break
				}
			}
		}
		if labelled || lvl.accepts(repoKey, title) {
			return i, true
		}
	}
	return 0, false
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
// ok is false when no declared level's label is present. ambiguous is true
// when labels for more than one DIFFERENT level are present at once -- a real
// data conflict the caller must not silently resolve by picking one.
func (s *Structure) LevelForLabels(labels []string) (level int, ok bool, ambiguous bool) {
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
func (s *Structure) LabelFor(level int, repoKey, title string) (string, bool) {
	if s == nil || level < 0 || level >= len(s.Levels) {
		return "", false
	}
	l := s.Levels[level]
	for _, m := range l.Accepts {
		if m.Repo != "" && m.Repo != repoKey {
			continue
		}
		if m.TitlePattern != "" {
			re, err := regexp.Compile(m.TitlePattern)
			if err != nil || !re.MatchString(title) {
				continue
			}
		}
		if m.Label != "" {
			return m.Label, true
		}
		break
	}
	return l.Label, l.Label != ""
}

// Labels returns every label the structure can apply, so a caller can tell a
// level label apart from any other label an issue happens to carry. Without
// it, "the type/* label disagrees" could not be distinguished from "this issue
// has a label facet has never heard of".
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
func (l Level) accepts(repoKey, title string) bool {
	if len(l.Accepts) == 0 {
		return true
	}
	for _, m := range l.Accepts {
		if m.Repo != "" && m.Repo != repoKey {
			continue
		}
		if m.TitlePattern != "" {
			re, err := regexp.Compile(m.TitlePattern)
			// An uncompilable pattern cannot match. It is unreachable via
			// Load, which validates first, but a hand-built Structure in a
			// test must not silently match everything.
			if err != nil || !re.MatchString(title) {
				continue
			}
		}
		return true
	}
	return false
}

// Describe renders what a level expects, for a refusal that names the fix
// rather than only the failure.
func (l Level) Describe() string {
	if len(l.Accepts) == 0 {
		return l.Name + " (anything)"
	}
	parts := make([]string, 0, len(l.Accepts))
	for _, m := range l.Accepts {
		var part string
		switch {
		case m.Repo != "" && m.TitlePattern != "":
			part = fmt.Sprintf("%s matching %s", m.Repo, m.TitlePattern)
		case m.Repo != "":
			part = "anything in " + m.Repo
		case m.TitlePattern != "":
			part = "a title matching " + m.TitlePattern
		default:
			part = "anything"
		}
		if m.Label != "" {
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
