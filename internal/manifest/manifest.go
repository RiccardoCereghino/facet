// Package manifest reads and writes .workspace.json, the source of truth for a
// workspace's layout.
//
// The on-disk format is frozen: it predates this program and is versioned in the
// user's config repo, so a write must reproduce what the previous PowerShell
// implementation produced, byte for byte. See TestGolden.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FileName is the manifest's name inside a workspace directory.
const FileName = ".workspace.json"

// Issue records the GitHub issue an ephemeral workspace was spawned for. It is
// absent from ordinary workspaces, hence the pointer + omitempty on Manifest.
type Issue struct {
	Repo    string   `json:"repo"`   // owner/name of the issue's home repo
	Number  int      `json:"number"` //
	Branch  string   `json:"branch"` // the gh issue develop branch
	Home    string   `json:"home"`   // clone key holding that branch
	URL     string   `json:"url"`    //
	Created string   `json:"created"`
	Labels  []string `json:"labels"`
}

// Manifest is a workspace's declared layout. Field order here is the on-disk key
// order; encoding/json sorts map keys, which matches the old Sort-Object.
type Manifest struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Links       map[string]string            `json:"links"`   // junction name -> project folder under ProjectsRoot
	Clones      map[string]string            `json:"clones"`  // dir name -> git URL to clone from
	Remotes     map[string]map[string]string `json:"remotes"` // clone name -> extra remotes besides origin
	LFS         map[string]bool              `json:"lfs"`     // clone name -> false to fetch LFS pointers only
	Origins     map[string]string            `json:"origins"` // project folder -> git origin URL
	Transient   []string                     `json:"transient"`
	Issue       *Issue                       `json:"issue,omitempty"`
}

// ensureInit replaces nil maps and slices with empty ones. Go marshals a nil map
// as `null`; the frozen format wants `{}`.
func (m *Manifest) ensureInit() {
	if m.Links == nil {
		m.Links = map[string]string{}
	}
	if m.Clones == nil {
		m.Clones = map[string]string{}
	}
	if m.Remotes == nil {
		m.Remotes = map[string]map[string]string{}
	}
	if m.LFS == nil {
		m.LFS = map[string]bool{}
	}
	if m.Origins == nil {
		m.Origins = map[string]string{}
	}
	if m.Transient == nil {
		m.Transient = []string{}
	}
}

// Validate enforces the three invariants the manifest schema has always had.
func (m *Manifest) Validate() error {
	var both []string
	for k := range m.Links {
		if _, ok := m.Clones[k]; ok {
			both = append(both, k)
		}
	}
	if len(both) > 0 {
		sort.Strings(both)
		return fmt.Errorf("manifest %q: %v declared as both a link and a clone", m.Name, both)
	}
	var orphanRemotes []string
	for k := range m.Remotes {
		if _, ok := m.Clones[k]; !ok {
			orphanRemotes = append(orphanRemotes, k)
		}
	}
	if len(orphanRemotes) > 0 {
		sort.Strings(orphanRemotes)
		return fmt.Errorf("manifest %q: remotes declared for non-clone %v", m.Name, orphanRemotes)
	}
	var orphanLFS []string
	for k := range m.LFS {
		if _, ok := m.Clones[k]; !ok {
			orphanLFS = append(orphanLFS, k)
		}
	}
	if len(orphanLFS) > 0 {
		sort.Strings(orphanLFS)
		return fmt.Errorf("manifest %q: lfs declared for non-clone %v", m.Name, orphanLFS)
	}
	return nil
}

// IsIssueWorkspace reports whether this workspace was spawned for a GitHub issue.
func (m *Manifest) IsIssueWorkspace() bool { return m.Issue != nil }

// Path returns the manifest path for a workspace directory.
func Path(workspaceDir string) string { return filepath.Join(workspaceDir, FileName) }

// Read loads and validates the manifest in workspaceDir.
func Read(workspaceDir string) (*Manifest, error) {
	b, err := os.ReadFile(Path(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s in %s: use `facet new` to create one", FileName, workspaceDir)
		}
		return nil, err
	}
	return Unmarshal(b)
}

// Unmarshal parses manifest bytes, filling in empty maps and validating.
func Unmarshal(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	m.ensureInit()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Marshal renders the manifest exactly as the frozen format requires: two-space
// indent, no HTML escaping, empty maps as {}, trailing newline, LF endings.
func (m *Manifest) Marshal() ([]byte, error) {
	m.ensureInit()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // Go escapes < > &; PowerShell 7 (Newtonsoft) does not
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil { // Encode appends the trailing newline
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write validates and atomically replaces the manifest in workspaceDir.
func (m *Manifest) Write(workspaceDir string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	p := Path(workspaceDir)
	tmp, err := os.CreateTemp(workspaceDir, ".workspace.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
