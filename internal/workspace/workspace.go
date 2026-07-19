// Package workspace implements the operations on a workspace directory: sync,
// list, create, and the add/remove of links and clones.
package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/fslink"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/lockfile"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// syncLockName is the per-workspace lockfile that serialises Sync across
// processes.
const syncLockName = ".facet-sync.lock"

// Reporter receives progress. The symbols mirror the tool this replaced, so the
// output stays familiar: "=" unchanged, "+" created, "v" fetching, "-" pruned,
// "!" warning, "~" manifest touched.
type Reporter struct{ W io.Writer }

func (r Reporter) line(sym, format string, a ...any) {
	if r.W == nil {
		return
	}
	fmt.Fprintf(r.W, "  %s %s\n", sym, fmt.Sprintf(format, a...))
}
func (r Reporter) Unchanged(f string, a ...any) { r.line("=", f, a...) }
func (r Reporter) Created(f string, a ...any)   { r.line("+", f, a...) }
func (r Reporter) Working(f string, a ...any)   { r.line("v", f, a...) }
func (r Reporter) Pruned(f string, a ...any)    { r.line("-", f, a...) }
func (r Reporter) Warn(f string, a ...any)      { r.line("!", f, a...) }
func (r Reporter) Note(f string, a ...any)      { r.line("~", f, a...) }
func (r Reporter) Header(f string, a ...any) {
	if r.W != nil {
		fmt.Fprintf(r.W, "%s\n", fmt.Sprintf(f, a...))
	}
}

// SourceResolver decides where a clone's objects come from. The direct resolver
// clones straight from the canonical URL; the mirror resolver clones from a
// local bare mirror and repoints origin afterwards.
type SourceResolver interface {
	// Resolve maps a canonical URL to the location to clone from, plus the URL
	// origin should be reset to afterwards ("" means leave it alone).
	Resolve(url string) (src, setOriginTo string, err error)
}

// DirectSource clones straight from the canonical URL.
type DirectSource struct{}

func (DirectSource) Resolve(url string) (string, string, error) { return url, "", nil }

// SyncOptions controls Sync.
type SyncOptions struct {
	// Prune removes links present on disk but absent from the manifest. It only
	// ever deletes a link; a clone is never at risk.
	Prune bool
	// Bootstrap clones a link's missing target under ProjectsRoot from its
	// recorded origin. Irrelevant to clones.
	Bootstrap bool
	// Source decides where clones are fetched from. Nil means DirectSource.
	Source SourceResolver
}

// Sync makes the workspace directory match its manifest, idempotently.
//
// Its central guarantee: an existing clone is never touched. Sync creates a
// missing clone and otherwise leaves it entirely alone -- no pull, no reset, no
// clean -- because it may hold the only copy of unpushed work.
func Sync(roots config.Roots, ws string, git gitx.Runner, rep Reporter, opt SyncOptions) error {
	// Serialise the whole read-modify-write across processes. Without this, two
	// agents syncing the same workspace can both find a clone dir missing and race
	// into it, or capture-and-write the manifest last-writer-wins and lose one
	// process's origin capture. Same lock discipline as the mirror.
	return lockfile.With(filepath.Join(ws, syncLockName), lockfile.Options{Warn: rep.Warn}, func() error {
		return syncLocked(roots, ws, git, rep, opt)
	})
}

func syncLocked(roots config.Roots, ws string, git gitx.Runner, rep Reporter, opt SyncOptions) error {
	m, err := manifest.Read(ws)
	if err != nil {
		return err
	}
	if opt.Source == nil {
		opt.Source = DirectSource{}
	}
	rep.Header("Syncing %s (%s)", m.Name, ws)

	dirty := false
	for _, name := range sortedKeys(m.Links) {
		changed, err := syncLink(roots, ws, m, name, git, rep, opt)
		if err != nil {
			return err
		}
		dirty = dirty || changed
	}
	for _, name := range sortedKeys(m.Clones) {
		if err := syncClone(ws, m, name, git, rep, opt); err != nil {
			return err
		}
	}
	if opt.Prune {
		if err := prune(ws, m, rep); err != nil {
			return err
		}
	}
	if dirty {
		if err := m.Write(ws); err != nil {
			return err
		}
		rep.Note("captured origin(s) into manifest")
	}
	return nil
}

