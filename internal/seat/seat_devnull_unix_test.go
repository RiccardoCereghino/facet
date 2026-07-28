//go:build unix

package seat

import (
	"os"
	"testing"
)

// The whole composition, against a real file that genuinely lies: writing to the
// null device succeeds, reports no error, and reads back as nothing at all.
//
// TestVerifyCatchesAWriteThatDidNotLand covers the comparison on every platform.
// This one is here because it needs no arrangement -- nothing is faked, no file
// is corrupted after the fact -- and so it proves that writeAndVerify as
// composed, rather than verify in isolation, is what refuses. Unix-only for the
// same reason it is convincing: it depends on a real device this platform has.
func TestWriteAndVerifyRefusesAWriteThatVanished(t *testing.T) {
	if _, err := os.Stat(os.DevNull); err != nil {
		t.Skipf("no %s on this system: %v", os.DevNull, err)
	}
	err := writeAndVerify(os.DevNull, []byte("w-example-12\n"))
	if err == nil {
		t.Fatalf("writeAndVerify(%s) reported success; the write did not land and nothing noticed", os.DevNull)
	}
	t.Logf("refused, as it must: %v", err)
}
