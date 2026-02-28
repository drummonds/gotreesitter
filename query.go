package gotreesitter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Query holds compiled patterns parsed from a tree-sitter .scm query file.
// It can be executed against a syntax tree to find matching nodes and
// return captured names.
type Query struct {
	patterns []Pattern
	captures []string // capture name by index

	rootCandidatesBySymbol map[Symbol][]int
	rootFallbackCandidates []int
}

// Pattern is a single top-level S-expression pattern in a query.
type Pattern struct {
	steps      []QueryStep
	predicates []QueryPredicate
}

// QueryStep is one matching instruction within a pattern.
type QueryStep struct {
	symbol       Symbol          // node type to match, or 0 for wildcard
	field        FieldID         // required field on parent, or 0
	absentFields []FieldID       // fields that must be absent on this node
	captureID    int             // first capture index into Query.captures, or -1
	captureIDs   []int           // all captures in declaration order
	isNamed      bool            // whether we expect a named node
	depth        int             // nesting depth (0 = top-level node in pattern)
	quantifier   queryQuantifier // ?, *, + (default: exactly one)
	anchorBefore bool            // '.' before this step (first child / immediate sibling)
	anchorAfter  bool            // '.' after this step (last child)
	// For alternation steps, alternatives lists the alternative symbols
	// that can match at this position. If non-nil, symbol is ignored.
	alternatives []alternativeSymbol
	// textMatch is for string literal matching ("func", "return", etc.).
	// When non-empty, we match anonymous nodes whose symbol name equals this.
	textMatch string
}

type queryQuantifier uint8

const (
	queryQuantifierOne queryQuantifier = iota
	queryQuantifierZeroOrOne
	queryQuantifierZeroOrMore
	queryQuantifierOneOrMore
)

type queryPredicateType uint8

const (
	predicateEq queryPredicateType = iota
	predicateNotEq
	predicateMatch
	predicateNotMatch
	predicateAnyOf
	predicateNotAnyOf
	predicateLuaMatch
	predicateHasAncestor
	predicateNotHasAncestor
	predicateNotHasParent
	predicateIs
	predicateIsNot
	predicateSet
	predicateOffset
	predicateAnyEq
	predicateAnyNotEq
	predicateAnyMatch
	predicateAnyNotMatch
	predicateSelectAdjacent
	predicateStrip
)

// QueryPredicate is a post-match constraint attached to a pattern.
// Supported forms:
//   - (#eq? @a @b)
//   - (#eq? @a "literal")
//   - (#not-eq? @a @b)
//   - (#not-eq? @a "literal")
//   - (#match? @a "regex")
//   - (#not-match? @a "regex")
//   - (#lua-match? @a "lua-pattern")
//   - (#any-of? @a "v1" "v2" ...)
//   - (#not-any-of? @a "v1" "v2" ...)
//   - (#any-eq? @a "literal"), (#any-eq? @a @b)
//   - (#any-not-eq? @a "literal"), (#any-not-eq? @a @b)
//   - (#any-match? @a "regex")
//   - (#any-not-match? @a "regex")
//   - (#has-ancestor? @a type ...)
//   - (#not-has-ancestor? @a type ...)
//   - (#not-has-parent? @a type ...)
//   - (#is? ...), (#is-not? ...)
//   - (#set! key value), (#offset! @cap ...)
type QueryPredicate struct {
	kind queryPredicateType

	leftCapture  string
	rightCapture string // optional for #eq? / #not-eq?
	// optional property/name token for #is? / #is-not?.
	property string
	literal  string // literal or regex source
	values   []string
	regex    *regexp.Regexp
	offset   [4]int // #offset! start_row start_col end_row end_col
}

// alternativeSymbol is one branch of an alternation like [(true) (false)].
type alternativeSymbol struct {
	symbol  Symbol
	isNamed bool
	// textMatch for string alternatives like "func"
	textMatch string
	// captureID is the first capture on this branch. captureIDs contains all.
	captureID  int
	captureIDs []int
	// steps/predicates represent a complex branch like
	// [(function_declaration name: (identifier) @name) ...].
	steps      []QueryStep
	predicates []QueryPredicate
}

// QueryMatch represents a successful pattern match with its captures.
type QueryMatch struct {
	PatternIndex int
	Captures     []QueryCapture
}

// QueryCapture is a single captured node within a match.
type QueryCapture struct {
	Name string
	Node *Node
	// TextOverride, when non-empty, replaces the node's source text for
	// downstream consumers. It is set by the #strip! directive.
	TextOverride string
}

// Text returns the effective text for this capture. If TextOverride is set
// (e.g. by the #strip! directive), it is returned. Otherwise the node's
// source text is returned.
func (c QueryCapture) Text(source []byte) string {
	if c.TextOverride != "" {
		return c.TextOverride
	}
	if c.Node == nil {
		return ""
	}
	return c.Node.Text(source)
}

type queryUnknownNodeTypeError struct {
	name string
}

func (e queryUnknownNodeTypeError) Error() string {
	return fmt.Sprintf("query: unknown node type %q", e.name)
}

// QueryCursor incrementally walks a node subtree and yields matches one by one.
// It is the streaming counterpart to Query.Execute and avoids materializing all
// matches up front.
type QueryCursor struct {
	query  *Query
	lang   *Language
	source []byte

	worklist []*Node

	hasByteRange bool
	startByte    uint32
	endByte      uint32

	hasPointRange bool
	startPoint    Point
	endPoint      Point

	currentNode       *Node
	currentCandidates []int
	candidateIdx      int

	// Pending captures from the last match returned by NextMatch.
	pendingCaptures   []QueryCapture
	pendingCaptureIdx int

	done bool
}

// NewQuery compiles query source (tree-sitter .scm format) against a language.
// It returns an error if the query syntax is invalid or references unknown
// node types or field names.
func NewQuery(source string, lang *Language) (*Query, error) {
	p := &queryParser{
		input: source,
		lang:  lang,
		q: &Query{
			captures: []string{},
		},
	}
	if err := p.parse(); err != nil {
		return nil, err
	}
	p.q.buildRootPatternIndex()
	return p.q, nil
}

// Execute runs the query against a syntax tree and returns all matches.
func (q *Query) Execute(tree *Tree) []QueryMatch {
	if tree == nil {
		return nil
	}
	return q.executeNode(tree.RootNode(), tree.Language(), tree.Source())
}

// ExecuteNode runs the query starting from a specific node.
//
// source is required for text predicates (like #eq? / #match?); pass the
// originating source bytes for correct predicate evaluation.
func (q *Query) ExecuteNode(node *Node, lang *Language, source []byte) []QueryMatch {
	return q.executeNode(node, lang, source)
}

// Exec creates a streaming cursor over matches rooted at node.
func (q *Query) Exec(node *Node, lang *Language, source []byte) *QueryCursor {
	c := &QueryCursor{
		query:  q,
		lang:   lang,
		source: source,
	}
	if node != nil {
		c.worklist = append(c.worklist, node)
	}
	return c
}

// SetByteRange restricts matches to nodes that intersect [startByte, endByte).
func (c *QueryCursor) SetByteRange(startByte, endByte uint32) {
	if c == nil {
		return
	}
	c.hasByteRange = true
	c.startByte = startByte
	c.endByte = endByte
}

// SetPointRange restricts matches to nodes that intersect [startPoint, endPoint).
func (c *QueryCursor) SetPointRange(startPoint, endPoint Point) {
	if c == nil {
		return
	}
	c.hasPointRange = true
	c.startPoint = startPoint
	c.endPoint = endPoint
}

func (c *QueryCursor) nodeIntersectsRanges(n *Node) bool {
	if n == nil {
		return false
	}
	if c.hasByteRange {
		if c.endByte <= c.startByte {
			return false
		}
		if n.endByte <= c.startByte || n.startByte >= c.endByte {
			return false
		}
	}
	if c.hasPointRange {
		if !pointLessThan(c.startPoint, c.endPoint) && c.startPoint != c.endPoint {
			return false
		}
		if !pointLessThan(n.startPoint, c.endPoint) && n.startPoint != c.endPoint {
			return false
		}
		if !pointLessThan(c.startPoint, n.endPoint) && c.startPoint != n.endPoint {
			return false
		}
	}
	return true
}

func (q *Query) executeNode(root *Node, lang *Language, source []byte) []QueryMatch {
	if root == nil || lang == nil {
		return nil
	}

	cursor := q.Exec(root, lang, source)
	var matches []QueryMatch
	for {
		m, ok := cursor.NextMatch()
		if !ok {
			break
		}
		matches = append(matches, m)
	}
	return matches
}

func (q *Query) rootPatternCandidates(sym Symbol) []int {
	if cands, ok := q.rootCandidatesBySymbol[sym]; ok {
		return cands
	}
	return q.rootFallbackCandidates
}

