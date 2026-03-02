package grammargen

import (
	"fmt"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// ConflictKind describes the type of LR conflict.
type ConflictKind int

const (
	ShiftReduce  ConflictKind = iota
	ReduceReduce
)

// ConflictDiag describes a conflict encountered during LR table construction.
type ConflictDiag struct {
	Kind       ConflictKind
	State      int
	LookaheadSym int
	Actions    []lrAction // the conflicting actions
	Resolution string     // how it was resolved (or "GLR" if kept)
}

func (d *ConflictDiag) String(ng *NormalizedGrammar) string {
	var b strings.Builder
	symName := func(id int) string {
		if id >= 0 && id < len(ng.Symbols) {
			return ng.Symbols[id].Name
		}
		return fmt.Sprintf("sym_%d", id)
	}
	prodStr := func(prodIdx int) string {
		if prodIdx < 0 || prodIdx >= len(ng.Productions) {
			return fmt.Sprintf("prod_%d", prodIdx)
		}
		p := &ng.Productions[prodIdx]
		var rhs []string
		for _, s := range p.RHS {
			rhs = append(rhs, symName(s))
		}
		return fmt.Sprintf("%s → %s", symName(p.LHS), strings.Join(rhs, " "))
	}

	switch d.Kind {
	case ShiftReduce:
		fmt.Fprintf(&b, "Shift/reduce conflict in state %d on %q:\n",
			d.State, symName(d.LookaheadSym))
		for _, a := range d.Actions {
			switch a.kind {
			case lrShift:
				fmt.Fprintf(&b, "  Shift → state %d (prec %d)\n", a.state, a.prec)
			case lrReduce:
				p := &ng.Productions[a.prodIdx]
				assocStr := ""
				switch p.Assoc {
				case AssocLeft:
					assocStr = ", left-associative"
				case AssocRight:
					assocStr = ", right-associative"
				}
				fmt.Fprintf(&b, "  Reduce: %s (prec %d%s)\n", prodStr(a.prodIdx), p.Prec, assocStr)
			}
		}
	case ReduceReduce:
		fmt.Fprintf(&b, "Reduce/reduce conflict in state %d on %q:\n",
			d.State, symName(d.LookaheadSym))
		for _, a := range d.Actions {
			p := &ng.Productions[a.prodIdx]
			fmt.Fprintf(&b, "  Reduce: %s (prec %d)\n", prodStr(a.prodIdx), p.Prec)
		}
	}
	fmt.Fprintf(&b, "  Resolution: %s", d.Resolution)
	return b.String()
}

// GenerateReport holds the result of grammar generation with diagnostics.
type GenerateReport struct {
	Language    *gotreesitter.Language
	Blob        []byte
	Conflicts   []ConflictDiag
	Warnings    []string
	SymbolCount int
	StateCount  int
	TokenCount  int
}

// resolveConflictsWithDiag is like resolveConflicts but collects diagnostics.
func resolveConflictsWithDiag(tables *LRTables, ng *NormalizedGrammar) ([]ConflictDiag, error) {
	var diags []ConflictDiag
	for state, actions := range tables.ActionTable {
		for sym, acts := range actions {
			if len(acts) <= 1 {
				continue
			}

			diag := ConflictDiag{
				State:        state,
				LookaheadSym: sym,
				Actions:      append([]lrAction{}, acts...),
			}

			// Classify conflict.
			hasShift, hasReduce := false, false
			for _, a := range acts {
				if a.kind == lrShift {
					hasShift = true
				}
				if a.kind == lrReduce {
					hasReduce = true
				}
			}
			if hasShift && hasReduce {
				diag.Kind = ShiftReduce
			} else {
				diag.Kind = ReduceReduce
			}

			resolved, err := resolveActionConflict(acts, ng)
			if err != nil {
				return diags, fmt.Errorf("state %d, symbol %d: %w", state, sym, err)
			}
			tables.ActionTable[state][sym] = resolved

			// Determine resolution description.
			switch {
			case len(resolved) > 1:
				diag.Resolution = "GLR (multiple actions kept)"
			case len(resolved) == 1 && resolved[0].kind == lrShift:
				diag.Resolution = "shift wins"
				if hasReduce {
					for _, a := range acts {
						if a.kind == lrReduce {
							p := &ng.Productions[a.prodIdx]
							if p.Prec > 0 || resolved[0].prec > 0 {
								diag.Resolution = fmt.Sprintf("shift wins (prec %d > %d)", resolved[0].prec, p.Prec)
							} else if p.Assoc == AssocRight {
								diag.Resolution = "shift wins (right-associative)"
							} else {
								diag.Resolution = "shift wins (default yacc behavior)"
							}
							break
						}
					}
				}
			case len(resolved) == 1 && resolved[0].kind == lrReduce:
				prod := &ng.Productions[resolved[0].prodIdx]
				if prod.Assoc == AssocLeft {
					diag.Resolution = "reduce wins (left-associative)"
				} else {
					diag.Resolution = fmt.Sprintf("reduce wins (prec %d)", prod.Prec)
				}
			case len(resolved) == 0:
				diag.Resolution = "error (non-associative)"
			}

			diags = append(diags, diag)
		}
	}
	return diags, nil
}

// Validate checks the grammar for common issues and returns warnings.
func Validate(g *Grammar) []string {
	var warnings []string

	if len(g.RuleOrder) == 0 {
		warnings = append(warnings, "grammar has no rules defined")
		return warnings
	}

	// Check for undefined symbol references.
	defined := make(map[string]bool)
	for _, name := range g.RuleOrder {
		defined[name] = true
	}
	// External symbols are also valid references.
	for _, ext := range g.Externals {
		if ext.Kind == RuleSymbol && ext.Value != "" {
			defined[ext.Value] = true
		}
	}
	for _, name := range g.RuleOrder {
		refs := collectSymbolRefs(g.Rules[name])
		for _, ref := range refs {
			if !defined[ref] {
				warnings = append(warnings, fmt.Sprintf("rule %q references undefined symbol %q", name, ref))
			}
		}
	}

	// Check for unreachable rules (not reachable from start symbol).
	reachable := make(map[string]bool)
	var walk func(name string)
	walk = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		if rule, ok := g.Rules[name]; ok {
			for _, ref := range collectSymbolRefs(rule) {
				walk(ref)
			}
		}
	}
	walk(g.RuleOrder[0]) // start from start symbol
	// Extras and externals can reference rules too.
	for _, extra := range g.Extras {
		for _, ref := range collectSymbolRefs(extra) {
			walk(ref)
		}
	}
	for _, ext := range g.Externals {
		for _, ref := range collectSymbolRefs(ext) {
			walk(ref)
		}
	}
	for _, name := range g.RuleOrder {
		if !reachable[name] {
			warnings = append(warnings, fmt.Sprintf("rule %q is unreachable from start symbol %q", name, g.RuleOrder[0]))
		}
	}

	// Check for empty choice alternatives.
	for _, name := range g.RuleOrder {
		checkEmptyChoice(g.Rules[name], name, &warnings)
	}

	// Check conflicts reference existing rules.
	for i, group := range g.Conflicts {
		for _, sym := range group {
			if !defined[sym] {
				warnings = append(warnings, fmt.Sprintf("conflict group %d references undefined rule %q", i, sym))
			}
		}
	}

	// Check supertypes reference existing rules.
	for _, st := range g.Supertypes {
		if !defined[st] {
			warnings = append(warnings, fmt.Sprintf("supertype %q is not a defined rule", st))
		}
	}

	// Check word token is defined.
	if g.Word != "" && !defined[g.Word] {
		warnings = append(warnings, fmt.Sprintf("word token %q is not a defined rule", g.Word))
	}

	return warnings
}

