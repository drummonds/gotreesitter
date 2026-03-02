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
	seen  map[closureItemKey]bool // persistent membership set for fast merge
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
		betaCache:  make(map[struct{ prodIdx, dot int }]*betaResult),
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

		// Use pre-computed transitions instead of recomputing gotoState.
		trans := ctx.transitions[stateIdx]

		for _, item := range itemSet.items {
			prod := &ng.Productions[item.prodIdx]

			if item.dot < len(prod.RHS) {
				// Dot not at end → shift or goto
				nextSym := prod.RHS[item.dot]
				targetState, ok := trans[nextSym]
				if !ok {
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

	// FIRST(β) cache: (prodIdx, dot) → first set + nullable flag
	betaCache map[struct{ prodIdx, dot int }]*betaResult

	// Item set management
	itemSets   []lrItemSet
	itemSetMap map[string]int // full LR(1) key → index
	coreMap    map[string]int // core key (prodIdx+dot only) → index

	// Transition cache: transitions[state][symbol] → target state
	// Populated during buildItemSets, used during table construction.
	transitions map[int]map[int]int
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

// closureItemKey is the identity of an LR(1) item.
type closureItemKey struct {
	prodIdx, dot, lookahead int
}

// coreItem identifies an LR(0) core (production + dot position).
type coreItem struct {
	prodIdx, dot int
}

// closureToSet computes the closure of items and returns an lrItemSet with a
// persistent seen map. Uses core-based closure: items sharing the same
// (prodIdx, dot) core are grouped, and lookaheads are propagated as sets.
// This is dramatically faster for grammars with many lookaheads per core.
func (ctx *lrContext) closureToSet(items []lrItem) lrItemSet {
	ng := ctx.ng
	tokenCount := ng.TokenCount()

	// Group input items by core, collecting lookahead sets.
	cores := make(map[coreItem]map[int]bool)
	var coreOrder []coreItem
	for _, item := range items {
		c := coreItem{item.prodIdx, item.dot}
		if cores[c] == nil {
			cores[c] = make(map[int]bool)
			coreOrder = append(coreOrder, c)
		}
		cores[c][item.lookahead] = true
	}

	// Worklist of cores that need (re-)processing. A core needs processing
	// when it gains new lookaheads that might propagate through nullable suffixes.
	inWorklist := make(map[coreItem]bool, len(coreOrder))
	worklist := make([]coreItem, len(coreOrder))
	copy(worklist, coreOrder)
	for _, c := range coreOrder {
		inWorklist[c] = true
	}

	for len(worklist) > 0 {
		c := worklist[0]
		worklist = worklist[1:]
		inWorklist[c] = false

		prod := &ng.Productions[c.prodIdx]
		if c.dot >= len(prod.RHS) {
			continue
		}

		nextSym := prod.RHS[c.dot]
		if nextSym < tokenCount {
			continue
		}

		br := ctx.getBetaFirst(lrItem{prodIdx: c.prodIdx, dot: c.dot})
		las := cores[c]

		for _, prodIdx := range ctx.prodsByLHS[nextSym] {
			target := coreItem{prodIdx, 0}
			targetLas := cores[target]
			isNew := targetLas == nil
			if isNew {
				targetLas = make(map[int]bool)
				cores[target] = targetLas
				coreOrder = append(coreOrder, target)
			}

			addedNew := false
			// FIRST(β) lookaheads — same for all source lookaheads.
			for la := range br.first {
				if !targetLas[la] {
					targetLas[la] = true
					addedNew = true
				}
			}
			// If β is nullable, propagate all source lookaheads.
			if br.nullable {
				for la := range las {
					if !targetLas[la] {
						targetLas[la] = true
						addedNew = true
					}
				}
			}
			// Re-process target if it gained new lookaheads and could propagate.
			if addedNew && !inWorklist[target] {
				worklist = append(worklist, target)
				inWorklist[target] = true
			}
		}
	}

	// Expand core→lookaheadSet into individual items.
	totalItems := 0
	for _, c := range coreOrder {
		totalItems += len(cores[c])
	}
	result := make([]lrItem, 0, totalItems)
	seen := make(map[closureItemKey]bool, totalItems)
	for _, c := range coreOrder {
		for la := range cores[c] {
			result = append(result, lrItem{prodIdx: c.prodIdx, dot: c.dot, lookahead: la})
			seen[closureItemKey{c.prodIdx, c.dot, la}] = true
		}
	}

	return lrItemSet{items: result, seen: seen}
}

// closureIncremental propagates new items through an existing (already-closed)
// item set. Uses core-based processing for efficiency.
func (ctx *lrContext) closureIncremental(set *lrItemSet, newItems []lrItem) {
	ng := ctx.ng
	tokenCount := ng.TokenCount()

	// Group new items by core.
	cores := make(map[coreItem]map[int]bool)
	var worklist []coreItem
	inWorklist := make(map[coreItem]bool)

	for _, item := range newItems {
		c := coreItem{item.prodIdx, item.dot}
		if cores[c] == nil {
			cores[c] = make(map[int]bool)
			worklist = append(worklist, c)
			inWorklist[c] = true
		}
		cores[c][item.lookahead] = true
	}

	for len(worklist) > 0 {
		c := worklist[0]
		worklist = worklist[1:]
		inWorklist[c] = false

		prod := &ng.Productions[c.prodIdx]
		if c.dot >= len(prod.RHS) {
			continue
		}

		nextSym := prod.RHS[c.dot]
		if nextSym < tokenCount {
			continue
		}

		br := ctx.getBetaFirst(lrItem{prodIdx: c.prodIdx, dot: c.dot})
		las := cores[c]

		for _, prodIdx := range ctx.prodsByLHS[nextSym] {
			target := coreItem{prodIdx, 0}
			targetLas := cores[target]
			if targetLas == nil {
				targetLas = make(map[int]bool)
				cores[target] = targetLas
			}

			addedNew := false
			for la := range br.first {
				key := closureItemKey{prodIdx, 0, la}
				if !set.seen[key] {
					set.seen[key] = true
					targetLas[la] = true
					set.items = append(set.items, lrItem{prodIdx: prodIdx, dot: 0, lookahead: la})
					addedNew = true
				}
			}
			if br.nullable {
				for la := range las {
					key := closureItemKey{prodIdx, 0, la}
					if !set.seen[key] {
						set.seen[key] = true
						targetLas[la] = true
						set.items = append(set.items, lrItem{prodIdx: prodIdx, dot: 0, lookahead: la})
						addedNew = true
					}
				}
			}
			if addedNew && !inWorklist[target] {
				worklist = append(worklist, target)
				inWorklist[target] = true
			}
		}
	}
}

// betaResult caches the FIRST set and nullability of a production suffix.
type betaResult struct {
	first    map[int]bool
	nullable bool
}

// getBetaFirst returns the cached FIRST(β) for the suffix after the dot in an item.
func (ctx *lrContext) getBetaFirst(item lrItem) *betaResult {
	bk := struct{ prodIdx, dot int }{item.prodIdx, item.dot}
	if cached, ok := ctx.betaCache[bk]; ok {
		return cached
	}
	ng := ctx.ng
	tokenCount := ng.TokenCount()
	prod := &ng.Productions[item.prodIdx]
	beta := prod.RHS[item.dot+1:]
	result := &betaResult{
		first:    ctx.firstOfSequence(beta),
		nullable: true,
	}
	for _, sym := range beta {
		if sym < tokenCount || !ctx.nullables[sym] {
			result.nullable = false
			break
		}
	}
	ctx.betaCache[bk] = result
	return result
}

// buildItemSets constructs LALR(1) item sets using core-based merging.
// States with the same core (same prodIdx+dot pairs, ignoring lookaheads)
// are merged, producing dramatically fewer states than full LR(1).
func (ctx *lrContext) buildItemSets() []lrItemSet {
	ctx.itemSetMap = make(map[string]int)
	ctx.coreMap = make(map[string]int)
	ctx.transitions = make(map[int]map[int]int)

	// Initial item set: closure of [S' → .S, $end]
	initialSet := ctx.closureToSet([]lrItem{{
		prodIdx:   ctx.ng.AugmentProdID,
		dot:       0,
		lookahead: 0, // $end
	}})
	initialSet.key = itemSetKey(initialSet.items)
	initialCore := coreKey(initialSet.items)
	ctx.itemSets = []lrItemSet{initialSet}
	ctx.itemSetMap[initialSet.key] = 0
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

			closedSet := ctx.closureToSet(advanced)
			core := coreKey(closedSet.items)

			var targetIdx int
			if existingIdx, exists := ctx.coreMap[core]; exists {
				targetIdx = existingIdx
				// LALR merge: add any new lookaheads to the existing state.
				newItems := mergeItemsReturnNew(&ctx.itemSets[existingIdx], closedSet.items)
				if len(newItems) > 0 {
					// Incremental closure: only propagate the newly-added items
					// through the existing state's persistent seen set.
					ctx.closureIncremental(&ctx.itemSets[existingIdx], newItems)
					if !inWorklist[existingIdx] {
						worklist = append(worklist, existingIdx)
						inWorklist[existingIdx] = true
					}
				}
			} else {
				// New core — create a new state.
				targetIdx = len(ctx.itemSets)
				closedSet.key = itemSetKey(closedSet.items)
				ctx.itemSetMap[closedSet.key] = targetIdx
				ctx.coreMap[core] = targetIdx
				ctx.itemSets = append(ctx.itemSets, closedSet)
				worklist = append(worklist, targetIdx)
				inWorklist[targetIdx] = true
			}
			// Record transition for table construction.
			if ctx.transitions[stateIdx] == nil {
				ctx.transitions[stateIdx] = make(map[int]int)
			}
			ctx.transitions[stateIdx][sym] = targetIdx
		}
	}

	return ctx.itemSets
}

// mergeItemsReturnNew adds items from src into dst using dst's persistent seen
// set, returning only the newly-added items.
func mergeItemsReturnNew(dst *lrItemSet, src []lrItem) []lrItem {
	var newItems []lrItem
	for _, item := range src {
		k := closureItemKey{item.prodIdx, item.dot, item.lookahead}
		if !dst.seen[k] {
			dst.seen[k] = true
			dst.items = append(dst.items, item)
			newItems = append(newItems, item)
		}
	}
	return newItems
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
