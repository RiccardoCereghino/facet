package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// labelGH is what the parity check needs: read the definitions, and write one.
type labelGH interface {
	RepoLabels(repo string) ([]ghx.RepoLabel, error)
	CreateLabel(repo string, label ghx.RepoLabel) error
}

// defaultLabelColour is used only when NO routed repository already defines the
// label, so there is nothing to copy. GitHub's own default grey.
const defaultLabelColour = "ededed"

// labelStatus is one repository's answer.
type labelStatus struct {
	Repo string
	// Required is how many labels this repository could actually be asked for,
	// which is not the same number for every repository -- a repo-scoped shape
	// declares a label only its own repository can ever be given. Printed so
	// "ok" against a shorter list is visible rather than surprising.
	Required int
	Missing  []string
	Created  []string
	Err      error
}

// runTreeLabels reports, and optionally closes, the gap between the labels the
// routing file's structure DECLARES and the labels each repository DEFINES.
//
// THE REQUIRED SET IS NOT A LIST IN THIS BINARY, AND IT IS PER REPOSITORY.
// It is Structure.LabelsFor(repoKey) -- exactly what `tree wire` may need to
// apply THERE. Hardcoding the names would put facet's own opinion about a
// hierarchy into a tool that deliberately holds none, and would go stale the
// day a level is added. It also satisfies the safety requirement on --create
// for free: the only labels it can ever create are ones the routing file
// already names.
//
// The per-repository part is not a refinement. A label declared on a
// repo-scoped shape -- `{"repo": "stele", "label": "type/seat"}` -- is
// reachable in that repository and nowhere else, so demanding it everywhere
// reports a gap the structure itself says can never be used, and --create
// would then define a label no wire there could ever apply.
func runTreeLabels(w io.Writer, gh labelGH, route *routing.Routing, repos []string, create bool) error {
	if route == nil || route.Structure == nil {
		return fmt.Errorf("nothing declares which labels a repository must have: the routing file has no `structure` block\n" +
			"fix: declare the levels, or drop this check -- facet knows that SOME labels are required, never which ones")
	}
	declared := route.Structure.Labels()
	if len(declared) == 0 {
		return fmt.Errorf("the `structure` block declares no labels, so there is no parity to check\n" +
			"fix: give a level (or an accepted shape) a `label`, or drop this check")
	}
	sort.Strings(declared)

	// Read every repository FIRST, so a label that already exists somewhere can
	// be copied rather than invented. Parity means the same label, not merely a
	// label with the same name in a different colour in every repo.
	defined := map[string][]ghx.RepoLabel{}
	statuses := make([]labelStatus, 0, len(repos))
	for _, repo := range repos {
		st := labelStatus{Repo: repo}
		have, err := gh.RepoLabels(repo)
		if err != nil {
			st.Err = err
			statuses = append(statuses, st)
			continue
		}
		defined[repo] = have
		byName := map[string]bool{}
		for _, l := range have {
			byName[l.Name] = true
		}
		// What THIS repository could be asked for, not what the structure
		// declares in total.
		want := route.Structure.LabelsFor(route.KeyForRepo(repo))
		sort.Strings(want)
		st.Required = len(want)
		for _, name := range want {
			if !byName[name] {
				st.Missing = append(st.Missing, name)
			}
		}
		statuses = append(statuses, st)
	}

	if create {
		known := existingDefinitions(defined)
		for i := range statuses {
			createMissing(w, gh, &statuses[i], known)
		}
	}

	return writeLabelReport(w, statuses, declared, create)
}

// existingDefinitions picks one definition per label name from the
// repositories that already have it, so a created label matches its siblings.
//
// The MOST COMMON definition wins, and ties are broken by colour so the answer
// does not depend on map iteration order -- a check that creates a differently
// coloured label depending on which run it was is not a parity tool.
func existingDefinitions(defined map[string][]ghx.RepoLabel) map[string]ghx.RepoLabel {
	counts := map[string]map[ghx.RepoLabel]int{}
	for _, labels := range defined {
		for _, l := range labels {
			if counts[l.Name] == nil {
				counts[l.Name] = map[ghx.RepoLabel]int{}
			}
			counts[l.Name][l]++
		}
	}
	out := map[string]ghx.RepoLabel{}
	for name, variants := range counts {
		var best ghx.RepoLabel
		bestN := -1
		for l, n := range variants {
			if n > bestN || (n == bestN && l.Color < best.Color) {
				best, bestN = l, n
			}
		}
		out[name] = best
	}
	return out
}

