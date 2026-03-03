package grammars

import (
	"testing"

	"github.com/drummonds/gotreesitter"
)

// top50CorrectnessLanguages is a curated broad-coverage correctness gate used
// in addition to the lock-backed C parity suite.
var top50CorrectnessLanguages = []string{
	"bash",
	"c",
	"cpp",
	"c_sharp",
	"cmake",
	"css",
	"dart",
	"elixir",
	"elm",
	"erlang",
	"go",
	"gomod",
	"graphql",
	"haskell",
	"hcl",
	"html",
	"ini",
	"java",
	"javascript",
	"json",
	"json5",
	"julia",
	"kotlin",
	"lua",
	"make",
	"markdown",
	"nix",
	"objc",
	"ocaml",
	"perl",
	"php",
	"powershell",
	"python",
	"r",
	"ruby",
	"rust",
	"scala",
	"scss",
	"sql",
	"svelte",
	"swift",
	"toml",
	"tsx",
	"typescript",
	"xml",
	"yaml",
	"zig",
	"awk",
	"clojure",
	"d",
}

func TestTop50ParseSmokeNoErrors(t *testing.T) {
	entries := AllLanguages()
	entryByName := make(map[string]LangEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name] = entry
	}
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	for _, name := range top50CorrectnessLanguages {
		name := name
		t.Run(name, func(t *testing.T) {
			entry, ok := entryByName[name]
			if !ok {
				t.Fatalf("language %q not registered", name)
			}
			lang := entry.Language()
			report := EvaluateParseSupport(entry, lang)
			src := []byte(ParseSmokeSample(name))
			parser := gotreesitter.NewParser(lang)

			var (
				tree *gotreesitter.Tree
				err  error
			)
			switch report.Backend {
			case ParseBackendTokenSource:
				if entry.TokenSourceFactory == nil {
					t.Fatalf("token source backend without factory for %q", name)
				}
				tree, err = parser.ParseWithTokenSource(src, entry.TokenSourceFactory(src, lang))
			case ParseBackendDFA, ParseBackendDFAPartial:
				tree, err = parser.Parse(src)
			default:
				t.Fatalf("unsupported parse backend %q for %q", report.Backend, name)
			}
			if err != nil {
				t.Fatalf("%s parse failed: %v", name, err)
			}
			if tree == nil || tree.RootNode() == nil {
				t.Fatalf("%s parse returned nil root", name)
			}
			defer tree.Release()

			root := tree.RootNode()
			if root.EndByte() != uint32(len(src)) {
				t.Fatalf("%s parse truncated: root.EndByte=%d sourceLen=%d", name, root.EndByte(), len(src))
			}
			if root.HasError() {
				t.Fatalf("%s smoke sample produced error nodes", name)
			}
		})
	}
}
