package grammargen

import (
	"fmt"
	"sort"
	"strings"
)

// Assoc is the associativity of a production.
type Assoc int

const (
	AssocNone  Assoc = iota
	AssocLeft
	AssocRight
)

// SymbolKind classifies a grammar symbol.
type SymbolKind int

const (
	SymbolTerminal    SymbolKind = iota // anonymous terminal like "{"
	SymbolNamedToken                    // named terminal like number, string_content
	SymbolExternal                      // external scanner token
	SymbolNonterminal                   // nonterminal rule
)

// SymbolInfo describes a grammar symbol.
type SymbolInfo struct {
	Name       string
	Visible    bool
	Named      bool
	Supertype  bool
	Kind       SymbolKind
	IsExtra    bool
	Immediate  bool // token.immediate — no preceding whitespace skip
}

// Production is a single LHS → RHS production with metadata.
type Production struct {
	LHS          int // symbol index
	RHS          []int // symbol indices
	Prec         int
	Assoc        Assoc
	DynPrec      int
	ProductionID int
	Fields       []FieldAssign // per-RHS-position field assignments
	Aliases      []AliasInfo   // per-RHS-position alias info
}

// FieldAssign maps a child position in a production to a field name.
type FieldAssign struct {
	ChildIndex int
	FieldName  string
}

// AliasInfo stores alias information for a child position.
type AliasInfo struct {
	ChildIndex int
	Name       string
	Named      bool
}

// TerminalPattern describes a terminal symbol's match pattern for DFA generation.
type TerminalPattern struct {
	SymbolID  int
	Rule      *Rule  // the flattened rule tree for NFA construction
	Priority  int    // higher = preferred on tie
	Immediate bool   // token.immediate
}

// NormalizedGrammar is the output of the normalize step.
type NormalizedGrammar struct {
	Symbols       []SymbolInfo
	Productions   []Production
	Terminals     []TerminalPattern
	ExtraSymbols  []int    // symbol indices of extras
	FieldNames    []string // index 0 is always ""
	Conflicts     [][]int  // symbol index groups
	Supertypes    []int    // symbol indices
	StartSymbol   int
	AugmentProdID int // production index for S' → S

	// Keyword support (populated when Grammar.Word is set).
	KeywordSymbols []int // symbol IDs that are keywords
	WordSymbolID   int   // word token symbol ID (e.g., identifier)
	KeywordEntries []TerminalPattern // keyword patterns for keyword DFA

	// External scanner support (populated when Grammar.Externals is set).
	ExternalSymbols []int // external token index → symbol ID
}

// symbolTable is used during normalization.
type symbolTable struct {
	byName    map[string]int
	symbols   []SymbolInfo
	nextID    int
	fieldMap  map[string]int
	fields    []string
}

func newSymbolTable() *symbolTable {
	st := &symbolTable{
		byName:   make(map[string]int),
		fieldMap: make(map[string]int),
		fields:   []string{""},  // index 0 is always ""
	}
	// Symbol 0 = "end" (EOF)
	st.addSymbol("end", SymbolInfo{
		Name:    "end",
		Visible: false,
		Named:   false,
		Kind:    SymbolTerminal,
	})
	return st
}

func (st *symbolTable) addSymbol(name string, info SymbolInfo) int {
	if id, ok := st.byName[name]; ok {
		// If re-registering as a named token (e.g., true: "true"),
		// upgrade the existing entry from anonymous to named.
		if info.Named && !st.symbols[id].Named {
			st.symbols[id].Named = true
			st.symbols[id].Kind = info.Kind
		}
		return id
	}
	id := len(st.symbols)
	st.byName[name] = id
	st.symbols = append(st.symbols, info)
	return id
}

func (st *symbolTable) getOrAdd(name string, info SymbolInfo) int {
	return st.addSymbol(name, info)
}

func (st *symbolTable) lookup(name string) (int, bool) {
	id, ok := st.byName[name]
	return id, ok
}

func (st *symbolTable) fieldID(name string) int {
	if id, ok := st.fieldMap[name]; ok {
		return id
	}
	id := len(st.fields)
	st.fieldMap[name] = id
	st.fields = append(st.fields, name)
	return id
}

