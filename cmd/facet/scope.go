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
			"owner/repo#n per line, alongside the seat name in .seat. `facet spawn` writes\n" +
			"both; this reads them, and adds an issue to a workspace that has been given\n" +
			"one after the fact.\n\n" +
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
		Use:   "add <owner/repo#n>...",
		Short: "Record another issue this workspace covers",
		Long: "Appends to .scope, creating it if the workspace has none. Adding an issue\n" +
			"already recorded changes nothing, so this is safe to repeat.",
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
