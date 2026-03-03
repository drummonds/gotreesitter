package grammars

import (
	"unicode"

	gotreesitter "github.com/drummonds/gotreesitter"
)

// External token indexes for the COBOL grammar.
const (
	cobolTokWhiteSpaces      = 0
	cobolTokLinePrefixComment = 1
	cobolTokLineSuffixComment = 2
	cobolTokLineComment       = 3
	cobolTokCommentEntry      = 4
	cobolTokMultilineString   = 5
)

const (
	cobolSymWhiteSpaces      gotreesitter.Symbol = 579
	cobolSymLinePrefixComment gotreesitter.Symbol = 580
	cobolSymLineSuffixComment gotreesitter.Symbol = 581
	cobolSymLineComment       gotreesitter.Symbol = 582
	cobolSymCommentEntry      gotreesitter.Symbol = 583
	cobolSymMultilineString   gotreesitter.Symbol = 584
)

// COBOL comment entry keywords (case-insensitive prefixes).
var cobolCommentEntryKeywords = []string{
	"author",
	"installlation",
	"date-written",
	"date-compiled",
	"security",
	"identification division",
	"environment division",
	"data division",
	"procedure division",
}

// CobolExternalScanner handles COBOL's column-based formatting.
type CobolExternalScanner struct{}

func (CobolExternalScanner) Create() any                           { return nil }
func (CobolExternalScanner) Destroy(payload any)                   {}
func (CobolExternalScanner) Serialize(payload any, buf []byte) int { return 0 }
func (CobolExternalScanner) Deserialize(payload any, buf []byte)   {}

func (CobolExternalScanner) Scan(payload any, lexer *gotreesitter.ExternalLexer, validSymbols []bool) bool {
	if lexer.Lookahead() == 0 {
		return false
	}

	// WHITE_SPACES: consume whitespace (including ; and ,)
	if cobolValid(validSymbols, cobolTokWhiteSpaces) {
		if cobolIsWhiteSpace(lexer.Lookahead()) {
			for cobolIsWhiteSpace(lexer.Lookahead()) {
				lexer.Advance(true)
			}
			lexer.SetResultSymbol(cobolSymWhiteSpaces)
			lexer.MarkEnd()
			return true
		}
	}

	// LINE_PREFIX_COMMENT: columns 1-6 (0-5)
	if cobolValid(validSymbols, cobolTokLinePrefixComment) && lexer.GetColumn() <= 5 {
		for lexer.GetColumn() <= 5 {
			lexer.Advance(true)
		}
		lexer.SetResultSymbol(cobolSymLinePrefixComment)
		lexer.MarkEnd()
		return true
	}

	// LINE_COMMENT: column 7 (index 6) with * or /
	if cobolValid(validSymbols, cobolTokLineComment) {
		if lexer.GetColumn() == 6 {
			if lexer.Lookahead() == '*' || lexer.Lookahead() == '/' {
				for lexer.Lookahead() != '\n' && lexer.Lookahead() != 0 {
					lexer.Advance(true)
				}
				lexer.SetResultSymbol(cobolSymLineComment)
				lexer.MarkEnd()
				return true
			}
			lexer.Advance(true)
			lexer.MarkEnd()
			return false
		}
	}

	// LINE_SUFFIX_COMMENT: column 73+ (index 72+)
	if cobolValid(validSymbols, cobolTokLineSuffixComment) {
		if lexer.GetColumn() >= 72 {
			for lexer.Lookahead() != '\n' && lexer.Lookahead() != 0 {
				lexer.Advance(true)
			}
			lexer.SetResultSymbol(cobolSymLineSuffixComment)
			lexer.MarkEnd()
			return true
		}
	}

	// COMMENT_ENTRY: content that doesn't start with a known keyword
	if cobolValid(validSymbols, cobolTokCommentEntry) {
		if !cobolStartsWithKeyword(lexer) {
			lexer.MarkEnd()
			lexer.SetResultSymbol(cobolSymCommentEntry)
			return true
		}
		return false
	}

	// MULTILINE_STRING: "..."  with continuation lines
	if cobolValid(validSymbols, cobolTokMultilineString) {
		for {
			if lexer.Lookahead() != '"' {
				return false
			}
			lexer.Advance(false)
			for lexer.Lookahead() != '"' && lexer.Lookahead() != 0 && lexer.GetColumn() < 72 {
				lexer.Advance(false)
			}
			if lexer.Lookahead() == '"' {
				lexer.SetResultSymbol(cobolSymMultilineString)
				lexer.Advance(false)
				lexer.MarkEnd()
				return true
			}
			// Skip to end of line
			for lexer.Lookahead() != 0 && lexer.Lookahead() != '\n' {
				lexer.Advance(true)
			}
			if lexer.Lookahead() == 0 {
				return false
			}
			lexer.Advance(true) // skip \n
			// Skip columns 1-6
			for i := 0; i <= 5; i++ {
				if lexer.Lookahead() == 0 || lexer.Lookahead() == '\n' {
					return false
				}
				lexer.Advance(true)
			}
			// Column 7 must be '-' for continuation
			if lexer.Lookahead() != '-' {
				return false
			}
			lexer.Advance(true)
			// Skip spaces to the continuation quote
			for lexer.Lookahead() == ' ' && lexer.GetColumn() < 72 {
				lexer.Advance(true)
			}
		}
	}

	return false
}

func cobolIsWhiteSpace(c rune) bool {
	return unicode.IsSpace(c) || c == ';' || c == ','
}

// cobolStartsWithKeyword checks if the current line starts with any comment entry keyword.
func cobolStartsWithKeyword(lexer *gotreesitter.ExternalLexer) bool {
	// Skip leading whitespace
	for lexer.Lookahead() == ' ' || lexer.Lookahead() == '\t' {
		lexer.Advance(true)
	}

	// Try to match each keyword
	type tracker struct {
		keyword string
		pos     int
		active  bool
	}
	trackers := make([]tracker, len(cobolCommentEntryKeywords))
	for i, kw := range cobolCommentEntryKeywords {
		trackers[i] = tracker{keyword: kw, pos: 0, active: true}
	}

	for {
		if lexer.GetColumn() > 71 || lexer.Lookahead() == '\n' || lexer.Lookahead() == 0 {
			return false
		}

		// Check if all matching has failed
		anyActive := false
		for i := range trackers {
			if trackers[i].active {
				anyActive = true
			}
		}
		if !anyActive {
			// Skip rest of line
			for lexer.GetColumn() < 71 && lexer.Lookahead() != '\n' && lexer.Lookahead() != 0 {
				lexer.Advance(true)
			}
			return false
		}

		ch := lexer.Lookahead()

		// Check if any keyword completed
		for i := range trackers {
			if trackers[i].active && trackers[i].pos >= len(trackers[i].keyword) {
				return true
			}
		}

		// Advance matching
		for i := range trackers {
			if trackers[i].active {
				k := rune(trackers[i].keyword[trackers[i].pos])
				trackers[i].active = cobolCIMatch(ch, k)
				trackers[i].pos++
			}
		}

		lexer.Advance(true)
	}
}

func cobolCIMatch(a, b rune) bool {
	return unicode.ToUpper(a) == unicode.ToUpper(b)
}

func cobolValid(vs []bool, i int) bool { return i < len(vs) && vs[i] }
