package grammars

import (
	"unicode"

	gotreesitter "github.com/drummonds/gotreesitter"
)

// External token indexes for the scss grammar.
// SCSS has 4 external tokens, but only _descendant_operator uses the scanner;
// the remaining tokens (pseudo_class colon, error_recovery, _concat) are
// handled by the DFA or always declined.
const (
	scssTokDescendantOp  = 0 // "_descendant_operator"
	scssTokColon         = 1 // ":"
	scssTokErrorRecovery = 2 // "__error_recovery"
	scssTokConcat        = 3 // "_concat"
)

// Concrete symbol IDs from the generated scss grammar ExternalSymbols.
const (
	scssSymDescendantOp  gotreesitter.Symbol = 85
	scssSymColon         gotreesitter.Symbol = 86
	scssSymErrorRecovery gotreesitter.Symbol = 87
	scssSymConcat        gotreesitter.Symbol = 88
)

// ScssExternalScanner implements gotreesitter.ExternalScanner for tree-sitter-scss.
//
// This is a Go port of the C external scanner from tree-sitter-scss. The
// scanner handles the descendant combinator operator — whitespace between
// two selectors (e.g., "div p"). It disambiguates whitespace-as-combinator
// from whitespace-before-property-value by checking what follows the space.
type ScssExternalScanner struct{}

func (ScssExternalScanner) Create() any                           { return nil }
func (ScssExternalScanner) Destroy(payload any)                   {}
func (ScssExternalScanner) Serialize(payload any, buf []byte) int { return 0 }
func (ScssExternalScanner) Deserialize(payload any, buf []byte)   {}

func (ScssExternalScanner) Scan(payload any, lexer *gotreesitter.ExternalLexer, validSymbols []bool) bool {
	ch := lexer.Lookahead()

	if isScssSpace(ch) && scssValid(validSymbols, scssTokDescendantOp) {
		return scanScssDescendantOp(lexer)
	}

	return false
}

// scanScssDescendantOp skips whitespace and checks if the next character
// looks like the start of a selector. If so, the whitespace is the CSS
// descendant combinator.
func scanScssDescendantOp(lexer *gotreesitter.ExternalLexer) bool {
	lexer.SetResultSymbol(scssSymDescendantOp)

	// Skip all whitespace.
	lexer.Advance(true)
	for isScssSpace(lexer.Lookahead()) {
		lexer.Advance(true)
	}
	lexer.MarkEnd()

	ch := lexer.Lookahead()
	// These characters indicate a selector follows.
	if ch == '#' || ch == '.' || ch == '[' || ch == '-' || ch == '&' || unicode.IsLetter(ch) || unicode.IsDigit(ch) {
		return true
	}

	// If ':' follows, disambiguate: pseudo-class (selector context) vs
	// property-value separator. Scan forward — if we hit '{' before ';' or '}',
	// it's a selector context.
	if ch == ':' {
		lexer.Advance(false)
		if isScssSpace(lexer.Lookahead()) {
			return false
		}
		for {
			ch = lexer.Lookahead()
			if ch == ';' || ch == '}' || ch == 0 {
				return false
			}
			if ch == '{' {
				return true
			}
			lexer.Advance(false)
		}
	}

	return false
}

func isScssSpace(ch rune) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f'
}

func scssValid(validSymbols []bool, idx int) bool {
	return idx >= 0 && idx < len(validSymbols) && validSymbols[idx]
}
