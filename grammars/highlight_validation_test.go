package grammars

import (
	"strings"
	"testing"

	"codeberg.org/hum3/gotreesitter"
)

// Languages where the highlight query compiles but the smoke sample is too
// simple to produce any highlight ranges. These are not bugs.
var highlightNoRangesExpected = map[string]bool{
	"jq":      true, // sample ".foo" has no nodes matching jq highlights
	"jsdoc":   true, // sample "/** hello */" has no nodes matching jsdoc highlights
	"nginx":   true, // sample "events {}" does not exercise most nginx captures
	"svelte":  true, // sample is plain HTML text, no svelte-specific nodes
	"wolfram": true, // sample "1 + 2" has no nodes matching wolfram highlights
	"cpp":     true, // current smoke sample is too small for useful capture coverage
	"haskell": true, // sample is intentionally tiny and misses most capture paths
	"haxe":    true, // sample "1;" intentionally minimal
	"tsx":     true, // sample has limited syntax for broad TSX highlight rules
}

func TestAllHighlightQueriesCompile(t *testing.T) {
	entries := AllLanguages()
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	var withQuery int
	var compileErrs int

	for _, entry := range entries {
		if strings.TrimSpace(entry.HighlightQuery) == "" {
			continue
		}
		withQuery++
		lang := entry.Language()
		if _, err := gotreesitter.NewQuery(entry.HighlightQuery, lang); err != nil {
			compileErrs++
			t.Errorf("%s: highlight query compile error: %v", entry.Name, err)
		}
		UnloadEmbeddedLanguage(entry.Name + ".bin")
	}

	t.Logf("highlight compile audit: with_query=%d compile_errors=%d", withQuery, compileErrs)
}

func TestAllTagsQueriesCompile(t *testing.T) {
	entries := AllLanguages()
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	var withQuery int
	var compileErrs int

	for _, entry := range entries {
		if strings.TrimSpace(entry.TagsQuery) == "" {
			continue
		}
		withQuery++
		lang := entry.Language()
		if _, err := gotreesitter.NewTagger(lang, entry.TagsQuery); err != nil {
			compileErrs++
			t.Errorf("%s: tags query compile error: %v", entry.Name, err)
		}
		UnloadEmbeddedLanguage(entry.Name + ".bin")
	}

	t.Logf("tags compile audit: with_query=%d compile_errors=%d", withQuery, compileErrs)
}

func TestHighlightQueriesProduceResults(t *testing.T) {
	entries := AllLanguages()
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	var tested, skippedNoQuery, skippedNoSample, skippedUnsupported int
	for _, entry := range entries {
		name := entry.Name
		if strings.TrimSpace(entry.HighlightQuery) == "" {
			skippedNoQuery++
			continue
		}

		lang := entry.Language()
		report := EvaluateParseSupport(entry, lang)
		if report.Backend == ParseBackendUnsupported {
			skippedUnsupported++
			UnloadEmbeddedLanguage(entry.Name + ".bin")
			continue
		}

		sample := ParseSmokeSample(name)
		if sample == "x\n" {
			skippedNoSample++
			UnloadEmbeddedLanguage(entry.Name + ".bin")
			continue
		}

		tested++
		t.Run(name, func(t *testing.T) {
			// Build highlighter options.
			var opts []gotreesitter.HighlighterOption
			if entry.TokenSourceFactory != nil {
				factory := entry.TokenSourceFactory
				opts = append(opts, gotreesitter.WithTokenSourceFactory(
					func(src []byte) gotreesitter.TokenSource {
						return factory(src, lang)
					},
				))
			}

			h, err := gotreesitter.NewHighlighter(lang, entry.HighlightQuery, opts...)
			if err != nil {
				t.Fatalf("compile highlight query: %v", err)
			}

			ranges := h.Highlight([]byte(sample))
			if len(ranges) == 0 && !highlightNoRangesExpected[name] {
				t.Errorf("highlight query compiled but produced 0 ranges for sample %q", sample)
			}
		})
		UnloadEmbeddedLanguage(entry.Name + ".bin")
	}

	t.Logf("highlight validation: tested=%d skipped(no_query=%d no_sample=%d unsupported=%d)",
		tested, skippedNoQuery, skippedNoSample, skippedUnsupported)

}
