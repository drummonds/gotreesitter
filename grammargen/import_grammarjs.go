package grammargen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// ImportGrammarJS parses a tree-sitter grammar.js file and returns a Grammar IR.
// This uses gotreesitter's own JavaScript grammar to parse the file, demonstrating
// the full-circle capability: gotreesitter parsing its own input format.
func ImportGrammarJS(source []byte) (*Grammar, error) {
	lang := grammars.JavascriptLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse grammar.js: %w", err)
	}

	root := tree.RootNode()
	imp := &jsImporter{
		source: source,
		lang:   lang,
	}

	return imp.extract(root)
}

type jsImporter struct {
	source []byte
	lang   *gotreesitter.Language
}

// nodeText returns the source text of a node.
func (imp *jsImporter) nodeText(n *gotreesitter.Node) string {
	return string(imp.source[n.StartByte():n.EndByte()])
}

// nodeType returns the type name of a node.
func (imp *jsImporter) nodeType(n *gotreesitter.Node) string {
	return n.Type(imp.lang)
}

// extract walks the AST to find module.exports = grammar({...}) and extracts
// all grammar components.
func (imp *jsImporter) extract(root *gotreesitter.Node) (*Grammar, error) {
	grammarObj, err := imp.findGrammarCall(root)
	if err != nil {
		return nil, err
	}

	g := NewGrammar("")

	for i := 0; i < int(grammarObj.NamedChildCount()); i++ {
		child := grammarObj.NamedChild(i)
		if imp.nodeType(child) != "pair" {
			continue
		}

		key := imp.getPairKey(child)
		value := imp.getPairValue(child)

		switch key {
		case "name":
			g.Name = imp.extractStringValue(value)

		case "rules":
			if err := imp.extractRules(value, g); err != nil {
				return nil, fmt.Errorf("extract rules: %w", err)
			}

		case "extras":
			extras, err := imp.extractRuleArray(value)
			if err != nil {
				return nil, fmt.Errorf("extract extras: %w", err)
			}
			g.Extras = extras

		case "conflicts":
			conflicts, err := imp.extractConflicts(value)
			if err != nil {
				return nil, fmt.Errorf("extract conflicts: %w", err)
			}
			g.Conflicts = conflicts

		case "externals":
			externals, err := imp.extractExternals(value)
			if err != nil {
				return nil, fmt.Errorf("extract externals: %w", err)
			}
			g.Externals = externals

		case "inline":
			g.Inline = imp.extractStringArray(value)

		case "word":
			g.Word = imp.extractWordRef(value)

		case "supertypes":
			g.Supertypes = imp.extractStringArray(value)
		}
	}

	return g, nil
}

// findGrammarCall locates the grammar({...}) call expression and returns
// the object argument.
func (imp *jsImporter) findGrammarCall(root *gotreesitter.Node) (*gotreesitter.Node, error) {
	var result *gotreesitter.Node

	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if result != nil {
			return
		}

		if imp.nodeType(n) == "call_expression" {
			fn := n.ChildByFieldName("function", imp.lang)
			if fn != nil && imp.nodeText(fn) == "grammar" {
				args := n.ChildByFieldName("arguments", imp.lang)
				if args != nil && int(args.NamedChildCount()) > 0 {
					firstArg := args.NamedChild(0)
					if imp.nodeType(firstArg) == "object" {
						result = firstArg
						return
					}
				}
			}
		}

		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)

	if result == nil {
		return nil, fmt.Errorf("could not find grammar({...}) call in source")
	}
	return result, nil
}

// getPairKey extracts the key string from an object pair node.
func (imp *jsImporter) getPairKey(pair *gotreesitter.Node) string {
	key := pair.ChildByFieldName("key", imp.lang)
	if key == nil {
		return ""
	}
	text := imp.nodeText(key)
	text = strings.Trim(text, `"'`)
	return text
}

// getPairValue returns the value node of an object pair.
func (imp *jsImporter) getPairValue(pair *gotreesitter.Node) *gotreesitter.Node {
	return pair.ChildByFieldName("value", imp.lang)
}

// extractStringValue extracts a string value from a string literal node.
func (imp *jsImporter) extractStringValue(n *gotreesitter.Node) string {
	text := imp.nodeText(n)
	if len(text) >= 2 {
		if (text[0] == '"' && text[len(text)-1] == '"') ||
			(text[0] == '\'' && text[len(text)-1] == '\'') {
			return text[1 : len(text)-1]
		}
	}
	return text
}

