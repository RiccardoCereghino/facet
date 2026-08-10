package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/seat"
)

// newTreeCmd groups the commands that read and write GitHub's sub-issue graph.
//
// The hierarchy any of this describes belongs to whoever configures it, not to
// facet. Every command here works on a tree facet never built, none of them is
// a precondition of another, and `facet spawn` neither knows nor cares whether
// an issue has a parent. An issue with no parent is a valid issue.
func newTreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Read and write the issue hierarchy",
		Long: "GitHub's sub-issue graph, read and written across repositories.\n\n" +
			"An issue tree is optional. facet has no opinion about whether issues are\n" +
			"arranged in a hierarchy at all, and `doctor` reports a shape violation only\n" +
			"where the routing file declares the levels to expect -- otherwise it checks\n" +
			"just the things that are wrong on any tree's own terms. A parentless issue\n" +
			"is never reported.",
	}
	cmd.AddCommand(newTreeWireCmd(), newTreeShowCmd(), newTreeListCmd(),
		newTreeStatusCmd(), newTreeDoctorCmd(), newTreeOrphansCmd(), newTreeLabelsCmd())
	return cmd
}

// newTreeOrphansCmd is the only command here that reads the complement.
//
// Every other one takes an issue and walks DOWN from it, so none of them can
// answer "what is under no root at all" -- an issue with no parent is by
// definition not below any node you could name. Answering it by hand means
// listing a repository's open issues and checking each one's parent, which is
// how nine unparented issues in one repository went unnoticed until someone
// thought to look (facet#116).
func newTreeOrphansCmd() *cobra.Command {
	var repos []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "orphans --repo <owner/name> [--repo <owner/name> …]",
		Short: "Open issues that hang under nothing",
		Long: "Lists the open issues in each named repository that have no parent.\n\n" +
			"IT IS A QUESTION, NOT A VERDICT. An issue with no parent is a perfectly\n" +
			"valid issue -- facet has no opinion about whether issues are arranged in a\n" +
			"hierarchy at all, and plenty of them are deliberately outside one. This\n" +
			"reports the set; deciding which of them is a gap is triage's job.\n\n" +
			"So FINDING ORPHANS IS EXIT 0. Exit 1 means a repository could not be read,\n" +
			"which is a different fact and the one worth failing on: silence about a\n" +
			"repository nobody could list would read as \"nothing unparented there\".\n\n" +
			"It is repo-scoped rather than a check inside `doctor` because `doctor`\n" +
			"takes a root, and an orphan is not under one.\n\n" +
			"--json, because the consumer is as likely to be a scheduled planner as a\n" +
			"human.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(repos) == 0 {
				return fmt.Errorf("no repository named\nfix: facet tree orphans --repo owner/name")
			}
			return runTreeOrphans(cmd.OutOrStdout(), gh, repos, asJSON)
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil,
		"a repository to scan, as owner/name; repeat for several")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON rather than lines")
	return cmd
}

