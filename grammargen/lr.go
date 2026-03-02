package grammargen

import (
	"fmt"
	"sort"
)

// lrItem is an LR(1) item: [A → α . β, a]
type lrItem struct {
	prodIdx   int    // index into productions
	dot       int    // position of dot in RHS
	lookahead int    // terminal symbol ID
}

// lrItemSet is a set of LR(1) items (one parser state).
type lrItemSet struct {
	items []lrItem
	key   string // canonical key for dedup
}

// lrAction is a parse table action.
type lrAction struct {
	kind     lrActionKind
	state    int   // shift target / goto target
	prodIdx  int   // reduce production index
	prec     int   // for shift: precedence of the item's production
	assoc    Assoc // for shift: associativity of the item's production
}

type lrActionKind int

const (
	lrShift  lrActionKind = iota
	lrReduce
	lrAccept
)

// LRTables holds the generated parse tables.
type LRTables struct {
	// ActionTable[state][symbol] = list of actions (multiple = conflict/GLR)
	ActionTable map[int]map[int][]lrAction
	GotoTable   map[int]map[int]int // [state][nonterminal] → target state
	StateCount  int
}

// buildLRTables constructs LR(1) parse tables from a normalized grammar.
func buildLRTables(ng *NormalizedGrammar) (*LRTables, error) {
	ctx := &lrContext{
		ng:         ng,
		firstSets:  make(map[int]map[int]bool),
		nullables:  make(map[int]bool),
		prodsByLHS: make(map[int][]int),
	}

	// Build production-by-LHS index for fast closure lookups.
	for i := range ng.Productions {
		lhs := ng.Productions[i].LHS
		ctx.prodsByLHS[lhs] = append(ctx.prodsByLHS[lhs], i)
	}

	// Compute FIRST and nullable sets.
	ctx.computeFirstSets()

	// Build LR(1) item sets (canonical collection).
	itemSets := ctx.buildItemSets()

	// Build action and goto tables.
	tables := &LRTables{
		ActionTable: make(map[int]map[int][]lrAction),
		GotoTable:   make(map[int]map[int]int),
		StateCount:  len(itemSets),
	}

	tokenCount := ng.TokenCount()

	for stateIdx, itemSet := range itemSets {
		tables.ActionTable[stateIdx] = make(map[int][]lrAction)
		tables.GotoTable[stateIdx] = make(map[int]int)

		for _, item := range itemSet.items {
			prod := &ng.Productions[item.prodIdx]

			if item.dot < len(prod.RHS) {
				// Dot not at end → shift or goto
				nextSym := prod.RHS[item.dot]
				targetState := ctx.gotoState(itemSet, nextSym)
				if targetState < 0 {
					continue
				}

				if nextSym < tokenCount {
					// Terminal → shift action
					tables.addAction(stateIdx, nextSym, lrAction{
						kind:  lrShift,
						state: targetState,
						prec:  prod.Prec,
						assoc: prod.Assoc,
					})
				} else {
					// Nonterminal → goto
					tables.GotoTable[stateIdx][nextSym] = targetState
				}
			} else {
				// Dot at end → reduce or accept
				if item.prodIdx == ng.AugmentProdID {
					// Augmented start production → accept
					tables.addAction(stateIdx, 0, lrAction{kind: lrAccept})
				} else {
					// Regular reduce
					tables.addAction(stateIdx, item.lookahead, lrAction{
						kind:    lrReduce,
						prodIdx: item.prodIdx,
					})
				}
			}
		}
	}

	return tables, nil
}

func (t *LRTables) addAction(state, sym int, action lrAction) {
	existing := t.ActionTable[state][sym]
	// Avoid duplicates.
	for i, a := range existing {
		if a.kind == action.kind && a.state == action.state {
			if a.kind == lrShift {
				// For shifts to the same target, keep the higher prec.
				// This matters when multiple items contribute shifts on
				// the same terminal (e.g. items from different productions).
				if action.prec > a.prec {
					existing[i].prec = action.prec
					existing[i].assoc = action.assoc
				}
				return
			}
			if a.prodIdx == action.prodIdx {
				return
			}
		}
	}
	t.ActionTable[state][sym] = append(existing, action)
}

// lrContext holds state during LR table construction.
type lrContext struct {
	ng        *NormalizedGrammar
	firstSets map[int]map[int]bool // symbol → set of terminal first symbols
	nullables map[int]bool         // symbol → can derive ε

	// Production index: LHS symbol → production indices
	prodsByLHS map[int][]int

	// Item set management
	itemSets   []lrItemSet
	itemSetMap map[string]int // full LR(1) key → index
	coreMap    map[string]int // core key (prodIdx+dot only) → index
}