// extractRules extracts the rules object: { rule_name: $ => rule_expr, ... }
func (imp *jsImporter) extractRules(rulesObj *gotreesitter.Node, g *Grammar) error {
	if imp.nodeType(rulesObj) != "object" {
		return fmt.Errorf("expected object for rules, got %s", imp.nodeType(rulesObj))
	}

	for i := 0; i < int(rulesObj.NamedChildCount()); i++ {
		child := rulesObj.NamedChild(i)
		if imp.nodeType(child) == "method_definition" {
			name := imp.getMethodName(child)
			body := imp.getMethodBody(child)
			if body == nil {
				continue
			}
			rule, err := imp.convertRuleExpr(body)
			if err != nil {
				return fmt.Errorf("rule %q: %w", name, err)
			}
			g.Define(name, rule)
			continue
		}
		if imp.nodeType(child) != "pair" {
			continue
		}

		name := imp.getPairKey(child)
		value := imp.getPairValue(child)

		ruleExpr := imp.extractArrowBody(value)
		if ruleExpr == nil {
			ruleExpr = value
		}

		rule, err := imp.convertRuleExpr(ruleExpr)
		if err != nil {
			return fmt.Errorf("rule %q: %w", name, err)
		}
		g.Define(name, rule)
	}

	return nil
}

// getMethodName extracts the name from a method definition.
func (imp *jsImporter) getMethodName(n *gotreesitter.Node) string {
	name := n.ChildByFieldName("name", imp.lang)
	if name == nil {
		return ""
	}
	return imp.nodeText(name)
}

// getMethodBody extracts the body expression from a method definition.
func (imp *jsImporter) getMethodBody(n *gotreesitter.Node) *gotreesitter.Node {
	body := n.ChildByFieldName("body", imp.lang)
	if body == nil {
		return nil
	}
	if imp.nodeType(body) == "statement_block" {
		for i := 0; i < int(body.NamedChildCount()); i++ {
			child := body.NamedChild(i)
			if imp.nodeType(child) == "return_statement" {
				if int(child.NamedChildCount()) > 0 {
					return child.NamedChild(0)
				}
			}
		}
	}
	return body
}

// extractArrowBody extracts the body expression from an arrow function.
func (imp *jsImporter) extractArrowBody(n *gotreesitter.Node) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	if imp.nodeType(n) == "arrow_function" {
		return n.ChildByFieldName("body", imp.lang)
	}
	return n
}

// convertRuleExpr converts a JavaScript AST expression into a Grammar Rule.
func (imp *jsImporter) convertRuleExpr(n *gotreesitter.Node) (*Rule, error) {
	if n == nil {
		return nil, fmt.Errorf("nil node")
	}

	typ := imp.nodeType(n)
	text := imp.nodeText(n)

	switch typ {
	case "call_expression":
		return imp.convertCallExpr(n)

	case "string":
		val := imp.extractStringValue(n)
		return Str(val), nil

	case "regex":
		pattern := imp.extractRegexPattern(n)
		return Pat(pattern), nil

	case "member_expression":
		return imp.convertMemberExpr(n)

	case "identifier":
		if text == "blank" {
			return Blank(), nil
		}
		return Sym(text), nil

	case "template_string":
		inner := text
		if len(inner) >= 2 {
			inner = inner[1 : len(inner)-1]
		}
		return Pat(inner), nil

	case "parenthesized_expression":
		if int(n.NamedChildCount()) > 0 {
			return imp.convertRuleExpr(n.NamedChild(0))
		}
		return nil, fmt.Errorf("empty parenthesized expression")

	default:
		return nil, fmt.Errorf("unsupported rule expression type %q: %s", typ, truncate(text, 80))
	}
}