// newTreeLabelsCmd asserts that every routed repository DEFINES the labels the
// structure declares.
//
// It is the parity nothing checked: `wire` records a node's level by applying a
// label, and applying a label a repository has never defined fails. Eleven
// repositories in the setup that produced facet#139 carried four different
// label sets, and each of them looked fine when checked on its own.
func newTreeLabelsCmd() *cobra.Command {
	var repos []string
	var create bool
	cmd := &cobra.Command{
		Use:   "labels [--repo <owner/name> …] [--create]",
		Short: "Check every repository defines the labels the structure declares",
		Long: "`wire` records the level it enforced by applying a label. A repository\n" +
			"that never DEFINED that label cannot be given it -- so the edge lands,\n" +
			"the level does not, and the tree gains a node whose level is unknown.\n\n" +
			"This asserts the parity. WITH NO --repo IT SWEEPS EVERY REPOSITORY IN\n" +
			"THE ROUTING FILE, because the gap is an estate property: the repositories\n" +
			"that are short are the ones nobody has touched recently, so a check aimed\n" +
			"at the repository in front of you is the check that already missed it.\n\n" +
			"The required set is whatever the `structure` block declares -- exactly\n" +
			"what `wire` may need to apply there, never a list inside facet. IT IS\n" +
			"PER REPOSITORY: a label declared on a repo-scoped shape is reachable in\n" +
			"that repository and nowhere else, so requiring it everywhere would\n" +
			"report a gap the structure itself says can never be used.\n\n" +
			"--create defines the missing ones, copying the definition from a routed\n" +
			"repository that already has it so the colour and description match. It\n" +
			"can only ever create a label the routing file already names for that\n" +
			"repository, so a typo cannot bring one into existence.\n\n" +
			"EXIT CODES, the same three `tree doctor` uses:\n" +
			"  0  every repository defines every label it can be asked for\n" +
			"  1  looked, and something is missing\n" +
			"  2  could NOT look -- no repository could be read, so nothing is\n" +
			"     reported and nothing is ruled out\n\n" +
			"A gap FOUND is 1 even when some repository could not be read: there is a\n" +
			"real finding, and the unchecked repositories are named in the message.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			route, err := loadRouting()
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				repos = routedRepos(route)
			}
			if len(repos) == 0 {
				return fmt.Errorf("no repository to check: the routing file maps none, and none was named\n" +
					"fix: facet tree labels --repo owner/name")
			}
			return runTreeLabels(cmd.OutOrStdout(), gh, route, repos, create)
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil,
		"a repository to check, as owner/name; repeat for several. Default: every repo in the routing file")
	cmd.Flags().BoolVar(&create, "create", false,
		"define the missing labels, only ever from the set the structure declares")
	return cmd
}

func newTreeWireCmd() *cobra.Command {
	var parent string
	cmd := &cobra.Command{
		Use:   "wire <owner/repo#n> --parent <owner/repo#n>",
		Short: "Make an issue a child of another",
		Long: "Creates the sub-issue edge, across repositories if need be.\n\n" +
			"An issue has exactly one parent, so wiring one that already has a parent\n" +
			"MOVES it: the existing edge is detached first, then the new one is\n" +
			"attached. GitHub has no atomic move, so this is two calls, and it prints\n" +
			"the previous parent when there was one so the detach is visible.\n\n" +
			"It also prints both tiers. A parent's complexity is an at-a-glance worst\n" +
			"case for the grouping and never an input to the child's merge authority --\n" +
			"stated on every wire because an edge that quietly changed who may merge\n" +
			"would look exactly like filing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if parent == "" {
				return fmt.Errorf("a parent is required\nfix: facet tree wire %s --parent owner/repo#123", args[0])
			}
			child, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			p, err := parseIssueRef(parent)
			if err != nil {
				return err
			}
			return runTreeWire(cmd.OutOrStdout(), gh, child, p)
		},
	}
	cmd.Flags().StringVar(&parent, "parent", "", "the issue to attach it to, as owner/repo#n")
	return cmd
}

func newTreeShowCmd() *cobra.Command {
	var depth int
	cmd := &cobra.Command{
		Use:   "show <owner/repo#n>",
		Short: "Render the tree below an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runTreeShow(cmd.OutOrStdout(), gh, ref, depth)
		},
	}
	cmd.Flags().IntVar(&depth, "depth", -1, "how many levels below the root to read; -1 for all")
	return cmd
}

func newTreeListCmd() *cobra.Command {
	var level string
	cmd := &cobra.Command{
		Use:   "list <owner/repo#n>",
		Short: "List the issues below one, flat",
		Long: "The same walk as `show`, without the shape -- for piping.\n\n" +
			"--level filters to one declared level, and needs a structure block in the\n" +
			"routing file to mean anything.\n\n" +
			"A level is assigned by matching a node against the rungs its parent's level\n" +
			"permits, and the shallowest match wins. So where a skippable rung is left\n" +
			"unconstrained it absorbs everything below it, and --level then returns a\n" +
			"different set than the level's name suggests: the rung above holds a mixture,\n" +
			"and the rung below returns only what sits deeper still. Constrain the\n" +
			"skippable rung if the distinction matters to the caller.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runTreeList(cmd.OutOrStdout(), gh, ref, level)
		},
	}
	cmd.Flags().StringVar(&level, "level", "", "only issues at this declared level")
	return cmd
}

func newTreeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <owner/repo#n>",
		Short: "How far the work below an issue has got",
		Long: "Counts the issues below one by their board status.\n\n" +
			"A GitHub issue is open or closed and nothing else -- \"in progress\" exists\n" +
			"only as a field on a Projects v2 item. So this needs a `project` block in\n" +
			"the routing file, and says so rather than reporting an unconfigured tree as\n" +
			"an untouched one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runTreeStatus(cmd.OutOrStdout(), gh, ref)
		},
	}
}

func newTreeDoctorCmd() *cobra.Command {
	var fixLabels bool
	cmd := &cobra.Command{
		Use:   "doctor <owner/repo#n>",
		Short: "Report defects in a tree's shape",
		Long: "Two sets of checks.\n\n" +
			"Always: a cycle, a node that cannot be read, and a closed parent with open\n" +
			"children -- all wrong on any tree's own terms.\n\n" +
			"Only with a `structure` block in the routing file: levels. Without one,\n" +
			"nothing about shape is reported, because which shape is right is a\n" +
			"contract facet does not hold. An issue with no parent is never a defect.\n\n" +
			"EXIT CODES, and they are the point of reading this:\n" +
			"  0  looked, and the tree is clean\n" +
			"  1  looked, and here are the defects\n" +
			"  2  could NOT look -- a malformed reference, an issue that does not exist,\n" +
			"     an HTTP error, an unreadable routing file\n\n" +
			"A caller must not read 2 as a finding. Only the failure path moved, so\n" +
			"anything treating non-zero as \"not clean\" keeps working unchanged.",
		Args: exactArgsOrCantLook(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				// Refusing the argument is the clearest case of not having
				// looked: nothing was read, so there is nothing to report.
				return withCode(exitCantLook, err)
			}
			return runTreeDoctor(cmd.OutOrStdout(), gh, ref, fixLabels)
		},
	}
	cmd.Flags().BoolVar(&fixLabels, "fix-labels", false,
		"record the level on every node missing its type label; never touches one that CONTRADICTS the tree")
	// Every way of failing before the walk gets the same answer, including the
	// ones cobra produces rather than this file: a mistyped flag, and a
	// configuration that could not be read. Left untagged they would default to
	// 1 and claim a finding.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return withCode(exitCantLook, err)
	})
	cmd.PersistentPreRunE = func(*cobra.Command, []string) error {
		return withCode(exitCantLook, loadRoots())
	}
	return cmd
}

// exactArgsOrCantLook is cobra.ExactArgs with the exit code a checker owes:
// being handed the wrong number of arguments is not a finding about any tree.
func exactArgsOrCantLook(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return withCode(exitCantLook, cobra.ExactArgs(n)(cmd, args))
	}
}

// parseIssueRef reads "owner/repo#n".
//
// The validation is seat.ParseRef's, reused rather than written again so the
// two cannot drift on what a well-formed reference is. Only the sentence is
// this command's own: that function's wording names a scope file, which would
// send someone reading a `tree` refusal to look at entirely the wrong thing.
func parseIssueRef(s string) (ghx.IssueRef, error) {
	r, err := seat.ParseRef(s)
	if err != nil {
		return ghx.IssueRef{}, fmt.Errorf("%q is not an issue reference\nfix: write it as owner/repo#123", s)
	}
	owner, name, _ := strings.Cut(r.Repo, "/")
	return ghx.IssueRef{Owner: owner, Repo: name, Number: r.Number}, nil
}
