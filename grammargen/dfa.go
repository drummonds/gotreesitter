package grammargen

import (
	"sort"

	"github.com/odvcencio/gotreesitter"
)

// dfaState is a state in the deterministic finite automaton.
type dfaState struct {
	transitions []dfaTransition
	accept      int  // symbol ID if accepting, 0 if not
	skip        bool // true for whitespace/extra tokens
}

// dfaTransition maps a character range to a next state.
type dfaTransition struct {
	lo, hi    rune
	nextState int
}

// buildLexDFA constructs a DFA from the terminal patterns and produces
// LexState tables compatible with the gotreesitter runtime.
// It builds per-lex-mode DFAs based on which terminals are valid in each mode.
func buildLexDFA(patterns []TerminalPattern, extraSymbols []int, lexModes []lexModeSpec) ([]gotreesitter.LexState, error) {
	extraSet := make(map[int]bool)
	for _, e := range extraSymbols {
		extraSet[e] = true
	}

	var allStates []gotreesitter.LexState

	for _, mode := range lexModes {
		// Filter patterns to only those valid in this mode.
		var modePatterns []TerminalPattern
		for _, p := range patterns {
			if mode.validSymbols[p.SymbolID] || extraSet[p.SymbolID] {
				modePatterns = append(modePatterns, p)
			}
		}

		// Build combined NFA for this mode's terminals.
		combined, err := buildCombinedNFA(modePatterns)
		if err != nil {
			return nil, err
		}

		// Convert NFA to DFA via subset construction.
		dfa := subsetConstruction(combined)

		// Mark skip states for extra symbols.
		for i := range dfa {
			if dfa[i].accept > 0 && extraSet[dfa[i].accept] {
				dfa[i].skip = true
			}
		}

		// Convert to LexState format.
		lexStates := convertDFAToLexStates(dfa, mode.skipWhitespace)

		// Offset all transition targets and skip-loop targets to account
		// for concatenation with previous modes' states.
		offset := len(allStates)
		if offset > 0 {
			for i := range lexStates {
				for j := range lexStates[i].Transitions {
					if lexStates[i].Transitions[j].NextState >= 0 {
						lexStates[i].Transitions[j].NextState += offset
					}
				}
				if lexStates[i].Default >= 0 {
					lexStates[i].Default += offset
				}
				if lexStates[i].EOF >= 0 {
					lexStates[i].EOF += offset
				}
			}
		}

		allStates = append(allStates, lexStates...)
	}

	return allStates, nil
}

// lexModeSpec describes what a lex mode should recognize.
type lexModeSpec struct {
	validSymbols   map[int]bool // terminal symbol IDs valid in this mode
	skipWhitespace bool         // whether to add skip transitions for whitespace
}

// stateSet is a sorted set of NFA state IDs (used as DFA state identity).
type stateSet struct {
	states []int
}

func (ss stateSet) key() string {
	// Use a compact key for map lookups.
	buf := make([]byte, len(ss.states)*4)
	for i, s := range ss.states {
		buf[i*4] = byte(s >> 24)
		buf[i*4+1] = byte(s >> 16)
		buf[i*4+2] = byte(s >> 8)
		buf[i*4+3] = byte(s)
	}
	return string(buf)
}

// subsetConstruction converts an NFA to a DFA using the subset construction algorithm.
func subsetConstruction(n *nfa) []dfaState {
	// Compute epsilon closure of start state.
	startClosure := epsilonClosure(n, []int{n.start})
	startSet := stateSet{states: startClosure}

	stateMap := make(map[string]int) // closure key → DFA state index
	var dfaStates []dfaState
	var worklist []stateSet

	addState := func(ss stateSet) int {
		k := ss.key()
		if id, ok := stateMap[k]; ok {
			return id
		}
		id := len(dfaStates)
		stateMap[k] = id

		// Determine accept symbol (highest priority = lowest priority number).
		accept := 0
		bestPriority := int(^uint(0) >> 1) // max int
		for _, s := range ss.states {
			if n.states[s].accept > 0 {
				if n.states[s].priority < bestPriority {
					bestPriority = n.states[s].priority
					accept = n.states[s].accept
				}
			}
		}

		dfaStates = append(dfaStates, dfaState{accept: accept})
		worklist = append(worklist, ss)
		return id
	}

	addState(startSet)

	for len(worklist) > 0 {
		current := worklist[0]
		worklist = worklist[1:]
		curID := stateMap[current.key()]

		// Collect all character ranges from transitions of current NFA states.
		ranges := collectTransitionRanges(n, current.states)

		// For each character range, compute the target NFA state set.
		for _, r := range ranges {
			targetStates := moveAndClose(n, current.states, r.lo, r.hi)
			if len(targetStates) == 0 {
				continue
			}
			targetSet := stateSet{states: targetStates}
			targetID := addState(targetSet)
			dfaStates[curID].transitions = append(dfaStates[curID].transitions,
				dfaTransition{lo: r.lo, hi: r.hi, nextState: targetID})
		}
	}

	return dfaStates
}

