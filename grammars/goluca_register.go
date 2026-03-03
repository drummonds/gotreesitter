package grammars

func init() {
	Register(LangEntry{
		Name:               "goluca",
		Extensions:         []string{".goluca"},
		Language:           GolucaLanguage,
		HighlightQuery:     golucaHighlightQuery,
		TokenSourceFactory: defaultTokenSourceFactory("goluca"),
	})
}

const golucaHighlightQuery = `
(date) @constant
(flag) @keyword
(payee) @string
(account) @type
(arrow) @operator
(linked_prefix) @operator
(description) @string
(amount) @number
(commodity) @constant
(comment) @comment
`
