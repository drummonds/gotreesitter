package grammars

import gotreesitter "codeberg.org/hum3/gotreesitter"

// External token indexes for the D grammar.
// The D external scanner handles nestable /+ +/ block comments
// and q"..." delimited strings. Other external tokens (directive,
// int_literal, etc.) are not handled here.
const (
	dTokDirective     = 0
	dTokIntLiteral    = 1
	dTokFloatLiteral  = 2
	dTokString        = 3
	dTokNotIn         = 4
	dTokNotIs         = 5
	dTokAfterEof      = 6
	dTokErrorSentinel = 7
)

const (
	dSymDirective gotreesitter.Symbol = 221
	dSymString    gotreesitter.Symbol = 224
)

// DExternalScanner handles nestable /+ +/ block comments and q"..." delimited strings for D.
type DExternalScanner struct{}

func (DExternalScanner) Create() any                           { return nil }
func (DExternalScanner) Destroy(payload any)                   {}
func (DExternalScanner) Serialize(payload any, buf []byte) int { return 0 }
func (DExternalScanner) Deserialize(payload any, buf []byte)   {}

func (DExternalScanner) Scan(payload any, lexer *gotreesitter.ExternalLexer, validSymbols []bool) bool {
	// Nesting block comment: /+ +/
	if lexer.Lookahead() == '/' && dValid(validSymbols, dTokDirective) {
		lexer.Advance(false)
		if lexer.Lookahead() != '+' {
			return false
		}
		lexer.Advance(false)

		depth := 1
		var last rune
		for depth > 0 {
			last = lexer.Lookahead()
			lexer.Advance(false)
			if last == '/' && lexer.Lookahead() == '+' {
				depth++
				last = 0
				lexer.Advance(false)
			} else if last == '+' && lexer.Lookahead() == '/' {
				depth--
				last = 0
				lexer.Advance(false)
			} else if lexer.Lookahead() == 0 {
				return false
			}
		}
		lexer.SetResultSymbol(dSymDirective)
		return true
	}

	// Delimited string: q"..."
	if lexer.Lookahead() == 'q' && dValid(validSymbols, dTokString) {
		lexer.Advance(false)
		if lexer.Lookahead() != '"' {
			return false
		}
		lexer.Advance(false)
		lexer.SetResultSymbol(dSymString)

		opener := lexer.Lookahead()
		var closer rune
		switch opener {
		case '(':
			closer = ')'
		case '[':
			closer = ']'
		case '{':
			closer = '}'
		case '<':
			closer = '>'
		default:
			// Identifier-delimited string: q"IDENT\n...IDENT"
			var delim []rune
			delim = append(delim, '\n')
			for lexer.Lookahead() != '\n' {
				ch := lexer.Lookahead()
				if !dIsIdentChar(ch) {
					return false
				}
				delim = append(delim, ch)
				lexer.Advance(false)
			}
			delim = append(delim, '"')

			delimPos := 0
			for {
				if lexer.Lookahead() == 0 {
					return false
				}
				if delimPos == len(delim) {
					return true
				}
				if lexer.Lookahead() == delim[delimPos] {
					delimPos++
				} else if lexer.Lookahead() == delim[0] {
					delimPos = 1
				} else {
					delimPos = 0
				}
				lexer.Advance(false)
			}
		}

		// Punctuation-delimited string
		depth := 1
		for depth > 0 {
			lexer.Advance(false)
			if lexer.Lookahead() == opener {
				depth++
			} else if lexer.Lookahead() == closer {
				depth--
			} else if lexer.Lookahead() == 0 {
				return false
			}
		}
		lexer.Advance(false) // consume last closer
		if lexer.Lookahead() != '"' {
			return false
		}
		lexer.Advance(false) // consume "
		return true
	}

	return false
}

func dIsIdentChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

func dValid(vs []bool, i int) bool { return i < len(vs) && vs[i] }