// Normalize transforms a Grammar into a NormalizedGrammar.
func Normalize(g *Grammar) (*NormalizedGrammar, error) {
	if len(g.RuleOrder) == 0 {
		return nil, fmt.Errorf("grammar has no rules")
	}

	st := newSymbolTable()
	ng := &NormalizedGrammar{}

	// Phase 1: Collect all string literals and register terminal symbols.
	// Walk all rules to find string literals (anonymous terminals).
	stringLiterals := collectStringLiterals(g)
	for _, s := range stringLiterals {
		st.addSymbol(s, SymbolInfo{
			Name:    s,
			Visible: true,
			Named:   false,
			Kind:    SymbolTerminal,
		})
	}

	// Phase 1b: Collect inline patterns (regex nodes inside non-terminal rules
	// that are NOT wrapped in token()). These become anonymous terminal symbols.
	inlinePatterns := collectInlinePatterns(g)
	for _, pat := range inlinePatterns {
		name := pat // use pattern value as key for lookup
		if _, ok := st.lookup(name); ok {
			continue // already registered
		}
		st.addSymbol(name, SymbolInfo{
			Name:    name,
			Visible: false,
			Named:   false,
			Kind:    SymbolTerminal,
		})
	}

	// Phase 2: Register named terminals (rules that are token() or token.immediate()
	// or simple patterns, and rules that resolve to string literals like "true").
	// Also register nonterminals.
	namedTokens, nonterminals := classifyRules(g)

	for _, name := range namedTokens {
		visible := !strings.HasPrefix(name, "_")
		st.addSymbol(name, SymbolInfo{
			Name:    name,
			Visible: visible,
			Named:   true,
			Kind:    SymbolNamedToken,
		})
	}

	// Phase 2b: Register extra terminal symbols (e.g. whitespace pattern)
	// BEFORE nonterminals so all terminals have contiguous low IDs.
	registerExtraTerminals(g, st)

	// Phase 2c: Register external scanner symbols.
	var externalSymbols []int
	if len(g.Externals) > 0 {
		externalSymbols = registerExternalSymbols(g, st)
	}

	// Record token count (terminals end here, before nonterminals).
	tokenCount := len(st.symbols)

	// Phase 3: Register nonterminal symbols.
	for _, name := range nonterminals {
		visible := !strings.HasPrefix(name, "_")
		isSupertype := false
		for _, s := range g.Supertypes {
			if s == name {
				isSupertype = true
				break
			}
		}
		st.addSymbol(name, SymbolInfo{
			Name:      name,
			Visible:   visible,
			Named:     true,
			Kind:      SymbolNonterminal,
			Supertype: isSupertype,
		})
	}

	// Phase 4: Pre-process rules — expand Optional, lift Repeat/Repeat1
	// into auxiliary nonterminals at ALL levels (including top-level).
	auxCounter := 0
	processedRules := make(map[string]*Rule)
	auxRules := make(map[string]*Rule)

	for _, name := range nonterminals {
		rule := g.Rules[name]
		if rule == nil {
			continue
		}
		processed := prepareRule(cloneRule(rule), name, st, auxRules, &auxCounter)
		processedRules[name] = processed
	}

	// Phase 5: Mark extra symbols.
	extraSymbols := resolveExtras(g, st)
	for _, eid := range extraSymbols {
		st.symbols[eid].IsExtra = true
	}

	// Phase 5b: Identify keywords when a word token is declared.
	var keywordSet map[int]bool
	var keywordSymbols []int
	var keywordEntries []TerminalPattern
	var wordSymbolID int
	if g.Word != "" {
		wordSymbolID, _ = st.lookup(g.Word)
		keywordSet, keywordSymbols, keywordEntries = identifyKeywords(g, st, stringLiterals)
	}

	// Phase 6: Extract terminal patterns for DFA generation.
	terminals, err := extractTerminals(g, st, stringLiterals, namedTokens, inlinePatterns, keywordSet)
	if err != nil {
		return nil, fmt.Errorf("extract terminals: %w", err)
	}

	// Phase 7: Extract productions from each nonterminal rule.
	var productions []Production
	prodIDCounter := 0

	// Add augmented start production: S' → startRule
	startName := g.RuleOrder[0]
	startSym, _ := st.lookup(startName)
	augStartSym := st.addSymbol("_start", SymbolInfo{
		Name:    "_start",
		Visible: false,
		Named:   false,
		Kind:    SymbolNonterminal,
	})

	augProd := Production{
		LHS:          augStartSym,
		RHS:          []int{startSym},
		ProductionID: prodIDCounter,
	}
	productions = append(productions, augProd)
	prodIDCounter++

	// Extract productions for each nonterminal rule.
	for _, name := range nonterminals {
		rule := processedRules[name]
		if rule == nil {
			continue
		}
		symID, _ := st.lookup(name)
		prods := flattenRule2(rule, symID, st, &prodIDCounter)
		productions = append(productions, prods...)
	}

	// Extract productions for auxiliary rules.
	auxNames := make([]string, 0, len(auxRules))
	for name := range auxRules {
		auxNames = append(auxNames, name)
	}
	sort.Strings(auxNames)
	for _, name := range auxNames {
		rule := auxRules[name]
		symID, _ := st.lookup(name)
		prods := flattenRule2(rule, symID, st, &prodIDCounter)
		productions = append(productions, prods...)
	}

	// Phase 8: Resolve conflicts.
	var conflicts [][]int
	for _, cgroup := range g.Conflicts {
		var syms []int
		for _, name := range cgroup {
			if id, ok := st.lookup(name); ok {
				syms = append(syms, id)
			}
		}
		conflicts = append(conflicts, syms)
	}

	// Phase 9: Resolve supertypes.
	var supertypes []int
	for _, name := range g.Supertypes {
		if id, ok := st.lookup(name); ok {
			supertypes = append(supertypes, id)
		}
	}

	ng.Symbols = st.symbols
	ng.Productions = productions
	ng.Terminals = terminals
	ng.ExtraSymbols = extraSymbols
	ng.FieldNames = st.fields
	ng.Conflicts = conflicts
	ng.Supertypes = supertypes
	ng.StartSymbol = augStartSym
	ng.AugmentProdID = 0
	ng.KeywordSymbols = keywordSymbols
	ng.WordSymbolID = wordSymbolID
	ng.KeywordEntries = keywordEntries
	ng.ExternalSymbols = externalSymbols

	// Set tokenCount boundary on symbols so assembly knows where terminals end.
	_ = tokenCount

	return ng, nil
}

