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
(time) @constant
(timezone) @constant
(tz_offset) @constant
(knowledge_datetime "%" @operator)
(period_anchor) @operator

(flag) @keyword

(payee) @string

(account) @type

(arrow) @operator

(linked_prefix) @operator

(description) @string

(amount) @number

(commodity) @constant

(comment) @comment

(metadata_key) @property
(metadata_value) @string

(param_name) @property
(param_value) @string

(option_key) @property
(option_value) @string

(alias_name) @variable

(customer_name) @string

"commodity" @keyword
"open" @keyword
"option" @keyword
"alias" @keyword
"customer" @keyword
"data" @keyword
"account" @keyword
"max-aggregate-balance" @keyword
`
