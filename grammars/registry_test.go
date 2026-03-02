package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestDetectLanguageGo(t *testing.T) {
	entry := DetectLanguage("main.go")
	if entry == nil {
		t.Fatal("expected to detect Go language for main.go, got nil")
	}
	if entry.Name != "go" {
		t.Fatalf("expected language name %q, got %q", "go", entry.Name)
	}
	if entry.TokenSourceFactory == nil {
		t.Fatal("expected Go language to register a TokenSourceFactory")
	}
}

func TestDetectLanguageUnknown(t *testing.T) {
	entry := DetectLanguage("readme.xyz")
	if entry != nil {
		t.Fatalf("expected nil for unknown extension, got %q", entry.Name)
	}
}

func TestAllLanguages(t *testing.T) {
	langs := AllLanguages()
	if len(langs) == 0 {
		t.Fatal("expected at least one registered language, got 0")
	}

	found := false
	for _, l := range langs {
		if l.Name == "go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected Go language to be registered")
	}
}

func TestDetectLanguageByShebang(t *testing.T) {
	// Linguist interpreter map resolves shebangs.
	entry := DetectLanguageByShebang("#!/usr/bin/env python3")
	if entry == nil || entry.Name != "python" {
		name := ""
		if entry != nil {
			name = entry.Name
		}
		t.Fatalf("DetectLanguageByShebang(python3) = %q, want %q", name, "python")
	}

	// Unknown interpreter returns nil.
	entry = DetectLanguageByShebang("#!/usr/bin/env nonexistent_interp_xyz")
	if entry != nil {
		t.Fatalf("expected nil for unknown interpreter, got %q", entry.Name)
	}
}

// parseSupportForLang evaluates parse support for a single language by name,
// loading only that grammar instead of all 206.
func parseSupportForLang(t *testing.T, name string) ParseSupport {
	t.Helper()
	entries := AllLanguages()
	for _, entry := range entries {
		if entry.Name == name {
			lang := entry.Language()
			t.Cleanup(func() { UnloadEmbeddedLanguage(entry.Name + ".bin") })
			return EvaluateParseSupport(entry, lang)
		}
	}
	t.Fatalf("language %q not registered", name)
	return ParseSupport{}
}

func TestAuditParseSupportIncludesGoCustomTokenSource(t *testing.T) {
	report := parseSupportForLang(t, "go")
	if report.Backend != ParseBackendTokenSource {
		t.Fatalf("expected go backend %q, got %q", ParseBackendTokenSource, report.Backend)
	}
}

func TestAuditParseSupportIncludesCCustomTokenSource(t *testing.T) {
	report := parseSupportForLang(t, "c")
	if report.Backend != ParseBackendTokenSource {
		t.Fatalf("expected c backend %q, got %q", ParseBackendTokenSource, report.Backend)
	}
}

func TestAuditParseSupportIncludesCppDFA(t *testing.T) {
	report := parseSupportForLang(t, "cpp")
	if report.Backend != ParseBackendDFA {
		t.Fatalf("expected cpp backend %q, got %q", ParseBackendDFA, report.Backend)
	}
}

func TestAuditParseSupportIncludesJSONCustomTokenSource(t *testing.T) {
	report := parseSupportForLang(t, "json")
	if report.Backend != ParseBackendTokenSource {
		t.Fatalf("expected json backend %q, got %q", ParseBackendTokenSource, report.Backend)
	}
}

func TestAuditParseSupportIncludesJavaCustomTokenSource(t *testing.T) {
	report := parseSupportForLang(t, "java")
	if report.Backend != ParseBackendTokenSource {
		t.Fatalf("expected java backend %q, got %q", ParseBackendTokenSource, report.Backend)
	}
}

func TestAuditParseSupportIncludesLuaCustomTokenSource(t *testing.T) {
	report := parseSupportForLang(t, "lua")
	if report.Backend != ParseBackendTokenSource {
		t.Fatalf("expected lua backend %q, got %q", ParseBackendTokenSource, report.Backend)
	}
}

func TestAuditParseSupportIncludesJavaScriptDFA(t *testing.T) {
	report := parseSupportForLang(t, "javascript")
	if report.Backend != ParseBackendDFA {
		t.Fatalf("expected javascript backend %q, got %q", ParseBackendDFA, report.Backend)
	}
}

func TestAuditParseSupportIncludesTypeScriptDFA(t *testing.T) {
	report := parseSupportForLang(t, "typescript")
	if report.Backend != ParseBackendDFA {
		t.Fatalf("expected typescript backend %q, got %q", ParseBackendDFA, report.Backend)
	}
}

func TestAuditParseSupportIncludesRustDFA(t *testing.T) {
	report := parseSupportForLang(t, "rust")
	if report.Backend != ParseBackendDFA {
		t.Fatalf("expected rust backend %q, got %q", ParseBackendDFA, report.Backend)
	}
}

func TestCoreLanguagesHaveCompilableTagsQuery(t *testing.T) {
	core := []string{
		"go",
		"python",
		"javascript",
		"typescript",
		"tsx",
		"rust",
		"java",
		"c",
		"cpp",
	}

	entries := AllLanguages()
	entryByName := make(map[string]LangEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name] = entry
	}

	for _, name := range core {
		name := name
		t.Run(name, func(t *testing.T) {
			entry, ok := entryByName[name]
			if !ok {
				t.Fatalf("expected %q language to be registered", name)
			}
			if entry.Language == nil {
				t.Fatalf("expected %q language loader", name)
			}
			if entry.TagsQuery == "" {
				t.Fatalf("expected non-empty TagsQuery for %q", name)
			}
			if _, err := gotreesitter.NewTagger(entry.Language(), entry.TagsQuery); err != nil {
				t.Fatalf("compile tags query for %q: %v", name, err)
			}
		})
	}
}

