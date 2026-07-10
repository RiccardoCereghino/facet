// Package knowledge loads the durable hazard fragments that get inlined into a
// spawned workspace's CLAUDE.md.
//
// A fragment holds invariants and traps -- things that are true about a system
// regardless of which issue you are working on. It deliberately does not hold
// status, phase, or "as of" notes: those belong in the long-lived workspace that
// owns the subject, named here by source_workspace. Keeping the two apart is the
// only thing stopping a fragment from silently becoming a second, staler source
// of truth.
package knowledge

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Fragment is one `area-*.md` file.
type Fragment struct {
	Name string // the file's base name, e.g. "area-backups"
	Meta Meta
	Body string // markdown, frontmatter stripped
}

// Meta is a fragment's YAML frontmatter.
type Meta struct {
	Area string `yaml:"area"`
	// SourceWorkspace names the living document this was extracted from.
	SourceWorkspace string `yaml:"source_workspace"`
	// LastReviewed is printed at spawn time, so a stale fragment says so.
	LastReviewed string `yaml:"last_reviewed"`
	// Kind must be "invariants". Anything else is a fragment that has drifted
	// into being a status board.
	Kind string `yaml:"kind"`
}

// Reviewed parses LastReviewed, or returns the zero time.
func (m Meta) Reviewed() time.Time {
	t, err := time.Parse("2006-01-02", m.LastReviewed)
	if err != nil {
		return time.Time{}
	}
	return t
}

// StaleAfter is when a fragment starts announcing its age at spawn time.
const StaleAfter = 120 * 24 * time.Hour

// IsStale reports whether the fragment has gone unreviewed for too long.
func (f Fragment) IsStale(now time.Time) bool {
	r := f.Meta.Reviewed()
	return r.IsZero() || now.Sub(r) > StaleAfter
}

var frontmatterDelim = []byte("---")

// utf8BOM is stripped before parsing: a Windows editor may have added one.
var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

// Load reads one fragment by name from dir.
func Load(dir, name string) (*Fragment, error) {
	b, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return nil, err
	}
	meta, body, err := split(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f := &Fragment{Name: name, Meta: meta, Body: string(bytes.TrimSpace(body))}
	if f.Meta.Kind != "" && f.Meta.Kind != "invariants" {
		return nil, fmt.Errorf("%s: kind is %q; fragments hold invariants, not status", name, f.Meta.Kind)
	}
	return f, nil
}

// LoadAll reads the named fragments, skipping (and reporting) any that are
// missing or malformed. A missing fragment must never block a spawn.
func LoadAll(dir string, names []string) ([]Fragment, []error) {
	var out []Fragment
	var errs []error
	for _, n := range names {
		f, err := Load(dir, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, *f)
	}
	return out, errs
}

// split separates YAML frontmatter from the markdown body.
func split(b []byte) (Meta, []byte, error) {
	b = bytes.TrimLeft(b, utf8BOM+" \t\r\n")
	if !bytes.HasPrefix(b, frontmatterDelim) {
		return Meta{}, b, nil // no frontmatter is allowed, just uninformative
	}
	rest := b[len(frontmatterDelim):]
	idx := bytes.Index(rest, append([]byte("\n"), frontmatterDelim...))
	if idx < 0 {
		return Meta{}, nil, fmt.Errorf("unterminated frontmatter")
	}
	var m Meta
	if err := yaml.Unmarshal(rest[:idx], &m); err != nil {
		return Meta{}, nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	body := rest[idx+1+len(frontmatterDelim):]
	return m, body, nil
}