func mergePatternIndexLists(a, b []int) []int {
	if len(a) == 0 {
		out := make([]int, len(b))
		copy(out, b)
		return out
	}
	if len(b) == 0 {
		out := make([]int, len(a))
		copy(out, a)
		return out
	}

	out := make([]int, 0, len(a)+len(b))
	i, j := 0, 0
	last := -1
	hasLast := false

	appendUnique := func(v int) {
		if hasLast && v == last {
			return
		}
		out = append(out, v)
		last = v
		hasLast = true
	}

	for i < len(a) && j < len(b) {
		if a[i] < b[j] {
			appendUnique(a[i])
			i++
			continue
		}
		if b[j] < a[i] {
			appendUnique(b[j])
			j++
			continue
		}
		appendUnique(a[i])
		i++
		j++
	}
	for ; i < len(a); i++ {
		appendUnique(a[i])
	}
	for ; j < len(b); j++ {
		appendUnique(b[j])
	}
	return out
}

func (q *Query) buildRootPatternIndex() {
	bySymbolExact := make(map[Symbol][]int)
	var wildcard []int
	var complex []int

	for pi, pat := range q.patterns {
		if len(pat.steps) == 0 {
			continue
		}
		step := pat.steps[0]

		if len(step.alternatives) > 0 {
			complexAlt := false
			for _, alt := range step.alternatives {
				if alt.textMatch != "" || alt.symbol == 0 {
					complexAlt = true
					break
				}
			}
			if complexAlt {
				complex = append(complex, pi)
				continue
			}

			seen := make(map[Symbol]struct{}, len(step.alternatives))
			for _, alt := range step.alternatives {
				if _, ok := seen[alt.symbol]; ok {
					continue
				}
				seen[alt.symbol] = struct{}{}
				bySymbolExact[alt.symbol] = append(bySymbolExact[alt.symbol], pi)
			}
			continue
		}

		if step.textMatch != "" {
			complex = append(complex, pi)
			continue
		}
		if step.symbol == 0 {
			wildcard = append(wildcard, pi)
			continue
		}

		bySymbolExact[step.symbol] = append(bySymbolExact[step.symbol], pi)
	}

	fallback := mergePatternIndexLists(wildcard, complex)
	q.rootFallbackCandidates = fallback
	q.rootCandidatesBySymbol = make(map[Symbol][]int, len(bySymbolExact))
	for sym, exact := range bySymbolExact {
		q.rootCandidatesBySymbol[sym] = mergePatternIndexLists(exact, fallback)
	}
}

// NextMatch yields the next query match from the cursor.
func (c *QueryCursor) NextMatch() (QueryMatch, bool) {
	if c == nil || c.done || c.query == nil || c.lang == nil {
		return QueryMatch{}, false
	}
	q := c.query
	if q.rootCandidatesBySymbol == nil && q.rootFallbackCandidates == nil {
		q.buildRootPatternIndex()
	}

	// If callers mix NextCapture and NextMatch, NextMatch advances at match
	// granularity and discards any partially-consumed capture buffer.
	c.pendingCaptures = nil
	c.pendingCaptureIdx = 0

	for {
		if c.currentNode == nil {
			if len(c.worklist) == 0 {
				c.done = true
				return QueryMatch{}, false
			}

			// Pop next node in DFS order.
			n := c.worklist[len(c.worklist)-1]
			c.worklist = c.worklist[:len(c.worklist)-1]
			if !c.nodeIntersectsRanges(n) {
				continue
			}

			// Push children in reverse order so leftmost is visited first.
			children := n.Children()
			for i := len(children) - 1; i >= 0; i-- {
				if c.nodeIntersectsRanges(children[i]) {
					c.worklist = append(c.worklist, children[i])
				}
			}

			c.currentNode = n
			c.currentCandidates = q.rootPatternCandidates(n.Symbol())
			c.candidateIdx = 0
		}

		for c.candidateIdx < len(c.currentCandidates) {
			pi := c.currentCandidates[c.candidateIdx]
			c.candidateIdx++
			pat := q.patterns[pi]
			if caps, ok := q.matchPattern(&pat, c.currentNode, c.lang, c.source); ok {
				return QueryMatch{
					PatternIndex: pi,
					Captures:     caps,
				}, true
			}
		}

		// Exhausted candidates for this node; advance to the next node.
		c.currentNode = nil
		c.currentCandidates = nil
		c.candidateIdx = 0
	}
}

// NextCapture yields captures in match order by draining NextMatch results.
// This is a practical first-pass ordering: captures are returned in each
// match's capture order, then by subsequent matches in DFS match order.
func (c *QueryCursor) NextCapture() (QueryCapture, bool) {
	if c == nil || c.done || c.query == nil || c.lang == nil {
		return QueryCapture{}, false
	}

	for {
		if c.pendingCaptureIdx < len(c.pendingCaptures) {
			cap := c.pendingCaptures[c.pendingCaptureIdx]
			c.pendingCaptureIdx++
			return cap, true
		}

		m, ok := c.NextMatch()
		if !ok {
			return QueryCapture{}, false
		}
		c.pendingCaptures = m.Captures
		c.pendingCaptureIdx = 0
	}
}

// matchPattern tries to match a pattern against the given node.
// The pattern's steps describe a nested structure; step depth 0 matches
// the given node, depth 1 matches its children, etc.
func (q *Query) matchPattern(pat *Pattern, node *Node, lang *Language, source []byte) ([]QueryCapture, bool) {
	if len(pat.steps) == 0 {
		return nil, false
	}

	var captures []QueryCapture
	ok := q.matchSteps(pat.steps, 0, node, lang, source, &captures)
	if !ok {
		return nil, false
	}
	if !q.matchesPredicates(pat.predicates, captures, lang, source) {
		return nil, false
	}
	captures = q.applyDirectives(pat.predicates, captures, source)
	return captures, true
}

func (q *Query) matchStepWithRollback(steps []QueryStep, stepIdx int, node *Node, lang *Language, source []byte, captures *[]QueryCapture) bool {
	checkpoint := len(*captures)
	if q.matchSteps(steps, stepIdx, node, lang, source, captures) {
		return true
	}
	*captures = (*captures)[:checkpoint]
	return false
}

