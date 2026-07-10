package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/fslink"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// NewOptions describes a workspace to create.
type NewOptions struct {
	Name        string
	Description string
	// Clones maps a directory name to the git URL to clone from.
	Clones map[string]string
	// Links maps a directory name to a project folder under ProjectsRoot.
	Links     map[string]string
	Transient []string
}

// New scaffolds a workspace: its directory, manifest, and a starter CLAUDE.md.
func New(roots config.Roots, git gitx.Runner, rep Reporter, opt NewOptions, syncOpt SyncOptions) (string, error) {
	if opt.Name == "" {
		return "", fmt.Errorf("a workspace needs a name")
	}
	ws := filepath.Join(roots.Workspaces, opt.Name)
	if _, err := os.Stat(ws); err == nil {
		return "", fmt.Errorf("%s already exists", ws)
	}
	if err := os.MkdirAll(ws, 0o777); err != nil {
		return "", err
	}
	m := &manifest.Manifest{
		Name:        opt.Name,
		Description: opt.Description,
		Links:       opt.Links,
		Clones:      opt.Clones,
		Transient:   opt.Transient,
	}
	if err := m.Write(ws); err != nil {
		return "", err
	}
	if err := Sync(roots, ws, git, rep, syncOpt); err != nil {
		return ws, err
	}
	return ws, nil
}

// AddClone records a clone the workspace owns outright, then syncs.
func AddClone(roots config.Roots, ws string, git gitx.Runner, rep Reporter,
	name, url string, remotes map[string]string, lfs *bool, transient bool, syncOpt SyncOptions) error {

	m, err := manifest.Read(ws)
	if err != nil {
		return err
	}
	if _, ok := m.Links[name]; ok {
		return fmt.Errorf("%q is already a link in %s; remove it first", name, m.Name)
	}
	m.Clones[name] = url
	if len(remotes) > 0 {
		m.Remotes[name] = remotes
	}
	if lfs != nil {
		m.LFS[name] = *lfs
	}
	if transient {
		m.Transient = appendUnique(m.Transient, name)
	}
	if err := m.Write(ws); err != nil {
		return err
	}
	return Sync(roots, ws, git, rep, syncOpt)
}

// AddLink records a junction into a shared project under ProjectsRoot, then syncs.
func AddLink(roots config.Roots, ws string, git gitx.Runner, rep Reporter,
	name, target, origin string, transient bool, syncOpt SyncOptions) error {

	m, err := manifest.Read(ws)
	if err != nil {
		return err
	}
	if _, ok := m.Clones[name]; ok {
		return fmt.Errorf("%q is already a clone in %s; remove it first", name, m.Name)
	}
	m.Links[name] = target
	if origin != "" {
		m.Origins[target] = origin
	} else if live := gitx.Origin(git, filepath.Join(roots.Projects, target)); live != "" {
		m.Origins[target] = live
	}
	if transient {
		m.Transient = appendUnique(m.Transient, name)
	}
	if err := m.Write(ws); err != nil {
		return err
	}
	if syncOpt.Bootstrap == false && m.Origins[target] != "" {
		if _, err := os.Stat(filepath.Join(roots.Projects, target)); os.IsNotExist(err) {
			syncOpt.Bootstrap = true // we know where it lives; fetch it
		}
	}
	return Sync(roots, ws, git, rep, syncOpt)
}

// RemoveResult says what Remove did, so the caller can warn appropriately.
type RemoveResult struct {
	WasClone bool
	// CheckoutLeft is the path of a clone's checkout, which is never deleted: it
	// may hold the only copy of unpushed work.
	CheckoutLeft string
}

// Remove drops an entry from the manifest.
//
// A link loses its reparse point; the real project under ProjectsRoot is
// untouched. A clone loses only its manifest entry -- the checkout stays on
// disk, because it may hold the only copy of unpushed work. Delete it yourself.
func Remove(ws string, name string, rep Reporter) (*RemoveResult, error) {
	m, err := manifest.Read(ws)
	if err != nil {
		return nil, err
	}
	res := &RemoveResult{}
	path := filepath.Join(ws, name)

	if _, ok := m.Clones[name]; ok {
		res.WasClone = true
		delete(m.Clones, name)
		delete(m.Remotes, name)
		delete(m.LFS, name)
		m.Transient = removeString(m.Transient, name)
		if err := m.Write(ws); err != nil {
			return nil, err
		}
		if _, err := os.Stat(path); err == nil {
			res.CheckoutLeft = path
		}
		return res, nil
	}

	target, ok := m.Links[name]
	if !ok {
		return nil, fmt.Errorf("%q is neither a link nor a clone in %s", name, m.Name)
	}
	if err := fslink.Remove(path); err != nil {
		return nil, err
	}
	delete(m.Links, name)
	// Drop the origin only when no remaining link points at that project.
	stillUsed := false
	for _, t := range m.Links {
		if t == target {
			stillUsed = true
		}
	}
	if !stillUsed {
		delete(m.Origins, target)
	}
	m.Transient = removeString(m.Transient, name)
	return res, m.Write(ws)
}

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

func removeString(xs []string, s string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