// computeFirstSets computes FIRST sets for all symbols.
func (ctx *lrContext) computeFirstSets() {
	ng := ctx.ng
	tokenCount := ng.TokenCount()

	// Initialize: terminals have FIRST = {self}
	for i, sym := range ng.Symbols {
		if sym.Kind == SymbolTerminal || sym.Kind == SymbolNamedToken || sym.Kind == SymbolExternal {
			ctx.firstSets[i] = map[int]bool{i: true}
		} else {
			ctx.firstSets[i] = make(map[int]bool)
		}
	}

	// Compute nullables.
	changed := true
	for changed {
		changed = false
		for _, prod := range ng.Productions {
			if ctx.nullables[prod.LHS] {
				continue
			}
			nullable := true
			for _, sym := range prod.RHS {
				if sym < tokenCount || !ctx.nullables[sym] {
					nullable = false
					break
				}
			}
			if nullable {
				ctx.nullables[prod.LHS] = true
				changed = true
			}
		}
	}

	// Iterate until fixed point.
	changed = true
	for changed {
		changed = false
		for _, prod := range ng.Productions {
			lhsFirst := ctx.firstSets[prod.LHS]
			for _, sym := range prod.RHS {
				symFirst := ctx.firstSets[sym]
				for f := range symFirst {
					if !lhsFirst[f] {
						lhsFirst[f] = true
						changed = true
					}
				}
				if sym >= tokenCount && ctx.nullables[sym] {
					continue
				}
				break
			}
		}
	}
}

// firstOfSequence computes FIRST(β) for a sequence of symbols.
func (ctx *lrContext) firstOfSequence(syms []int) map[int]bool {
	result := make(map[int]bool)
	tokenCount := ctx.ng.TokenCount()
	for _, sym := range syms {
		for f := range ctx.firstSets[sym] {
			result[f] = true
		}
		if sym < tokenCount || !ctx.nullables[sym] {
			return result
		}
	}
	return result
}

// closure computes the closure of an LR(1) item set.
func (ctx *lrContext) closure(items []lrItem) []lrItem {
	ng := ctx.ng
	tokenCount := ng.TokenCount()

	// Use a set to track items.
	type itemKey struct {
		prodIdx, dot, lookahead int
	}
	seen := make(map[itemKey]bool)
	for _, item := range items {
		seen[itemKey{item.prodIdx, item.dot, item.lookahead}] = true
	}

	worklist := make([]lrItem, len(items))
	copy(worklist, items)

	for len(worklist) > 0 {
		item := worklist[0]
		worklist = worklist[1:]

		prod := &ng.Productions[item.prodIdx]
		if item.dot >= len(prod.RHS) {
			continue
		}

		nextSym := prod.RHS[item.dot]
		if nextSym < tokenCount {
			continue // terminal — no closure needed
		}

		// Compute FIRST(β a) where β = prod.RHS[dot+1:] and a = lookahead.
		beta := prod.RHS[item.dot+1:]
		firstBetaA := ctx.firstOfSequence(beta)
		// If β can derive ε, add the lookahead.
		allNullable := true
		for _, sym := range beta {
			if sym < tokenCount || !ctx.nullables[sym] {
				allNullable = false
				break
			}
		}
		if allNullable {
			firstBetaA[item.lookahead] = true
		}

		// For each production B → γ, add [B → .γ, b] for b ∈ FIRST(βa).
		for _, prodIdx := range ctx.prodsByLHS[nextSym] {
			for la := range firstBetaA {
				key := itemKey{prodIdx, 0, la}
				if !seen[key] {
					seen[key] = true
					newItem := lrItem{prodIdx: prodIdx, dot: 0, lookahead: la}
					items = append(items, newItem)
					worklist = append(worklist, newItem)
				}
			}
		}
	}

	return items
}

// gotoState computes the GOTO of an item set for a given symbol.
// Returns the target state index, or -1 if no transition.
func (ctx *lrContext) gotoState(set lrItemSet, sym int) int {
	var advanced []lrItem
	for _, item := range set.items {
		prod := &ctx.ng.Productions[item.prodIdx]
		if item.dot < len(prod.RHS) && prod.RHS[item.dot] == sym {
			advanced = append(advanced, lrItem{
				prodIdx:   item.prodIdx,
				dot:       item.dot + 1,
				lookahead: item.lookahead,
			})
		}
	}
	if len(advanced) == 0 {
		return -1
	}

	closed := ctx.closure(advanced)
	core := coreKey(closed)
	if idx, ok := ctx.coreMap[core]; ok {
		return idx
	}
	return -1
}