func (q *Query) matchesPredicates(predicates []QueryPredicate, captures []QueryCapture, lang *Language, source []byte) bool {
	if len(predicates) == 0 {
		return true
	}

	for _, pred := range predicates {
		switch pred.kind {
		case predicateEq:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			right := pred.literal
			if pred.rightCapture != "" {
				var okRight bool
				right, okRight = captureText(pred.rightCapture, captures, source)
				if !okRight {
					return false
				}
			}
			if left != right {
				return false
			}

		case predicateNotEq:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			right := pred.literal
			if pred.rightCapture != "" {
				var okRight bool
				right, okRight = captureText(pred.rightCapture, captures, source)
				if !okRight {
					return false
				}
			}
			if left == right {
				return false
			}

		case predicateMatch:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			if pred.regex == nil || !pred.regex.MatchString(left) {
				return false
			}

		case predicateNotMatch:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			if pred.regex != nil && pred.regex.MatchString(left) {
				return false
			}

		case predicateAnyEq:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			right := pred.literal
			if pred.rightCapture != "" {
				var ok bool
				right, ok = captureText(pred.rightCapture, captures, source)
				if !ok {
					return false
				}
			}
			found := false
			for _, n := range nodes {
				if n.Text(source) == right {
					found = true
					break
				}
			}
			if !found {
				return false
			}

		case predicateAnyNotEq:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			right := pred.literal
			if pred.rightCapture != "" {
				var ok bool
				right, ok = captureText(pred.rightCapture, captures, source)
				if !ok {
					return false
				}
			}
			found := false
			for _, n := range nodes {
				if n.Text(source) != right {
					found = true
					break
				}
			}
			if !found {
				return false
			}

		case predicateAnyMatch:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			if pred.regex == nil {
				return false
			}
			found := false
			for _, n := range nodes {
				if pred.regex.MatchString(n.Text(source)) {
					found = true
					break
				}
			}
			if !found {
				return false
			}

		case predicateAnyNotMatch:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			if pred.regex == nil {
				return false
			}
			found := false
			for _, n := range nodes {
				if !pred.regex.MatchString(n.Text(source)) {
					found = true
					break
				}
			}
			if !found {
				return false
			}

		case predicateLuaMatch:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			if pred.regex == nil || !pred.regex.MatchString(left) {
				return false
			}

		case predicateAnyOf:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			matched := false
			for _, v := range pred.values {
				if left == v {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}

		case predicateNotAnyOf:
			left, ok := captureText(pred.leftCapture, captures, source)
			if !ok {
				return false
			}
			for _, v := range pred.values {
				if left == v {
					return false
				}
			}

		case predicateHasAncestor:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			hasAny := false
			for _, n := range nodes {
				if nodeHasAncestorType(n, pred.values, lang) {
					hasAny = true
					break
				}
			}
			if !hasAny {
				return false
			}

		case predicateNotHasAncestor:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			for _, n := range nodes {
				if nodeHasAncestorType(n, pred.values, lang) {
					return false
				}
			}

		case predicateNotHasParent:
			nodes := captureNodes(pred.leftCapture, captures)
			if len(nodes) == 0 {
				return false
			}
			for _, n := range nodes {
				parent := n.Parent()
				if parent != nil && typeNameMatchesAny(parent.Type(lang), pred.values) {
					return false
				}
			}

		case predicateIs:
			if !predicateIsSatisfied(pred, captures) {
				return false
			}

		case predicateIsNot:
			if predicateIsSatisfied(pred, captures) {
				return false
			}

		case predicateSet, predicateOffset, predicateSelectAdjacent, predicateStrip:
			// Directives do not affect whether a match exists.
			continue

		default:
			return false
		}
	}

	return true
}

// applyDirectives applies capture-modifying directives (#select-adjacent!,
// #strip!) to the captures list after a match has been accepted.
func (q *Query) applyDirectives(predicates []QueryPredicate, captures []QueryCapture, source []byte) []QueryCapture {
	for _, pred := range predicates {
		switch pred.kind {
		case predicateSelectAdjacent:
			captures = applySelectAdjacent(pred, captures)
		case predicateStrip:
			captures = applyStrip(pred, captures, source)
		}
	}
	return captures
}

// applySelectAdjacent filters the captures named by pred.leftCapture to only
// those that are byte-adjacent to at least one capture named by
// pred.rightCapture. "Adjacent" means one node's end byte equals the other's
// start byte.
func applySelectAdjacent(pred QueryPredicate, captures []QueryCapture) []QueryCapture {
	itemsName := pred.leftCapture
	anchorName := pred.rightCapture

	// Collect anchor byte boundaries.
	type boundary struct {
		start, end uint32
	}
	var anchors []boundary
	for _, c := range captures {
		if c.Name == anchorName && c.Node != nil {
			anchors = append(anchors, boundary{c.Node.StartByte(), c.Node.EndByte()})
		}
	}
	if len(anchors) == 0 {
		// No anchors — remove all items captures.
		out := captures[:0]
		for _, c := range captures {
			if c.Name != itemsName {
				out = append(out, c)
			}
		}
		return out
	}

	isAdjacent := func(n *Node) bool {
		if n == nil {
			return false
		}
		nStart := n.StartByte()
		nEnd := n.EndByte()
		for _, a := range anchors {
			if nEnd == a.start || nStart == a.end {
				return true
			}
		}
		return false
	}

	out := make([]QueryCapture, 0, len(captures))
	for _, c := range captures {
		if c.Name == itemsName {
			if isAdjacent(c.Node) {
				out = append(out, c)
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// applyStrip applies the #strip! directive: for each capture named by
// pred.leftCapture, it sets TextOverride to the node's text with all
// matches of pred.regex removed.
func applyStrip(pred QueryPredicate, captures []QueryCapture, source []byte) []QueryCapture {
	if pred.regex == nil {
		return captures
	}
	for i := range captures {
		if captures[i].Name == pred.leftCapture && captures[i].Node != nil {
			text := captures[i].Node.Text(source)
			stripped := pred.regex.ReplaceAllString(text, "")
			if stripped != text {
				captures[i].TextOverride = stripped
			}
		}
	}
	return captures
}

func captureNodes(name string, captures []QueryCapture) []*Node {
	var nodes []*Node
	for _, c := range captures {
		if c.Name == name && c.Node != nil {
			nodes = append(nodes, c.Node)
		}
	}
	return nodes
}

func typeNameMatchesAny(typeName string, names []string) bool {
	for _, n := range names {
		if n == typeName {
			return true
		}
	}
	return false
}

func nodeHasAncestorType(node *Node, typeNames []string, lang *Language) bool {
	if node == nil || lang == nil {
		return false
	}
	for p := node.Parent(); p != nil; p = p.Parent() {
		if typeNameMatchesAny(p.Type(lang), typeNames) {
			return true
		}
	}
	return false
}

func capturePropertyMatches(captureName string, property string) bool {
	prop := strings.Trim(property, "\"")
	switch prop {
	case "local":
		return strings.Contains(captureName, "local") || strings.Contains(captureName, "parameter")
	case "local.parameter", "parameter":
		return strings.Contains(captureName, "parameter")
	case "function":
		return strings.Contains(captureName, "function")
	case "var", "variable":
		return strings.Contains(captureName, "var") || strings.Contains(captureName, "variable")
	}
	if captureName == prop {
		return true
	}
	return strings.HasSuffix(captureName, "."+prop)
}

func predicateIsSatisfied(pred QueryPredicate, captures []QueryCapture) bool {
	if pred.property == "" {
		return false
	}
	if pred.leftCapture != "" {
		nodes := captureNodes(pred.leftCapture, captures)
		if len(nodes) == 0 {
			return false
		}
		return capturePropertyMatches(pred.leftCapture, pred.property)
	}

	for _, c := range captures {
		if capturePropertyMatches(c.Name, pred.property) {
			return true
		}
	}
	return false
}

func captureText(name string, captures []QueryCapture, source []byte) (string, bool) {
	for _, c := range captures {
		if c.Name == name {
			if source == nil {
				return "", false
			}
			return c.Node.Text(source), true
		}
	}
	return "", false
}

type queryChildStepInfo struct {
	stepIdx int
	field   FieldID
}

// matchSteps matches a contiguous slice of steps starting at stepIdx
// against the given node at the expected depth.
func (q *Query) matchSteps(steps []QueryStep, stepIdx int, node *Node, lang *Language, source []byte, captures *[]QueryCapture) bool {
	if stepIdx >= len(steps) {
		return false
	}

	step := &steps[stepIdx]

	if len(step.alternatives) > 0 {
		if !q.matchAlternationStep(step, node, lang, source, captures) {
			return false
		}
	} else {
		// Check if this node matches the current step.
		if !q.nodeMatchesStep(step, node, lang) {
			return false
		}
		q.appendCaptureIDs(step.captureIDs, step.captureID, node, captures)
	}

	// Find child steps (steps at depth = step.depth + 1) that are direct
	// descendants of this step.
	childDepth := step.depth + 1
	childStart := stepIdx + 1

	// If there are no more steps, we matched successfully.
	if childStart >= len(steps) {
		return true
	}

	// If the next step is at the same depth or shallower, there are no
	// child constraints -- we matched.
	if steps[childStart].depth <= step.depth {
		return true
	}

	// Collect child step indices at childDepth (stop when we see a step
	// at a depth <= step.depth, meaning it belongs to a sibling/ancestor).
	var childSteps []queryChildStepInfo
	for i := childStart; i < len(steps); i++ {
		if steps[i].depth <= step.depth {
			break
		}
		if steps[i].depth == childDepth {
			childSteps = append(childSteps, queryChildStepInfo{
				stepIdx: i,
				field:   steps[i].field,
			})
		}
	}
	return q.matchChildSteps(node, steps, childSteps, lang, source, captures)
}

func (q *Query) appendCaptureIDs(ids []int, legacyID int, node *Node, captures *[]QueryCapture) {
	if len(ids) > 0 {
		for _, captureID := range ids {
			*captures = append(*captures, QueryCapture{
				Name: q.captures[captureID],
				Node: node,
			})
		}
		return
	}
	if legacyID >= 0 {
		*captures = append(*captures, QueryCapture{
			Name: q.captures[legacyID],
			Node: node,
		})
	}
}

func quantifierBounds(quantifier queryQuantifier) (int, int, bool) {
	switch quantifier {
	case queryQuantifierOne:
		return 1, 1, true
	case queryQuantifierZeroOrOne:
		return 0, 1, true
	case queryQuantifierZeroOrMore:
		return 0, -1, true
	case queryQuantifierOneOrMore:
		return 1, -1, true
	default:
		return 0, 0, false
	}
}

func (q *Query) stepAnchorsSatisfied(
	step *QueryStep,
	childPos int,
	hasNamed bool,
	firstNamedPos int,
	lastNamedPos int,
	prevHasNamed bool,
	prevLastNamedPos int,
	parentLastNamedPos int,
) bool {
	if step.anchorBefore {
		if !hasNamed {
			return false
		}
		if childPos == 0 {
			if firstNamedPos != 0 {
				return false
			}
		} else {
			if !prevHasNamed {
				return false
			}
			if firstNamedPos != prevLastNamedPos+1 {
				return false
			}
		}
	}

	if step.anchorAfter {
		if !hasNamed {
			return false
		}
		if lastNamedPos != parentLastNamedPos {
			return false
		}
	}

	return true
}

func (q *Query) matchChildSteps(
	parent *Node,
	steps []QueryStep,
	childSteps []queryChildStepInfo,
	lang *Language,
	source []byte,
	captures *[]QueryCapture,
) bool {
	children := parent.Children()
	namedPosByIndex := make([]int, len(children))
	namedPos := 0
	for i, child := range children {
		if child != nil && child.IsNamed() {
			namedPosByIndex[i] = namedPos
			namedPos++
		} else {
			namedPosByIndex[i] = -1
		}
	}
	parentLastNamedPos := namedPos - 1

	return q.matchChildStepsRecursive(
		parent, children, namedPosByIndex, parentLastNamedPos,
		steps, childSteps, 0, 0, false, -1,
		lang, source, captures,
	)
}

func (q *Query) matchChildStepsRecursive(
	parent *Node,
	children []*Node,
	namedPosByIndex []int,
	parentLastNamedPos int,
	steps []QueryStep,
	childSteps []queryChildStepInfo,
	childPos int,
	nextChildIdx int,
	prevHasNamed bool,
	prevLastNamedPos int,
	lang *Language,
	source []byte,
	captures *[]QueryCapture,
) bool {
	if childPos >= len(childSteps) {
		return true
	}

	cs := childSteps[childPos]
	step := &steps[cs.stepIdx]
	minCount, maxCount, ok := quantifierBounds(step.quantifier)
	if !ok {
		return false
	}

	var candidateIndices []int
	if cs.field != 0 {
		fieldName := ""
		if int(cs.field) < len(lang.FieldNames) {
			fieldName = lang.FieldNames[cs.field]
		}
		if fieldName == "" {
			return false
		}

		fieldChild := parent.ChildByFieldName(fieldName, lang)
		if fieldChild != nil {
			fieldIdx := -1
			for i, child := range children {
				if child == fieldChild {
					fieldIdx = i
					break
				}
			}
			if fieldIdx >= nextChildIdx && q.nodeMatchesStep(step, fieldChild, lang) {
				candidateIndices = append(candidateIndices, fieldIdx)
			}
		}
	} else {
		for i := nextChildIdx; i < len(children); i++ {
			child := children[i]
			if q.nodeMatchesStep(step, child, lang) {
				candidateIndices = append(candidateIndices, i)
			}
		}
	}

	if maxCount < 0 || maxCount > len(candidateIndices) {
		maxCount = len(candidateIndices)
	}
	if minCount > len(candidateIndices) {
		return false
	}

	// Greedy-first for consistency with prior quantifier behavior; backtrack as needed.
	for count := maxCount; count >= minCount; count-- {
		checkpoint := len(*captures)
		var tryCombinations func(
			candidatePos int,
			chosen int,
			nextIdx int,
			hasNamed bool,
			firstNamedPos int,
			lastNamedPos int,
		) bool

		tryCombinations = func(
			candidatePos int,
			chosen int,
			nextIdx int,
			hasNamed bool,
			firstNamedPos int,
			lastNamedPos int,
		) bool {
			if chosen == count {
				if !q.stepAnchorsSatisfied(
					step, childPos, hasNamed, firstNamedPos, lastNamedPos,
					prevHasNamed, prevLastNamedPos, parentLastNamedPos,
				) {
					return false
				}
				return q.matchChildStepsRecursive(
					parent, children, namedPosByIndex, parentLastNamedPos,
					steps, childSteps, childPos+1, nextIdx, hasNamed, lastNamedPos,
					lang, source, captures,
				)
			}

			remaining := count - chosen
			limit := len(candidateIndices) - remaining
			for i := candidatePos; i <= limit; i++ {
				childIdx := candidateIndices[i]
				child := children[childIdx]

				childCheckpoint := len(*captures)
				if !q.matchStepWithRollback(steps, cs.stepIdx, child, lang, source, captures) {
					*captures = (*captures)[:childCheckpoint]
					continue
				}

				nextIdxForChoice := nextIdx
				if childIdx+1 > nextIdxForChoice {
					nextIdxForChoice = childIdx + 1
				}

				hasNamedForChoice := hasNamed
				firstNamedForChoice := firstNamedPos
				lastNamedForChoice := lastNamedPos
				if namedPos := namedPosByIndex[childIdx]; namedPos >= 0 {
					if !hasNamedForChoice {
						hasNamedForChoice = true
						firstNamedForChoice = namedPos
					}
					lastNamedForChoice = namedPos
				}

				if tryCombinations(
					i+1, chosen+1, nextIdxForChoice,
					hasNamedForChoice, firstNamedForChoice, lastNamedForChoice,
				) {
					return true
				}

				*captures = (*captures)[:childCheckpoint]
			}

			return false
		}

		if tryCombinations(0, 0, nextChildIdx, false, -1, -1) {
			return true
		}

		*captures = (*captures)[:checkpoint]
	}

	return false
}

func (q *Query) matchAlternationStep(step *QueryStep, node *Node, lang *Language, source []byte, captures *[]QueryCapture) bool {
	for _, alt := range step.alternatives {
		if !alternativeMatchesNode(alt, node, lang) {
			continue
		}

		checkpoint := len(*captures)

		// Captures on the alternation itself apply regardless of chosen branch.
		q.appendCaptureIDs(step.captureIDs, step.captureID, node, captures)

		if len(alt.steps) > 0 {
			if !q.matchStepWithRollback(alt.steps, 0, node, lang, source, captures) {
				*captures = (*captures)[:checkpoint]
				continue
			}
			if len(alt.predicates) > 0 && !q.matchesPredicates(alt.predicates, *captures, lang, source) {
				*captures = (*captures)[:checkpoint]
				continue
			}
			return true
		}

		// Simple alternation branch captures (no nested structure).
		q.appendCaptureIDs(alt.captureIDs, alt.captureID, node, captures)
		return true
	}
	return false
}

// nodeMatchesStep checks if a single node matches a single step's type/symbol constraint.
func (q *Query) nodeMatchesStep(step *QueryStep, node *Node, lang *Language) bool {
	// Alternation matching.
	if len(step.alternatives) > 0 {
		for _, alt := range step.alternatives {
			if alternativeMatchesNode(alt, node, lang) {
				return true
			}
		}
		return false
	}

	// Text matching for string literals like "func".
	if step.textMatch != "" {
		return !node.IsNamed() && node.Type(lang) == step.textMatch
	}

	// Wildcard (symbol == 0 and no textMatch and no alternatives).
	if step.symbol == 0 {
		return true
	}

	// Symbol matching.
	if node.Symbol() != step.symbol {
		return false
	}

	// Named check.
	if step.isNamed && !node.IsNamed() {
		return false
	}

	// Field-negation constraints: each listed field must be absent.
	for _, fid := range step.absentFields {
		if int(fid) <= 0 || int(fid) >= len(lang.FieldNames) {
			return false
		}
		fieldName := lang.FieldNames[fid]
		if fieldName == "" {
			return false
		}
		if node.ChildByFieldName(fieldName, lang) != nil {
			return false
		}
	}

	return true
}

func alternativeMatchesNode(alt alternativeSymbol, node *Node, lang *Language) bool {
	// Wildcard in alternation `( _ )` should match any node.
	if alt.symbol == 0 && alt.textMatch == "" {
		return true
	}

	if alt.textMatch != "" {
		// String match for anonymous nodes.
		return !node.IsNamed() && node.Type(lang) == alt.textMatch
	}

	return node.Symbol() == alt.symbol && node.IsNamed() == alt.isNamed
}

// PatternCount returns the number of patterns in the query.
func (q *Query) PatternCount() int {
	return len(q.patterns)
}

// CaptureNames returns the list of unique capture names used in the query.
func (q *Query) CaptureNames() []string {
	return q.captures
}

// SetValues returns the values of a #set! directive with the given key
// for a match's pattern, or nil if not present. This is used by
// InjectionParser to read injection.language metadata.
func (m QueryMatch) SetValues(q *Query, key string) []string {
	if q == nil || m.PatternIndex < 0 || m.PatternIndex >= len(q.patterns) {
		return nil
	}
	for _, pred := range q.patterns[m.PatternIndex].predicates {
		if pred.kind == predicateSet && pred.literal == key {
			return pred.values
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// S-expression parser
// --------------------------------------------------------------------------

// queryParser parses tree-sitter .scm query files into a Query.
type queryParser struct {
	input string
	pos   int
	lang  *Language
	q     *Query
}

func (p *queryParser) parse() error {
	for {
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) {
			break
		}

		ch := p.input[p.pos]

		if ch == '(' && p.pos+1 < len(p.input) && p.input[p.pos+1] == '#' {
			if len(p.q.patterns) == 0 {
				return fmt.Errorf("query: predicate must follow a pattern at position %d", p.pos)
			}
			pred, err := p.parsePredicate()
			if err != nil {
				return err
			}
			last := &p.q.patterns[len(p.q.patterns)-1]
			last.predicates = append(last.predicates, pred)
			if err := p.validatePatternPredicates(last); err != nil {
				return err
			}
			continue
		}

		switch {
		case ch == '(':
			// A top-level pattern.
			pat, err := p.parsePattern(0, 0)
			if err != nil {
				return err
			}
			p.q.patterns = append(p.q.patterns, *pat)

		case ch == '[':
			// Top-level alternation: ["func" "return"] @keyword
			pat, err := p.parseAlternationPattern(0, 0)
			if err != nil {
				return err
			}
			p.q.patterns = append(p.q.patterns, *pat)

		case ch == '"':
			// Top-level string match: "func" @keyword
			pat, err := p.parseStringPattern(0)
			if err != nil {
				return err
			}
			p.q.patterns = append(p.q.patterns, *pat)

		case isIdentStart(ch):
			// Top-level field shorthand: field: (pattern)
			pat, err := p.parseFieldShorthandPattern(0)
			if err != nil {
				return err
			}
			p.q.patterns = append(p.q.patterns, *pat)

		case ch == '.':
			return fmt.Errorf("query: unexpected top-level anchor '.' at position %d", p.pos)

		default:
			return fmt.Errorf("query: unexpected character %q at position %d", string(ch), p.pos)
		}
	}
	return nil
}

// parsePattern parses a parenthesized S-expression pattern.
// depth is the nesting depth for the steps produced.
func (p *queryParser) parsePattern(depth int, parentSymbolHint Symbol) (*Pattern, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '(' {
		return nil, fmt.Errorf("query: expected '(' at position %d", p.pos)
	}
	p.pos++ // consume '('
	p.skipWhitespaceAndComments()

	pat := &Pattern{}
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("query: unexpected end of input, expected node type or pattern")
	}

	rootIdx := -1

	// Parse the root element. This supports:
	//   - standard node patterns: (identifier ...)
	//   - parenthesized strings: ("(") @punctuation.bracket
	//   - grouping wrappers: ((identifier) ... (#set! ...))
	switch ch := p.input[p.pos]; {
	case isIdentStart(ch):
		nodeType, err := p.readIdentifier()
		if err != nil {
			return nil, fmt.Errorf("query: expected node type after '(' at position %d: %w", p.pos, err)
		}
		step, err := p.stepFromIdentifierName(depth, nodeType)
		if err != nil {
			return nil, err
		}
		pat.steps = append(pat.steps, step)
		rootIdx = 0

	case ch == '"':
		text, err := p.readString()
		if err != nil {
			return nil, err
		}
		pat.steps = append(pat.steps, QueryStep{
			captureID: -1,
			depth:     depth,
			textMatch: text,
		})
		rootIdx = 0

	case ch == '(' || ch == '[':
		innerPat, err := p.parsePatternElement(depth, parentSymbolHint)
		if err != nil {
			return nil, err
		}
		if len(innerPat.steps) == 0 {
			return nil, fmt.Errorf("query: empty grouped pattern at position %d", p.pos)
		}
		pat.steps = append(pat.steps, innerPat.steps...)
		pat.predicates = append(pat.predicates, innerPat.predicates...)
		rootIdx = 0

	default:
		return nil, fmt.Errorf("query: expected node type after '(' at position %d: query: expected identifier at position %d", p.pos, p.pos)
	}

	// Parse children, fields, and captures until ')'.
	pendingAnchor := false
	lastChildRootIdx := -1
	appendChildPattern := func(childPat *Pattern) {
		if childPat == nil || len(childPat.steps) == 0 {
			return
		}
		if pendingAnchor {
			childPat.steps[0].anchorBefore = true
			pendingAnchor = false
		}
		childRootIdx := len(pat.steps)
		pat.predicates = append(pat.predicates, childPat.predicates...)
		pat.steps = append(pat.steps, childPat.steps...)
		lastChildRootIdx = childRootIdx
	}
	for {
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) {
			return nil, fmt.Errorf("query: unexpected end of input, expected ')'")
		}

		ch := p.input[p.pos]

		if ch == ')' {
			if pendingAnchor && lastChildRootIdx >= 0 {
				pat.steps[lastChildRootIdx].anchorAfter = true
			}
			p.pos++ // consume ')'
			break
		}

		if ch == '.' {
			// Anchor operators:
			//   - before child: first-child / immediate-sibling anchor
			//   - after child: last-child anchor
			// Anchors only affect child constraints at this depth.
			p.pos++
			pendingAnchor = true
			continue
		}

		if ch == '!' {
			// Field-negation constraint like !type_parameters.
			p.pos++
			p.skipWhitespaceAndComments()
			fieldName, err := p.readIdentifier()
			if err != nil {
				return nil, err
			}
			if rootIdx >= 0 && rootIdx < len(pat.steps) {
				parentSymbol := pat.steps[rootIdx].symbol
				fieldID, err := p.resolveField(fieldName, parentSymbol, parentSymbolHint)
				if err != nil {
					return nil, err
				}
				pat.steps[rootIdx].absentFields = append(pat.steps[rootIdx].absentFields, fieldID)
			}
			continue
		}

		if ch == '@' {
			// Capture for the current node.
			capName, err := p.readCapture()
			if err != nil {
				return nil, err
			}
			capID := p.ensureCapture(capName)
			if rootIdx >= 0 && rootIdx < len(pat.steps) {
				p.addCaptureToStep(&pat.steps[rootIdx], capID)
			}
			continue
		}

		if ch == '(' {
			// Predicate expression.
			if p.pos+1 < len(p.input) && p.input[p.pos+1] == '#' {
				pred, err := p.parsePredicate()
				if err != nil {
					return nil, err
				}
				pat.predicates = append(pat.predicates, pred)
				continue
			}

			// Nested pattern (child constraint).
			currentRootSymbol := Symbol(0)
			if rootIdx >= 0 && rootIdx < len(pat.steps) {
				currentRootSymbol = pat.steps[rootIdx].symbol
			}
			childPat, err := p.parsePatternElement(depth+1, currentRootSymbol)
			if err != nil {
				return nil, err
			}
			appendChildPattern(childPat)
			continue
		}

		if ch == '[' {
			// Alternation child.
			currentRootSymbol := Symbol(0)
			if rootIdx >= 0 && rootIdx < len(pat.steps) {
				currentRootSymbol = pat.steps[rootIdx].symbol
			}
			childPat, err := p.parsePatternElement(depth+1, currentRootSymbol)
			if err != nil {
				return nil, err
			}
			appendChildPattern(childPat)
			continue
		}

		if ch == '"' {
			// String child.
			currentRootSymbol := Symbol(0)
			if rootIdx >= 0 && rootIdx < len(pat.steps) {
				currentRootSymbol = pat.steps[rootIdx].symbol
			}
			childPat, err := p.parsePatternElement(depth+1, currentRootSymbol)
			if err != nil {
				return nil, err
			}
			appendChildPattern(childPat)
			continue
		}

		// Check for field: syntax (identifier followed by ':')
		if isIdentStart(ch) {
			ident, err := p.readIdentifier()
			if err != nil {
				return nil, err
			}
			afterIdent := p.pos
			p.skipWhitespaceAndComments()
			if p.pos < len(p.input) && p.input[p.pos] == ':' {
				// It's a field constraint.
				p.pos++ // consume ':'
				p.skipWhitespaceAndComments()

				parentSymbol := Symbol(0)
				if rootIdx >= 0 && rootIdx < len(pat.steps) {
					parentSymbol = pat.steps[rootIdx].symbol
				}
				fieldID, err := p.resolveField(ident, parentSymbol, parentSymbolHint)
				if err != nil {
					return nil, err
				}

				// The child pattern follows.
				if p.pos >= len(p.input) {
					return nil, fmt.Errorf("query: expected child pattern after field %q", ident)
				}

				childPat, err := p.parsePatternElement(depth+1, parentSymbol)
				if err != nil {
					return nil, err
				}
				if len(childPat.steps) > 0 {
					childPat.steps[0].field = fieldID
				}
				appendChildPattern(childPat)
			} else {
				// Bare shorthand child pattern like `_` or `identifier`.
				p.pos = afterIdent
				childPat, err := p.parseIdentifierPatternFromName(depth+1, ident)
				if err != nil {
					return nil, err
				}
				appendChildPattern(childPat)
			}
			continue
		}

		return nil, fmt.Errorf("query: unexpected character %q at position %d", string(ch), p.pos)
	}

	// Check for capture after the closing paren.
	p.skipWhitespaceAndComments()
	if quantifier, ok := p.readStepQuantifier(); ok {
		if rootIdx >= 0 && rootIdx < len(pat.steps) {
			pat.steps[rootIdx].quantifier = quantifier
		}
		p.skipWhitespaceAndComments()
	}
	for p.pos < len(p.input) && p.input[p.pos] == '@' {
		capName, err := p.readCapture()
		if err != nil {
			return nil, err
		}
		capID := p.ensureCapture(capName)
		if rootIdx >= 0 && rootIdx < len(pat.steps) {
			p.addCaptureToStep(&pat.steps[rootIdx], capID)
		}
		p.skipWhitespaceAndComments()
	}

	if err := p.validatePatternPredicates(pat); err != nil {
		return nil, err
	}

	return pat, nil
}

// parseAlternationPattern parses [...] alternation syntax.
func (p *queryParser) parseAlternationPattern(depth int, parentSymbolHint Symbol) (*Pattern, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '[' {
		return nil, fmt.Errorf("query: expected '[' at position %d", p.pos)
	}
	p.pos++ // consume '['
	p.skipWhitespaceAndComments()

	var alts []alternativeSymbol

	for {
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) {
			return nil, fmt.Errorf("query: unexpected end of input in alternation")
		}

		if p.input[p.pos] == ']' {
			p.pos++ // consume ']'
			break
		}

		ch := p.input[p.pos]
		if ch == '.' {
			// Anchors inside alternations are parsed for compatibility and ignored.
			p.pos++
			continue
		}

		var branchPat *Pattern
		var err error
		if ch == '(' || ch == '[' || ch == '"' {
			branchPat, err = p.parsePatternElement(depth, parentSymbolHint)
		} else if isIdentStart(ch) {
			// Alternation may contain field shorthand branches like:
			// [name: (identifier) alias: (identifier)].
			ident, readErr := p.readIdentifier()
			if readErr != nil {
				return nil, readErr
			}
			p.skipWhitespaceAndComments()
			if p.pos < len(p.input) && p.input[p.pos] == ':' {
				p.pos++ // consume ':'
				p.skipWhitespaceAndComments()
				branchPat, err = p.parsePatternElement(depth, parentSymbolHint)
			} else {
				branchPat, err = p.parseIdentifierPatternFromName(depth, ident)
			}
		} else {
			return nil, fmt.Errorf("query: unexpected character %q in alternation at position %d", string(ch), p.pos)
		}
		if err != nil {
			return nil, err
		}
		if len(branchPat.steps) == 0 {
			continue
		}

		root := branchPat.steps[0]
		alt := alternativeSymbol{
			symbol:    root.symbol,
			isNamed:   root.isNamed,
			textMatch: root.textMatch,
			captureID: -1,
		}
		if len(branchPat.predicates) > 0 || len(branchPat.steps) > 1 {
			alt.steps = make([]QueryStep, len(branchPat.steps))
			copy(alt.steps, branchPat.steps)
			alt.predicates = make([]QueryPredicate, len(branchPat.predicates))
			copy(alt.predicates, branchPat.predicates)
		} else {
			if len(root.captureIDs) > 0 {
				for _, capID := range root.captureIDs {
					p.addCaptureToAlternative(&alt, capID)
				}
			} else if root.captureID >= 0 {
				p.addCaptureToAlternative(&alt, root.captureID)
			}
		}
		alts = append(alts, alt)
	}

	if len(alts) == 0 {
		return nil, fmt.Errorf("query: empty alternation")
	}

	step := QueryStep{
		captureID:    -1,
		depth:        depth,
		alternatives: alts,
	}

	// Check for capture after ']'.
	p.skipWhitespaceAndComments()
	if quantifier, ok := p.readStepQuantifier(); ok {
		step.quantifier = quantifier
		p.skipWhitespaceAndComments()
	}
	for p.pos < len(p.input) && p.input[p.pos] == '@' {
		capName, err := p.readCapture()
		if err != nil {
			return nil, err
		}
		p.addCaptureToStep(&step, p.ensureCapture(capName))
		p.skipWhitespaceAndComments()
	}

	return &Pattern{steps: []QueryStep{step}}, nil
}

// parseStringPattern parses a "string" pattern for matching anonymous nodes.
func (p *queryParser) parseStringPattern(depth int) (*Pattern, error) {
	text, err := p.readString()
	if err != nil {
		return nil, err
	}

	step := QueryStep{
		captureID: -1,
		depth:     depth,
		textMatch: text,
	}

	// Check for capture after the string.
	p.skipWhitespaceAndComments()
	if quantifier, ok := p.readStepQuantifier(); ok {
		step.quantifier = quantifier
		p.skipWhitespaceAndComments()
	}
	for p.pos < len(p.input) && p.input[p.pos] == '@' {
		capName, err := p.readCapture()
		if err != nil {
			return nil, err
		}
		p.addCaptureToStep(&step, p.ensureCapture(capName))
		p.skipWhitespaceAndComments()
	}

	return &Pattern{steps: []QueryStep{step}}, nil
}

// parsePatternElement parses one query element at the given depth.
// Supported forms:
//   - (pattern ...)
//   - [alternation ...]
//   - "string"
//   - identifier / _ (shorthand single-node pattern)
func (p *queryParser) parsePatternElement(depth int, parentSymbolHint Symbol) (*Pattern, error) {
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("query: expected pattern element at end of input")
	}

	switch ch := p.input[p.pos]; {
	case ch == '(':
		return p.parsePattern(depth, parentSymbolHint)
	case ch == '[':
		return p.parseAlternationPattern(depth, parentSymbolHint)
	case ch == '"':
		return p.parseStringPattern(depth)
	case isIdentStart(ch):
		name, err := p.readIdentifier()
		if err != nil {
			return nil, err
		}
		return p.parseIdentifierPatternFromName(depth, name)
	default:
		return nil, fmt.Errorf("query: expected '(' or '[' or '\"' or identifier at position %d", p.pos)
	}
}

func (p *queryParser) stepFromIdentifierName(depth int, name string) (QueryStep, error) {
	sym, isNamed, err := p.resolveSymbol(name)
	if err != nil {
		return QueryStep{}, err
	}

	return QueryStep{
		symbol:    sym,
		isNamed:   isNamed,
		captureID: -1,
		depth:     depth,
	}, nil
}

func (p *queryParser) parseIdentifierPatternFromName(depth int, name string) (*Pattern, error) {
	step, err := p.stepFromIdentifierName(depth, name)
	if err != nil {
		return nil, err
	}

	p.skipWhitespaceAndComments()
	if quantifier, ok := p.readStepQuantifier(); ok {
		step.quantifier = quantifier
		p.skipWhitespaceAndComments()
	}
	for p.pos < len(p.input) && p.input[p.pos] == '@' {
		capName, err := p.readCapture()
		if err != nil {
			return nil, err
		}
		p.addCaptureToStep(&step, p.ensureCapture(capName))
		p.skipWhitespaceAndComments()
	}

	return &Pattern{steps: []QueryStep{step}}, nil
}

func (p *queryParser) parseFieldShorthandPattern(depth int) (*Pattern, error) {
	fieldName, err := p.readIdentifier()
	if err != nil {
		return nil, err
	}
	p.skipWhitespaceAndComments()
	if p.pos >= len(p.input) || p.input[p.pos] != ':' {
		return nil, fmt.Errorf("query: unexpected identifier %q at position %d", fieldName, p.pos)
	}
	p.pos++ // consume ':'
	p.skipWhitespaceAndComments()

	fieldID, err := p.resolveField(fieldName, 0, 0)
	if err != nil {
		return nil, err
	}

	childPat, err := p.parsePatternElement(depth+1, 0)
	if err != nil {
		return nil, err
	}
	if len(childPat.steps) > 0 {
		childPat.steps[0].field = fieldID
	}

	// Use a wildcard root so field constraints can still be represented in the
	// existing matcher shape.
	root := QueryStep{
		symbol:    0,
		isNamed:   false,
		captureID: -1,
		depth:     depth,
	}
	pat := &Pattern{steps: []QueryStep{root}}
	pat.steps = append(pat.steps, childPat.steps...)
	pat.predicates = append(pat.predicates, childPat.predicates...)
	return pat, nil
}

func (p *queryParser) parsePredicate() (QueryPredicate, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '(' {
		return QueryPredicate{}, fmt.Errorf("query: expected '(' at position %d", p.pos)
	}
	p.pos++ // consume '('
	p.skipWhitespaceAndComments()

	name, err := p.readPredicateName()
	if err != nil {
		return QueryPredicate{}, err
	}

	switch name {
	case "#eq?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		pred := QueryPredicate{
			kind:        predicateEq,
			leftCapture: left,
		}
		if rightIsCapture {
			pred.rightCapture = right
		} else {
			pred.literal = right
		}
		return pred, nil

	case "#not-eq?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		pred := QueryPredicate{
			kind:        predicateNotEq,
			leftCapture: left,
		}
		if rightIsCapture {
			pred.rightCapture = right
		} else {
			pred.literal = right
		}
		return pred, nil

	case "#match?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		if rightIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #match? second argument must be a string literal")
		}
		rx, err := regexp.Compile(right)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid regex in #match?: %w", err)
		}
		return QueryPredicate{
			kind:        predicateMatch,
			leftCapture: left,
			literal:     right,
			regex:       rx,
		}, nil

	case "#not-match?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		if rightIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #not-match? second argument must be a string literal")
		}
		rx, err := regexp.Compile(right)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid regex in #not-match?: %w", err)
		}
		return QueryPredicate{
			kind:        predicateNotMatch,
			leftCapture: left,
			literal:     right,
			regex:       rx,
		}, nil

	case "#any-eq?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		pred := QueryPredicate{
			kind:        predicateAnyEq,
			leftCapture: left,
		}
		if rightIsCapture {
			pred.rightCapture = right
		} else {
			pred.literal = right
		}
		return pred, nil

	case "#any-not-eq?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		pred := QueryPredicate{
			kind:        predicateAnyNotEq,
			leftCapture: left,
		}
		if rightIsCapture {
			pred.rightCapture = right
		} else {
			pred.literal = right
		}
		return pred, nil

	case "#any-match?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		if rightIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #any-match? second argument must be a string literal")
		}
		rx, err := regexp.Compile(right)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid regex in #any-match?: %w", err)
		}
		return QueryPredicate{
			kind:        predicateAnyMatch,
			leftCapture: left,
			literal:     right,
			regex:       rx,
		}, nil

	case "#any-not-match?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		if rightIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #any-not-match? second argument must be a string literal")
		}
		rx, err := regexp.Compile(right)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid regex in #any-not-match?: %w", err)
		}
		return QueryPredicate{
			kind:        predicateAnyNotMatch,
			leftCapture: left,
			literal:     right,
			regex:       rx,
		}, nil

	case "#lua-match?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		p.skipWhitespaceAndComments()
		right, rightIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		if rightIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #lua-match? second argument must be a string literal")
		}
		rx, err := compileLuaPattern(right)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid lua pattern in #lua-match?: %w", err)
		}
		return QueryPredicate{
			kind:        predicateLuaMatch,
			leftCapture: left,
			literal:     right,
			regex:       rx,
		}, nil

	case "#any-of?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		var values []string
		for {
			p.skipWhitespaceAndComments()
			if p.pos >= len(p.input) {
				return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
			}
			if p.input[p.pos] == ')' {
				p.pos++ // consume ')'
				break
			}
			v, kind, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			if kind == predicateArgCapture {
				return QueryPredicate{}, fmt.Errorf("query: #any-of? arguments after first must be non-capture literals")
			}
			values = append(values, v)
		}
		if len(values) == 0 {
			return QueryPredicate{}, fmt.Errorf("query: #any-of? requires at least one string literal")
		}
		return QueryPredicate{
			kind:        predicateAnyOf,
			leftCapture: left,
			values:      values,
		}, nil

	case "#not-any-of?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		var values []string
		for {
			p.skipWhitespaceAndComments()
			if p.pos >= len(p.input) {
				return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
			}
			if p.input[p.pos] == ')' {
				p.pos++ // consume ')'
				break
			}
			v, kind, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			if kind == predicateArgCapture {
				return QueryPredicate{}, fmt.Errorf("query: #not-any-of? arguments after first must be non-capture literals")
			}
			values = append(values, v)
		}
		if len(values) == 0 {
			return QueryPredicate{}, fmt.Errorf("query: #not-any-of? requires at least one literal")
		}
		return QueryPredicate{
			kind:        predicateNotAnyOf,
			leftCapture: left,
			values:      values,
		}, nil

	case "#has-ancestor?", "#not-has-ancestor?", "#not-has-parent?":
		p.skipWhitespaceAndComments()
		left, leftIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !leftIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: first predicate argument must be a capture in %s", name)
		}

		var types []string
		for {
			p.skipWhitespaceAndComments()
			if p.pos >= len(p.input) {
				return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
			}
			if p.input[p.pos] == ')' {
				p.pos++ // consume ')'
				break
			}
			tok, kind, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			if kind == predicateArgCapture {
				return QueryPredicate{}, fmt.Errorf("query: %s node type arguments must be non-capture identifiers", name)
			}
			types = append(types, tok)
		}
		if len(types) == 0 {
			return QueryPredicate{}, fmt.Errorf("query: %s requires at least one node type name", name)
		}
		kind := predicateHasAncestor
		if name == "#not-has-ancestor?" {
			kind = predicateNotHasAncestor
		}
		if name == "#not-has-parent?" {
			kind = predicateNotHasParent
		}
		return QueryPredicate{
			kind:        kind,
			leftCapture: left,
			values:      types,
		}, nil

	case "#is?", "#is-not?":
		var args []struct {
			value string
			kind  predicateArgKind
		}
		for {
			p.skipWhitespaceAndComments()
			if p.pos >= len(p.input) {
				return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
			}
			if p.input[p.pos] == ')' {
				p.pos++ // consume ')'
				break
			}
			tok, kind, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			args = append(args, struct {
				value string
				kind  predicateArgKind
			}{value: tok, kind: kind})
		}
		if len(args) == 0 {
			return QueryPredicate{}, fmt.Errorf("query: %s requires arguments", name)
		}

		pred := QueryPredicate{}
		if name == "#is?" {
			pred.kind = predicateIs
		} else {
			pred.kind = predicateIsNot
		}

		if args[0].kind == predicateArgCapture {
			pred.leftCapture = args[0].value
			if len(args) < 2 {
				return QueryPredicate{}, fmt.Errorf("query: %s capture form requires a property argument", name)
			}
			if args[1].kind == predicateArgCapture {
				return QueryPredicate{}, fmt.Errorf("query: %s property argument cannot be a capture", name)
			}
			pred.property = args[1].value
		} else {
			pred.property = args[0].value
			if len(args) >= 2 {
				if args[1].kind != predicateArgCapture {
					return QueryPredicate{}, fmt.Errorf("query: %s second argument must be a capture when provided", name)
				}
				pred.leftCapture = args[1].value
			}
		}
		return pred, nil

	case "#set!":
		p.skipWhitespaceAndComments()
		key, kind, err := p.readPredicateToken()
		if err != nil {
			return QueryPredicate{}, err
		}
		if kind == predicateArgCapture {
			return QueryPredicate{}, fmt.Errorf("query: #set! key must be a non-capture token")
		}
		var values []string
		for {
			p.skipWhitespaceAndComments()
			if p.pos >= len(p.input) {
				return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
			}
			if p.input[p.pos] == ')' {
				p.pos++
				break
			}
			v, _, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			values = append(values, v)
		}
		return QueryPredicate{
			kind:    predicateSet,
			literal: key,
			values:  values,
		}, nil

	case "#offset!":
		p.skipWhitespaceAndComments()
		capName, kind, err := p.readPredicateToken()
		if err != nil {
			return QueryPredicate{}, err
		}
		if kind != predicateArgCapture {
			return QueryPredicate{}, fmt.Errorf("query: #offset! first argument must be a capture")
		}
		var nums [4]int
		for i := 0; i < 4; i++ {
			p.skipWhitespaceAndComments()
			tok, tokKind, err := p.readPredicateToken()
			if err != nil {
				return QueryPredicate{}, err
			}
			if tokKind == predicateArgCapture {
				return QueryPredicate{}, fmt.Errorf("query: #offset! numeric arguments must be literals")
			}
			n, convErr := strconv.Atoi(tok)
			if convErr != nil {
				return QueryPredicate{}, fmt.Errorf("query: #offset! invalid integer %q", tok)
			}
			nums[i] = n
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'
		return QueryPredicate{
			kind:        predicateOffset,
			leftCapture: capName,
			offset:      nums,
		}, nil

	case "#select-adjacent!":
		p.skipWhitespaceAndComments()
		items, itemsIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !itemsIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #select-adjacent! first argument must be a capture")
		}

		p.skipWhitespaceAndComments()
		anchor, anchorIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !anchorIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #select-adjacent! second argument must be a capture")
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		return QueryPredicate{
			kind:         predicateSelectAdjacent,
			leftCapture:  items,
			rightCapture: anchor,
		}, nil

	case "#strip!":
		p.skipWhitespaceAndComments()
		capName, capIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if !capIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #strip! first argument must be a capture")
		}

		p.skipWhitespaceAndComments()
		pattern, patternIsCapture, err := p.readPredicateArg()
		if err != nil {
			return QueryPredicate{}, err
		}
		if patternIsCapture {
			return QueryPredicate{}, fmt.Errorf("query: #strip! second argument must be a string literal (regex)")
		}
		p.skipWhitespaceAndComments()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return QueryPredicate{}, fmt.Errorf("query: expected ')' to close predicate at position %d", p.pos)
		}
		p.pos++ // consume ')'

		rx, err := regexp.Compile(pattern)
		if err != nil {
			return QueryPredicate{}, fmt.Errorf("query: invalid regex in #strip!: %w", err)
		}
		return QueryPredicate{
			kind:        predicateStrip,
			leftCapture: capName,
			literal:     pattern,
			regex:       rx,
		}, nil

	default:
		return QueryPredicate{}, fmt.Errorf("query: unsupported predicate %q", name)
	}
}

