package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// forbidViewIssue fails the test if `facet spawn` ever reaches the forge for
// the issue. A repo marked spawnable:false is refused for a reason that is
// true before any network call -- routing.json says so -- so the refusal
// must land before ViewIssue, exactly like the seat and scope checks it sits
// beside in runSpawn.
type forbidViewIssue struct {
	fakeGH
	t *testing.T
}

func (f *forbidViewIssue) ViewIssue(repo string, number int) (*ghx.Issue, error) {
	f.t.Errorf("spawn looked up %s#%d before checking spawnable:false", repo, number)
	return f.fakeGH.ViewIssue(repo, number)
}

// withSpawnableRouting writes a routing file with one spawnable repo and one
// marked spawnable:false, and points the package-level roots at it. It also
// stubs the SSH-key half of the credential preflight runSpawn runs first --
// the CI runner has none, and these tests are about spawnable:false, not
// about the machine's SSH state (already covered by preflight_test.go).
func withSpawnableRouting(t *testing.T) {
	t.Helper()
	prevCheck := checkSSHKey
	checkSSHKey = func(string) ([]ghx.Problem, string) { return nil, "" }
	t.Cleanup(func() { checkSSHKey = prevCheck })

	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	contents := `{
		"version": 1,
		"repos": {
			"lab-workspaces": {"dir": "lab-workspaces", "url": "https://example.invalid/lab-workspaces.git", "spawnable": false},
			"gateway":        {"dir": "gateway",        "url": "https://example.invalid/gateway.git"}
		},
		"ownerRepoToKey": {
			"acme/lab-workspaces": "lab-workspaces",
			"acme/gateway": "gateway"
		},
		"aliases": {},
		"areaMap": {},
		"knowledgeByArea": {},
		"pathHints": {}
	}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := roots
	roots = config.Roots{Routing: path}
	t.Cleanup(func() { roots = prev })
}

// TestSpawnRefusesSpawnableFalse is the easy half: the refusal itself, with a
// reason and a fix line, and no touch of the forge on the way there.
func TestSpawnRefusesSpawnableFalse(t *testing.T) {
	withSpawnableRouting(t)

	prevGH := gh
	gh = &forbidViewIssue{t: t}
	t.Cleanup(func() { gh = prevGH })

	err := runSpawn(spawnOpts{
		Repo: "acme/lab-workspaces", Number: 1, Seat: "w-example-1",
	})
	if err == nil {
		t.Fatal("spawn accepted a repo marked spawnable:false")
	}
	if !strings.Contains(err.Error(), "spawnable:false") {
		t.Errorf("error %q does not name the field", err)
	}
	if !strings.Contains(err.Error(), "lab-workspaces") {
		t.Errorf("error %q does not name the repo", err)
	}
	if !strings.Contains(err.Error(), "fix:") {
		t.Errorf("error %q does not tell the reader how to fix it", err)
	}
}

// recordingGH notes whether ViewIssue was reached, returning a minimal open
// issue so the caller can proceed past it instead of panicking on a nil one.
type recordingGH struct {
	fakeGH
	reached *bool
}

func (r *recordingGH) ViewIssue(_ string, _ int) (*ghx.Issue, error) {
	*r.reached = true
	return &ghx.Issue{Number: 1, State: "OPEN"}, nil
}

// TestSpawnStillReachesTheForgeForASpawnableRepo is the negative test: the
// deliverable this issue actually asked for. A refusal that fires for every
// repo, or a check wired to the wrong field, would pass the test above and
// still be wrong -- this proves an ordinary spawnable repo is completely
// unaffected and the request still reaches ViewIssue instead of being turned
// away by the same gate.
func TestSpawnStillReachesTheForgeForASpawnableRepo(t *testing.T) {
	withSpawnableRouting(t)

	reached := false
	prevGH := gh
	gh = &recordingGH{reached: &reached}
	t.Cleanup(func() { gh = prevGH })

	// Past ViewIssue, runSpawn goes on to create a workspace on disk (no
	// --dry-run) and will fail for reasons unrelated to spawnable:false --
	// that is expected and not what this test checks.
	_ = runSpawn(spawnOpts{
		Repo: "acme/gateway", Number: 1, Seat: "w-example-1", DryRun: true,
	})
	if !reached {
		t.Error("spawn never reached ViewIssue for a repo with no spawnable:false marker")
	}
}
