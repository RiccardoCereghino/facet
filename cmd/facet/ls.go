package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/manifest"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List a workspace's entries and their state on disk, or every workspace at a workspaces root",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			if _, err := os.Stat(manifest.Path(ws)); err != nil {
				// ws isn't a workspace itself -- see if it's the root that holds them.
				if dirs, dirsErr := workspace.Dirs(ws, true); dirsErr == nil && len(dirs) > 0 {
					return lsRoot(ws, dirs)
				}
			}
			m, entries, err := workspace.List(roots, ws, git)
			if err != nil {
				return err
			}
			fmt.Printf("%s -- %s\n\n", m.Name, m.Description)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ENTRY\tKIND\tSTATUS\tTARGET\tORIGIN")
			for _, e := range entries {
				name := e.Name
				if e.Transient {
					name += " *"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, e.Kind, e.Status, e.Target, e.Origin)
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

// lsRoot lists every workspace under root, each with a health summary rolled
// up from its entries' statuses -- the workspaces-root counterpart to the
// single-workspace listing above.
func lsRoot(root string, dirs []string) error {
	fmt.Printf("%s -- workspaces root\n\n", root)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "WORKSPACE\tENTRIES\tHEALTH")
	for _, dir := range dirs {
		name := filepath.Base(dir)
		_, entries, err := workspace.List(roots, dir, git)
		if err != nil {
			_, _ = fmt.Fprintf(w, "%s\t-\terror: %v\n", name, err)
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", name, len(entries), summarizeHealth(entries))
	}
	return w.Flush()
}

// summarizeHealth rolls up a workspace's entries into one status word, reusing
// the ok/broken/missing vocabulary workspace.List already reports per entry.
func summarizeHealth(entries []workspace.Entry) string {
	var broken, missing int
	for _, e := range entries {
		switch e.Status {
		case "broken":
			broken++
		case "missing":
			missing++
		}
	}
	if broken == 0 && missing == 0 {
		return "ok"
	}
	var parts []string
	if broken > 0 {
		parts = append(parts, fmt.Sprintf("%d broken", broken))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", missing))
	}
	return strings.Join(parts, ", ")
}
