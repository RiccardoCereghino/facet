// scope.go is the read and amend half of the seat files `facet spawn` writes.
// Creating a workspace is where they come from; this is for the case the
// creating command cannot cover, which is a workspace handed a second issue
// after it already exists. The alternative to recording that is whatever works
// in the workspace claiming it, which is the thing these files exist to avoid.

package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/seat"
	"github.com/RiccardoCereghino/facet/internal/tree"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newScopeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Show or extend the issues a workspace covers",
		Long: "A workspace records the issues it legitimately covers in .scope, one\n" +
			"owner/repo#n per line, alongside the seat name in .seat and the issue that\n" +
			"describes the seat itself in .seat-issue. `facet spawn` writes all three;\n" +
			"this reads them, and adds an issue to a workspace that has been given one\n" +
			"after the fact.\n\n" +
			"A line may also read landing:owner/repo -- a repo this workspace's PULL\n" +
			"REQUESTS land in without claiming any issue there. Use it when a seat's\n" +
			"issues are filed in one repo and its work lands in another: naming some\n" +
			"unrelated issue in the landing repo would admit every PR there while\n" +
			"asserting the seat covers work it does not (facet#97).\n\n" +
			"Both subcommands find the workspace by walking UP from the working directory,\n" +
			"because the work is done inside a repository subdirectory rather than at the\n" +
			"workspace root.\n\n" +
			"A workspace with no .scope has no recorded scope, which means nothing to\n" +
			"check rather than nothing permitted -- a workspace that covers no single\n" +
			"issue is a real thing and must not need an exemption to exist.",
	}
	cmd.AddCommand(newScopeListCmd(), newScopeAddCmd(), newScopeRemoveCmd(), newScopeSetCmd())
	return cmd
}

func newScopeListCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the seat and the issues this workspace covers",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			ws, err := config.FindWorkspace(path)
			if err != nil {
				return err
			}
			return runScopeList(os.Stdout, ws)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "start the search here instead of the working directory")
	return cmd
}

func runScopeList(w io.Writer, ws string) error {
	name, err := seat.ReadName(ws)
	if err != nil {
		return err
	}
	refs, err := seat.ReadScope(ws)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "workspace: %s\n", ws)
	if name == "" {
		// Said plainly rather than shown as an empty field: a workspace created
		// before this was written has no seat, and that should read as a fact
		// about the workspace rather than as a blank nobody notices.
		_, _ = fmt.Fprintf(w, "seat:      (none recorded in %s)\n", seat.NameFile)
	} else {
		_, _ = fmt.Fprintf(w, "seat:      %s\n", name)
	}
	// The seat issue is reported before the scope, and reported even when there
	// is none, for the same reason the seat is: this command is where an
	// operator looks when identity is in doubt, and a field that vanishes when
	// unset cannot be distinguished from one nobody thought to print.
	//
	// A malformed .seat-issue is surfaced rather than swallowed. It is the one
	// state that means the spawner's write is broken, and `facet scope list` is
	// the command run after every re-seed precisely to catch that.
	seatIssue, haveSeatIssue, siErr := seat.ReadSeatIssue(ws)
	switch {
	case siErr != nil:
		_, _ = fmt.Fprintf(w, "seat issue: (UNREADABLE) %v\n", siErr)
	case !haveSeatIssue:
		_, _ = fmt.Fprintf(w, "seat issue: (none recorded in %s)\n", seat.SeatIssueFile)
	default:
		_, _ = fmt.Fprintf(w, "seat issue: %s\n", seatIssue)
	}

	if len(refs) == 0 {
		_, _ = fmt.Fprintf(w, "scope:     (none recorded in %s)\n", seat.ScopeFile)
		return nil
	}
	_, _ = fmt.Fprintln(w, "scope:")
	for _, r := range refs {
		_, _ = fmt.Fprintf(w, "  %s\n", r)
	}
	return nil
}

func newScopeAddCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "add <owner/repo#n | landing:owner/repo>...",
		Short: "Record another issue -- or landing repo -- this workspace covers",
		Long: "Appends to .scope, creating it if the workspace has none. Adding an entry\n" +
			"already recorded changes nothing, so this is safe to repeat.\n\n" +
			"landing:owner/repo records a repo this workspace's pull requests land in\n" +
			"without claiming any issue there (facet#97).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			refs, err := seat.ParseRefs(args)
			if err != nil {
				return err
			}
			ws, err := config.FindWorkspace(path)
			if err != nil {
				return err
			}
			added, err := seat.AppendScope(ws, refs)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			for _, r := range refs {
				if containsRef(added, r) {
					rep.Created("%s: %s", seat.ScopeFile, r)
				} else {
					rep.Unchanged("%s: %s (already recorded)", seat.ScopeFile, r)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "start the search here instead of the working directory")
	return cmd
}

func containsRef(refs []seat.Ref, want seat.Ref) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

