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
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSync(path, prune, bootstrap, viaMirror)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "workspace directory (default: working directory)")
	cmd.Flags().BoolVar(&prune, "prune", false, "remove links present on disk but absent from the manifest")
	cmd.Flags().BoolVar(&bootstrap, "bootstrap", false, "clone a link's missing target from its recorded origin")
	cmd.Flags().BoolVar(&viaMirror, "via-mirror", false, "clone from a local bare mirror, hardlinking the object store")
	return cmd
}

// runSync is sync's body, split from the cobra wiring so the credential gate
// can be asserted directly rather than only through cobra's RunE.
//
// The gate goes first, exactly as in spawn: sync clones from GitHub with the
// ambient credential (workspace.Sync -> syncClone -> gitx.Clone) every bit as
// much as spawn does, and until facet#109 it was the only guarded verb --
// `facet sync` on an unsound credential failed deep inside a clone instead of
// at the point where the cause was knowable.
func runSync(path string, prune, bootstrap, viaMirror bool) error {
	if err := requirePreflight(os.Stderr, "sync"); err != nil {
		return err
	}
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRestore()
		},
	}
	return cmd
}

// runRestore is restore's body, gated the same way and for the same reason as
// runSync -- it is the same clone mechanism run over every workspace, so the
// same unsound credential fails it the same way.
func runRestore() error {
	if err := requirePreflight(os.Stderr, "restore"); err != nil {
		return err
	}
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
}
