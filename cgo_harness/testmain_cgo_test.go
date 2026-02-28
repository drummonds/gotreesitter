//go:build cgo && treesitter_c_parity

package cgoharness

import (
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Corpus parity uses larger synthetic inputs; keep a higher node budget so
	// full-parse correctness checks are not dominated by budget truncation.
	if strings.TrimSpace(os.Getenv("GOT_PARSE_NODE_LIMIT_SCALE")) == "" {
		_ = os.Setenv("GOT_PARSE_NODE_LIMIT_SCALE", "4")
	}
	os.Exit(m.Run())
}
