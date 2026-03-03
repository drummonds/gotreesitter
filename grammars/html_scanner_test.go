package grammars

import (
	"testing"

	gotreesitter "github.com/drummonds/gotreesitter"
)

func TestHTMLScannerParseCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty html", "<html></html>"},
		{"html+body", "<html><body></body></html>"},
		{"html+head", "<html><head></head></html>"},
		{"html+head+body", "<html><head></head><body></body></html>"},
		{"head+title", "<html><head><title>T</title></head></html>"},
		{"head+title+body", "<html><head><title>T</title></head><body></body></html>"},
		{"full doc", "<html><head><title>T</title></head><body><p>X</p></body></html>"},
		{"body only with text", "<html><body>Hello</body></html>"},
		{"implicit head close", "<html><head><body></body></html>"},
		{"two p siblings", "<div><p>A</p><p>B</p></div>"},
		{"two li siblings", "<ul><li>A</li><li>B</li></ul>"},
		{"two span siblings", "<div><span>A</span><span>B</span></div>"},
		{"attributes", `<div class="main"><p id="x">Hello</p></div>`},
		{"self closing", `<br/><img src="x.png"/>`},
		{"comment", `<div><!-- comment --><p>X</p></div>`},
		{"doctype", `<!DOCTYPE html><html><body>X</body></html>`},
		{"deeply nested", "<div><div><div><p>X</p></div></div></div>"},
		{"script", "<html><head><script>var x = 1;</script></head></html>"},
		{"style", "<html><head><style>.x { color: red; }</style></head></html>"},
	}

	lang := HtmlLanguage()
	parser := gotreesitter.NewParser(lang)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, err := parser.Parse([]byte(tt.input))
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			bt := gotreesitter.Bind(tree)
			root := bt.RootNode()
			rootType := bt.NodeType(root)
			hasErr := root.HasError()

			if rootType != "document" {
				t.Errorf("expected root type 'document', got %q", rootType)
			}
			if hasErr {
				t.Errorf("expected no errors, but HasError=true for %q", tt.input)
				// Dump tree on failure for debugging
				gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
					indent := ""
					for i := 0; i < depth; i++ {
						indent += "  "
					}
					text := bt.NodeText(node)
					if len(text) > 60 {
						text = text[:60] + "..."
					}
					t.Logf("%s[%s] sym=%d err=%v %q", indent, bt.NodeType(node), node.Symbol(), node.HasError(), text)
					if depth > 6 {
						return gotreesitter.WalkSkipChildren
					}
					return gotreesitter.WalkContinue
				})
			}
			tree.Release()
		})
	}
}
