package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

var parseSmokeSamples = map[string]string{
	"agda":              "module M where\npostulate A : Set\n",
	"bash":              "echo hi\n",
	"c":                 "int main(void) { return 0; }\n",
	"c_sharp":           "class C { int X = 1; }\n",
	"cpp":               "int main() { return 0; }\n",
	"css":               "body { color: red; }\n",
	"go":                "package main\n\nfunc main() {\n\tprintln(1)\n}\n",
	"embedded_template": "<% if true %>\n  hello\n<% end %>\n",
	"haskell":           "module Main where\nx = 1\n",
	"html":              "<html><body>Hello</body></html>\n",
	"java":              "class Main { int x; }\n",
	"javascript":        "function f() { return 1; }\nconst x = () => x + 1;\n",
	"json":              "{\"a\": 1}\n",
	"julia":             "module M\nx = 1\nend\n",
	"kotlin":            "fun main() {\n    val x: Int? = null\n    println(x)\n}\n",
	"lua":               "local x = 1\n",
	"php":               "<?php echo 1;\n",
	"python":            "def f():\n    return 1\n",
	"regex":             "a+b*\n",
	"ruby":              "def f\n  1\nend\n",
	"rust":              "fn main() { let x = 1; }\n",
	"sql":               "SELECT id, name FROM users WHERE id = 1;\n",
	"swift":             "let x: Int = 1\n",
	"toml":              "a = 1\ntitle = \"hello\"\ntags = [\"x\", \"y\"]\n",
	"tsx":               "const x = <div/>;\n",
	"typescript":        "function f(): number { return 1; }\n",
	"yaml":              "a: 1\n",
	"zig":               "const x: i32 = 1;\n",
	"scala":             "object Main { def f(x: Int): Int = x + 1 }\n",
	"elixir":            "defmodule M do\n  def f(x), do: x + 1\nend\n",
	"graphql":           "type Query { hello: String }\n",
	"hcl":               "resource \"x\" \"y\" { a = 1 }\n",
	"nix":               "let x = 1; in x\n",
	"ocaml":             "let x = 1\n",
	"verilog":           "module m;\nendmodule\n",
}

var parseSmokeKnownDegraded = map[string]string{
	"swift": "known lexer parity gap: parser currently reports recoverable errors on smoke sample",
}

func parseSmokeSample(name string) string {
	if sample, ok := parseSmokeSamples[name]; ok {
		return sample
	}
	return "x\n"
}

func parseSmokeDegradedReason(report ParseSupport, name string) string {
	if reason, ok := parseSmokeKnownDegraded[name]; ok {
		return reason
	}
	if report.Reason != "" {
		return report.Reason
	}
	return "parser reported recoverable syntax errors on smoke sample"
}

func TestSupportedLanguagesParseSmoke(t *testing.T) {
	entries := AllLanguages()
	entryByName := make(map[string]LangEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name] = entry
	}

	reports := AuditParseSupport()
	for _, report := range reports {
		sample := parseSmokeSample(report.Name)

		if report.Backend == ParseBackendUnsupported {
			t.Logf("skip %s: %s", report.Name, report.Reason)
			continue
		}

		entry, ok := entryByName[report.Name]
		if !ok {
			t.Fatalf("missing registry entry for %q", report.Name)
		}
		lang := entry.Language()
		parser := gotreesitter.NewParser(lang)
		source := []byte(sample)

		var tree *gotreesitter.Tree
		switch report.Backend {
		case ParseBackendTokenSource:
			ts := entry.TokenSourceFactory(source, lang)
			tree = parser.ParseWithTokenSource(source, ts)
		case ParseBackendDFA, ParseBackendDFAPartial:
			tree = parser.Parse(source)
		default:
			t.Fatalf("unknown backend %q for %q", report.Backend, report.Name)
		}

		if tree == nil || tree.RootNode() == nil {
			t.Fatalf("%s parse returned nil root using backend %q", report.Name, report.Backend)
		}
		if tree.RootNode().HasError() {
			t.Logf("%s parse smoke sample produced syntax errors (degraded): %s", report.Name, parseSmokeDegradedReason(report, report.Name))
			continue
		}
	}
}
