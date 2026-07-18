package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/spf13/cobra"
)

func newFileCmd() *cobra.Command {
	var (
		repo     string
		title    string
		body     string
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
			"let the intake workflow convert it. `gh` has no --type flag to give us.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFile(fileOpts{
				Repo: repo, Title: title, Body: body, BodyFile: bodyFile,
				Labels: labels, Repos: repos, Force: force, DryRun: dryRun,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the repository to file in, as owner/name -- where the BRANCH will land (required)")
	f.StringVar(&title, "title", "", "issue title: `component: imperative statement` (required)")
	f.StringVar(&body, "body", "", "issue body")
	f.StringVar(&bodyFile, "body-file", "", "read the body from a file, or - for stdin")
	f.StringSliceVar(&labels, "label", nil, "labels to apply (repeatable)")
	f.StringSliceVar(&repos, "repos", nil, "every repo the work touches, as routing keys; recorded in the body")
	f.BoolVar(&force, "force", false, "file even when a similar issue already exists")
	f.BoolVar(&dryRun, "dry-run", false, "run the checks and the duplicate search, create nothing")
	return cmd
}

type fileOpts struct {
	Repo, Title, Body, BodyFile string
	Labels, Repos               []string
	Force, DryRun               bool
}

func runFile(o fileOpts) error {
	if o.Repo == "" {
		return fmt.Errorf("--repo is required (owner/name)")
	}
	if o.Title == "" {
		return fmt.Errorf("--title is required")
	}
	if o.Body != "" && o.BodyFile != "" {
		return fmt.Errorf("pass --body or --body-file, not both")
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

	body, err := readBody(o)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("an issue with no body is a note to nobody: pass --body or --body-file")
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
	return nil
}

func readBody(o fileOpts) (string, error) {
	switch {
	case o.BodyFile == "-":
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	case o.BodyFile != "":
		b, err := os.ReadFile(o.BodyFile)
		return string(b), err
	default:
		return o.Body, nil
	}
}
