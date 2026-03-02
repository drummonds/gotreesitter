package grammargen

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// regexNode represents a node in the parsed regex AST.
type regexNode struct {
	kind     regexKind
	children []*regexNode
	lo, hi   rune // for charRange
	runes    []runeRange // for charClass
	negate   bool        // for charClass
	value    rune        // for literal
	count    int         // for counted repetition {n} min
	countMax int         // for {n,m}: max (-1 = unbounded, 0 = same as count)
}

type regexKind int

const (
	regexLiteral  regexKind = iota // single character
	regexCharClass                 // [a-z] or [^a-z]
	regexDot                       // . (any character except \n)
	regexSeq                       // concatenation
	regexAlt                       // alternation |
	regexStar                      // * zero-or-more
	regexPlus                      // + one-or-more
	regexQuestion                  // ? optional
	regexCount                     // {n} exactly n times
)

// runeRange is an inclusive character range.
type runeRange struct {
	lo, hi rune
}

// parseRegex parses a tree-sitter-compatible regex pattern into an AST.
func parseRegex(pattern string) (*regexNode, error) {
	p := &regexParser{input: pattern}
	node, err := p.parseAlt()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.input) {
		return nil, fmt.Errorf("unexpected character at position %d: %q", p.pos, string(p.input[p.pos]))
	}
	return node, nil
}

type regexParser struct {
	input string
	pos   int
}

func (p *regexParser) peek() (rune, bool) {
	if p.pos >= len(p.input) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(p.input[p.pos:])
	return r, true
}

func (p *regexParser) advance() rune {
	r, size := utf8.DecodeRuneInString(p.input[p.pos:])
	p.pos += size
	return r
}

// parseAlt parses alternation: a|b|c
func (p *regexParser) parseAlt() (*regexNode, error) {
	left, err := p.parseSeq()
	if err != nil {
		return nil, err
	}
	r, ok := p.peek()
	if !ok || r != '|' {
		return left, nil
	}
	alts := []*regexNode{left}
	for {
		r, ok = p.peek()
		if !ok || r != '|' {
			break
		}
		p.advance() // consume '|'
		alt, err := p.parseSeq()
		if err != nil {
			return nil, err
		}
		alts = append(alts, alt)
	}
	return &regexNode{kind: regexAlt, children: alts}, nil
}

// parseSeq parses concatenation of atoms.
func (p *regexParser) parseSeq() (*regexNode, error) {
	var items []*regexNode
	for {
		r, ok := p.peek()
		if !ok || r == '|' || r == ')' {
			break
		}
		atom, err := p.parseQuantified()
		if err != nil {
			return nil, err
		}
		items = append(items, atom)
	}
	if len(items) == 0 {
		return &regexNode{kind: regexSeq}, nil // empty sequence
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return &regexNode{kind: regexSeq, children: items}, nil
}

// parseQuantified parses an atom with optional quantifier: a*, a+, a?, a{n}
func (p *regexParser) parseQuantified() (*regexNode, error) {
	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	r, ok := p.peek()
	if !ok {
		return atom, nil
	}
	switch r {
	case '*':
		p.advance()
		return &regexNode{kind: regexStar, children: []*regexNode{atom}}, nil
	case '+':
		p.advance()
		return &regexNode{kind: regexPlus, children: []*regexNode{atom}}, nil
	case '?':
		p.advance()
		return &regexNode{kind: regexQuestion, children: []*regexNode{atom}}, nil
	case '{':
		min, max, err := p.parseCount()
		if err != nil {
			return nil, err
		}
		return &regexNode{kind: regexCount, children: []*regexNode{atom}, count: min, countMax: max}, nil
	}
	return atom, nil
}

// parseCount parses counted repetition: {n}, {n,m}, {n,}.
// Returns (min, max) where max=-1 means unbounded.
func (p *regexParser) parseCount() (int, int, error) {
	p.advance() // consume '{'
	start := p.pos
	for {
		r, ok := p.peek()
		if !ok {
			return 0, 0, fmt.Errorf("unterminated {}")
		}
		if r == '}' {
			break
		}
		p.advance()
	}
	content := p.input[start:p.pos]
	p.advance() // consume '}'

	if idx := strings.Index(content, ","); idx >= 0 {
		minStr := content[:idx]
		maxStr := content[idx+1:]
		min, err := strconv.Atoi(strings.TrimSpace(minStr))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid min in {%s}: %w", content, err)
		}
		if strings.TrimSpace(maxStr) == "" {
			return min, -1, nil // {n,} — unbounded
		}
		max, err := strconv.Atoi(strings.TrimSpace(maxStr))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid max in {%s}: %w", content, err)
		}
		return min, max, nil
	}

	n, err := strconv.Atoi(content)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid count in {%s}: %w", content, err)
	}
	return n, n, nil // {n} — exactly n
}

