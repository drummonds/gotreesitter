package gotreesitter

import (
	"bytes"
	"sync"
	"time"
)

// Parser reads parse tables from a Language and produces a syntax tree.
// It supports GLR parsing: when a (state, symbol) pair maps to multiple
// actions, the parser forks the stack and explores all alternatives in
// parallel while preserving distinct parse paths. Duplicate stack
// versions are collapsed and ambiguities are resolved at selection time.
//
// Parser is not safe for concurrent use. Use one parser per goroutine, or
// guard shared parser instances with external synchronization.
type Parser struct {
	language          *Language
	reuseCursor       reuseCursor
	reuseScratch      reuseScratch
	reuseMu           sync.Mutex
	fullArenaHint     uint32
	hasRecoverState   []bool
	hasRecoverSymbol  []bool
	recoverByState    [][]recoverSymbolAction
	hasKeywordState   []bool
	forceRawSpanAll   bool
	forceRawSpanTable []bool
	included          []Range
	denseLimit        int
	smallBase         int
	smallLookup       [][]smallActionPair
}

type smallActionPair struct {
	sym uint16
	val uint16
}

type recoverSymbolAction struct {
	sym    uint16
	action ParseAction
}

const (
	// maxForkCloneDepth limits GLR stack cloning for pathological ambiguity.
	// Above this depth, we execute only the first action to avoid runaway work.
	maxForkCloneDepth = 4 * 1024
	// maxConsecutivePrimaryReduces prevents infinite reduce loops on the
	// primary stack when no token advancement occurs.
	maxConsecutivePrimaryReduces = 10
)

// IncrementalParseProfile attributes incremental parse time into coarse buckets.
//
// ReuseCursorNanos includes reuse-cursor setup and subtree-candidate checks.
// ReparseNanos includes the remainder of incremental parsing/rebuild work.
type IncrementalParseProfile struct {
	ReuseCursorNanos   int64
	ReparseNanos       int64
	ReusedSubtrees     uint64
	ReusedBytes        uint64
	NewNodesAllocated  uint64
	RecoverSearches    uint64
	RecoverStateChecks uint64
	RecoverStateSkips  uint64
	RecoverSymbolSkips uint64
	RecoverLookups     uint64
	RecoverHits        uint64
	MaxStacksSeen      int
	EntryScratchPeak   uint64
}

type incrementalParseTiming struct {
	totalNanos         int64
	reuseNanos         int64
	reusedSubtrees     uint64
	reusedBytes        uint64
	newNodes           uint64
	recoverSearches    uint64
	recoverStateChecks uint64
	recoverStateSkips  uint64
	recoverSymbolSkips uint64
	recoverLookups     uint64
	recoverHits        uint64
	maxStacksSeen      int
	entryScratchPeak   uint64
}

type parseReuseState struct {
	reusedAny bool
	arenaRefs []*nodeArena
}

// NewParser creates a new Parser for the given language.
func NewParser(lang *Language) *Parser {
	p := &Parser{language: lang}
	if lang != nil {
		p.forceRawSpanAll = lang.Name == "yaml"
		for i, name := range lang.SymbolNames {
			if name != "statement_list" {
				continue
			}
			if p.forceRawSpanTable == nil {
				p.forceRawSpanTable = make([]bool, len(lang.SymbolNames))
			}
			p.forceRawSpanTable[i] = true
		}
		if lang.LargeStateCount > 0 {
			p.denseLimit = int(lang.LargeStateCount)
		} else {
			p.denseLimit = len(lang.ParseTable)
		}
		p.smallBase = int(lang.LargeStateCount)
		if len(lang.SmallParseTableMap) > 0 && len(lang.SmallParseTable) > 0 {
			p.smallLookup = buildSmallLookup(lang)
		}
		p.recoverByState, p.hasRecoverState, p.hasRecoverSymbol = buildRecoverActionsByState(lang)
		p.hasKeywordState = buildKeywordStates(lang)
	}
	return p
}