// newScopeRemoveCmd is the opposite of add (facet#112): a boundary that can
// only widen is a ratchet, and a wrong entry was permanent -- correcting one
// meant editing .scope by hand, exactly what these tools exist to make
// unnecessary. Removing something not present is a no-op, not a refusal, for
// the same idempotence reason add has.
func newScopeRemoveCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "remove <owner/repo#n | landing:owner/repo>...",
		Short: "Drop an entry this workspace no longer covers",
		Long: "Rewrites .scope without the given entries, printing what remains.\n\n" +
			"Removing an entry not present changes nothing, so this is safe to repeat.\n" +
			"If nothing is left, .scope is deleted rather than left empty -- absent means\n" +
			"nothing recorded, the same rule `facet spawn` and `scope add` already follow.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			refs, err := seat.ParseRefs(args)
			if err != nil {
				return err
			}
			ws, err := config.FindWorkspace(path)
			if err != nil {
				return err
			}
			removed, remaining, err := seat.RemoveScope(ws, refs)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			for _, r := range refs {
				if containsRef(removed, r) {
					rep.Pruned("%s: dropped %s", seat.ScopeFile, r)
				} else {
					rep.Unchanged("%s: %s (was not recorded)", seat.ScopeFile, r)
				}
			}
			printScopeAfter(os.Stdout, remaining)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "start the search here instead of the working directory")
	return cmd
}

// newScopeSetCmd replaces the scope wholesale, printing the previous value
// the way `tree wire` prints the parent an edge moved a child away from --
// so an edit that turns out wrong is recoverable from the command's own
// output, not just from memory.
func newScopeSetCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "set <owner/repo#n | landing:owner/repo>...",
		Short: "Replace the scope wholesale, printing what it replaced",
		Long: "Unlike add and remove, this always writes: passing the current scope back\n" +
			"unchanged still counts as a set. Pass no arguments to clear .scope entirely.",
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			refs, err := seat.ParseRefs(args)
			if err != nil {
				return err
			}
			ws, err := config.FindWorkspace(path)
			if err != nil {
				return err
			}
			previous, err := seat.SetScope(ws, refs)
			if err != nil {
				return err
			}
			if len(previous) == 0 {
				_, _ = fmt.Fprintf(os.Stdout, "%s: was empty\n", seat.ScopeFile)
			} else {
				_, _ = fmt.Fprintf(os.Stdout, "%s: replaced %s\n", seat.ScopeFile, seat.Join(previous))
			}
			printScopeAfter(os.Stdout, seat.Dedupe(refs))
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "start the search here instead of the working directory")
	return cmd
}

// printScopeAfter states the scope a remove/set left behind and, since
// seat.sh derives a seat's lane and mode from the max complexity over its
// scope, the worst-case tier that implies -- so an edit that silently
// changes what a re-seed would derive is stated at the moment of the edit,
// the same discipline `tree wire` already applies to merge authority.
//
// This is informational, not authoritative: what gad/seat.sh actually
// derives lives outside this repo (facet#109's investigation found no tier
// derivation here at all), so a read failure or an unlabelled issue is
// reported rather than silently dropped from the worst case.
func printScopeAfter(w io.Writer, refs []seat.Ref) {
	if len(refs) == 0 {
		_, _ = fmt.Fprintf(w, "scope:     (none recorded)\n")
		return
	}
	_, _ = fmt.Fprintln(w, "scope now:")
	for _, r := range refs {
		_, _ = fmt.Fprintf(w, "  %s\n", r)
	}
	reportResultingTier(w, gh, refs)
}

// reportResultingTier is split from printScopeAfter so a test can drive it
// directly against a scripted ghx.Client without going through cobra.
func reportResultingTier(w io.Writer, client ghx.Client, refs []seat.Ref) {
	worst := 0
	checked := 0
	var problems []string
	for _, r := range refs {
		if r.Landing {
			continue
		}
		iss, err := client.ViewIssue(r.Repo, r.Number)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: could not read it (%v)", r, err))
			continue
		}
		if iss == nil {
			problems = append(problems, fmt.Sprintf("%s: not found", r))
			continue
		}
		checked++
		tier, found := (&tree.Node{Labels: iss.LabelNames()}).Tier()
		if tier == "" {
			if len(found) > 1 {
				problems = append(problems, fmt.Sprintf("%s: ambiguous complexity labels (%s)", r, strings.Join(found, ", ")))
			} else {
				problems = append(problems, fmt.Sprintf("%s: no complexity label", r))
			}
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(tier, "c")); err == nil && n > worst {
			worst = n
		}
	}
	if checked == 0 && len(problems) == 0 {
		return // scope is landing-only; there is no issue tier to report
	}
	if worst > 0 {
		_, _ = fmt.Fprintf(w, "tier:      c%d (worst case over %d %s in scope; not what gad/seat.sh derives -- informational)\n",
			worst, checked, plural(checked, "issue", "issues"))
	}
	for _, p := range problems {
		_, _ = fmt.Fprintf(w, "  ! %s\n", p)
	}
}
