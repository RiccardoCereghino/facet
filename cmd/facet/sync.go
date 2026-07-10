package main

import (
	"fmt"
	"os"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/mirror"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	var (
		path      string
		prune     bool
		bootstrap bool
		viaMirror bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Make the workspace directory match its manifest",
		Long: "Creates any missing link or clone declared in .workspace.json.\n\n" +
			"An existing clone is never touched -- no pull, no reset, no clean -- because\n" +
			"it may hold the only copy of unpushed work. --prune deletes only links.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			return workspace.Sync(roots, ws, git, rep, workspace.SyncOptions{
				Prune:     prune,
				Bootstrap: bootstrap,
				Source:    sourceFor(viaMirror, rep),
			})
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "workspace directory (default: working directory)")
	cmd.Flags().BoolVar(&prune, "prune", false, "remove links present on disk but absent from the manifest")
	cmd.Flags().BoolVar(&bootstrap, "bootstrap", false, "clone a link's missing target from its recorded origin")
	cmd.Flags().BoolVar(&viaMirror, "via-mirror", false, "clone from a local bare mirror, hardlinking the object store")
	return cmd
}

// sourceFor picks where clones come from: straight off the forge, or hardlinked
// out of a local bare mirror.
func sourceFor(viaMirror bool, rep workspace.Reporter) workspace.SourceResolver {
	if !viaMirror {
		return workspace.DirectSource{}
	}
	return &mirror.Store{
		Root:   roots.Mirrors,
		Git:    git,
		Report: func(f string, a ...any) { rep.Working(f, a...) },
		Warn:   func(f string, a ...any) { rep.Warn(f, a...) },
	}
}

func newRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Bootstrap and sync every workspace (new-machine entry point)",
		Long: "Runs `sync --bootstrap` over every workspace under the workspaces root.\n" +
			"Ephemeral issue workspaces are skipped: they are gitignored by design.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := workspace.Dirs(roots.Workspaces, false)
			if err != nil {
				return err
			}
			rep := workspace.Reporter{W: os.Stdout}
			for _, dir := range dirs {
				if err := workspace.Sync(roots, dir, git, rep, workspace.SyncOptions{Bootstrap: true}); err != nil {
					return fmt.Errorf("%s: %w", dir, err)
				}
			}
			return nil
		},
	}
	return cmd
}