func compileLuaPattern(pattern string) (*regexp.Regexp, error) {
	var out strings.Builder
	inClass := false

	writeLuaClass := func(ch byte, inClass bool) bool {
		if inClass {
			switch ch {
			case 'a':
				out.WriteString("A-Za-z")
			case 'c':
				out.WriteString("[:cntrl:]")
			case 'd':
				out.WriteString("0-9")
			case 'l':
				out.WriteString("a-z")
			case 'p':
				out.WriteString("[:punct:]")
			case 's':
				out.WriteString("\\s")
			case 'u':
				out.WriteString("A-Z")
			case 'w':
				out.WriteString("A-Za-z0-9")
			case 'x':
				out.WriteString("A-Fa-f0-9")
			case 'z':
				out.WriteString("\\x00")
			default:
				return false
			}
			return true
		}
		switch ch {
		case 'a':
			out.WriteString("[A-Za-z]")
		case 'c':
			out.WriteString("[[:cntrl:]]")
		case 'd':
			out.WriteString("[0-9]")
		case 'l':
			out.WriteString("[a-z]")
		case 'p':
			out.WriteString("[[:punct:]]")
		case 's':
			out.WriteString("\\s")
		case 'u':
			out.WriteString("[A-Z]")
		case 'w':
			out.WriteString("[A-Za-z0-9]")
		case 'x':
			out.WriteString("[A-Fa-f0-9]")
		case 'z':
			out.WriteString("\\x00")
		default:
			return false
		}
		return true
	}

	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '[':
			inClass = true
			out.WriteByte(ch)
		case ']':
			inClass = false
			out.WriteByte(ch)
		case '%':
			if i+1 >= len(pattern) {
				out.WriteString("%")
				continue
			}
			i++
			next := pattern[i]
			if writeLuaClass(next, inClass) {
				continue
			}
			out.WriteString(regexp.QuoteMeta(string(next)))
		default:
			out.WriteByte(ch)
		}
	}

	return regexp.Compile(out.String())
}

