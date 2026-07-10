package workspace

import (
	"os"
	"path/filepath"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/fslink"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// Entry is one row of `facet ls`.
type Entry struct {
	Name      string // the directory inside the workspace
	Kind      string // "junction"/"symlink", or "clone"
	Target    string // project folder for a link; git URL for a clone
	Status    string // ok | broken | missing
	Transient bool
	Origin    string
}

// List describes every entry a workspace declares, and its state on disk.
func List(roots config.Roots, ws string, git gitx.Runner) (*manifest.Manifest, []Entry, error) {
	m, err := manifest.Read(ws)
	if err != nil {
		return nil, nil, err
	}
	transient := map[string]bool{}
	for _, t := range m.Transient {
		transient[t] = true
	}

	var out []Entry
	for _, name := range sortedKeys(m.Links) {
		project := m.Links[name]
		e := Entry{
			Name: name, Kind: fslink.Kind, Target: project,
			Transient: transient[name], Origin: m.Origins[project],
			Status: "broken",
		}
		if target, ok, _ := fslink.Read(filepath.Join(ws, name)); ok {
			if samePath(target, filepath.Join(roots.Projects, project)) {
				e.Status = "ok"
			}
		}
		out = append(out, e)
	}
	for _, name := range sortedKeys(m.Clones) {
		dir := filepath.Join(ws, name)
		e := Entry{Name: name, Kind: "clone", Target: m.Clones[name], Transient: transient[name]}
		switch {
		case !exists(dir):
			e.Status = "missing"
		case !gitx.IsRepo(dir):
			e.Status = "broken"
		default:
			e.Status = "ok"
			e.Origin = gitx.Origin(git, dir)
		}
		out = append(out, e)
	}
	return m, out, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
