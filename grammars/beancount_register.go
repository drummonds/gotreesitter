package grammars

func init() {
	Register(LangEntry{
		Name:               "beancount",
		Extensions:         []string{".beancount"},
		Language:           BeancountLanguage,
		HighlightQuery:     beancountHighlightQuery,
		TokenSourceFactory: defaultTokenSourceFactory("beancount"),
	})
}

const beancountHighlightQuery = `
; Keywords — entry type keywords
[
  "open"
  "close"
  "balance"
  "pad"
  "event"
  "query"
  "note"
  "document"
  "custom"
  "commodity"
  "price"
  "txn"
  "pushtag"
  "poptag"
  "pushmeta"
  "popmeta"
  "option"
  "include"
  "plugin"
] @keyword

; Strings
(string) @string

; Numbers
(number) @number

; Dates
(date) @constant

; Accounts
(account) @type

; Currencies
(currency) @constant

; Tags and links
(tag) @tag
(link) @attribute

; Comments
(comment) @comment

; Flags
(flag) @keyword

; Booleans
(bool) @constant.builtin

; Operators
[
  (plus)
  (minus)
  (asterisk)
  (slash)
  (at)
  (atat)
] @operator
`
