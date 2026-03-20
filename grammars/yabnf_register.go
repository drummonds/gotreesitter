package grammars

func init() {
	Register(LangEntry{
		Name:               "yabnf",
		Extensions:         []string{".yabnf"},
		Language:           YabnfLanguage,
		HighlightQuery:     yabnfHighlightQuery,
		TokenSourceFactory: defaultTokenSourceFactory("yabnf"),
	})
}

const yabnfHighlightQuery = `
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
(directive_name) @keyword
(prec_name) @attribute
(field_name) @property
(alias_name) @property
(pattern_value) @string.regexp
(directive_string) @string
(prec_value) @number
"@field" @attribute
"@alias" @attribute
"@token" @attribute
"@immediate-token" @attribute
"@pattern" @attribute
`