// TokenCount returns the number of terminal symbols (including symbol 0 = end).
func (ng *NormalizedGrammar) TokenCount() int {
	count := 0
	for _, s := range ng.Symbols {
		if s.Kind == SymbolTerminal || s.Kind == SymbolNamedToken || s.Kind == SymbolExternal {
			count++
		}
	}
	return count
}

// collectStringLiterals walks all rules and collects unique string literals
// in order of first appearance.
func collectStringLiterals(g *Grammar) []string {
	seen := make(map[string]bool)
	var result []string

	var walk func(r *Rule, inToken bool)
	walk = func(r *Rule, inToken bool) {
		if r == nil {
			return
		}
		switch r.Kind {
		case RuleString:
			if !inToken && !seen[r.Value] {
				seen[r.Value] = true
				result = append(result, r.Value)
			}
		case RuleToken, RuleImmToken:
			// String literals inside token() are part of the token pattern,
			// not standalone terminals.
			for _, c := range r.Children {
				walk(c, true)
			}
			return
		}
		for _, c := range r.Children {
			walk(c, inToken)
		}
	}

	// Walk extras first (they may contain patterns).
	for _, e := range g.Extras {
		walk(e, false)
	}
	// Walk rules in definition order.
	for _, name := range g.RuleOrder {
		walk(g.Rules[name], false)
	}
	return result
}