func (p *Parser) parseIncrementalInternal(source []byte, oldTree *Tree, ts TokenSource, timing *incrementalParseTiming) *Tree {
	// Fast path: unchanged source and no recorded edits.
	if canReuseUnchangedTree(source, oldTree, p.language) {
		return oldTree
	}

	// DFA with external scanner cannot safely skip — scanner state must be
	// preserved token-by-token, so reuse is disabled for that combination.
	// All other token sources (DFA without external scanner, custom lexers)
	// support efficient skipping via PointSkippableTokenSource.
	if _, isDFA := ts.(*dfaTokenSource); isDFA && p.language != nil && p.language.ExternalScanner != nil {
		return p.parseInternal(source, ts, nil, nil, arenaClassFull, timing)
	}

	p.reuseMu.Lock()
	defer p.reuseMu.Unlock()

	var reuse *reuseCursor
	if timing != nil {
		reuseStart := time.Now()
		reuse = p.reuseCursor.reset(oldTree, source, &p.reuseScratch)
		timing.reuseNanos += time.Since(reuseStart).Nanoseconds()
	} else {
		reuse = p.reuseCursor.reset(oldTree, source, &p.reuseScratch)
	}
	arenaClass := incrementalArenaClassForSource(source)
	tree := p.parseInternal(source, ts, reuse, oldTree, arenaClass, timing)
	if reuse != nil {
		if timing != nil {
			reuseStart := time.Now()
			reuse.commitScratch(&p.reuseScratch)
			timing.reuseNanos += time.Since(reuseStart).Nanoseconds()
		} else {
			reuse.commitScratch(&p.reuseScratch)
		}
	}
	return tree
}

func incrementalArenaClassForSource(source []byte) arenaClass {
	arenaClass := arenaClassIncremental
	// Very large files can outgrow incremental defaults and trigger repeated
	// fallback allocations; use full-parse slab sizing only beyond this point.
	const incrementalUseFullArenaThreshold = 1 * 1024 * 1024
	if len(source) >= incrementalUseFullArenaThreshold {
		arenaClass = arenaClassFull
	}
	return arenaClass
}

func canReuseUnchangedTree(source []byte, oldTree *Tree, lang *Language) bool {
	if oldTree == nil || oldTree.language != lang || len(oldTree.edits) != 0 {
		return false
	}
	oldSource := oldTree.source
	if len(oldSource) != len(source) {
		return false
	}
	if len(source) == 0 {
		return true
	}
	// Common incremental no-edit case: caller passes the same source slice.
	// Pointer equality avoids memcmp on hot no-op reparses.
	if &oldSource[0] == &source[0] {
		return true
	}
	return bytes.Equal(oldSource, source)
}