// collectSymbolRefs returns all symbol references in a rule tree.
func collectSymbolRefs(r *Rule) []string {
	if r == nil {
		return nil
	}
	var refs []string
	if r.Kind == RuleSymbol {
		refs = append(refs, r.Value)
	}
	for _, child := range r.Children {
		refs = append(refs, collectSymbolRefs(child)...)
	}
	return refs
}

// checkEmptyChoice warns about choice rules with blank alternatives
// that might indicate a mistake (usually Optional should be used instead).
func checkEmptyChoice(r *Rule, ruleName string, warnings *[]string) {
	if r == nil {
		return
	}
	for _, child := range r.Children {
		checkEmptyChoice(child, ruleName, warnings)
	}
}

// RunTests generates the grammar and runs all embedded test cases.
// Returns nil if all tests pass, or an error describing failures.
func RunTests(g *Grammar) error {
	if len(g.Tests) == 0 {
		return nil
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		return fmt.Errorf("generate failed: %w", err)
	}

	var failures []string
	for _, tc := range g.Tests {
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse([]byte(tc.Input))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: parse error: %v", tc.Name, err))
			continue
		}

		sexp := tree.RootNode().SExpr(lang)
		hasError := strings.Contains(sexp, "ERROR")

		if tc.ExpectError {
			if !hasError {
				failures = append(failures, fmt.Sprintf("%s: expected ERROR nodes but got: %s", tc.Name, sexp))
			}
			continue
		}

		if hasError {
			failures = append(failures, fmt.Sprintf("%s: unexpected ERROR in tree: %s", tc.Name, sexp))
			continue
		}

		if tc.Expected != "" && sexp != tc.Expected {
			failures = append(failures, fmt.Sprintf("%s: tree mismatch\n  got:      %s\n  expected: %s", tc.Name, sexp, tc.Expected))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d test(s) failed:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return nil
}

// GenerateWithReport compiles a grammar and returns a full diagnostic report.
func GenerateWithReport(g *Grammar) (*GenerateReport, error) {
	report := &GenerateReport{}

	// Validate first.
	report.Warnings = Validate(g)

	ng, err := Normalize(g)
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}

	tables, err := buildLRTables(ng)
	if err != nil {
		return nil, fmt.Errorf("build LR tables: %w", err)
	}

	// Resolve conflicts with diagnostics.
	diags, err := resolveConflictsWithDiag(tables, ng)
	if err != nil {
		return nil, fmt.Errorf("resolve conflicts: %w", err)
	}
	report.Conflicts = diags

	// Build lex DFA.
	tokenCount := ng.TokenCount()
	immediateTokens := make(map[int]bool)
	for _, t := range ng.Terminals {
		if t.Immediate {
			immediateTokens[t.SymbolID] = true
		}
	}

	keywordSet := make(map[int]bool, len(ng.KeywordSymbols))
	for _, ks := range ng.KeywordSymbols {
		keywordSet[ks] = true
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
		ng.WordSymbolID,
		keywordSet,
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

	if len(keywordLexStates) > 0 {
		lang.KeywordLexStates = keywordLexStates
		lang.KeywordCaptureToken = gotreesitter.Symbol(ng.WordSymbolID)
	}

	report.Language = lang
	report.SymbolCount = int(lang.SymbolCount)
	report.StateCount = int(lang.StateCount)
	report.TokenCount = int(lang.TokenCount)

	blob, err := encodeLanguageBlob(lang)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	report.Blob = blob

	return report, nil
}
