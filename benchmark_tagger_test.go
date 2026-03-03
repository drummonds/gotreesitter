package gotreesitter_test

import (
	"bytes"
	"testing"

	"github.com/drummonds/gotreesitter"
	"github.com/drummonds/gotreesitter/grammars"
)

// benchTagsQuery is a tags query suitable for Go source.
// It matches function/method definitions and call references.
const benchTagsQuery = `
(function_declaration (identifier) @name) @definition.function
(method_declaration (field_identifier) @name) @definition.method
(call_expression (identifier) @name) @reference.call
(call_expression (selector_expression (field_identifier) @name)) @reference.call
(type_declaration (type_spec (type_identifier) @name)) @definition.type
`

// BenchmarkTaggerTag measures tagging a 500-function Go file from scratch.
func BenchmarkTaggerTag(b *testing.B) {
	entry := grammars.DetectLanguage("main.go")
	if entry == nil {
		b.Skip("Go grammar not available")
	}

	lang := entry.Language()
	src := makeGoBenchmarkSource(benchmarkFuncCount(b))

	var opts []gotreesitter.TaggerOption
	if entry.TokenSourceFactory != nil {
		factory := entry.TokenSourceFactory
		opts = append(opts, gotreesitter.WithTaggerTokenSourceFactory(func(s []byte) gotreesitter.TokenSource {
			return factory(s, lang)
		}))
	}

	tagger, err := gotreesitter.NewTagger(lang, benchTagsQuery, opts...)
	if err != nil {
		b.Fatalf("NewTagger failed: %v", err)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tags := tagger.Tag(src)
		if len(tags) == 0 {
			b.Fatal("tagger returned no tags")
		}
	}
}

// BenchmarkTaggerTagIncremental measures re-tagging after a single-byte edit.
func BenchmarkTaggerTagIncremental(b *testing.B) {
	entry := grammars.DetectLanguage("main.go")
	if entry == nil {
		b.Skip("Go grammar not available")
	}

	lang := entry.Language()
	src := makeGoBenchmarkSource(benchmarkFuncCount(b))

	// Locate the edit site: "v := 0" -> toggle the '0'.
	editAt := bytes.Index(src, []byte("v := 0"))
	if editAt < 0 {
		b.Fatal("could not find edit marker")
	}
	editAt += len("v := ")
	start := pointAtOffset(src, editAt)
	end := pointAtOffset(src, editAt+1)

	// Build the initial tree.
	parser := gotreesitter.NewParser(lang)
	ts := mustGoTokenSource(b, src, lang)
	tree, err := parser.ParseWithTokenSource(src, ts)
	if err != nil {
		b.Fatalf("initial parse failed: %v", err)
	}
	if tree.RootNode() == nil {
		b.Fatal("initial parse returned nil root")
	}

	var opts []gotreesitter.TaggerOption
	if entry.TokenSourceFactory != nil {
		factory := entry.TokenSourceFactory
		opts = append(opts, gotreesitter.WithTaggerTokenSourceFactory(func(s []byte) gotreesitter.TokenSource {
			return factory(s, lang)
		}))
	}

	tagger, err := gotreesitter.NewTagger(lang, benchTagsQuery, opts...)
	if err != nil {
		b.Fatalf("NewTagger failed: %v", err)
	}

	edit := gotreesitter.InputEdit{
		StartByte:   uint32(editAt),
		OldEndByte:  uint32(editAt + 1),
		NewEndByte:  uint32(editAt + 1),
		StartPoint:  start,
		OldEndPoint: end,
		NewEndPoint: end,
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Toggle one ASCII digit in place.
		if src[editAt] == '0' {
			src[editAt] = '1'
		} else {
			src[editAt] = '0'
		}

		tree.Edit(edit)
		tags, newTree := tagger.TagIncremental(src, tree)
		if len(tags) == 0 {
			b.Fatal("incremental tagger returned no tags")
		}
		if newTree != tree {
			tree.Release()
		}
		tree = newTree
	}
	tree.Release()
}