func syncLink(roots config.Roots, ws string, m *manifest.Manifest, name string, git gitx.Runner, rep Reporter, opt SyncOptions) (dirty bool, err error) {
	projectName := m.Links[name]
	target := filepath.Join(roots.Projects, projectName)
	linkPath := filepath.Join(ws, name)

	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		origin := m.Origins[projectName]
		switch {
		case opt.Bootstrap && origin != "":
			rep.Working("%s : cloning %s -> %s", name, origin, projectName)
			// Route through gitx.Clone so link bootstrap shares the hardened clone
			// logic (core.longpaths, --no-checkout + reset, LFS handling) that
			// syncClone uses, instead of a raw `git clone` that fails on Windows
			// long-path or LFS-hooked repos a normal clone handles.
			if err := gitx.Clone(git, origin, target, gitx.CloneOptions{}); err != nil {
				rep.Warn("%s -> %s (clone failed: %v)", name, projectName, err)
				return false, nil
			}
		case opt.Bootstrap:
			rep.Warn("%s -> %s (target missing, no origin recorded -- cannot bootstrap)", name, projectName)
			return false, nil
		default:
			rep.Warn("%s -> %s (target missing under Projects; pass --bootstrap to clone from origin)", name, projectName)
			return false, nil
		}
	}

	// Refresh the recorded origin from the live repo.
	if live := gitx.Origin(git, target); live != "" && m.Origins[projectName] != live {
		m.Origins[projectName] = live
		dirty = true
	}

	current, isLink, err := fslink.Read(linkPath)
	if err != nil {
		return dirty, err
	}
	if isLink && samePath(current, target) {
		rep.Unchanged("%s", name)
		return dirty, nil
	}
	if _, statErr := os.Lstat(linkPath); statErr == nil {
		if err := fslink.Remove(linkPath); err != nil {
			return dirty, err
		}
	}
	if err := fslink.Create(linkPath, target); err != nil {
		return dirty, err
	}
	rep.Created("%s -> %s", name, projectName)
	return dirty, nil
}

func syncClone(ws string, m *manifest.Manifest, name string, git gitx.Runner, rep Reporter, opt SyncOptions) error {
	dir := filepath.Join(ws, name)
	url := m.Clones[name]

	if _, err := os.Lstat(dir); err == nil {
		isLink, err := fslink.IsLink(dir)
		if err != nil {
			return err
		}
		if isLink {
			rep.Warn("%s is a %s, but the manifest declares it a clone. Remove it, then re-run.", name, fslink.Kind)
			return nil
		}
		// Present. Never touch it: it may hold the only copy of unpushed work.
		rep.Unchanged("%s (clone)", name)
	} else {
		src, setOrigin, err := opt.Source.Resolve(url)
		if err != nil {
			return err
		}
		skipLFS := false
		if v, ok := m.LFS[name]; ok && !v {
			skipLFS = true
		}
		note := ""
		if skipLFS {
			note = " (LFS pointers only)"
		}
		rep.Working("%s : cloning %s%s", name, src, note)
		if err := gitx.Clone(git, src, dir, gitx.CloneOptions{SkipLFS: skipLFS, SetOriginTo: setOrigin}); err != nil {
			rep.Warn("%s (clone failed: %v)", name, err)
			return nil
		}
		rep.Created("%s (cloned)", name)
	}

	// Extra remotes (a fork's `upstream`, say). Added when missing; a URL that
	// disagrees with the manifest is reported, never silently rewritten.
	for _, rn := range sortedKeys(m.Remotes[name]) {
		want := m.Remotes[name][rn]
		have, exists := gitx.RemoteURL(git, dir, rn)
		switch {
		case !exists:
			if _, err := git.Run(dir, nil, "remote", "add", rn, want); err != nil {
				return err
			}
			rep.Created("  remote %s -> %s", rn, want)
		case have != want:
			rep.Warn("  remote %q on %s is %s, manifest says %s (left as-is)", rn, name, have, want)
		}
	}
	return nil
}

// prune deletes links on disk that the manifest no longer declares. It only ever
// deletes a link, so a clone -- which may hold unpushed work -- is never at risk.
func prune(ws string, m *manifest.Manifest, rep Reporter) error {
	entries, err := os.ReadDir(ws)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, declared := m.Links[e.Name()]; declared {
			continue
		}
		p := filepath.Join(ws, e.Name())
		isLink, err := fslink.IsLink(p)
		if err != nil || !isLink {
			continue
		}
		if err := fslink.Remove(p); err != nil {
			return err
		}
		rep.Pruned("%s (pruned, not in manifest)", e.Name())
	}
	return nil
}

// Dirs lists the workspace directories under root. Issue workspaces are
// ephemeral and gitignored, so callers that rebuild a machine skip them.
func Dirs(root string, includeIssue bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !includeIssue && strings.HasPrefix(e.Name(), config.IssuePrefix) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(manifest.Path(dir)); err != nil {
			continue
		}
		out = append(out, dir)
	}
	return out, nil
}

// samePath compares two filesystem paths, case-insensitively on Windows.
func samePath(a, b string) bool {
	a = strings.TrimRight(filepath.Clean(a), string(os.PathSeparator))
	b = strings.TrimRight(filepath.Clean(b), string(os.PathSeparator))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
