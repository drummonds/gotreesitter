package grammars

func init() {
	Register(LangEntry{
		Name:               "pta",
		Extensions:         []string{".pta"},
		Language:           PtaLanguage,
		HighlightQuery:     ptaHighlightQuery,
		TokenSourceFactory: defaultTokenSourceFactory("pta"),
	})
}

const ptaHighlightQuery = `
(date) @constant
(flag) @keyword
(payee) @string
(account) @type
(arrow) @operator
(linked_prefix) @operator
(word) @comment
(amount) @number
(commodity) @constant
(comment) @comment
`