// createMissing defines this repository's missing labels, recording what
// landed. A failure stops that repository and is reported; the rest continue,
// because one repository this credential cannot write to must not hide the
// state of the others.
func createMissing(w io.Writer, gh labelGH, st *labelStatus, known map[string]ghx.RepoLabel) {
	if st.Err != nil {
		return
	}
	remaining := st.Missing[:0:0]
	for _, name := range st.Missing {
		def, ok := known[name]
		if !ok {
			// No routed repository defines it, so there is nothing to copy.
			def = ghx.RepoLabel{Name: name, Color: defaultLabelColour}
		}
		if err := gh.CreateLabel(st.Repo, def); err != nil {
			st.Err = fmt.Errorf("creating %s: %w", name, err)
			remaining = append(remaining, name)
			continue
		}
		_, _ = fmt.Fprintf(w, "  created %-22s in %s\n", name, st.Repo)
		st.Created = append(st.Created, name)
	}
	st.Missing = remaining
}

// writeLabelReport prints a line per repository and fails if any gap is left.
//
// EVERY REPOSITORY GETS A LINE, including the ones with nothing wrong. The
// defect this closes was invisible precisely because the tooling only ever
// spoke up about the repository in front of it, and eleven repositories with
// four different answers looked fine one at a time.
func writeLabelReport(w io.Writer, statuses []labelStatus, declared []string, created bool) error {
	if created {
		_, _ = fmt.Fprintln(w)
	}
	_, _ = fmt.Fprintf(w, "declared by the routing file's structure: %s\n", strings.Join(declared, " "))
	_, _ = fmt.Fprintf(w, "  a label declared on a repo-scoped shape is required only in that repo,"+
		" so the count per row differs\n\n")

	var gaps, unreadable []string
	for _, st := range statuses {
		switch {
		case st.Err != nil:
			_, _ = fmt.Fprintf(w, "  %-40s COULD NOT CHECK: %s\n", st.Repo, st.Err)
			unreadable = append(unreadable, st.Repo)
		case len(st.Missing) > 0:
			_, _ = fmt.Fprintf(w, "  %-40s MISSING %s\n", st.Repo, strings.Join(st.Missing, " "))
			gaps = append(gaps, st.Repo)
		case len(st.Created) > 0:
			_, _ = fmt.Fprintf(w, "  %-40s ok (%d of %d created)\n", st.Repo, len(st.Created), st.Required)
		default:
			_, _ = fmt.Fprintf(w, "  %-40s ok (%d required)\n", st.Repo, st.Required)
		}
	}
	_, _ = fmt.Fprintln(w)

	switch {
	case len(gaps) == 0 && len(unreadable) == 0:
		_, _ = fmt.Fprintf(w, "every repository defines every label it can be asked for\n")
		return nil
	case len(unreadable) > 0 && len(gaps) == 0:
		// NOTHING WAS FOUND AND NOTHING WAS RULED OUT: this is `tree doctor`'s
		// exit 2, the same fact in the same tool. Two verbs answering "I could
		// not look" with two different codes is the inconsistency facet#138
		// exists to remove.
		return withCode(exitCantLook, fmt.Errorf("could not check %d of %d %s: %s\n"+
			"  %s labels are neither reported nor ruled out",
			len(unreadable), len(statuses), plural(len(unreadable), "repository", "repositories"),
			strings.Join(unreadable, ", "), plural(len(unreadable), "its", "their")))
	}
	// The fix line is the command itself, because a report of a gap that is
	// mechanically closable and does not say so teaches people to close it by
	// hand, one repository at a time -- which is how the gap got here.
	msg := fmt.Sprintf("%d of %d %s %s a declared label: %s\n"+
		"  a wire into one of these records the edge and NOT the level\n"+
		"fix: facet tree labels --create",
		len(gaps), len(statuses), plural(len(gaps), "repository", "repositories"),
		plural(len(gaps), "is missing", "are missing"), strings.Join(gaps, ", "))
	if len(unreadable) > 0 {
		msg += fmt.Sprintf("\n  and %d could not be checked at all: %s",
			len(unreadable), strings.Join(unreadable, ", "))
	}
	// A gap FOUND is exit 1 even when some repository could not be read: there
	// is a real finding here, and the repositories that went unchecked are
	// named in the message rather than folded into the code.
	return fmt.Errorf("%s", msg)
}

// routedRepos lists every "owner/name" the routing file knows, sorted.
//
// Sweeping ALL of them is the default because the gap this closes is an estate
// property: eleven repositories, four different answers, and each one looked
// fine when checked alone. The two with the fewest labels were the two nobody
// had touched recently -- so a check aimed only at the repository in front of
// you is the check that already existed and already missed it.
func routedRepos(route *routing.Routing) []string {
	if route == nil {
		return nil
	}
	out := make([]string, 0, len(route.OwnerRepoToKey))
	for ownerRepo := range route.OwnerRepoToKey {
		out = append(out, ownerRepo)
	}
	sort.Strings(out)
	return out
}
