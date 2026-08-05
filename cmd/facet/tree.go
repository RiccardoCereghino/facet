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
		newTreeStatusCmd(), newTreeDoctorCmd())
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
			"contract facet does not hold. An issue with no parent is never a defect.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runTreeDoctor(cmd.OutOrStdout(), gh, ref, fixLabels)
		},
	}
	cmd.Flags().BoolVar(&fixLabels, "fix-labels", false,
		"record the level on every node missing its type label; never touches one that CONTRADICTS the tree")
	return cmd
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
