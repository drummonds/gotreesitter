package grammars

import (
	"testing"

	"codeberg.org/hum3/gotreesitter"
)

func TestParseFile(t *testing.T) {
	bt, err := ParseFile("main.go", []byte("package main\n\nfunc main() {}\n"))
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	root := bt.RootNode()
	if root == nil {
		t.Fatal("ParseFile returned nil root")
	}
	if got := bt.NodeType(root); got != "source_file" {
		t.Errorf("root type = %q, want %q", got, "source_file")
	}
}

func TestParseFileUnknownExtension(t *testing.T) {
	_, err := ParseFile("file.xyz", []byte("hello"))
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
}

func TestParseFileEmptySource(t *testing.T) {
	bt, err := ParseFile("main.go", []byte{})
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()
}

func TestParseFilePython(t *testing.T) {
	bt, err := ParseFile("script.py", []byte("def hello():\n    pass\n"))
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	defer bt.Release()

	if bt.RootNode() == nil {
		t.Fatal("ParseFile returned nil root for Python")
	}

	found := false
	gotreesitter.Walk(bt.RootNode(), func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if bt.NodeType(node) == "function_definition" {
			found = true
			return gotreesitter.WalkStop
		}
		return gotreesitter.WalkContinue
	})
	if !found {
		t.Error("expected to find function_definition in Python parse tree")
	}
}