// parseAtom parses a single atom: literal, charclass, group, dot, escape.
func (p *regexParser) parseAtom() (*regexNode, error) {
	r, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("unexpected end of pattern")
	}
	switch r {
	case '(':
		p.advance() // consume '('
		// Check for non-capturing group (?:...)
		if r2, ok := p.peek(); ok && r2 == '?' {
			p.advance() // consume '?'
			if r3, ok := p.peek(); ok && r3 == ':' {
				p.advance() // consume ':'
			}
		}
		inner, err := p.parseAlt()
		if err != nil {
			return nil, err
		}
		r, ok = p.peek()
		if !ok || r != ')' {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.advance() // consume ')'
		return inner, nil
	case '[':
		return p.parseCharClass()
	case '.':
		p.advance()
		return &regexNode{kind: regexDot}, nil
	case '\\':
		return p.parseEscape()
	default:
		p.advance()
		return &regexNode{kind: regexLiteral, value: r}, nil
	}
}

// parseCharClass parses [a-z], [^a-z], etc.
func (p *regexParser) parseCharClass() (*regexNode, error) {
	p.advance() // consume '['
	negate := false
	if r, ok := p.peek(); ok && r == '^' {
		negate = true
		p.advance()
	}
	var ranges []runeRange
	first := true
	for {
		r, ok := p.peek()
		if !ok {
			return nil, fmt.Errorf("unterminated character class")
		}
		if r == ']' && !first {
			p.advance()
			break
		}
		first = false
		ch, err := p.parseCharClassChar()
		if err != nil {
			return nil, err
		}
		// Check for range: a-z
		if r2, ok := p.peek(); ok && r2 == '-' {
			saved := p.pos
			p.advance() // consume '-'
			if r3, ok := p.peek(); ok && r3 != ']' {
				hi, err := p.parseCharClassChar()
				if err != nil {
					return nil, err
				}
				ranges = append(ranges, runeRange{ch, hi})
				continue
			}
			p.pos = saved // backtrack, '-' is literal at end
		}
		ranges = append(ranges, runeRange{ch, ch})
	}
	return &regexNode{kind: regexCharClass, runes: ranges, negate: negate}, nil
}

// parseCharClassChar parses a single character inside a character class.
func (p *regexParser) parseCharClassChar() (rune, error) {
	r, ok := p.peek()
	if !ok {
		return 0, fmt.Errorf("unexpected end in character class")
	}
	if r == '\\' {
		p.advance()
		return p.parseEscapeChar()
	}
	p.advance()
	return r, nil
}

// parseEscape parses an escape sequence.
func (p *regexParser) parseEscape() (*regexNode, error) {
	p.advance() // consume '\\'
	// Check for shorthand character classes before consuming.
	r, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("unexpected end after \\")
	}
	switch r {
	case 'd': // \d → [0-9]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{{'0', '9'}}}, nil
	case 'D': // \D → [^0-9]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{{'0', '9'}}, negate: true}, nil
	case 'w': // \w → [a-zA-Z0-9_]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{
			{'a', 'z'}, {'A', 'Z'}, {'0', '9'}, {'_', '_'},
		}}, nil
	case 'W': // \W → [^a-zA-Z0-9_]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{
			{'a', 'z'}, {'A', 'Z'}, {'0', '9'}, {'_', '_'},
		}, negate: true}, nil
	case 's': // \s → [\t\n\r \f\v]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{
			{' ', ' '}, {'\t', '\t'}, {'\n', '\n'}, {'\r', '\r'}, {'\f', '\f'}, {'\v', '\v'},
		}}, nil
	case 'S': // \S → [^\t\n\r \f\v]
		p.advance()
		return &regexNode{kind: regexCharClass, runes: []runeRange{
			{' ', ' '}, {'\t', '\t'}, {'\n', '\n'}, {'\r', '\r'}, {'\f', '\f'}, {'\v', '\v'},
		}, negate: true}, nil
	}
	ch, err := p.parseEscapeChar()
	if err != nil {
		return nil, err
	}
	return &regexNode{kind: regexLiteral, value: ch}, nil
}

