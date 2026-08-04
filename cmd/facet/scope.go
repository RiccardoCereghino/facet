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

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/seat"
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
	cmd.AddCommand(newScopeListCmd(), newScopeAddCmd())
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