func TestInferredTagsQueryCoverage(t *testing.T) {
	entries := AllLanguages()
	if len(entries) == 0 {
		t.Fatal("expected registered languages")
	}

	withTags := 0
	for _, entry := range entries {
		if entry.TagsQuery != "" {
			withTags++
		}
	}

	// Core set (9) is explicit. Inference should expand this materially.
	if withTags < 30 {
		t.Fatalf("expected inferred tags query coverage to be >=30 languages, got %d", withTags)
	}
}

func TestDetectLanguageByName(t *testing.T) {
	tests := []struct {
		input    string
		wantName string // empty = expect nil
	}{
		// Direct grammar name always works (even with empty linguist map).
		{"go", "go"},
		{"python", "python"},
		{"javascript", "javascript"},
		// Unknown.
		{"nonexistent_language_xyz", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := DetectLanguageByName(tt.input)
		if tt.wantName == "" {
			if got != nil {
				t.Errorf("DetectLanguageByName(%q) = %q, want nil", tt.input, got.Name)
			}
		} else {
			if got == nil {
				t.Errorf("DetectLanguageByName(%q) = nil, want %q", tt.input, tt.wantName)
			} else if got.Name != tt.wantName {
				t.Errorf("DetectLanguageByName(%q) = %q, want %q", tt.input, got.Name, tt.wantName)
			}
		}
	}
}

func TestDisplayName(t *testing.T) {
	// Linguist-mapped name.
	entry := &LangEntry{Name: "c_sharp"}
	got := DisplayName(entry)
	if got != "C#" {
		t.Errorf("DisplayName(c_sharp) = %q, want %q", got, "C#")
	}
	// Fallback to title-case for unmapped names.
	entry2 := &LangEntry{Name: "some_unknown_lang"}
	got2 := DisplayName(entry2)
	if got2 != "Some Unknown Lang" {
		t.Errorf("DisplayName(some_unknown_lang) fallback = %q, want %q", got2, "Some Unknown Lang")
	}
	if DisplayName(nil) != "" {
		t.Error("DisplayName(nil) should return empty string")
	}
}

func TestDetectLanguageByNameAliases(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
	}{
		// Linguist canonical names (mixed case).
		{"Go", "go"},
		{"Python", "python"},
		{"JavaScript", "javascript"},
		{"TypeScript", "typescript"},
		{"C++", "cpp"},
		{"C#", "c_sharp"},
		{"Objective-C", "objc"},
		{"F#", "fsharp"},
		{"Shell", "bash"},
		{"Makefile", "make"},
		{"TSX", "tsx"},
		{"Rust", "rust"},
		{"Ruby", "ruby"},
		{"Java", "java"},
		{"HTML", "html"},
		{"CSS", "css"},
		{"YAML", "yaml"},
		{"TOML", "toml"},
		{"SQL", "sql"},
		{"Kotlin", "kotlin"},
		{"Swift", "swift"},
		{"Scala", "scala"},
		{"Elixir", "elixir"},
		// Linguist aliases.
		{"golang", "go"},
		{"js", "javascript"},
		{"ts", "typescript"},
		{"py", "python"},
		{"rb", "ruby"},
		{"rs", "rust"},
		// Case insensitivity.
		{"PYTHON", "python"},
		{"c++", "cpp"},
		{"f#", "fsharp"},
		{"shell", "bash"},
		{"javascript", "javascript"},
		{"makefile", "make"},
		// Edge: gotreesitter name directly.
		{"cpp", "cpp"},
		{"c_sharp", "c_sharp"},
		{"objc", "objc"},
		{"fsharp", "fsharp"},
		{"bash", "bash"},
	}
	for _, tt := range tests {
		got := DetectLanguageByName(tt.input)
		if got == nil {
			t.Errorf("DetectLanguageByName(%q) = nil, want %q", tt.input, tt.wantName)
		} else if got.Name != tt.wantName {
			t.Errorf("DetectLanguageByName(%q) = %q, want %q", tt.input, got.Name, tt.wantName)
		}
	}
}

func TestDisplayNamePopulated(t *testing.T) {
	tests := []struct {
		grammar string
		want    string
	}{
		{"cpp", "C++"},
		{"c_sharp", "C#"},
		{"objc", "Objective-C"},
		{"fsharp", "F#"},
		{"javascript", "JavaScript"},
		{"typescript", "TypeScript"},
		{"bash", "Shell"},
		{"make", "Makefile"},
		{"go", "Go"},
		{"python", "Python"},
		{"rust", "Rust"},
		{"ruby", "Ruby"},
		{"java", "Java"},
		{"html", "HTML"},
		{"css", "CSS"},
		{"yaml", "YAML"},
		{"sql", "SQL"},
	}
	for _, tt := range tests {
		entry := lookupByName(tt.grammar)
		if entry == nil {
			t.Errorf("lookupByName(%q) = nil", tt.grammar)
			continue
		}
		got := DisplayName(entry)
		if got != tt.want {
			t.Errorf("DisplayName(%q) = %q, want %q", tt.grammar, got, tt.want)
		}
	}
}