// parseEscapeChar returns the escaped character value.
func (p *regexParser) parseEscapeChar() (rune, error) {
	r, ok := p.peek()
	if !ok {
		return 0, fmt.Errorf("unexpected end after \\")
	}
	p.advance()
	switch r {
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case '\\':
		return '\\', nil
	case '"':
		return '"', nil
	case '/':
		return '/', nil
	case '[', ']', '(', ')', '{', '}', '.', '*', '+', '?', '|', '^', '$', '-':
		return r, nil
	case 'u':
		return p.parseUnicodeEscape()
	default:
		return r, nil
	}
}

// parseUnicodeEscape parses \uXXXX.
func (p *regexParser) parseUnicodeEscape() (rune, error) {
	if p.pos+4 > len(p.input) {
		return 0, fmt.Errorf("incomplete \\u escape")
	}
	hex := p.input[p.pos : p.pos+4]
	p.pos += 4
	n, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid \\u escape: %s", hex)
	}
	return rune(n), nil
}

// expandRegexToRule converts a parsed regex AST into a grammar Rule tree
// suitable for NFA construction.
func expandRegexToRule(node *regexNode) *Rule {
	switch node.kind {
	case regexLiteral:
		return &Rule{Kind: RuleString, Value: string(node.value)}
	case regexCharClass:
		return regexCharClassToRule(node)
	case regexDot:
		// Match any character except \n
		return regexCharClassToRule(&regexNode{
			kind:   regexCharClass,
			runes:  []runeRange{{'\n', '\n'}},
			negate: true,
		})
	case regexSeq:
		if len(node.children) == 0 {
			return Blank()
		}
		if len(node.children) == 1 {
			return expandRegexToRule(node.children[0])
		}
		children := make([]*Rule, len(node.children))
		for i, c := range node.children {
			children[i] = expandRegexToRule(c)
		}
		return Seq(children...)
	case regexAlt:
		children := make([]*Rule, len(node.children))
		for i, c := range node.children {
			children[i] = expandRegexToRule(c)
		}
		return Choice(children...)
	case regexStar:
		return Repeat(expandRegexToRule(node.children[0]))
	case regexPlus:
		return Repeat1(expandRegexToRule(node.children[0]))
	case regexQuestion:
		return Optional(expandRegexToRule(node.children[0]))
	case regexCount:
		inner := expandRegexToRule(node.children[0])
		min := node.count
		max := node.countMax
		if min <= 0 && max <= 0 {
			return Blank()
		}
		if max == -1 {
			// {n,} — n required + zero-or-more
			parts := make([]*Rule, min+1)
			for i := 0; i < min; i++ {
				parts[i] = cloneRule(inner)
			}
			parts[min] = Repeat(cloneRule(inner))
			return Seq(parts...)
		}
		// {n,m} or {n} — n required + (m-n) optional
		parts := make([]*Rule, max)
		for i := 0; i < min; i++ {
			parts[i] = cloneRule(inner)
		}
		for i := min; i < max; i++ {
			parts[i] = Optional(cloneRule(inner))
		}
		return Seq(parts...)
	default:
		return Blank()
	}
}

func regexCharClassToRule(node *regexNode) *Rule {
	// Encode as a special Pattern rule with the char class info
	r := &Rule{Kind: RulePattern}
	var buf strings.Builder
	buf.WriteByte('[')
	if node.negate {
		buf.WriteByte('^')
	}
	for _, rr := range node.runes {
		writeRuneForCharClass(&buf, rr.lo)
		if rr.hi != rr.lo {
			buf.WriteByte('-')
			writeRuneForCharClass(&buf, rr.hi)
		}
	}
	buf.WriteByte(']')
	r.Value = buf.String()
	return r
}

func writeRuneForCharClass(buf *strings.Builder, r rune) {
	switch r {
	case '\\', ']', '-', '^':
		buf.WriteByte('\\')
		buf.WriteRune(r)
	default:
		buf.WriteRune(r)
	}
}

// cloneRule creates a deep copy of a Rule.
func cloneRule(r *Rule) *Rule {
	if r == nil {
		return nil
	}
	cp := *r
	if len(r.Children) > 0 {
		cp.Children = make([]*Rule, len(r.Children))
		for i, c := range r.Children {
			cp.Children[i] = cloneRule(c)
		}
	}
	return &cp
}

// expandPatternRule parses a regex pattern string and returns a Rule tree.
func expandPatternRule(pattern string) (*Rule, error) {
	node, err := parseRegex(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex parse %q: %w", pattern, err)
	}
	return expandRegexToRule(node), nil
}