func (p *queryParser) validatePatternPredicates(pat *Pattern) error {
	if len(pat.predicates) == 0 {
		return nil
	}
	// Keep validation permissive. Runtime predicate evaluation rejects matches
	// when required captures are missing.
	return nil
}

// readIdentifier reads an identifier (node type name, field name).
// Identifiers can contain letters, digits, underscores, dots, and hyphens.
func (p *queryParser) readPredicateName() (string, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '#' {
		return "", fmt.Errorf("query: expected predicate name at position %d", p.pos)
	}
	start := p.pos
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ')' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return "", fmt.Errorf("query: expected predicate name at position %d", start)
	}
	return p.input[start:p.pos], nil
}

func (p *queryParser) readStepQuantifier() (queryQuantifier, bool) {
	if p.pos >= len(p.input) {
		return queryQuantifierOne, false
	}
	switch p.input[p.pos] {
	case '?':
		p.pos++
		return queryQuantifierZeroOrOne, true
	case '*':
		p.pos++
		return queryQuantifierZeroOrMore, true
	case '+':
		p.pos++
		return queryQuantifierOneOrMore, true
	default:
		return queryQuantifierOne, false
	}
}

type predicateArgKind uint8

const (
	predicateArgCapture predicateArgKind = iota
	predicateArgString
	predicateArgAtom
)

