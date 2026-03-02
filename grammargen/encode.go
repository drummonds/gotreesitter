package grammargen

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"

	"github.com/odvcencio/gotreesitter"
)

// Generate compiles a Grammar definition into a binary blob that
// gotreesitter can load via DecodeLanguageBlob / loadEmbeddedLanguage.
func Generate(g *Grammar) ([]byte, error) {
	// Phase 1: Normalize grammar.
	ng, err := Normalize(g)
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}

	// Phase 2: Build LR(1) parse tables.
	tables, err := buildLRTables(ng)
	if err != nil {
		return nil, fmt.Errorf("build LR tables: %w", err)
	}

	// Phase 3: Resolve conflicts.
	if err := resolveConflicts(tables, ng); err != nil {
		return nil, fmt.Errorf("resolve conflicts: %w", err)
	}

	// Phase 4: Compute lex modes based on parse table.
	tokenCount := ng.TokenCount()
	immediateTokens := make(map[int]bool)
	for _, t := range ng.Terminals {
		if t.Immediate {
			immediateTokens[t.SymbolID] = true
		}
	}

	lexModes, stateToMode := computeLexModes(
		tables.StateCount,
		tokenCount,
		func(state, sym int) bool {
			if acts, ok := tables.ActionTable[state]; ok {
				if _, ok := acts[sym]; ok {
					return true
				}
			}
			return false
		},
		ng.ExtraSymbols,
		immediateTokens,
		ng.ExternalSymbols,
	)

	// Phase 5: Build lex DFA per mode.
	lexStates, err := buildLexDFA(ng.Terminals, ng.ExtraSymbols, lexModes)
	if err != nil {
		return nil, fmt.Errorf("build lex DFA: %w", err)
	}

	// Compute lex mode offsets (cumulative DFA state counts).
	lexModeOffsets := make([]int, len(lexModes))
	offset := 0
	for i, mode := range lexModes {
		lexModeOffsets[i] = offset
		// Count DFA states for this mode by rebuilding (inefficient but correct).
		// We already built them all concatenated. Each mode's states are contiguous.
		var modePatterns []TerminalPattern
		extraSet := make(map[int]bool)
		for _, e := range ng.ExtraSymbols {
			extraSet[e] = true
		}
		for _, p := range ng.Terminals {
			if mode.validSymbols[p.SymbolID] || extraSet[p.SymbolID] {
				modePatterns = append(modePatterns, p)
			}
		}
		combined, _ := buildCombinedNFA(modePatterns)
		if combined != nil {
			dfa := subsetConstruction(combined)
			offset += len(dfa)
		}
	}

	// Phase 5b: Build keyword DFA if word token is declared.
	var keywordLexStates []gotreesitter.LexState
	if len(ng.KeywordEntries) > 0 {
		kls, err := buildLexDFA(ng.KeywordEntries, nil, []lexModeSpec{{
			validSymbols:   allSymbolsSet(ng.KeywordEntries),
			skipWhitespace: false,
		}})
		if err != nil {
			return nil, fmt.Errorf("build keyword DFA: %w", err)
		}
		keywordLexStates = kls
	}

	// Phase 6: Assemble Language struct.
	lang, err := assemble(ng, tables, lexStates, stateToMode, lexModeOffsets)
	if err != nil {
		return nil, fmt.Errorf("assemble: %w", err)
	}
	lang.Name = g.Name

	// Set keyword fields.
	if len(keywordLexStates) > 0 {
		lang.KeywordLexStates = keywordLexStates
		lang.KeywordCaptureToken = gotreesitter.Symbol(ng.WordSymbolID)
	}

	// Phase 7: Encode to binary blob.
	blob, err := encodeLanguageBlob(lang)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return blob, nil
}

// GenerateLanguage compiles a Grammar into a Language struct without encoding.
func GenerateLanguage(g *Grammar) (*gotreesitter.Language, error) {
	ng, err := Normalize(g)
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}

	tables, err := buildLRTables(ng)
	if err != nil {
		return nil, fmt.Errorf("build LR tables: %w", err)
	}

	if err := resolveConflicts(tables, ng); err != nil {
		return nil, fmt.Errorf("resolve conflicts: %w", err)
	}

	tokenCount := ng.TokenCount()
	immediateTokens := make(map[int]bool)
	for _, t := range ng.Terminals {
		if t.Immediate {
			immediateTokens[t.SymbolID] = true
		}
	}

	lexModes, stateToMode := computeLexModes(
		tables.StateCount,
		tokenCount,
		func(state, sym int) bool {
			if acts, ok := tables.ActionTable[state]; ok {
				if _, ok := acts[sym]; ok {
					return true
				}
			}
			return false
		},
		ng.ExtraSymbols,
		immediateTokens,
		ng.ExternalSymbols,
	)

	lexStates, err := buildLexDFA(ng.Terminals, ng.ExtraSymbols, lexModes)
	if err != nil {
		return nil, fmt.Errorf("build lex DFA: %w", err)
	}

	lexModeOffsets := make([]int, len(lexModes))
	offset := 0
	for i, mode := range lexModes {
		lexModeOffsets[i] = offset
		var modePatterns []TerminalPattern
		extraSet := make(map[int]bool)
		for _, e := range ng.ExtraSymbols {
			extraSet[e] = true
		}
		for _, p := range ng.Terminals {
			if mode.validSymbols[p.SymbolID] || extraSet[p.SymbolID] {
				modePatterns = append(modePatterns, p)
			}
		}
		combined, _ := buildCombinedNFA(modePatterns)
		if combined != nil {
			dfa := subsetConstruction(combined)
			offset += len(dfa)
		}
	}

	// Build keyword DFA if word token is declared.
	var keywordLexStates []gotreesitter.LexState
	if len(ng.KeywordEntries) > 0 {
		kls, err := buildLexDFA(ng.KeywordEntries, nil, []lexModeSpec{{
			validSymbols:   allSymbolsSet(ng.KeywordEntries),
			skipWhitespace: false,
		}})
		if err != nil {
			return nil, fmt.Errorf("build keyword DFA: %w", err)
		}
		keywordLexStates = kls
	}

	lang, err := assemble(ng, tables, lexStates, stateToMode, lexModeOffsets)
	if err != nil {
		return nil, fmt.Errorf("assemble: %w", err)
	}
	lang.Name = g.Name

	// Set keyword fields.
	if len(keywordLexStates) > 0 {
		lang.KeywordLexStates = keywordLexStates
		lang.KeywordCaptureToken = gotreesitter.Symbol(ng.WordSymbolID)
	}

	return lang, nil
}

// allSymbolsSet returns a set containing all symbol IDs from the patterns.
func allSymbolsSet(patterns []TerminalPattern) map[int]bool {
	s := make(map[int]bool, len(patterns))
	for _, p := range patterns {
		s[p.SymbolID] = true
	}
	return s
}

// encodeLanguageBlob serializes a Language using gob+gzip.
func encodeLanguageBlob(lang *gotreesitter.Language) ([]byte, error) {
	var out bytes.Buffer
	gzw := gzip.NewWriter(&out)
	if err := gob.NewEncoder(gzw).Encode(lang); err != nil {
		_ = gzw.Close()
		return nil, fmt.Errorf("encode language blob: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("finalize language blob: %w", err)
	}
	return out.Bytes(), nil
}