// buildItemSets constructs LALR(1) item sets using core-based merging.
// States with the same core (same prodIdx+dot pairs, ignoring lookaheads)
// are merged, producing dramatically fewer states than full LR(1).
func (ctx *lrContext) buildItemSets() []lrItemSet {
	ctx.itemSetMap = make(map[string]int)
	ctx.coreMap = make(map[string]int)

	// Initial item set: closure of [S' → .S, $end]
	initial := ctx.closure([]lrItem{{
		prodIdx:   ctx.ng.AugmentProdID,
		dot:       0,
		lookahead: 0, // $end
	}})

	initialKey := itemSetKey(initial)
	initialCore := coreKey(initial)
	ctx.itemSets = []lrItemSet{{items: initial, key: initialKey}}
	ctx.itemSetMap[initialKey] = 0
	ctx.coreMap[initialCore] = 0

	worklist := []int{0}
	inWorklist := map[int]bool{0: true}

	for len(worklist) > 0 {
		stateIdx := worklist[0]
		worklist = worklist[1:]
		inWorklist[stateIdx] = false
		itemSet := ctx.itemSets[stateIdx]

		// Collect all symbols after the dot.
		symsSeen := make(map[int]bool)
		var syms []int
		for _, item := range itemSet.items {
			prod := &ctx.ng.Productions[item.prodIdx]
			if item.dot < len(prod.RHS) {
				sym := prod.RHS[item.dot]
				if !symsSeen[sym] {
					symsSeen[sym] = true
					syms = append(syms, sym)
				}
			}
		}

		for _, sym := range syms {
			// Compute GOTO(itemSet, sym).
			var advanced []lrItem
			for _, item := range itemSet.items {
				prod := &ctx.ng.Productions[item.prodIdx]
				if item.dot < len(prod.RHS) && prod.RHS[item.dot] == sym {
					advanced = append(advanced, lrItem{
						prodIdx:   item.prodIdx,
						dot:       item.dot + 1,
						lookahead: item.lookahead,
					})
				}
			}
			if len(advanced) == 0 {
				continue
			}

			closed := ctx.closure(advanced)
			core := coreKey(closed)

			if existingIdx, exists := ctx.coreMap[core]; exists {
				// LALR merge: add any new lookaheads to the existing state.
				if merged := mergeItems(&ctx.itemSets[existingIdx], closed); merged {
					// Re-close the merged state to propagate new lookaheads.
					ctx.itemSets[existingIdx].items = ctx.closure(ctx.itemSets[existingIdx].items)
					// Re-process this state since items changed.
					if !inWorklist[existingIdx] {
						worklist = append(worklist, existingIdx)
						inWorklist[existingIdx] = true
					}
				}
			} else {
				// New core — create a new state.
				newIdx := len(ctx.itemSets)
				key := itemSetKey(closed)
				ctx.itemSetMap[key] = newIdx
				ctx.coreMap[core] = newIdx
				ctx.itemSets = append(ctx.itemSets, lrItemSet{items: closed, key: key})
				worklist = append(worklist, newIdx)
				inWorklist[newIdx] = true
			}
		}
	}

	return ctx.itemSets
}

// mergeItems adds items from src into dst, returning true if any new items were added.
func mergeItems(dst *lrItemSet, src []lrItem) bool {
	type itemKey struct{ prodIdx, dot, lookahead int }
	existing := make(map[itemKey]bool, len(dst.items))
	for _, item := range dst.items {
		existing[itemKey{item.prodIdx, item.dot, item.lookahead}] = true
	}

	added := false
	for _, item := range src {
		k := itemKey{item.prodIdx, item.dot, item.lookahead}
		if !existing[k] {
			dst.items = append(dst.items, item)
			existing[k] = true
			added = true
		}
	}
	return added
}

// coreKey computes a key from only the (prodIdx, dot) pairs, ignoring lookaheads.
// States with the same core key are LALR-mergeable.
func coreKey(items []lrItem) string {
	// Collect unique (prodIdx, dot) pairs.
	type core struct{ prodIdx, dot int }
	seen := make(map[core]bool)
	var cores []core
	for _, item := range items {
		c := core{item.prodIdx, item.dot}
		if !seen[c] {
			seen[c] = true
			cores = append(cores, c)
		}
	}
	sort.Slice(cores, func(i, j int) bool {
		if cores[i].prodIdx != cores[j].prodIdx {
			return cores[i].prodIdx < cores[j].prodIdx
		}
		return cores[i].dot < cores[j].dot
	})

	buf := make([]byte, 0, len(cores)*4)
	for _, c := range cores {
		buf = append(buf,
			byte(c.prodIdx>>8), byte(c.prodIdx),
			byte(c.dot>>8), byte(c.dot),
		)
	}
	return string(buf)
}