// collectInlinePatterns walks all non-terminal rules and collects RulePattern
// nodes that appear inline (not inside Token() wrappers and not as top-level
// terminal rules). These anonymous regex patterns need their own terminal symbols.
func collectInlinePatterns(g *Grammar) []string {
	seen := make(map[string]bool)
	var result []string

	var walk func(r *Rule, inToken bool)
	walk = func(r *Rule, inToken bool) {
		if r == nil {
			return
		}
		switch r.Kind {
		case RulePattern:
			if !inToken && !seen[r.Value] {
				seen[r.Value] = true
				result = append(result, r.Value)
			}
			return
		case RuleToken, RuleImmToken:
			// Patterns inside token() are handled as part of the token, not inline.
			return
		}
		for _, c := range r.Children {
			walk(c, inToken)
		}
	}

	for _, name := range g.RuleOrder {
		rule := g.Rules[name]
		if !isTerminalRule(rule) {
			walk(rule, false)
		}
	}
	// Also check extras for inline patterns (already handled by _whitespace,
	// but walk for completeness).
	for _, e := range g.Extras {
		walk(e, false)
	}
	return result
}

// classifyRules separates rule names into named tokens (terminal rules)
// and nonterminals. A rule is a "named token" if its definition is:
// - a string literal (like true: "true")
// - wrapped in token() or token.immediate()
// - a pattern
// Otherwise it's a nonterminal.
func classifyRules(g *Grammar) (tokens, nonterms []string) {
	for _, name := range g.RuleOrder {
		rule := g.Rules[name]
		if isTerminalRule(rule) {
			tokens = append(tokens, name)
		} else {
			nonterms = append(nonterms, name)
		}
	}
	return
}

// isTerminalRule returns true if the rule defines a terminal token.
func isTerminalRule(r *Rule) bool {
	if r == nil {
		return false
	}
	switch r.Kind {
	case RuleString:
		return true
	case RulePattern:
		return true
	case RuleToken, RuleImmToken:
		return true
	case RulePrec, RulePrecLeft, RulePrecRight, RulePrecDynamic:
		if len(r.Children) > 0 {
			return isTerminalRule(r.Children[0])
		}
	}
	return false
}

// prepareRule normalizes a rule tree for production extraction:
// - Expands Optional(x) → Choice(x, Blank())
// - Replaces Repeat(x) and Repeat1(x) with auxiliary nonterminal symbols
// This handles repeat/repeat1 at ALL levels including the root.
func prepareRule(r *Rule, parentName string, st *symbolTable, auxRules map[string]*Rule, counter *int) *Rule {
	if r == nil {
		return r
	}
	// Don't descend into token boundaries.
	if r.Kind == RuleToken || r.Kind == RuleImmToken {
		return r
	}

	// Handle the current node.
	switch r.Kind {
	case RuleRepeat:
		*counter++
		auxName := fmt.Sprintf("_%s_repeat%d", parentName, *counter)
		if _, exists := st.lookup(auxName); !exists {
			st.addSymbol(auxName, SymbolInfo{
				Name: auxName, Visible: false, Named: false, Kind: SymbolNonterminal,
			})
			inner := r.Children[0]
			preparedInner := prepareRule(cloneRule(inner), parentName, st, auxRules, counter)
			auxRules[auxName] = Choice(
				Seq(Sym(auxName), preparedInner),
				Blank(),
			)
		}
		return Sym(auxName)

	case RuleRepeat1:
		*counter++
		auxName := fmt.Sprintf("_%s_repeat1_%d", parentName, *counter)
		if _, exists := st.lookup(auxName); !exists {
			st.addSymbol(auxName, SymbolInfo{
				Name: auxName, Visible: false, Named: false, Kind: SymbolNonterminal,
			})
			inner := r.Children[0]
			preparedInner := prepareRule(cloneRule(inner), parentName, st, auxRules, counter)
			auxRules[auxName] = Choice(
				Seq(Sym(auxName), cloneRule(preparedInner)),
				cloneRule(preparedInner),
			)
		}
		return Sym(auxName)

	case RuleOptional:
		// optional(x) → choice(x, blank)
		inner := prepareRule(r.Children[0], parentName, st, auxRules, counter)
		return Choice(inner, Blank())
	}

	// Recurse into children.
	for i, c := range r.Children {
		r.Children[i] = prepareRule(c, parentName, st, auxRules, counter)
	}
	return r
}

