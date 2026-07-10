package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List a workspace's entries and their state on disk",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			m, entries, err := workspace.List(roots, ws, git)
			if err != nil {
				return err
			}
			fmt.Printf("%s -- %s\n\n", m.Name, m.Description)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ENTRY\tKIND\tSTATUS\tTARGET\tORIGIN")
			for _, e := range entries {
				name := e.Name
				if e.Transient {
					name += " *"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, e.Kind, e.Status, e.Target, e.Origin)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, e := range entries {
				if e.Transient {
					fmt.Println("\n* transient -- here for now, may be swapped out")
					break
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "workspace directory (default: working directory)")
	return cmd
}