// convertCallExpr converts a function call like seq(...), choice(...) etc.
func (imp *jsImporter) convertCallExpr(n *gotreesitter.Node) (*Rule, error) {
	fn := n.ChildByFieldName("function", imp.lang)
	args := n.ChildByFieldName("arguments", imp.lang)

	if fn == nil || args == nil {
		return nil, fmt.Errorf("malformed call expression")
	}

	fnText := imp.nodeText(fn)

	// Handle member calls like prec.left(...), token.immediate(...)
	if imp.nodeType(fn) == "member_expression" {
		obj := fn.ChildByFieldName("object", imp.lang)
		prop := fn.ChildByFieldName("property", imp.lang)
		if obj != nil && prop != nil {
			fnText = imp.nodeText(obj) + "." + imp.nodeText(prop)
		}
	}

	// Collect arguments.
	var argNodes []*gotreesitter.Node
	for i := 0; i < int(args.NamedChildCount()); i++ {
		argNodes = append(argNodes, args.NamedChild(i))
	}

	switch fnText {
	case "seq":
		children, err := imp.convertRuleArgs(argNodes)
		if err != nil {
			return nil, fmt.Errorf("seq: %w", err)
		}
		return Seq(children...), nil

	case "choice":
		children, err := imp.convertRuleArgs(argNodes)
		if err != nil {
			return nil, fmt.Errorf("choice: %w", err)
		}
		return Choice(children...), nil

	case "repeat":
		if len(argNodes) != 1 {
			return nil, fmt.Errorf("repeat expects 1 arg, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		return Repeat(child), nil

	case "repeat1":
		if len(argNodes) != 1 {
			return nil, fmt.Errorf("repeat1 expects 1 arg, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		return Repeat1(child), nil

	case "optional":
		if len(argNodes) != 1 {
			return nil, fmt.Errorf("optional expects 1 arg, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		return Optional(child), nil

	case "token":
		if len(argNodes) != 1 {
			return nil, fmt.Errorf("token expects 1 arg, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		return Token(child), nil

	case "token.immediate":
		if len(argNodes) != 1 {
			return nil, fmt.Errorf("token.immediate expects 1 arg, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		return ImmToken(child), nil

	case "field":
		if len(argNodes) != 2 {
			return nil, fmt.Errorf("field expects 2 args, got %d", len(argNodes))
		}
		name := imp.extractStringValue(argNodes[0])
		child, err := imp.convertRuleExpr(argNodes[1])
		if err != nil {
			return nil, err
		}
		return Field(name, child), nil

	case "prec":
		return imp.convertPrecCall(argNodes, func(n int, r *Rule) *Rule {
			return Prec(n, r)
		})

	case "prec.left":
		return imp.convertPrecCall(argNodes, func(n int, r *Rule) *Rule {
			return PrecLeft(n, r)
		})

	case "prec.right":
		return imp.convertPrecCall(argNodes, func(n int, r *Rule) *Rule {
			return PrecRight(n, r)
		})

	case "prec.dynamic":
		return imp.convertPrecCall(argNodes, func(n int, r *Rule) *Rule {
			return PrecDynamic(n, r)
		})

	case "alias":
		if len(argNodes) < 2 {
			return nil, fmt.Errorf("alias expects 2-3 args, got %d", len(argNodes))
		}
		child, err := imp.convertRuleExpr(argNodes[0])
		if err != nil {
			return nil, err
		}
		aliasTarget := argNodes[1]
		var aliasName string
		named := false
		if imp.nodeType(aliasTarget) == "string" {
			aliasName = imp.extractStringValue(aliasTarget)
		} else if imp.nodeType(aliasTarget) == "member_expression" {
			aliasName = imp.extractMemberProp(aliasTarget)
			named = true
		} else {
			aliasName = imp.nodeText(aliasTarget)
			named = true
		}
		return Alias(child, aliasName, named), nil

	default:
		return nil, fmt.Errorf("unsupported function call %q", fnText)
	}
}

// convertPrecCall converts prec/prec.left/prec.right/prec.dynamic calls.
func (imp *jsImporter) convertPrecCall(args []*gotreesitter.Node, make_ func(int, *Rule) *Rule) (*Rule, error) {
	switch len(args) {
	case 1:
		child, err := imp.convertRuleExpr(args[0])
		if err != nil {
			return nil, err
		}
		return make_(0, child), nil
	case 2:
		prec, err := imp.extractIntValue(args[0])
		if err != nil {
			return nil, fmt.Errorf("precedence: %w", err)
		}
		child, err := imp.convertRuleExpr(args[1])
		if err != nil {
			return nil, err
		}
		return make_(prec, child), nil
	default:
		return nil, fmt.Errorf("prec expects 1-2 args, got %d", len(args))
	}
}

// convertMemberExpr converts $.rule_name → Sym("rule_name").
func (imp *jsImporter) convertMemberExpr(n *gotreesitter.Node) (*Rule, error) {
	prop := imp.extractMemberProp(n)
	if prop == "" {
		return nil, fmt.Errorf("could not extract property from member expression: %s", imp.nodeText(n))
	}
	return Sym(prop), nil
}

// extractMemberProp extracts the property name from a member expression.
func (imp *jsImporter) extractMemberProp(n *gotreesitter.Node) string {
	prop := n.ChildByFieldName("property", imp.lang)
	if prop != nil {
		return imp.nodeText(prop)
	}
	return ""
}

// convertRuleArgs converts a slice of AST nodes to Rules.
func (imp *jsImporter) convertRuleArgs(nodes []*gotreesitter.Node) ([]*Rule, error) {
	var rules []*Rule
	for _, n := range nodes {
		r, err := imp.convertRuleExpr(n)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// extractRuleArray extracts an array of rule expressions (e.g. extras: $ => [...]).
func (imp *jsImporter) extractRuleArray(n *gotreesitter.Node) ([]*Rule, error) {
	body := imp.extractArrowBody(n)
	if body == nil {
		body = n
	}

	if imp.nodeType(body) != "array" {
		r, err := imp.convertRuleExpr(body)
		if err != nil {
			return nil, err
		}
		return []*Rule{r}, nil
	}

	var rules []*Rule
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		r, err := imp.convertRuleExpr(child)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// extractConflicts extracts conflict declarations: $ => [[$.a, $.b], ...]
func (imp *jsImporter) extractConflicts(n *gotreesitter.Node) ([][]string, error) {
	body := imp.extractArrowBody(n)
	if body == nil {
		body = n
	}

	if imp.nodeType(body) != "array" {
		return nil, nil
	}

	var conflicts [][]string
	for i := 0; i < int(body.NamedChildCount()); i++ {
		group := body.NamedChild(i)
		if imp.nodeType(group) != "array" {
			continue
		}
		var names []string
		for j := 0; j < int(group.NamedChildCount()); j++ {
			elem := group.NamedChild(j)
			if imp.nodeType(elem) == "member_expression" {
				names = append(names, imp.extractMemberProp(elem))
			}
		}
		if len(names) > 0 {
			conflicts = append(conflicts, names)
		}
	}
	return conflicts, nil
}

// extractExternals extracts external token declarations.
func (imp *jsImporter) extractExternals(n *gotreesitter.Node) ([]*Rule, error) {
	return imp.extractRuleArray(n)
}

// extractStringArray extracts an array of strings (for inline, supertypes).
func (imp *jsImporter) extractStringArray(n *gotreesitter.Node) []string {
	body := imp.extractArrowBody(n)
	if body == nil {
		body = n
	}

	if imp.nodeType(body) != "array" {
		return nil
	}

	var result []string
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		if imp.nodeType(child) == "string" {
			result = append(result, imp.extractStringValue(child))
		} else if imp.nodeType(child) == "member_expression" {
			result = append(result, imp.extractMemberProp(child))
		}
	}
	return result
}

// extractWordRef extracts the word token reference: $ => $.identifier
func (imp *jsImporter) extractWordRef(n *gotreesitter.Node) string {
	body := imp.extractArrowBody(n)
	if body == nil {
		body = n
	}

	if imp.nodeType(body) == "member_expression" {
		return imp.extractMemberProp(body)
	}
	if imp.nodeType(body) == "string" {
		return imp.extractStringValue(body)
	}
	return imp.nodeText(body)
}

// extractIntValue extracts an integer from a number or unary expression node.
func (imp *jsImporter) extractIntValue(n *gotreesitter.Node) (int, error) {
	text := imp.nodeText(n)
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("expected integer, got %q", text)
	}
	return v, nil
}

// extractRegexPattern extracts the pattern from a regex literal /pattern/flags.
func (imp *jsImporter) extractRegexPattern(n *gotreesitter.Node) string {
	text := imp.nodeText(n)
	if len(text) >= 2 && text[0] == '/' {
		end := strings.LastIndex(text, "/")
		if end > 0 {
			return text[1:end]
		}
	}
	return text
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