// registerExtraTerminals pre-registers terminal symbols from extras
// so they get contiguous IDs before nonterminals.
func registerExtraTerminals(g *Grammar, st *symbolTable) {
	for _, e := range g.Extras {
		if e == nil {
			continue
		}
		if e.Kind == RulePattern {
			st.getOrAdd("_whitespace", SymbolInfo{
				Name: "_whitespace", Visible: false, Named: false, Kind: SymbolTerminal,
			})
		}
	}
}

// registerExternalSymbols registers external scanner symbols from g.Externals.
// Each external token gets a symbol ID with Kind=SymbolExternal.
// Returns the mapping: external token index → symbol ID.
func registerExternalSymbols(g *Grammar, st *symbolTable) []int {
	var extSyms []int
	for _, ext := range g.Externals {
		if ext == nil {
			continue
		}
		name := ""
		switch ext.Kind {
		case RuleSymbol:
			name = ext.Value
		case RuleString:
			name = ext.Value
		default:
			continue
		}
		visible := !strings.HasPrefix(name, "_")
		id := st.addSymbol(name, SymbolInfo{
			Name:    name,
			Visible: visible,
			Named:   true,
			Kind:    SymbolExternal,
		})
		extSyms = append(extSyms, id)
	}
	return extSyms
}

// resolveExtras returns symbol IDs for the extra rules.
func resolveExtras(g *Grammar, st *symbolTable) []int {
	var extras []int
	for _, e := range g.Extras {
		if e == nil {
			continue
		}
		switch e.Kind {
		case RulePattern:
			if id, ok := st.lookup("_whitespace"); ok {
				extras = append(extras, id)
			}
		case RuleSymbol:
			if id, ok := st.lookup(e.Value); ok {
				extras = append(extras, id)
			}
		case RuleString:
			if id, ok := st.lookup(e.Value); ok {
				extras = append(extras, id)
			}
		}
	}
	return extras
}

// extractTerminals builds TerminalPattern entries for DFA generation.
// When keywordSet is non-nil, string terminals that are keywords are excluded
// from the main DFA (they're handled by the keyword DFA instead).
func extractTerminals(g *Grammar, st *symbolTable, stringLits []string, namedTokens []string, inlinePatterns []string, keywordSet map[int]bool) ([]TerminalPattern, error) {
	var patterns []TerminalPattern
	priority := 0

	// String literals become simple string-match patterns.
	for _, s := range stringLits {
		id, ok := st.lookup(s)
		if !ok {
			continue
		}
		// Skip keywords — they're recognized via the word token + keyword DFA.
		if keywordSet != nil && keywordSet[id] {
			priority++
			continue
		}
		patterns = append(patterns, TerminalPattern{
			SymbolID: id,
			Rule:     Str(s),
			Priority: priority,
		})
		priority++
	}

	// Inline patterns (regex appearing directly in non-terminal rules, not in token()).
	for _, pat := range inlinePatterns {
		id, ok := st.lookup(pat)
		if !ok {
			continue
		}
		expanded, err := expandPatternRule(pat)
		if err != nil {
			return nil, fmt.Errorf("expand inline pattern %q: %w", pat, err)
		}
		patterns = append(patterns, TerminalPattern{
			SymbolID: id,
			Rule:     expanded,
			Priority: priority,
		})
		priority++
	}

	// Named tokens (rules that are token/pattern/string-literal rules).
	for _, name := range namedTokens {
		id, ok := st.lookup(name)
		if !ok {
			continue
		}
		rule := g.Rules[name]
		expanded, imm, err := expandTokenRule(rule)
		if err != nil {
			return nil, fmt.Errorf("expand token %q: %w", name, err)
		}
		patterns = append(patterns, TerminalPattern{
			SymbolID:  id,
			Rule:      expanded,
			Priority:  priority,
			Immediate: imm,
		})
		priority++
	}

	// Extra patterns (like /\s/).
	for _, e := range g.Extras {
		if e != nil && e.Kind == RulePattern {
			id, ok := st.lookup("_whitespace")
			if !ok {
				continue
			}
			expanded, err := expandPatternRule(e.Value)
			if err != nil {
				return nil, fmt.Errorf("expand extra pattern: %w", err)
			}
			patterns = append(patterns, TerminalPattern{
				SymbolID: id,
				Rule:     expanded,
				Priority: -1, // lowest priority
			})
		}
	}

	return patterns, nil
}

