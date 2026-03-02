// Package grammars provides 206 embedded tree-sitter grammars as compressed
// binary blobs with lazy loading. Use AllLanguages to enumerate available
// grammars, DetectLanguage to match by file extension or shebang, or call
// individual language functions (e.g. GoLanguage()) for direct access.
package grammars

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// LangEntry holds a registered language with its grammar, extensions, and highlight query.
type LangEntry struct {
	Name               string
	Extensions         []string                      // e.g. [".go", ".mod"]
	Shebangs           []string                      // e.g. ["#!/usr/bin/env python"]
	Language           func() *gotreesitter.Language // lazy loader
	HighlightQuery     string
	TagsQuery          string                                                                 // tree-sitter tags.scm query for symbol extraction
	TokenSourceFactory func(src []byte, lang *gotreesitter.Language) gotreesitter.TokenSource // nil = use DFA
	Quality            ParseQuality                                                           // populated lazily by AllLanguages
}

var registry []LangEntry

// Register adds a language to the registry.
func Register(entry LangEntry) {
	if !languageEnabled(entry.Name) {
		return
	}
	if entry.TokenSourceFactory == nil {
		entry.TokenSourceFactory = defaultTokenSourceFactory(entry.Name)
	}
	registry = append(registry, entry)
}

// DetectLanguage returns the LangEntry for a filename, or nil if unknown.
// Matches by extension first, then shebang.
func DetectLanguage(filename string) *LangEntry {
	// match by extension
	for i := range registry {
		for _, ext := range registry[i].Extensions {
			if strings.HasSuffix(filename, ext) {
				return &registry[i]
			}
		}
	}
	return nil
}

// DetectLanguageByShebang checks the first line of content for shebang matches.
func DetectLanguageByShebang(firstLine string) *LangEntry {
	for i := range registry {
		for _, shebang := range registry[i].Shebangs {
			if strings.HasPrefix(firstLine, shebang) {
				return &registry[i]
			}
		}
	}
	return nil
}

// AllLanguages returns all registered languages.
func AllLanguages() []LangEntry {
	out := make([]LangEntry, len(registry))
	copy(out, registry)
	for i := range out {
		if strings.TrimSpace(out[i].TagsQuery) != "" {
			continue
		}
		out[i].TagsQuery = inferredTagsQuery(out[i])
	}
	return out
}

// lookupByName returns the LangEntry with the given grammar name, or nil.
func lookupByName(name string) *LangEntry {
	for i := range registry {
		if registry[i].Name == name {
			return &registry[i]
		}
	}
	return nil
}

// normalizeLinguistKey lowercases and trims input, preserving special
// characters (+, #, etc.) so "C++" and "F#" map correctly.
func normalizeLinguistKey(name string) string {
	return strings.TrimSpace(strings.ToLower(name))
}

// DetectLanguageByName returns the LangEntry for any linguist canonical name,
// alias, or gotreesitter grammar name. Returns nil if unknown.
//
// Accepts: "C++", "cpp", "Go", "golang", "Shell", "bash", "F#", "fsharp", etc.
func DetectLanguageByName(name string) *LangEntry {
	key := normalizeLinguistKey(name)
	if grammarName, ok := linguistToGrammar[key]; ok {
		return lookupByName(grammarName)
	}
	return lookupByName(key)
}

// DisplayName returns the linguist canonical display name for a language
// (e.g., "C++" for cpp, "JavaScript" for javascript). Falls back to
// title-casing the grammar name if no linguist match exists.
func DisplayName(entry *LangEntry) string {
	if entry == nil {
		return ""
	}
	if dn, ok := grammarDisplayNames[entry.Name]; ok {
		return dn
	}
	// Fallback: title-case with underscores as spaces.
	words := strings.Split(entry.Name, "_")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
