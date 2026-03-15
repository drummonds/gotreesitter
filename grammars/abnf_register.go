package grammars

func init() {
	Register(LangEntry{
		Name:               "abnf",
		Extensions:         []string{".abnf"},
		Language:           AbnfLanguage,
		HighlightQuery:     abnfHighlightQuery,
		TokenSourceFactory: defaultTokenSourceFactory("abnf"),
	})
}

const abnfHighlightQuery = `
(rulename) @variable
(core_rulename) @type.builtin
(defined_as) @operator
(char_val) @string
(num_val (bin_val) @number)
(num_val (dec_val) @number)
(num_val (hex_val) @number)
(prose_val) @string.special
(repeat) @number
(comment) @comment
`