// identifyKeywords determines which string terminals are keywords.
// A keyword is a string terminal whose characters all match the word token's
// pattern. Returns the keyword set, ordered symbol IDs, and terminal patterns
// for keyword DFA construction.
func identifyKeywords(g *Grammar, st *symbolTable, stringLits []string) (map[int]bool, []int, []TerminalPattern) {
	wordRule := g.Rules[g.Word]
	if wordRule == nil {
		return nil, nil, nil
	}

	// Build a test DFA from the word pattern.
	expanded, _, err := expandTokenRule(wordRule)
	if err != nil {
		return nil, nil, nil
	}
	b := newNFABuilder()
	frag, err := b.buildFromRule(expanded)
	if err != nil {
		return nil, nil, nil
	}
	b.states[frag.end].accept = 1 // any non-zero accept
	b.states[frag.end].priority = 0

	dfa := subsetConstruction(&nfa{states: b.states, start: frag.start})

	keywordSet := make(map[int]bool)
	var keywordSyms []int
	var keywordEntries []TerminalPattern
	priority := 0

	for _, s := range stringLits {
		id, ok := st.lookup(s)
		if !ok {
			continue
		}
		if matchesDFA(dfa, s) {
			keywordSet[id] = true
			keywordSyms = append(keywordSyms, id)
			keywordEntries = append(keywordEntries, TerminalPattern{
				SymbolID: id,
				Rule:     Str(s),
				Priority: priority,
			})
			priority++
		}
	}

	return keywordSet, keywordSyms, keywordEntries
}