// epsilonClosure computes the epsilon closure of a set of NFA states.
func epsilonClosure(n *nfa, states []int) []int {
	seen := make(map[int]bool)
	var stack []int
	for _, s := range states {
		if !seen[s] {
			seen[s] = true
			stack = append(stack, s)
		}
	}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, t := range n.states[s].transitions {
			if t.epsilon && !seen[t.nextState] {
				seen[t.nextState] = true
				stack = append(stack, t.nextState)
			}
		}
	}
	result := make([]int, 0, len(seen))
	for s := range seen {
		result = append(result, s)
	}
	sort.Ints(result)
	return result
}

// collectTransitionRanges collects all non-epsilon character transition ranges
// from the given NFA states and partitions them into non-overlapping ranges.
func collectTransitionRanges(n *nfa, states []int) []runeRange {
	// Collect boundary points.
	var points []rune
	pointSet := make(map[rune]bool)
	addPoint := func(r rune) {
		if !pointSet[r] {
			pointSet[r] = true
			points = append(points, r)
		}
	}

	for _, s := range states {
		for _, t := range n.states[s].transitions {
			if t.epsilon {
				continue
			}
			addPoint(t.lo)
			addPoint(t.hi + 1) // exclusive upper bound
		}
	}

	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })

	// Create non-overlapping ranges from boundary points.
	var ranges []runeRange
	for i := 0; i < len(points); i++ {
		lo := points[i]
		var hi rune
		if i+1 < len(points) {
			hi = points[i+1] - 1
		} else {
			hi = lo
		}
		if lo > hi {
			continue
		}
		// Check if any NFA transition covers this range.
		hasTransition := false
		for _, s := range states {
			for _, t := range n.states[s].transitions {
				if !t.epsilon && t.lo <= lo && t.hi >= hi {
					hasTransition = true
					break
				}
			}
			if hasTransition {
				break
			}
		}
		if hasTransition {
			ranges = append(ranges, runeRange{lo, hi})
		}
	}

	return mergeAdjacentRanges(ranges, n, states)
}

// mergeAdjacentRanges merges adjacent ranges that lead to the same target state set.
func mergeAdjacentRanges(ranges []runeRange, n *nfa, states []int) []runeRange {
	if len(ranges) <= 1 {
		return ranges
	}
	var merged []runeRange
	cur := ranges[0]
	curTarget := moveTargets(n, states, cur.lo, cur.hi)

	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		nextTarget := moveTargets(n, states, next.lo, next.hi)
		if next.lo == cur.hi+1 && sameIntSlice(curTarget, nextTarget) {
			cur.hi = next.hi
		} else {
			merged = append(merged, cur)
			cur = next
			curTarget = nextTarget
		}
	}
	merged = append(merged, cur)
	return merged
}