// itemSetKey computes a canonical string key for an item set (full LR(1) key).
func itemSetKey(items []lrItem) string {
	// Sort items for canonical form.
	sorted := make([]lrItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].prodIdx != sorted[j].prodIdx {
			return sorted[i].prodIdx < sorted[j].prodIdx
		}
		if sorted[i].dot != sorted[j].dot {
			return sorted[i].dot < sorted[j].dot
		}
		return sorted[i].lookahead < sorted[j].lookahead
	})

	// Build key.
	buf := make([]byte, 0, len(sorted)*12)
	for _, item := range sorted {
		buf = append(buf,
			byte(item.prodIdx>>8), byte(item.prodIdx),
			byte(item.dot>>8), byte(item.dot),
			byte(item.lookahead>>8), byte(item.lookahead),
		)
	}
	return string(buf)
}

// resolveConflicts resolves shift/reduce and reduce/reduce conflicts
// using precedence and associativity.
func resolveConflicts(tables *LRTables, ng *NormalizedGrammar) error {
	for state, actions := range tables.ActionTable {
		for sym, acts := range actions {
			if len(acts) <= 1 {
				continue
			}

			resolved, err := resolveActionConflict(acts, ng)
			if err != nil {
				return fmt.Errorf("state %d, symbol %d: %w", state, sym, err)
			}
			tables.ActionTable[state][sym] = resolved
		}
	}
	return nil
}

// resolveActionConflict resolves a conflict between multiple actions.
func resolveActionConflict(actions []lrAction, ng *NormalizedGrammar) ([]lrAction, error) {
	if len(actions) <= 1 {
		return actions, nil
	}

	// Separate shifts and reduces.
	var shifts, reduces []lrAction
	for _, a := range actions {
		switch a.kind {
		case lrShift:
			shifts = append(shifts, a)
		case lrReduce:
			reduces = append(reduces, a)
		case lrAccept:
			return []lrAction{a}, nil
		}
	}

	// Shift/reduce conflict.
	if len(shifts) > 0 && len(reduces) > 0 {
		shift := shifts[0]
		reduce := reduces[0]
		prod := &ng.Productions[reduce.prodIdx]

		// Use precedence to resolve.
		// The shift prec comes from the item's production (attached during
		// table construction), not from a global symbol-to-prec lookup.
		shiftPrec := shift.prec
		reducePrec := prod.Prec

		if reducePrec != 0 || shiftPrec != 0 {
			if reducePrec > shiftPrec {
				return []lrAction{reduce}, nil
			}
			if shiftPrec > reducePrec {
				return []lrAction{shift}, nil
			}
			// Equal precedence: use associativity from the reduce production.
			switch prod.Assoc {
			case AssocLeft:
				return []lrAction{reduce}, nil
			case AssocRight:
				return []lrAction{shift}, nil
			case AssocNone:
				// Non-associative: neither action (error).
				return nil, nil
			}
		}

		// Check declared conflicts for GLR.
		if isDeclaredConflict(reduce.prodIdx, ng) {
			return actions, nil // keep both for GLR
		}

		// Default: prefer shift (like yacc/bison).
		return []lrAction{shift}, nil
	}

	// Reduce/reduce conflict.
	if len(reduces) > 1 {
		// Check if all reduces are part of the same declared conflict group → GLR.
		if allInDeclaredConflict(reduces, ng) {
			return reduces, nil // keep all for GLR
		}

		// Higher precedence wins.
		best := reduces[0]
		bestPrec := ng.Productions[best.prodIdx].Prec
		for _, r := range reduces[1:] {
			p := ng.Productions[r.prodIdx].Prec
			if p > bestPrec {
				best = r
				bestPrec = p
			}
		}
		return []lrAction{best}, nil
	}

	return actions, nil
}

// isDeclaredConflict checks if the production's LHS is part of a declared conflict.
func isDeclaredConflict(prodIdx int, ng *NormalizedGrammar) bool {
	prod := &ng.Productions[prodIdx]
	for _, cgroup := range ng.Conflicts {
		for _, sym := range cgroup {
			if sym == prod.LHS {
				return true
			}
		}
	}
	return false
}

// allInDeclaredConflict checks if all reduce actions have their LHS symbols
// in the same declared conflict group. This enables GLR forking.
func allInDeclaredConflict(reduces []lrAction, ng *NormalizedGrammar) bool {
	if len(reduces) < 2 || len(ng.Conflicts) == 0 {
		return false
	}
	for _, cgroup := range ng.Conflicts {
		cgroupSet := make(map[int]bool, len(cgroup))
		for _, sym := range cgroup {
			cgroupSet[sym] = true
		}
		allFound := true
		for _, r := range reduces {
			lhs := ng.Productions[r.prodIdx].LHS
			if !cgroupSet[lhs] {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}
