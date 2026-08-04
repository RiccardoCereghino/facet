package main

import (
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// withUnsoundCredential points gh at a logged-out status and stubs the
// SSH-key half of preflight, so a refusal is attributable to the credential
// check alone and not to the CI runner's absent SSH key.
func withUnsoundCredential(t *testing.T) {
	t.Helper()
	prevGH := gh
	gh = &fakeGH{auth: &ghx.AuthStatus{State: ghx.StateAbsent}}
	t.Cleanup(func() { gh = prevGH })

	prevCheck := checkSSHKey
	checkSSHKey = func(string) ([]ghx.Problem, string) { return nil, "" }
	t.Cleanup(func() { checkSSHKey = prevCheck })
}

func withSoundCredential(t *testing.T) {
	t.Helper()
	prevGH := gh
	gh = &fakeGH{}
	t.Cleanup(func() { gh = prevGH })

	prevCheck := checkSSHKey
	checkSSHKey = func(string) ([]ghx.Problem, string) { return nil, "" }
	t.Cleanup(func() { checkSSHKey = prevCheck })
}

// sync clones from GitHub with the ambient credential exactly like spawn --
// facet#109's finding was that only spawn was gated, so sync failed deep
// inside a clone instead of at the point where the cause was knowable. This
// is the regression test for the fix: the refusal must happen before
// ResolveWorkspace/workspace.Sync ever run, which is why it fires even
// against a path that does not exist.
func TestSyncRefusesOnUnsoundCredential(t *testing.T) {
	withUnsoundCredential(t)

	err := runSync(t.TempDir()+"/does-not-exist", false, false, false)
	if err == nil {
		t.Fatal("sync proceeded on an unsound credential")
	}
	if !strings.Contains(err.Error(), "sync refused") {
		t.Errorf("error = %q, want it to name sync's own refusal", err)
	}
}

// restore is in the identical position -- it is workspace.Sync run over every
// workspace on the machine, so the same credential gate applies for the same
// reason.
func TestRestoreRefusesOnUnsoundCredential(t *testing.T) {
	withUnsoundCredential(t)
	prev := roots
	roots = config.Roots{Workspaces: t.TempDir()}
	t.Cleanup(func() { roots = prev })

	err := runRestore()
	if err == nil {
		t.Fatal("restore proceeded on an unsound credential")
	}
	if !strings.Contains(err.Error(), "restore refused") {
		t.Errorf("error = %q, want it to name restore's own refusal", err)
	}
}

// A sound credential must still let sync proceed to its normal work (here,
// failing on the missing workspace path rather than on the credential) --
// otherwise the gate would be indistinguishable from an unconditional
// refusal.
func TestSyncProceedsOnASoundCredential(t *testing.T) {
	withSoundCredential(t)

	err := runSync(t.TempDir()+"/does-not-exist", false, false, false)
	if err == nil {
		t.Fatal("sync succeeded against a path that does not exist")
	}
	if strings.Contains(err.Error(), "refused") {
		t.Errorf("a sound credential must not be reported as the reason: %v", err)
	}
}
