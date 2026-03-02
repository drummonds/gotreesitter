//go:build cgo && treesitter_c_parity

package cgoharness

import (
	"os"
	"os/exec"
	"testing"
)

// TestParityGLRFloorElixir ensures the minimal explicit GLR cap used in tests
// remains parity-safe for Elixir. This protects against future pruning changes
// that might regress correctness at GOT_GLR_MAX_STACKS=1.
func TestParityGLRFloorElixir(t *testing.T) {
	if testing.Short() {
		t.Skip("skip subprocess parity floor check in -short mode")
	}

	cmd := exec.Command(
		"go", "test", ".", "-tags", "treesitter_c_parity",
		"-run", "^TestParityFreshParse/elixir$", "-count=1",
	)
	cmd.Env = append(os.Environ(), "GOT_GLR_MAX_STACKS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected parity success at GOT_GLR_MAX_STACKS=1; output:\n%s", string(out))
	}
}