// parseInternal is the core GLR parsing loop shared by Parse and
// ParseWithTokenSource.
//
// It maintains a set of parse stacks. For unambiguous grammars (single
// action per table entry), there is exactly one stack and the algorithm
// reduces to standard LR parsing. When multiple actions exist for a
// (state, symbol) pair, the parser forks: one stack per alternative.
// Stacks that error out are dropped. Only duplicate stack versions are
// merged; distinct alternatives are preserved.
func (p *Parser) parseInternal(source []byte, ts TokenSource, reuse *reuseCursor, oldTree *Tree, arenaClass arenaClass, timing *incrementalParseTiming) *Tree {
	var parseStart time.Time
	if timing != nil {
		parseStart = time.Now()
	}
	deferParentLinks := reuse == nil && oldTree == nil
	if closer, ok := ts.(interface{ Close() }); ok {
		defer closer.Close()
	}
	scratch := acquireParserScratch()
	defer releaseParserScratch(scratch, deferParentLinks)
	trackChildErrors := !deferParentLinks

	arena := acquireNodeArena(arenaClass)
	arena.skipChildClear = reuse == nil && oldTree == nil
	if timing != nil {
		startUsed := arena.used
		defer func() {
			timing.totalNanos += time.Since(parseStart).Nanoseconds()
			if arena.used >= startUsed {
				timing.newNodes += uint64(arena.used - startUsed)
			}
			peak := uint64(scratch.entries.peakEntriesUsed())
			if peak > timing.entryScratchPeak {
				timing.entryScratchPeak = peak
			}
		}()
	}
	if arenaClass == arenaClassFull {
		defer func() {
			p.recordFullArenaUsage(arena.used)
		}()
	}
	switch arenaClass {
	case arenaClassFull:
		arena.ensureNodeCapacity(parseFullArenaNodeCapacity(len(source), p.fullArenaHintCapacity()))
		scratch.entries.ensureInitialCap(parseFullEntryScratchCapacity(len(source)))
	case arenaClassIncremental:
		arena.ensureNodeCapacity(parseIncrementalArenaNodeCapacity(len(source)))
		scratch.entries.ensureInitialCap(parseIncrementalEntryScratchCapacity(len(source)))
	}
	var reuseState parseReuseState
	nodeCount := 0
	iterationsUsed := 0
	peakStackDepth := 0
	maxStacksSeen := 0
	var perfTokensConsumed uint64
	var lastTokenEndByte uint32
	var lastTokenSymbol Symbol
	var lastTokenWasEOF bool
	tokenSourceEOFEarly := false
	expectedEOFByte := uint32(len(source))
	if len(p.included) > 0 {
		expectedEOFByte = p.included[len(p.included)-1].EndByte
	}
	parseRuntime := ParseRuntime{
		StopReason:      ParseStopNone,
		SourceLen:       uint32(len(source)),
		ExpectedEOFByte: expectedEOFByte,
	}
	finalizeTree := func(tree *Tree, stopReason ParseStopReason) *Tree {
		if tokenSourceEOFEarly && (stopReason == ParseStopAccepted || stopReason == ParseStopNone) {
			stopReason = ParseStopTokenSourceEOF
		}
		parseRuntime.StopReason = stopReason
		parseRuntime.Iterations = iterationsUsed
		parseRuntime.NodesAllocated = nodeCount
		parseRuntime.PeakStackDepth = peakStackDepth
		parseRuntime.MaxStacksSeen = maxStacksSeen
		parseRuntime.TokensConsumed = perfTokensConsumed
		parseRuntime.LastTokenEndByte = lastTokenEndByte
		parseRuntime.LastTokenSymbol = lastTokenSymbol
		parseRuntime.LastTokenWasEOF = lastTokenWasEOF
		parseRuntime.TokenSourceEOFEarly = tokenSourceEOFEarly
		parseRuntime.RootEndByte = 0
		parseRuntime.Truncated = false
		if tree != nil && tree.RootNode() != nil {
			parseRuntime.RootEndByte = tree.RootNode().EndByte()
			parseRuntime.Truncated = parseRuntime.RootEndByte < expectedEOFByte
		}
		if tree != nil {
			tree.setParseRuntime(parseRuntime)
		}
		return tree
	}
	finalize := func(stacks []glrStack, stopReason ParseStopReason) *Tree {
		tree := p.buildResultFromGLR(stacks, source, arena, oldTree, &reuseState, &scratch.nodeLinks)
		return finalizeTree(tree, stopReason)
	}
	finalizeErrorTree := func(stopReason ParseStopReason) *Tree {
		arena.Release()
		return finalizeTree(parseErrorTree(source, p.language), stopReason)
	}

	var stacksBuf [4]glrStack
	stacks := stacksBuf[:1]
	initialStackCap := 64 * 1024
	if reuse != nil {
		// Incremental reparses often borrow scratch slabs from an earlier full
		// parse. Preallocating that full retained capacity forces large memclr
		// work on every edit; keep incremental stack preallocation modest.
		initialStackCap = defaultStackEntrySlabCap
	}
	stacks[0] = newGLRStackWithScratchCap(p.language.InitialState, &scratch.entries, initialStackCap)
	stacks[0].recoverabilityKnown = true
	stacks[0].mayRecover = p.stateCanRecover(p.language.InitialState)
	maxStacksSeen = len(stacks)
	if timing != nil && timing.maxStacksSeen < len(stacks) {
		timing.maxStacksSeen = len(stacks)
	}
	maxStacks := parseMaxGLRStacksValue()
	mergePerKeyCap := maxStacksPerMergeKey
	if reuse != nil {
		// Incremental reparses benefit from tighter GLR retention because
		// edits are localized and we prioritize latency over broad ambiguity fanout.
		if maxStacks > 32 {
			maxStacks = 32
		}
		if mergePerKeyCap > 4 {
			mergePerKeyCap = 4
		}
	}
	scratch.merge.perKeyCap = mergePerKeyCap

	maxIter := parseIterations(len(source))
	maxDepth := parseStackDepth(len(source))
	maxNodes := parseNodeLimit(len(source))
	parseRuntime.IterationLimit = maxIter
	parseRuntime.StackDepthLimit = maxDepth
	parseRuntime.NodeLimit = maxNodes

	needToken := true
	var tok Token

	// Per-primary-stack infinite-reduce detection.
	var lastReduceState StateID
	var consecutiveReduces int

	for iter := 0; iter < maxIter; iter++ {
		iterationsUsed = iter + 1
		if perfCountersEnabled {
			perfRecordMaxConcurrentStacks(len(stacks))
		}
		if timing != nil && len(stacks) > timing.maxStacksSeen {
			timing.maxStacksSeen = len(stacks)
		}
		if len(stacks) > maxStacksSeen {
			maxStacksSeen = len(stacks)
		}
		// Fast-path the overwhelmingly common non-GLR case with one live stack.
		if len(stacks) == 1 {
			if stacks[0].dead {
				return finalizeErrorTree(ParseStopNoStacksAlive)
			}
		} else {
			// Prune dead stacks and collapse only truly duplicate stack versions.
			stacks = mergeStacksWithScratch(stacks, &scratch.merge)
			if len(stacks) == 0 {
				return finalizeErrorTree(ParseStopNoStacksAlive)
			}
		}
		// Cap the number of parallel stacks to prevent combinatorial explosion.
		// Keep the most promising stacks instead of truncating by insertion
		// order, which can discard viable parses on highly-ambiguous inputs.
		if len(stacks) > maxStacks {
			if perfCountersEnabled {
				perfRecordGlobalCapCull(len(stacks), maxStacks)
			}
			stacks = retainTopStacks(stacks, maxStacks)
		}

		// Keep the most promising stack in slot 0 because several parser
		// heuristics (lex-mode selection, reduce-loop detection, depth cap)
		// currently key off the primary stack.
		if len(stacks) > 1 {
			p.promotePrimaryStack(stacks)
		}
		for i := range stacks {
			stacks[i].cacheEntries = false
			if stacks[i].gss.head != nil {
				stacks[i].entries = nil
			}
		}

		// Safety: if the primary stack has grown beyond the depth cap,
		// or we've allocated too many nodes, return what we have.
		primaryDepth := stacks[0].depth()
		if primaryDepth > peakStackDepth {
			peakStackDepth = primaryDepth
		}
		if primaryDepth > maxDepth {
			return finalize(stacks, ParseStopStackDepthLimit)
		}
		if nodeCount > maxNodes {
			return finalize(stacks, ParseStopNodeLimit)
		}

		// Use the primary (first) stack's state for DFA lex mode selection.
		// Pass all active GLR stack states so external scanner valid symbols
		// are computed as the union across all stacks.
		if stateful, ok := ts.(parserStateTokenSource); ok {
			stateful.SetParserState(stacks[0].top().state)
			if len(stacks) > 1 {
				glrBuf := scratch.glrStates[:0]
				if cap(glrBuf) < len(stacks) {
					glrBuf = make([]StateID, 0, len(stacks))
				}
				for si := range stacks {
					if !stacks[si].dead {
						glrBuf = append(glrBuf, stacks[si].top().state)
					}
				}
				scratch.glrStates = glrBuf
				stateful.SetGLRStates(glrBuf)
			} else {
				if len(scratch.glrStates) > 0 {
					scratch.glrStates = scratch.glrStates[:0]
				}
				stateful.SetGLRStates(nil)
			}
		}

		// --- Token acquisition and incremental reuse ---
		if needToken {
			tok = ts.Next()
			perfTokensConsumed++
			lastTokenEndByte = tok.EndByte
			lastTokenSymbol = tok.Symbol
			lastTokenWasEOF = tok.Symbol == 0 && tok.StartByte == tok.EndByte
			if lastTokenWasEOF && tok.EndByte < expectedEOFByte {
				tokenSourceEOFEarly = true
			}
			// Clear per-stack shifted flags so all stacks process the
			// new token.
			for si := range stacks {
				stacks[si].shifted = false
			}
		}

		// Incremental parsing fast-path: when there is a single active stack,
		// try to reuse an unchanged subtree starting at the current token.
		if reuse != nil && len(stacks) == 1 && !stacks[0].dead && tok.Symbol != 0 {
			if timing != nil {
				reuseStart := time.Now()
				nextTok, reusedBytes, ok := p.tryReuseSubtree(&stacks[0], tok, ts, reuse, &scratch.entries, &scratch.gss)
				timing.reuseNanos += time.Since(reuseStart).Nanoseconds()
				if ok {
					timing.reusedSubtrees++
					timing.reusedBytes += uint64(reusedBytes)
					reuseState.markReused(stacks[0].top().node, arena)
					tok = nextTok
					needToken = false
					consecutiveReduces = 0
					continue
				}
			} else {
				if nextTok, _, ok := p.tryReuseSubtree(&stacks[0], tok, ts, reuse, &scratch.entries, &scratch.gss); ok {
					reuseState.markReused(stacks[0].top().node, arena)
					tok = nextTok
					needToken = false
					consecutiveReduces = 0
					continue
				}
			}
		}

		// --- Action application for all alive stacks ---
		// Process all alive stacks for this token.
		// We iterate by index because forks may append to `stacks`.
		numStacks := len(stacks)
		anyReduced := false

		parseActions := p.language.ParseActions
		for si := 0; si < numStacks; si++ {
			s := &stacks[si]
			if s.dead || s.shifted {
				continue
			}

			currentState := s.top().state
			actionIdx := p.lookupActionIndex(currentState, tok.Symbol)
			var actions []ParseAction
			if actionIdx != 0 && int(actionIdx) < len(parseActions) {
				actions = parseActions[actionIdx].Actions
			}

			// --- Extra token handling (comments, whitespace) ---
			if len(actions) > 0 &&
				actions[0].Type == ParseActionShift && actions[0].Extra {
				named := p.isNamedSymbol(tok.Symbol)
				leaf := newLeafNodeInArena(arena, tok.Symbol, named,
					tok.StartByte, tok.EndByte, tok.StartPoint, tok.EndPoint)
				leaf.isExtra = true
				leaf.preGotoState = currentState
				leaf.parseState = currentState
				p.pushStackNode(s, currentState, leaf, &scratch.entries, &scratch.gss)
				nodeCount++
				needToken = true
				continue
			}

			// --- No action: error handling ---
			if len(actions) == 0 {
				if tok.Symbol == 0 {
					if tok.StartByte == tok.EndByte {
						// True EOF. If this is the only stack, return result.
						if len(stacks) == 1 {
							return finalize(stacks, ParseStopAccepted)
						}
						// Multiple stacks at EOF: this one is done.
						// Mark dead so merge picks the best remaining.
						s.dead = true
						continue
					}
					// Zero-symbol width token: skip.
					needToken = true
					continue
				}

				// When multiple alternatives exist, drop no-action stacks
				// immediately instead of running deep recovery scans.
				if len(stacks) > 1 {
					s.dead = true
					continue
				}

				// Try grammar-directed recovery by searching the stack for
				// the nearest state that can recover on this lookahead.
				if depth, recoverAct, ok := p.findRecoverActionOnStack(s, tok.Symbol, timing); ok {
					if !s.truncate(depth + 1) {
						s.dead = true
						continue
					}
					p.applyAction(s, recoverAct, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
					needToken = true
					continue
				}

				// Only stack: error recovery — wrap token in error node.
				if s.depth() == 0 {
					return finalize(stacks, ParseStopNoStacksAlive)
				}
				errNode := newLeafNodeInArena(arena, errorSymbol, false,
					tok.StartByte, tok.EndByte, tok.StartPoint, tok.EndPoint)
				errNode.hasError = true
				trackChildErrors = true
				if perfCountersEnabled {
					perfRecordErrorNode()
				}
				errNode.parseState = currentState
				p.pushStackNode(s, currentState, errNode, &scratch.entries, &scratch.gss)
				nodeCount++
				needToken = true
				continue
			}

			// --- GLR: fork for multiple actions ---
			// For single-action entries (the common case), no fork occurs.
			// For multi-action entries, clone the stack for each alternative.
			if len(actions) > 1 {
				if perfCountersEnabled {
					rrConflict, rsConflict := classifyConflictShape(actions)
					switch {
					case rrConflict:
						perfRecordConflictRR()
					case rsConflict:
						perfRecordConflictRS()
					default:
						perfRecordConflictOther()
					}
				}
				if reuse != nil {
					act := actions[0]
					p.applyAction(s, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
					continue
				}
				if perfCountersEnabled {
					perfRecordFork(len(actions), perfTokensConsumed)
				}
				// Deep-stack GLR forks can trigger pathological clone volumes on
				// very large inputs. At extreme depths, take the primary action
				// to keep parsing bounded.
				if s.depth() > maxForkCloneDepth {
					act := actions[0]
					p.applyAction(s, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
					continue
				}
				// Copy the current stack value before appending forks.
				// Appending can reallocate `stacks`, which would invalidate `s`.
				base := *s
				for ai := 1; ai < len(actions); ai++ {
					fork := base.cloneWithScratch(&scratch.gss)
					act := actions[ai]
					p.applyAction(&fork, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
					stacks = append(stacks, fork)
				}
				// Re-acquire the pointer after possible reallocation.
				s = &stacks[si]
				act := actions[0]
				p.applyAction(s, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
			} else {
				act := actions[0]
				if act.Type == ParseActionReduce {
					p.applyActionWithReduceChain(s, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
				} else {
					p.applyAction(s, act, tok, &anyReduced, &nodeCount, arena, &scratch.entries, &scratch.gss, &scratch.tmpEntries, deferParentLinks, &trackChildErrors)
				}
			}
		}

		// After processing all stacks: determine whether to advance the
		// token. If any stack reduced, reuse the same token (the reducing
		// stacks have new top states and need to re-check the action for
		// the current lookahead). Otherwise, advance to next token.
		if anyReduced {
			needToken = false

			// Infinite-reduce detection (for the primary stack).
			if len(stacks) > 0 && !stacks[0].dead {
				topState := stacks[0].top().state
				if topState == lastReduceState {
					consecutiveReduces++
				} else {
					lastReduceState = topState
					consecutiveReduces = 1
				}
				if consecutiveReduces > maxConsecutivePrimaryReduces {
					needToken = true
					consecutiveReduces = 0
				}
			}
		} else {
			needToken = true
			consecutiveReduces = 0
		}

		// Check for accept on any stack.
		for si := range stacks {
			if stacks[si].accepted {
				return finalize(stacks[si:si+1], ParseStopAccepted)
			}
		}
	}

	// Iteration limit reached.
	return finalize(stacks, ParseStopIterationLimit)
}

func (p *Parser) promotePrimaryStack(stacks []glrStack) {
	if len(stacks) <= 1 {
		return
	}
	best := 0
	for i := 1; i < len(stacks); i++ {
		if stackComparePtr(&stacks[i], &stacks[best]) > 0 {
			best = i
		}
	}
	if best != 0 {
		stacks[0], stacks[best] = stacks[best], stacks[0]
	}
}

func retainTopStacks(stacks []glrStack, keep int) []glrStack {
	if keep <= 0 {
		return stacks[:0]
	}
	if len(stacks) <= keep {
		return stacks
	}
	for i := 0; i < keep; i++ {
		best := i
		for j := i + 1; j < len(stacks); j++ {
			if stackComparePtr(&stacks[j], &stacks[best]) > 0 {
				best = j
			}
		}
		if best != i {
			stacks[i], stacks[best] = stacks[best], stacks[i]
		}
	}
	return stacks[:keep]
}

func classifyConflictShape(actions []ParseAction) (rrConflict, rsConflict bool) {
	if len(actions) < 2 {
		return false, false
	}
	reduceCount := 0
	hasShift := false
	hasOther := false
	for i := range actions {
		switch actions[i].Type {
		case ParseActionReduce:
			reduceCount++
		case ParseActionShift:
			hasShift = true
		default:
			hasOther = true
		}
	}
	if hasOther || reduceCount == 0 {
		return false, false
	}
	if hasShift {
		return false, true
	}
	return reduceCount >= 2, false
}
