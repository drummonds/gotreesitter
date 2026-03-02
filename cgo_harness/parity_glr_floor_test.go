//go:build cgo && treesitter_c_parity

package cgoharness

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestParityGLRFloorElixir ensures the known GLR floor remains visible:
// with GOT_GLR_MAX_STACKS=1, elixir fresh-parse parity is expected to fail.
// This guards against silently lowering the cap below the current safe floor.
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
	if err == nil {
		t.Fatalf("expected parity failure at GOT_GLR_MAX_STACKS=1; output:\n%s", string(out))
	}
	output := string(out)
	if !strings.Contains(output, "FAIL") || !strings.Contains(output, "elixir") {
		t.Fatalf("unexpected subprocess output:\n%s", output)
	}
}
