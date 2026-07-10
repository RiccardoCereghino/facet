package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

// pairs parses "name=value" arguments into a map.
func pairs(kind string, entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("bad --%s %q; expected name=value", kind, e)
		}
		out[k] = v
	}
	return out, nil
}

func newNewCmd() *cobra.Command {
	var (
		desc      string
		clones    []string
		links     []string
		transient []string
		viaMirror bool
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a workspace and its entries",
		Long: "Creates the directory and .workspace.json, then syncs.\n\n" +
			"A --clone entry is a checkout the workspace owns outright. A --link entry is\n" +
			"a junction into a shared repo under the projects root, whose working tree every\n" +
			"linking workspace sees at once.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := pairs("clone", clones)
			if err != nil {
				return err
			}
			lk, err := pairs("link", links)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			ws, err := workspace.New(roots, git, rep, workspace.NewOptions{
				Name: args[0], Description: desc, Clones: cl, Links: lk, Transient: transient,
			}, workspace.SyncOptions{Source: sourceFor(viaMirror, rep)})
			if err != nil {
				return err
			}
			fmt.Printf("\nWorkspace ready: %s\n", ws)
			fmt.Println("write a CLAUDE.md saying what it is for.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&desc, "desc", "", "one-line purpose")
	f.StringSliceVar(&clones, "clone", nil, "dir=giturl (repeatable)")
	f.StringSliceVar(&links, "link", nil, "dir=ProjectFolder (repeatable)")
	f.StringSliceVar(&transient, "transient", nil, "entries here for now, likely to be swapped out")
	f.BoolVar(&viaMirror, "via-mirror", false, "clone from a local bare mirror")
	return cmd
}

func newAddCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "add", Short: "Add a clone or a link to a workspace"}
	cmd.AddCommand(newAddCloneCmd(), newAddLinkCmd())
	return cmd
}

func newAddCloneCmd() *cobra.Command {
	var (
		path      string
		remotes   []string
		noLFS     bool
		transient bool
		viaMirror bool
	)
	cmd := &cobra.Command{
		Use:   "clone <dir> <giturl>",
		Short: "Add a checkout the workspace owns outright",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			rm, err := pairs("remote", remotes)
			if err != nil {
				return err
			}
			var lfs *bool
			if noLFS {
				f := false
				lfs = &f
			}
			rep := workspace.Reporter{W: os.Stdout}
			return workspace.AddClone(roots, ws, git, rep, args[0], args[1], rm, lfs, transient,
				workspace.SyncOptions{Source: sourceFor(viaMirror, rep)})
		},
	}
	f := cmd.Flags()
	f.StringVar(&path, "path", "", "workspace directory (default: working directory)")
	f.StringSliceVar(&remotes, "remote", nil, "name=url for an extra remote, e.g. upstream=... (repeatable)")
	f.BoolVar(&noLFS, "no-lfs", false, "fetch Git-LFS pointers rather than blobs")
	f.BoolVar(&transient, "transient", false, "mark as here for now, likely to be swapped out")
	f.BoolVar(&viaMirror, "via-mirror", false, "clone from a local bare mirror")
	return cmd
}

func newAddLinkCmd() *cobra.Command {
	var (
		path      string
		origin    string
		transient bool
	)
	cmd := &cobra.Command{
		Use:   "link <dir> <ProjectFolder>",
		Short: "Add a junction into a shared repo under the projects root",
		Long: "Every workspace linking a project sees one working tree: one branch, one\n" +
			"dirty index. Use a clone when a workspace must not share.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			return workspace.AddLink(roots, ws, git, rep, args[0], args[1], origin, transient,
				workspace.SyncOptions{})
		},
	}
	f := cmd.Flags()
	f.StringVar(&path, "path", "", "workspace directory (default: working directory)")
	f.StringVar(&origin, "origin", "", "git URL to clone the project from if it is missing")
	f.BoolVar(&transient, "transient", false, "mark as here for now, likely to be swapped out")
	return cmd
}

func newRmCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "rm <entry>",
		Short: "Drop a clone or a link from a workspace's manifest",
		Long: "A link loses its reparse point; the real project is untouched.\n\n" +
			"A clone loses only its manifest entry. Its checkout stays on disk, because it\n" +
			"may hold the only copy of unpushed work -- delete it yourself, after pushing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			res, err := workspace.Remove(ws, args[0], workspace.Reporter{W: os.Stdout})
			if err != nil {
				return err
			}
			if res.WasClone {
				fmt.Printf("removed clone %q from the manifest.\n", args[0])
				if res.CheckoutLeft != "" {
					fmt.Printf("the checkout remains at %s -- it is the only copy of any unpushed\n"+
						"branch. Push, then delete it yourself.\n", res.CheckoutLeft)
				}
				return nil
			}
			fmt.Printf("removed link %q. The project it pointed at is untouched.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "workspace directory (default: working directory)")
	return cmd
}
