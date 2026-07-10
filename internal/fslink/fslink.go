// Package fslink creates and inspects the filesystem links that back a
// workspace's `links` entries: directory junctions on Windows, symlinks
// elsewhere.
//
// The safety property that matters, inherited from the PowerShell original:
// Remove deletes only the link itself, never the target's contents, and refuses
// outright when handed something that is not a link. On Windows that check
// cannot be os.Lstat's ModeSymlink bit -- a junction reports ModeIrregular, and
// so does every other reparse point (OneDrive placeholders, AppExecLink). It has
// to be the reparse tag.
package fslink

import (
	"fmt"
	"os"
)

// ErrNotALink is returned by Remove when the path exists but is not a link.
type ErrNotALink struct{ Path string }

func (e *ErrNotALink) Error() string {
	return fmt.Sprintf("%s is not a link; refusing to delete", e.Path)
}

// Remove deletes the link at path, never the directory it points at. It refuses
// if path exists and is not a link. A missing path is not an error.
func Remove(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	ok, err := IsLink(path)
	if err != nil {
		return err
	}
	if !ok {
		return &ErrNotALink{Path: path}
	}
	return os.Remove(path)
}