// matchesDFA tests if a string is fully accepted by a DFA.
func matchesDFA(dfa []dfaState, s string) bool {
	state := 0
	for _, ch := range s {
		found := false
		for _, t := range dfa[state].transitions {
			if ch >= t.lo && ch <= t.hi {
				state = t.nextState
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return dfa[state].accept != 0
}

// expandTokenRule flattens a token rule into a Rule tree suitable for
// NFA construction. Returns the expanded rule and whether it's immediate.
func expandTokenRule(r *Rule) (*Rule, bool, error) {
	if r == nil {
		return Blank(), false, nil
	}
	switch r.Kind {
	case RuleString:
		return Str(r.Value), false, nil
	case RulePattern:
		expanded, err := expandPatternRule(r.Value)
		return expanded, false, err
	case RuleToken:
		inner, err := flattenTokenInner(r.Children[0])
		return inner, false, err
	case RuleImmToken:
		inner, err := flattenTokenInner(r.Children[0])
		return inner, true, err
	case RulePrec, RulePrecLeft, RulePrecRight, RulePrecDynamic:
		if len(r.Children) > 0 {
			return expandTokenRule(r.Children[0])
		}
		return Blank(), false, nil
	default:
		return Blank(), false, fmt.Errorf("unexpected rule kind %d in token position", r.Kind)
	}
}

// flattenTokenInner expands the interior of a token() rule for NFA construction.
// Inside a token, everything is part of one lexer pattern.
func flattenTokenInner(r *Rule) (*Rule, error) {
	if r == nil {
		return Blank(), nil
	}
	switch r.Kind {
	case RuleString:
		return Str(r.Value), nil
	case RulePattern:
		return expandPatternRule(r.Value)
	case RuleSeq:
		children := make([]*Rule, len(r.Children))
		for i, c := range r.Children {
			exp, err := flattenTokenInner(c)
			if err != nil {
				return nil, err
			}
			children[i] = exp
		}
		return Seq(children...), nil
	case RuleChoice:
		children := make([]*Rule, len(r.Children))
		for i, c := range r.Children {
			exp, err := flattenTokenInner(c)
			if err != nil {
				return nil, err
			}
			children[i] = exp
		}
		return Choice(children...), nil
	case RuleRepeat:
		inner, err := flattenTokenInner(r.Children[0])
		if err != nil {
			return nil, err
		}
		return Repeat(inner), nil
	case RuleRepeat1:
		inner, err := flattenTokenInner(r.Children[0])
		if err != nil {
			return nil, err
		}
		return Repeat1(inner), nil
	case RuleOptional:
		inner, err := flattenTokenInner(r.Children[0])
		if err != nil {
			return nil, err
		}
		return Optional(inner), nil
	case RulePrec, RulePrecLeft, RulePrecRight, RulePrecDynamic:
		if len(r.Children) > 0 {
			return flattenTokenInner(r.Children[0])
		}
		return Blank(), nil
	case RuleBlank:
		return Blank(), nil
	default:
		return nil, fmt.Errorf("unexpected rule kind %d inside token", r.Kind)
	}
}

// flattenRule2 extracts all productions from a prepared rule tree.
// It properly handles Choice at any level by enumerating all alternatives.
func flattenRule2(r *Rule, lhsID int, st *symbolTable, prodIDCounter *int) []Production {
	if r == nil {
		return nil
	}

	// Unwrap precedence/assoc wrappers at the top level.
	prec, assoc, dynPrec, inner := unwrapPrec(r)

	switch inner.Kind {
	case RuleChoice:
		var prods []Production
		for _, alt := range inner.Children {
			altPrec, altAssoc, altDyn, altInner := unwrapPrec(alt)
			if altPrec == 0 {
				altPrec = prec
			}
			if altAssoc == AssocNone {
				altAssoc = assoc
			}
			if altDyn == 0 {
				altDyn = dynPrec
			}
			// Recursively flatten — alternatives may contain more choices.
			altProds := flattenRule2(altInner, lhsID, st, prodIDCounter)
			for i := range altProds {
				if altProds[i].Prec == 0 {
					altProds[i].Prec = altPrec
				}
				if altProds[i].Assoc == AssocNone {
					altProds[i].Assoc = altAssoc
				}
				if altProds[i].DynPrec == 0 {
					altProds[i].DynPrec = altDyn
				}
			}
			prods = append(prods, altProds...)
		}
		return prods

	case RuleBlank:
		prod := Production{
			LHS:          lhsID,
			Prec:         prec,
			Assoc:        assoc,
			DynPrec:      dynPrec,
			ProductionID: *prodIDCounter,
		}
		*prodIDCounter++
		return []Production{prod}

	default:
		// Enumerate all alternatives from Choice-within-Seq by expanding
		// the rule into a list of "flat" RHS sequences.
		alternatives := enumerateAlternatives(inner)
		var prods []Production
		for _, alt := range alternatives {
			prod := Production{
				LHS:          lhsID,
				Prec:         prec,
				Assoc:        assoc,
				DynPrec:      dynPrec,
				ProductionID: *prodIDCounter,
			}
			*prodIDCounter++

			var rhs []int
			var fields []FieldAssign
			var aliases []AliasInfo
			collectLinearRHS(alt, st, &rhs, &fields, &aliases)
			prod.RHS = rhs
			prod.Fields = fields
			prod.Aliases = aliases
			prods = append(prods, prod)
		}
		return prods
	}
}

// rhsElement is a single element in a flattened RHS.
type rhsElement struct {
	rule       *Rule
	fieldName  string // non-empty if wrapped in a Field
	aliasName  string // non-empty if wrapped in an Alias
	aliasNamed bool   // true if alias is a named symbol ($.name form)
}

// enumerateAlternatives expands a rule containing inline Choice nodes
// into multiple flat sequences (one per alternative combination).
func enumerateAlternatives(r *Rule) [][]*rhsElement {
	if r == nil {
		return [][]*rhsElement{{}}
	}
	switch r.Kind {
	case RuleChoice:
		var all [][]*rhsElement
		for _, child := range r.Children {
			all = append(all, enumerateAlternatives(child)...)
		}
		return all

	case RuleSeq:
		// Start with one empty sequence.
		result := [][]*rhsElement{{}}
		for _, child := range r.Children {
			childAlts := enumerateAlternatives(child)
			var newResult [][]*rhsElement
			for _, existing := range result {
				for _, childAlt := range childAlts {
					combined := make([]*rhsElement, len(existing)+len(childAlt))
					copy(combined, existing)
					copy(combined[len(existing):], childAlt)
					newResult = append(newResult, combined)
				}
			}
			result = newResult
		}
		return result

	case RuleField:
		if len(r.Children) == 0 {
			return [][]*rhsElement{{}}
		}
		// Enumerate alternatives inside the field, tagging each with the field name.
		innerAlts := enumerateAlternatives(r.Children[0])
		var result [][]*rhsElement
		for _, alt := range innerAlts {
			tagged := make([]*rhsElement, len(alt))
			for i, elem := range alt {
				cp := *elem
				if cp.fieldName == "" {
					cp.fieldName = r.Value
				}
				tagged[i] = &cp
			}
			result = append(result, tagged)
		}
		return result

	case RuleAlias:
		if len(r.Children) == 0 {
			return [][]*rhsElement{{}}
		}
		// Enumerate alternatives inside the alias, tagging each with the alias name.
		innerAlts := enumerateAlternatives(r.Children[0])
		var result [][]*rhsElement
		for _, alt := range innerAlts {
			tagged := make([]*rhsElement, len(alt))
			for i, elem := range alt {
				cp := *elem
				if cp.aliasName == "" {
					cp.aliasName = r.Value
					cp.aliasNamed = r.Named
				}
				tagged[i] = &cp
			}
			result = append(result, tagged)
		}
		return result

	case RulePrec, RulePrecLeft, RulePrecRight, RulePrecDynamic:
		if len(r.Children) > 0 {
			return enumerateAlternatives(r.Children[0])
		}
		return [][]*rhsElement{{}}

	case RuleBlank:
		// Epsilon — empty sequence.
		return [][]*rhsElement{{}}

	default:
		// Leaf node (String, Symbol, etc.) — single element.
		return [][]*rhsElement{{&rhsElement{rule: r}}}
	}
}

// collectLinearRHS converts a flat list of rhsElements into symbol IDs, field assignments, and alias info.
func collectLinearRHS(elems []*rhsElement, st *symbolTable, rhs *[]int, fields *[]FieldAssign, aliases *[]AliasInfo) {
	for _, elem := range elems {
		childIdx := len(*rhs)
		addRuleSymbol(elem.rule, st, rhs)
		if elem.fieldName != "" && len(*rhs) > childIdx {
			st.fieldID(elem.fieldName)
			*fields = append(*fields, FieldAssign{
				ChildIndex: childIdx,
				FieldName:  elem.fieldName,
			})
		}
		if elem.aliasName != "" && len(*rhs) > childIdx {
			*aliases = append(*aliases, AliasInfo{
				ChildIndex: childIdx,
				Name:       elem.aliasName,
				Named:      elem.aliasNamed,
			})
		}
	}
}

// addRuleSymbol resolves a rule to a symbol ID and appends it to rhs.
func addRuleSymbol(r *Rule, st *symbolTable, rhs *[]int) {
	if r == nil {
		return
	}
	switch r.Kind {
	case RuleString:
		if id, ok := st.lookup(r.Value); ok {
			*rhs = append(*rhs, id)
		}
	case RuleSymbol:
		if id, ok := st.lookup(r.Value); ok {
			*rhs = append(*rhs, id)
		}
	case RulePattern:
		// Inline patterns are registered by their pattern value.
		if id, ok := st.lookup(r.Value); ok {
			*rhs = append(*rhs, id)
		}
	}
}

// unwrapPrec strips precedence/associativity wrappers from a rule.
func unwrapPrec(r *Rule) (prec int, assoc Assoc, dynPrec int, inner *Rule) {
	for r != nil {
		switch r.Kind {
		case RulePrec:
			prec = r.Prec
			if len(r.Children) > 0 {
				r = r.Children[0]
				continue
			}
		case RulePrecLeft:
			prec = r.Prec
			assoc = AssocLeft
			if len(r.Children) > 0 {
				r = r.Children[0]
				continue
			}
		case RulePrecRight:
			prec = r.Prec
			assoc = AssocRight
			if len(r.Children) > 0 {
				r = r.Children[0]
				continue
			}
		case RulePrecDynamic:
			dynPrec = r.Prec
			if len(r.Children) > 0 {
				r = r.Children[0]
				continue
			}
		}
		break
	}
	return prec, assoc, dynPrec, r
}