func (p *queryParser) readPredicateToken() (arg string, kind predicateArgKind, err error) {
	if p.pos >= len(p.input) {
		return "", predicateArgAtom, fmt.Errorf("query: expected predicate argument at end of input")
	}

	switch p.input[p.pos] {
	case '@':
		name, err := p.readCapture()
		if err != nil {
			return "", predicateArgAtom, err
		}
		return name, predicateArgCapture, nil
	case '"':
		text, err := p.readString()
		if err != nil {
			return "", predicateArgAtom, err
		}
		return text, predicateArgString, nil
	default:
		start := p.pos
		for p.pos < len(p.input) {
			ch := p.input[p.pos]
			if ch == ')' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
				break
			}
			p.pos++
		}
		if p.pos == start {
			return "", predicateArgAtom, fmt.Errorf("query: expected predicate argument at position %d", p.pos)
		}
		return p.input[start:p.pos], predicateArgAtom, nil
	}
}

func (p *queryParser) readPredicateArg() (arg string, isCapture bool, err error) {
	if p.pos >= len(p.input) {
		return "", false, fmt.Errorf("query: expected predicate argument at end of input")
	}

	switch p.input[p.pos] {
	case '@':
		name, err := p.readCapture()
		if err != nil {
			return "", false, err
		}
		return name, true, nil
	case '"':
		text, err := p.readString()
		if err != nil {
			return "", false, err
		}
		return text, false, nil
	default:
		return "", false, fmt.Errorf("query: expected capture or string literal in predicate at position %d", p.pos)
	}
}