func moveTargets(n *nfa, states []int, lo, hi rune) []int {
	var targets []int
	seen := make(map[int]bool)
	for _, s := range states {
		for _, t := range n.states[s].transitions {
			if !t.epsilon && t.lo <= lo && t.hi >= hi && !seen[t.nextState] {
				seen[t.nextState] = true
				targets = append(targets, t.nextState)
			}
		}
	}
	sort.Ints(targets)
	return targets
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// moveAndClose computes move(states, [lo,hi]) followed by epsilon closure.
func moveAndClose(n *nfa, states []int, lo, hi rune) []int {
	var targets []int
	seen := make(map[int]bool)
	for _, s := range states {
		for _, t := range n.states[s].transitions {
			if !t.epsilon && t.lo <= lo && t.hi >= hi && !seen[t.nextState] {
				seen[t.nextState] = true
				targets = append(targets, t.nextState)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return epsilonClosure(n, targets)
}

// convertDFAToLexStates converts internal DFA states to gotreesitter LexState format.
func convertDFAToLexStates(dfa []dfaState, addSkipTransitions bool) []gotreesitter.LexState {
	states := make([]gotreesitter.LexState, len(dfa))
	for i, ds := range dfa {
		ls := gotreesitter.LexState{
			AcceptToken: gotreesitter.Symbol(ds.accept),
			Skip:        ds.skip,
			Default:     -1,
			EOF:         -1,
		}

		for _, t := range ds.transitions {
			ls.Transitions = append(ls.Transitions, gotreesitter.LexTransition{
				Lo:        t.lo,
				Hi:        t.hi,
				NextState: t.nextState,
			})
		}

		states[i] = ls
	}

	// For the start state (index 0, local), add skip transitions for whitespace
	// characters if requested. The skip transitions loop back to state 0 (local).
	// The offset adjustment happens later during concatenation.
	if addSkipTransitions && len(states) > 0 {
		addWhitespaceSkip(&states[0])
	}

	return states
}

// addWhitespaceSkip modifies the start state to have skip transitions for
// whitespace characters (\t, \n, \r, space). These transitions loop back
// to the start state with Skip=true.
//
// IMPORTANT: We must NOT mark existing DFA transitions as Skip. Existing
// transitions were created by real terminal patterns (e.g., \r?\n) and must
// remain non-skip so the lexer can match them as real tokens. We only add
// NEW skip transitions for whitespace characters that have no existing
// transition. The DFA already handles whitespace via the extra symbol's
// accepting states (LexState.Skip = true).
func addWhitespaceSkip(state *gotreesitter.LexState) {
	wsRanges := []runeRange{
		{'\t', '\n'}, // \t and \n
		{'\r', '\r'}, // \r
		{' ', ' '},   // space
	}

	for _, ws := range wsRanges {
		// Check if ANY existing transition overlaps with this whitespace range.
		// If so, leave it alone — a real terminal needs that character range.
		// We only add skip transitions for characters that have no existing
		// DFA path, because the DFA already handles extras via accept-state
		// Skip flags.
		overlaps := false
		for i := range state.Transitions {
			t := &state.Transitions[i]
			// Check if the ranges overlap at all.
			if t.Lo <= ws.hi && t.Hi >= ws.lo {
				overlaps = true
				break
			}
		}
		if !overlaps {
			state.Transitions = append(state.Transitions, gotreesitter.LexTransition{
				Lo:        ws.lo,
				Hi:        ws.hi,
				NextState: 0, // loops back to start state (local index)
				Skip:      true,
			})
		}
	}

	// Sort transitions by Lo for deterministic behavior.
	sort.Slice(state.Transitions, func(i, j int) bool {
		return state.Transitions[i].Lo < state.Transitions[j].Lo
	})
}

// computeLexModes determines the lex modes needed for the parse table.
// Each unique set of valid terminal symbols gets its own lex mode.
// Returns the lex mode specs and a mapping from parser state to lex mode index.
func computeLexModes(
	stateCount int,
	tokenCount int,
	actionLookup func(state, sym int) bool,
	extraSymbols []int,
	immediateTokens map[int]bool,
	externalSymbols []int,
) ([]lexModeSpec, []int) {
	extraSet := make(map[int]bool)
	for _, e := range extraSymbols {
		extraSet[e] = true
	}

	// External tokens are handled by the external scanner, not the DFA.
	// Exclude them from lex mode computation to avoid creating spurious
	// lex modes based on external token validity differences.
	extSet := make(map[int]bool)
	for _, e := range externalSymbols {
		extSet[e] = true
	}

	modeMap := make(map[string]int) // key → mode index
	var modes []lexModeSpec
	stateToMode := make([]int, stateCount)

	for state := 0; state < stateCount; state++ {
		// Collect valid terminal symbols for this state.
		validSyms := make(map[int]bool)
		hasImmediate := false
		for sym := 1; sym < tokenCount; sym++ {
			if extSet[sym] {
				continue // skip external tokens
			}
			if actionLookup(state, sym) {
				validSyms[sym] = true
				if immediateTokens[sym] {
					hasImmediate = true
				}
			}
		}

		// Determine if whitespace should be skipped in this mode.
		// If any valid token is an immediate token and NO non-immediate
		// tokens are valid, then don't skip whitespace.
		skipWS := !hasImmediate || len(validSyms) > countImmediate(validSyms, immediateTokens)

		// Build key from sorted valid symbols.
		key := buildModeKey(validSyms, skipWS)

		if modeIdx, ok := modeMap[key]; ok {
			stateToMode[state] = modeIdx
		} else {
			modeIdx := len(modes)
			modeMap[key] = modeIdx
			modes = append(modes, lexModeSpec{
				validSymbols:   validSyms,
				skipWhitespace: skipWS,
			})
			stateToMode[state] = modeIdx
		}
	}

	return modes, stateToMode
}

func countImmediate(syms map[int]bool, imm map[int]bool) int {
	n := 0
	for s := range syms {
		if imm[s] {
			n++
		}
	}
	return n
}

func buildModeKey(syms map[int]bool, skip bool) string {
	sorted := make([]int, 0, len(syms))
	for s := range syms {
		sorted = append(sorted, s)
	}
	sort.Ints(sorted)
	buf := make([]byte, len(sorted)*4+1)
	for i, s := range sorted {
		buf[i*4] = byte(s >> 24)
		buf[i*4+1] = byte(s >> 16)
		buf[i*4+2] = byte(s >> 8)
		buf[i*4+3] = byte(s)
	}
	if skip {
		buf[len(buf)-1] = 1
	}
	return string(buf)
}
