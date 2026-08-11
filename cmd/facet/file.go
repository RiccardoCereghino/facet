package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/spf13/cobra"
)

func newFileCmd() *cobra.Command {
	var (
		repo     string
		title    string
		bodyFile string
		labels   []string
		repos    []string
		force    bool
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "file",
		Short: "File a GitHub issue that satisfies the conventions",
		Long: "Searches for a duplicate first, checks the title and the labels against the\n" +
			"conventions in the routing file, records the repos the work touches, and only\n" +
			"then creates the issue.\n\n" +
			"Concurrent sessions file into the same repository and a duplicate has already\n" +
			"happened, so the search is not optional -- `--force` files anyway, and says so.\n\n" +
			"Do not pass a `bug` or `enhancement` label expecting a type: apply the label and\n" +
			"let the intake workflow convert it. `gh` has no --type flag to give us.\n\n" +
			"The body comes from a file, never an argument, exactly like `facet comment\n" +
			"post` (facet#108): backticks in a shell argument run as command substitution\n" +
			"and silently eat the text around them, which has mangled filed issues more\n" +
			"than once -- an --body flag existed here until this was found, and was the\n" +
			"one place in this CLI still offering it.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runFile(fileOpts{
				Repo: repo, Title: title, BodyFile: bodyFile,
				Labels: labels, Repos: repos, Force: force, DryRun: dryRun,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the repository to file in, as owner/name -- where the BRANCH will land (required)")
	f.StringVar(&title, "title", "", "issue title: `component: imperative statement` (required)")
	f.StringVar(&bodyFile, "body-file", "", "file holding the issue body; - for stdin (required)")
	f.StringSliceVar(&labels, "label", nil, "labels to apply (repeatable)")
	f.StringSliceVar(&repos, "repos", nil, "every repo the work touches, as routing keys; recorded in the body")
	f.BoolVar(&force, "force", false, "file even when a similar issue already exists")
	f.BoolVar(&dryRun, "dry-run", false, "run the checks and the duplicate search, create nothing")
	return cmd
}

type fileOpts struct {
	Repo, Title, BodyFile string
	Labels, Repos         []string
	Force, DryRun         bool
}

func runFile(o fileOpts) error {
	if o.Repo == "" {
		return fmt.Errorf("--repo is required (owner/name)")
	}
	if o.Title == "" {
		return fmt.Errorf("--title is required")
	}

	route, err := routing.Load(roots.Routing)
	if err != nil {
		return err
	}
	if route.KeyForRepo(o.Repo) == "" {
		return fmt.Errorf("%s is not in %s's ownerRepoToKey", o.Repo, roots.Routing)
	}
	for _, k := range o.Repos {
		if _, ok := route.Repos[k]; !ok {
			return fmt.Errorf("--repos %q is not a repo key in %s", k, roots.Routing)
		}
	}

	body, err := readBodyFile(o.BodyFile, "issue")
	if err != nil {
		return err
	}

	// Every violation at once. An agent that has to rediscover one rule per
	// attempt will give up and file a bare issue instead.
	if err := route.Conventions.Check(o.Title, o.Labels); err != nil {
		return fmt.Errorf("this issue does not satisfy the conventions:\n%w", err)
	}

	// The repo set is the author's answer to the question `facet spawn` would
	// otherwise have to guess. Recording it here means the first spawn is exact.
	if len(o.Repos) > 0 {
		body, _ = routing.UpsertScope(body, o.Repos)
	}

	terms := routing.SearchTerms(o.Title)
	dupes, err := gh.SearchIssues(o.Repo, terms)
	if err != nil {
		// A search that cannot run must not silently become a search that found
		// nothing. Refuse, unless the caller has already accepted the risk.
		if !o.Force {
			return fmt.Errorf("duplicate search failed (%w); pass --force to file anyway", err)
		}
		fmt.Fprintf(os.Stderr, "! duplicate search failed: %v\n", err)
	}
	if len(dupes) > 0 {
		fmt.Printf("similar issues already in %s (searched title for %q):\n", o.Repo, terms)
		for _, d := range dupes {
			fmt.Printf("  #%-5d %-6s %s\n", d.Number, strings.ToLower(d.State), d.Title)
		}
		if !o.Force {
			return fmt.Errorf("not filing: one of the above may be the same issue -- pass --force if it is not")
		}
		fmt.Println("--force: filing anyway.")
	}

	if o.DryRun {
		fmt.Printf("\n--dry-run: nothing was created.\n\n%s\n\n%s\n", o.Title, body)
		return nil
	}

	url, err := gh.CreateIssue(o.Repo, o.Title, body, o.Labels)
	if err != nil {
		return err
	}
	fmt.Println(url)

	createBlockedByEdges(route, o.Repo, url, body)
	return nil
}

// createBlockedByEdges creates a native GitHub issue-dependency edge for every
// resolvable reference in the new issue's "Blocked by / waiting on" section.
// An edge that cannot be created -- a cross-owner ref, one the API rejects, or
// one it cannot resolve -- is reported and skipped: the issue is already
// filed, and a missing edge is not worth losing that over.
// It takes the routing table because a repo shorthand -- `harness#121`, the
// dominant form in these bodies -- can only be resolved against `repos`
// (facet#104). Without it the reference is silently not a reference, and the
// dependency exists only as prose.
func createBlockedByEdges(route *routing.Routing, repo, issueURL, body string) {
	refs := route.ParseBlockedBy(body)
	if len(refs) == 0 {
		return
	}
	number, err := issueNumber(issueURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "! could not parse issue number from %s, skipping blocked-by edges: %v\n", issueURL, err)
		return
	}
	owner := strings.SplitN(repo, "/", 2)[0]
	seen := map[string]bool{} // "target#number", after resolving a bare ref to its repo

	for _, ref := range refs {
		target := repo
		label := fmt.Sprintf("#%d", ref.Number)
		if ref.OwnerRepo != "" {
			target = ref.OwnerRepo
			label = target + label
			if refOwner := strings.SplitN(target, "/", 2)[0]; !strings.EqualFold(refOwner, owner) {
				fmt.Fprintf(os.Stderr, "! blocked-by %s: cross-owner refs are not supported, skipping\n", label)
				continue
			}
		}
		// `#42` and `acme/gateway#42` name the same issue once ref.OwnerRepo
		// is resolved against repo -- dedupe here, where both spellings
		// converge on one identity, rather than on the raw parsed ref.
		key := fmt.Sprintf("%s#%d", target, ref.Number)
		if seen[key] {
			continue
		}
		seen[key] = true

		id, err := gh.IssueID(target, ref.Number)
		if err != nil {
			fmt.Fprintf(os.Stderr, "! blocked-by %s: could not resolve, skipping: %v\n", label, err)
			continue
		}
		if err := gh.AddBlockedBy(repo, number, id); err != nil {
			fmt.Fprintf(os.Stderr, "! blocked-by %s: could not create edge, skipping: %v\n", label, err)
			continue
		}
		fmt.Printf("blocked by %s\n", label)
	}
}

// issueNumber pulls the trailing number off an issue URL, e.g.
// https://github.com/acme/gateway/issues/42 -> 42.
func issueNumber(issueURL string) (int, error) {
	i := strings.LastIndex(issueURL, "/")
	if i < 0 {
		return 0, fmt.Errorf("no / in %q", issueURL)
	}
	return strconv.Atoi(issueURL[i+1:])
}