func (p *queryParser) readIdentifier() (string, error) {
	start := p.pos
	for p.pos < len(p.input) {
		ch := rune(p.input[p.pos])
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '.' || ch == '-' || ch == '/' {
			p.pos++
		} else {
			break
		}
	}
	if p.pos == start {
		return "", fmt.Errorf("query: expected identifier at position %d", p.pos)
	}
	return p.input[start:p.pos], nil
}

// readCapture reads a @capture_name token. It consumes the '@' and the name.
func (p *queryParser) readCapture() (string, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '@' {
		return "", fmt.Errorf("query: expected '@' at position %d", p.pos)
	}
	p.pos++ // consume '@'
	name, err := p.readIdentifier()
	if err != nil {
		return "", fmt.Errorf("query: expected capture name after '@': %w", err)
	}
	return name, nil
}

// readString reads a quoted string like "func". Consumes the quotes.
func (p *queryParser) readString() (string, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '"' {
		return "", fmt.Errorf("query: expected '\"' at position %d", p.pos)
	}
	p.pos++ // consume opening '"'
	var sb strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '\\' && p.pos+1 < len(p.input) {
			p.pos++
			sb.WriteByte(p.input[p.pos])
			p.pos++
			continue
		}
		if ch == '"' {
			p.pos++ // consume closing '"'
			return sb.String(), nil
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return "", fmt.Errorf("query: unterminated string")
}

// skipWhitespaceAndComments skips whitespace and ;-style line comments.
func (p *queryParser) skipWhitespaceAndComments() {
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			p.pos++
			continue
		}
		if ch == ';' {
			// Skip to end of line.
			for p.pos < len(p.input) && p.input[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		break
	}
}

// resolveSymbol looks up a node type name in the language, returning the
// symbol ID and whether it's a named symbol. Uses Language.SymbolByName
// for O(1) lookup.
func (p *queryParser) resolveSymbol(name string) (Symbol, bool, error) {
	if name == "_" {
		return 0, false, nil
	}
	if name == "ERROR" {
		return errorSymbol, true, nil
	}
	if name == "MISSING" {
		return 0, false, nil
	}

	sym, ok := p.lang.SymbolByName(name)
	if !ok {
		// Some highlight queries use supertype-like names such as
		// "pattern/wildcard". Fall back to the rightmost segment when needed.
		if idx := strings.LastIndex(name, "/"); idx >= 0 && idx+1 < len(name) {
			if fallback, fallbackOK := p.lang.SymbolByName(name[idx+1:]); fallbackOK {
				sym = fallback
				ok = true
			}
		}
	}
	if !ok {
		return 0, false, queryUnknownNodeTypeError{name: name}
	}
	isNamed := false
	if int(sym) < len(p.lang.SymbolMetadata) {
		isNamed = p.lang.SymbolMetadata[sym].Named
	}
	return sym, isNamed, nil
}

// resolveField looks up a field name in the language with compatibility
// fallbacks for grammar/query naming drift.
func (p *queryParser) resolveField(name string, parentSymbol Symbol, parentSymbolHint Symbol) (FieldID, error) {
	fid, ok := p.lang.FieldByName(name)
	if ok {
		return fid, nil
	}

	// Some bundled queries use short field names like "key" while grammars
	// expose prefixed names (for example "option_key"). If parent type is
	// known, try parentName_fieldName first.
	seenSymbols := map[Symbol]struct{}{}
	for _, sym := range []Symbol{parentSymbol, parentSymbolHint} {
		if _, seen := seenSymbols[sym]; seen {
			continue
		}
		seenSymbols[sym] = struct{}{}
		if int(sym) < 0 || int(sym) >= len(p.lang.SymbolNames) {
			continue
		}
		parentName := p.lang.SymbolNames[sym]
		if parentName == "" {
			continue
		}
		candidate := parentName + "_" + name
		if fid, ok := p.lang.FieldByName(candidate); ok {
			return fid, nil
		}
		// Allow nested names like "foo/bar" -> "bar_name".
		if idx := strings.LastIndex(parentName, "/"); idx >= 0 && idx+1 < len(parentName) {
			candidate = parentName[idx+1:] + "_" + name
			if fid, ok := p.lang.FieldByName(candidate); ok {
				return fid, nil
			}
		}
	}

	// As a final fallback, allow a unique *_name suffix match.
	matchID := FieldID(0)
	matchCount := 0
	suffix := "_" + name
	for id, fieldName := range p.lang.FieldNames {
		if fieldName == "" {
			continue
		}
		if strings.HasSuffix(fieldName, suffix) {
			matchID = FieldID(id)
			matchCount++
		}
	}
	if matchCount == 1 {
		return matchID, nil
	}

	return 0, fmt.Errorf("query: unknown field name %q", name)
}

// ensureCapture returns the index for a capture name, adding it if new.
func (p *queryParser) ensureCapture(name string) int {
	for i, cn := range p.q.captures {
		if cn == name {
			return i
		}
	}
	idx := len(p.q.captures)
	p.q.captures = append(p.q.captures, name)
	return idx
}

func (p *queryParser) addCaptureToStep(step *QueryStep, captureID int) {
	if step.captureID < 0 {
		step.captureID = captureID
	}
	step.captureIDs = append(step.captureIDs, captureID)
}

func (p *queryParser) addCaptureToAlternative(alt *alternativeSymbol, captureID int) {
	if alt.captureID < 0 {
		alt.captureID = captureID
	}
	alt.captureIDs = append(alt.captureIDs, captureID)
}

// isIdentStart reports whether a byte can start an identifier.
func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}
