package grammargen

// Behavioral parity tests for grammargen.
//
// These tests verify that grammars produced by grammargen behave identically
// to the existing ts2go-extracted blobs. "Behavioral parity" means:
//   - Same S-expression (node types, structure) for identical inputs
//   - Same byte ranges for each node
//   - Same field names
//   - No ERROR nodes where the reference parser has none
//
// The tests do NOT require bit-identical table layouts. The generator may
// produce different state counts, symbol ordering, or table encoding — as
// long as the parse trees are structurally equivalent.
//
// Three tiers:
//   Tier 1 (merge-blocking): JSON — we have a Go DSL grammar and can compare
//          against the existing json.bin blob.
//   Tier 2 (regression-tracked): Future grammars where known divergences are
//          tracked and can only shrink.
//   Tier 3 (diagnostic): Informational comparison for grammars imported from
//          grammar.js files.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// ── Node-by-node tree comparison infrastructure ─────────────────────────────

// parityDivergence describes a single structural difference between two trees.
type parityDivergence struct {
	Path     string // breadcrumb path, e.g. "root/object[0]/pair[1]"
	Category string // "type", "range", "childCount", "field", "error", "named", "missing"
	GenValue string // value from generated grammar
	RefValue string // value from reference grammar
}

func (d parityDivergence) String() string {
	return fmt.Sprintf("%s: %s (gen=%s, ref=%s)", d.Path, d.Category, d.GenValue, d.RefValue)
}

// compareTreesDeep does a recursive node-by-node comparison of two parse trees.
// It returns all divergences found, up to maxDivergences.
func compareTreesDeep(
	genNode *gotreesitter.Node, genLang *gotreesitter.Language,
	refNode *gotreesitter.Node, refLang *gotreesitter.Language,
	path string, maxDivergences int,
) []parityDivergence {
	var divs []parityDivergence
	compareTreesDeepRec(genNode, genLang, refNode, refLang, path, maxDivergences, &divs)
	return divs
}

func compareTreesDeepRec(
	genNode *gotreesitter.Node, genLang *gotreesitter.Language,
	refNode *gotreesitter.Node, refLang *gotreesitter.Language,
	path string, maxDivergences int,
	divs *[]parityDivergence,
) {
	if len(*divs) >= maxDivergences {
		return
	}

	if genNode == nil && refNode == nil {
		return
	}
	if genNode == nil {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "missing",
			GenValue: "<nil>", RefValue: refNode.Type(refLang),
		})
		return
	}
	if refNode == nil {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "missing",
			GenValue: genNode.Type(genLang), RefValue: "<nil>",
		})
		return
	}

	genType := genNode.Type(genLang)
	refType := refNode.Type(refLang)

	// Check node type.
	if genType != refType {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "type",
			GenValue: genType, RefValue: refType,
		})
		return // shape mismatch — don't recurse
	}

	// Check byte ranges.
	if genNode.StartByte() != refNode.StartByte() || genNode.EndByte() != refNode.EndByte() {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "range",
			GenValue: fmt.Sprintf("[%d:%d]", genNode.StartByte(), genNode.EndByte()),
			RefValue: fmt.Sprintf("[%d:%d]", refNode.StartByte(), refNode.EndByte()),
		})
	}

	// Check named status.
	if genNode.IsNamed() != refNode.IsNamed() {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "named",
			GenValue: fmt.Sprintf("%v", genNode.IsNamed()),
			RefValue: fmt.Sprintf("%v", refNode.IsNamed()),
		})
	}

	// Check error status.
	if genNode.IsError() != refNode.IsError() {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "error",
			GenValue: fmt.Sprintf("%v", genNode.IsError()),
			RefValue: fmt.Sprintf("%v", refNode.IsError()),
		})
	}

	// Check child count.
	genCC := genNode.ChildCount()
	refCC := refNode.ChildCount()
	if genCC != refCC {
		*divs = append(*divs, parityDivergence{
			Path: path, Category: "childCount",
			GenValue: fmt.Sprintf("%d", genCC),
			RefValue: fmt.Sprintf("%d", refCC),
		})
		return // shape mismatch — don't recurse
	}

	// Recurse into children.
	for i := 0; i < genCC; i++ {
		childPath := fmt.Sprintf("%s[%d]", path, i)
		genChild := genNode.Child(i)
		refChild := refNode.Child(i)
		if genChild != nil {
			childType := genChild.Type(genLang)
			if genChild.IsNamed() {
				childPath = fmt.Sprintf("%s/%s", path, childType)
				// Disambiguate siblings with same type.
				sameTypeBefore := 0
				for j := 0; j < i; j++ {
					sib := genNode.Child(j)
					if sib != nil && sib.Type(genLang) == childType && sib.IsNamed() {
						sameTypeBefore++
					}
				}
				if sameTypeBefore > 0 {
					childPath = fmt.Sprintf("%s/%s[%d]", path, childType, sameTypeBefore)
				}
			}
		}
		compareTreesDeepRec(genChild, genLang, refChild, refLang, childPath, maxDivergences, divs)
	}
}

// compareSExpr is a simpler comparison that just checks S-expressions match.
func compareSExpr(
	genNode *gotreesitter.Node, genLang *gotreesitter.Language,
	refNode *gotreesitter.Node, refLang *gotreesitter.Language,
) (genSexp, refSexp string, match bool) {
	genSexp = genNode.SExpr(genLang)
	refSexp = refNode.SExpr(refLang)
	return genSexp, refSexp, genSexp == refSexp
}

// ── JSON Parity Gate (Tier 1: merge-blocking) ──────────────────────────────

// jsonParityInputs is a comprehensive set of JSON inputs exercising all
// JSON grammar features. The test verifies that grammargen's JSONGrammar()
// produces identical parse trees to the existing json.bin blob for each input.
var jsonParityInputs = []struct {
	name  string
	input string
}{
	// Primitives.
	{"null", `null`},
	{"true", `true`},
	{"false", `false`},
	{"zero", `0`},
	{"integer", `42`},
	{"negative", `-1`},
	{"float", `3.14`},
	{"negative float", `-0.5`},
	{"exponent", `1e10`},
	{"neg exponent", `2.5e-3`},
	{"pos exponent", `1E+2`},
	{"empty string", `""`},
	{"simple string", `"hello"`},
	{"string with spaces", `"hello world"`},

	// Objects.
	{"empty object", `{}`},
	{"single pair", `{"key": "value"}`},
	{"multi pair", `{"a": 1, "b": 2, "c": 3}`},
	{"nested object", `{"outer": {"inner": 1}}`},
	{"number key", `{"key": 42}`},
	{"bool values", `{"t": true, "f": false}`},
	{"null value", `{"n": null}`},

	// Arrays.
	{"empty array", `[]`},
	{"single element", `[1]`},
	{"multi element", `[1, 2, 3]`},
	{"mixed array", `[1, "two", true, null]`},
	{"nested array", `[[1, 2], [3, 4]]`},
	{"array of objects", `[{"a": 1}, {"b": 2}]`},

	// Complex nesting.
	{"object with array", `{"key": [1, true, null]}`},
	{"array with object", `[{"name": "test", "count": 42, "active": true}]`},
	{"deep nesting", `{"a": {"b": {"c": [1, [2, [3]]]}}}`},

	// Smoke sample (same as grammars package).
	{"smoke sample", `{"a": 1}`},
}

func TestParityJSONStructure(t *testing.T) {
	// Load the reference JSON grammar (ts2go-extracted).
	refLang := grammars.JsonLanguage()
	if refLang == nil {
		t.Fatal("reference JSON language not available")
	}

	// Generate our JSON grammar.
	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	for _, tc := range jsonParityInputs {
		t.Run(tc.name, func(t *testing.T) {
			// Skip known divergences tracked in the regression gate.
			if allowed, ok := knownJSONDivergences[tc.name]; ok && allowed > 0 {
				t.Skipf("known divergence (%d allowed) — tracked in TestParityJSONRegressionGate", allowed)
			}

			src := []byte(tc.input)

			// Parse with reference.
			refParser := gotreesitter.NewParser(refLang)
			refTree, err := refParser.Parse(src)
			if err != nil {
				t.Fatalf("reference parse failed: %v", err)
			}
			refRoot := refTree.RootNode()

			// Parse with generated.
			genParser := gotreesitter.NewParser(genLang)
			genTree, err := genParser.Parse(src)
			if err != nil {
				t.Fatalf("generated parse failed: %v", err)
			}
			genRoot := genTree.RootNode()

			// Compare S-expressions first (fast check).
			genSexp, refSexp, match := compareSExpr(genRoot, genLang, refRoot, refLang)
			if !match {
				t.Errorf("S-expression mismatch:\n  gen: %s\n  ref: %s", genSexp, refSexp)
			}

			// Deep node comparison (byte ranges, child counts, etc.).
			divs := compareTreesDeep(genRoot, genLang, refRoot, refLang, "root", 20)
			for _, d := range divs {
				t.Errorf("divergence: %s", d)
			}
		})
	}
}

func TestParityJSONNoErrors(t *testing.T) {
	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	for _, tc := range jsonParityInputs {
		t.Run(tc.name, func(t *testing.T) {
			parser := gotreesitter.NewParser(genLang)
			tree, err := parser.Parse([]byte(tc.input))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			sexp := tree.RootNode().SExpr(genLang)
			if strings.Contains(sexp, "ERROR") {
				t.Errorf("generated parser produced ERROR: %s", sexp)
			}
			if strings.Contains(sexp, "MISSING") {
				t.Errorf("generated parser produced MISSING: %s", sexp)
			}
		})
	}
}

func TestParityJSONFields(t *testing.T) {
	// Verify that field names (key, value) work identically.
	refLang := grammars.JsonLanguage()
	if refLang == nil {
		t.Fatal("reference JSON language not available")
	}

	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	inputs := []string{
		`{"key": "value"}`,
		`{"a": 1, "b": [2, 3]}`,
		`{"outer": {"inner": true}}`,
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			src := []byte(input)

			// Parse with both.
			refTree, _ := gotreesitter.NewParser(refLang).Parse(src)
			genTree, _ := gotreesitter.NewParser(genLang).Parse(src)

			// Walk both trees and collect field annotations.
			refFields := collectFields(refTree.RootNode(), refLang, "root")
			genFields := collectFields(genTree.RootNode(), genLang, "root")

			// Compare field sets.
			for path, refField := range refFields {
				genField, ok := genFields[path]
				if !ok {
					t.Errorf("ref has field at %s (%s) but gen does not", path, refField)
					continue
				}
				if genField != refField {
					t.Errorf("field mismatch at %s: gen=%s ref=%s", path, genField, refField)
				}
			}
			for path, genField := range genFields {
				if _, ok := refFields[path]; !ok {
					t.Errorf("gen has extra field at %s (%s)", path, genField)
				}
			}
		})
	}
}

// collectFields walks a tree and returns a map of path→fieldName for all
// nodes that have a field annotation.
func collectFields(node *gotreesitter.Node, lang *gotreesitter.Language, path string) map[string]string {
	fields := make(map[string]string)
	collectFieldsRec(node, lang, path, fields)
	return fields
}

func collectFieldsRec(node *gotreesitter.Node, lang *gotreesitter.Language, path string, out map[string]string) {
	if node == nil {
		return
	}
	for i := 0; i < node.ChildCount(); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		childType := child.Type(lang)
		childPath := fmt.Sprintf("%s/%s", path, childType)

		// Check if this child has a field name.
		fieldName := node.FieldNameForChild(i, lang)
		if fieldName != "" {
			out[childPath] = fieldName
		}

		collectFieldsRec(child, lang, childPath, out)
	}
}

// ── Parity Snapshot Tests ───────────────────────────────────────────────────

// paritySnapshot captures the expected S-expression for a grammargen-produced
// grammar on a given input. These golden snapshots lock in correct behavior
// and detect regressions.
var paritySnapshots = map[string]struct {
	grammarFn func() *Grammar
	input     string
	golden    string // expected S-expression
}{
	"json/smoke": {
		grammarFn: JSONGrammar,
		input:     `{"a": 1}`,
		golden:    "(document (object (pair (string (string_content)) (number))))",
	},
	"json/nested": {
		grammarFn: JSONGrammar,
		input:     `{"key": [1, true, null]}`,
		golden:    "(document (object (pair (string (string_content)) (array (number) (true) (null)))))",
	},
	"calc/precedence": {
		grammarFn: CalcGrammar,
		input:     `1 + 2 * 3`,
		// 1 + (2 * 3) — multiply binds tighter
		golden: "(program (expression (expression (number)) (expression (expression (number)) (expression (number)))))",
	},
	"calc/left_assoc": {
		grammarFn: CalcGrammar,
		input:     `1 + 2 + 3`,
		// (1 + 2) + 3 — left-associative
		golden: "(program (expression (expression (expression (number)) (expression (number))) (expression (number))))",
	},
}

func TestParitySnapshots(t *testing.T) {
	for name, snap := range paritySnapshots {
		t.Run(name, func(t *testing.T) {
			lang, err := GenerateLanguage(snap.grammarFn())
			if err != nil {
				t.Fatalf("GenerateLanguage failed: %v", err)
			}

			parser := gotreesitter.NewParser(lang)
			tree, err := parser.Parse([]byte(snap.input))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}

			got := tree.RootNode().SExpr(lang)
			if got != snap.golden {
				t.Errorf("S-expression mismatch:\n  got:  %s\n  want: %s", got, snap.golden)
			}
		})
	}
}

// ── Parity: All Built-in Grammars Parse Without Errors ──────────────────────

// builtinParityGrammars maps grammar names to their constructor and a set of
// inputs that must parse without ERROR nodes. This is a merge-blocking gate.
var builtinParityGrammars = []struct {
	name      string
	grammarFn func() *Grammar
	inputs    []string
}{
	{
		name:      "json",
		grammarFn: JSONGrammar,
		inputs: []string{
			`null`, `true`, `false`, `42`, `-3.14`, `"hello"`,
			`{}`, `[]`, `{"key": "value"}`, `[1, 2, 3]`,
			`{"a": [1, true, null]}`,
			`{"name": "test", "count": 42, "active": true}`,
			`[{"a": 1}, {"b": 2}]`,
			`{"deep": {"nested": {"value": [1, [2, [3]]]}}}`,
		},
	},
	{
		name:      "calc",
		grammarFn: CalcGrammar,
		inputs: []string{
			`42`, `1 + 2`, `3 * 4`, `1 + 2 * 3`,
			`(1 + 2) * 3`, `-5`, `1 + 2 + 3`,
		},
	},
	{
		name:      "glr",
		grammarFn: GLRGrammar,
		inputs: []string{
			`a ;`, `a * b ;`, `int * x ;`,
		},
	},
	{
		name:      "keyword",
		grammarFn: KeywordGrammar,
		inputs: []string{
			`var x = 1;`, `return 42;`, `foo;`, `x + 1;`,
		},
	},
	{
		name:      "alias",
		grammarFn: AliasSuperGrammar,
		inputs: []string{
			`x = 42;`, `1 + 2;`, `x = 1 + 2;`,
		},
	},
}

func TestParityBuiltinNoErrors(t *testing.T) {
	for _, bg := range builtinParityGrammars {
		t.Run(bg.name, func(t *testing.T) {
			lang, err := GenerateLanguage(bg.grammarFn())
			if err != nil {
				t.Fatalf("GenerateLanguage failed: %v", err)
			}

			for _, input := range bg.inputs {
				t.Run(input, func(t *testing.T) {
					parser := gotreesitter.NewParser(lang)
					tree, err := parser.Parse([]byte(input))
					if err != nil {
						t.Fatalf("parse failed: %v", err)
					}
					sexp := tree.RootNode().SExpr(lang)
					if strings.Contains(sexp, "ERROR") {
						t.Errorf("ERROR in tree: %s", sexp)
					}
				})
			}
		})
	}
}

// ── Parity: Generation Stability ────────────────────────────────────────────

// TestParityGenerationDeterministic verifies that generating the same grammar
// twice produces behaviorally identical results. The blob bytes may differ due
// to map iteration order in Go, but the parse trees must be identical.
func TestParityGenerationDeterministic(t *testing.T) {
	type testGrammar struct {
		name   string
		fn     func() *Grammar
		inputs []string
	}
	gs := []testGrammar{
		{"json", JSONGrammar, []string{`null`, `{"a": 1}`, `[1, "x", true]`}},
		{"calc", CalcGrammar, []string{`1 + 2 * 3`, `(1 + 2) + 3`}},
		{"glr", GLRGrammar, []string{`a * b ;`, `int * x ;`}},
		{"keyword", KeywordGrammar, []string{`var x = 1;`, `return 42;`}},
		{"alias", AliasSuperGrammar, []string{`x = 42;`, `1 + 2;`}},
	}

	for _, g := range gs {
		t.Run(g.name, func(t *testing.T) {
			lang1, err := GenerateLanguage(g.fn())
			if err != nil {
				t.Fatalf("first generate failed: %v", err)
			}
			lang2, err := GenerateLanguage(g.fn())
			if err != nil {
				t.Fatalf("second generate failed: %v", err)
			}

			// Structural properties must match.
			if lang1.SymbolCount != lang2.SymbolCount {
				t.Errorf("SymbolCount: %d vs %d", lang1.SymbolCount, lang2.SymbolCount)
			}
			if lang1.TokenCount != lang2.TokenCount {
				t.Errorf("TokenCount: %d vs %d", lang1.TokenCount, lang2.TokenCount)
			}
			if lang1.StateCount != lang2.StateCount {
				t.Errorf("StateCount: %d vs %d", lang1.StateCount, lang2.StateCount)
			}

			// Parse trees must be identical.
			for _, input := range g.inputs {
				t.Run(input, func(t *testing.T) {
					src := []byte(input)
					tree1, _ := gotreesitter.NewParser(lang1).Parse(src)
					tree2, _ := gotreesitter.NewParser(lang2).Parse(src)

					sexp1 := tree1.RootNode().SExpr(lang1)
					sexp2 := tree2.RootNode().SExpr(lang2)
					if sexp1 != sexp2 {
						t.Errorf("S-expression mismatch:\n  gen1: %s\n  gen2: %s", sexp1, sexp2)
					}

					// Deep comparison for byte ranges etc.
					divs := compareTreesDeep(
						tree1.RootNode(), lang1,
						tree2.RootNode(), lang2,
						"root", 10,
					)
					for _, d := range divs {
						t.Errorf("divergence: %s", d)
					}
				})
			}
		})
	}
}

// ── Parity: Cross-Reference with Existing Blobs ─────────────────────────────

// knownJSONDivergences tracks the number of known structural divergences per
// test input when comparing grammargen's JSON against the existing blob.
// This map can only shrink — increasing a count or adding new entries is a
// regression and will fail the test.
var knownJSONDivergences = map[string]int{
	// grammargen correctly tokenizes 1E+2 as a single number (per JSON spec:
	// exponent = [eE][+-]?[0-9]+). The reference ts2go-extracted DFA splits
	// it into two tokens. grammargen is more correct here.
	"pos exponent": 1,
}

func TestParityJSONRegressionGate(t *testing.T) {
	refLang := grammars.JsonLanguage()
	if refLang == nil {
		t.Fatal("reference JSON language not available")
	}

	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	for _, tc := range jsonParityInputs {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.input)

			refTree, _ := gotreesitter.NewParser(refLang).Parse(src)
			genTree, _ := gotreesitter.NewParser(genLang).Parse(src)

			divs := compareTreesDeep(genTree.RootNode(), genLang, refTree.RootNode(), refLang, "root", 50)

			allowed := knownJSONDivergences[tc.name]
			if len(divs) > allowed {
				t.Errorf("REGRESSION: %d divergences (allowed %d):", len(divs), allowed)
				for _, d := range divs {
					t.Errorf("  %s", d)
				}
			} else if len(divs) < allowed {
				t.Logf("IMPROVEMENT: only %d divergences (was %d) — update knownJSONDivergences", len(divs), allowed)
			}
		})
	}
}

// ── Parity: Grammar Properties Gate ─────────────────────────────────────────

func TestParityJSONProperties(t *testing.T) {
	refLang := grammars.JsonLanguage()
	if refLang == nil {
		t.Fatal("reference JSON language not available")
	}

	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	// Symbol names should overlap substantially. grammargen may have different
	// hidden rule naming, but all visible/named symbols must match.
	refVisibleSyms := make(map[string]bool)
	for i, name := range refLang.SymbolNames {
		if i < len(refLang.SymbolMetadata) && refLang.SymbolMetadata[i].Visible {
			refVisibleSyms[name] = true
		}
	}

	genVisibleSyms := make(map[string]bool)
	for i, name := range genLang.SymbolNames {
		if i < len(genLang.SymbolMetadata) && genLang.SymbolMetadata[i].Visible {
			genVisibleSyms[name] = true
		}
	}

	// Symbols that the reference has but grammargen intentionally omits.
	// tree-sitter adds a "comment" symbol to all grammars by default;
	// grammargen only includes it if the grammar declares a comment rule.
	refOnlyExpected := map[string]bool{
		"comment": true,
	}

	// Every visible symbol in the reference should exist in generated
	// (modulo intentional omissions).
	for name := range refVisibleSyms {
		if refOnlyExpected[name] {
			continue
		}
		if !genVisibleSyms[name] {
			t.Errorf("reference visible symbol %q missing from generated", name)
		}
	}

	// Field names must match.
	refFieldSet := make(map[string]bool)
	for _, f := range refLang.FieldNames {
		if f != "" {
			refFieldSet[f] = true
		}
	}
	genFieldSet := make(map[string]bool)
	for _, f := range genLang.FieldNames {
		if f != "" {
			genFieldSet[f] = true
		}
	}
	for f := range refFieldSet {
		if !genFieldSet[f] {
			t.Errorf("reference field %q missing from generated", f)
		}
	}

	t.Logf("ref: %d symbols, %d tokens, %d states, %d fields",
		refLang.SymbolCount, refLang.TokenCount, refLang.StateCount, refLang.FieldCount)
	t.Logf("gen: %d symbols, %d tokens, %d states, %d fields",
		genLang.SymbolCount, genLang.TokenCount, genLang.StateCount, genLang.FieldCount)
}

// ── Parity: Correctness Golden (matches grammars/correctness_test.go) ───────

func TestParityJSONCorrectnessGolden(t *testing.T) {
	// The grammars package has a golden S-expression for JSON:
	//   (document (object (pair (string (string_content)) (number))))
	// parsed from the smoke sample: {"a": 1}
	//
	// grammargen's JSON should produce the same tree.
	genLang, err := GenerateLanguage(JSONGrammar())
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	input := `{"a": 1}`
	golden := "(document (object (pair (string (string_content)) (number))))"

	parser := gotreesitter.NewParser(genLang)
	tree, err := parser.Parse([]byte(input))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := tree.RootNode().SExpr(genLang)
	if got != golden {
		t.Errorf("S-expression mismatch:\n  got:  %s\n  want: %s", got, golden)
	}
}

// ── Parity: Round-trip through blob encoding ────────────────────────────────

func TestParityJSONBlobRoundTrip(t *testing.T) {
	// Generate blob, decode it, parse with the decoded language, compare
	// against direct GenerateLanguage() result.
	g := JSONGrammar()

	blob, err := Generate(g)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	directLang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage failed: %v", err)
	}

	// Decode the blob using our local decode function.
	blobLang, err := decodeLanguageBlob(blob)
	if err != nil {
		t.Fatalf("DecodeLanguageBlob failed: %v", err)
	}

	inputs := []string{`null`, `{"a": 1}`, `[1, 2, 3]`}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			src := []byte(input)

			directTree, _ := gotreesitter.NewParser(directLang).Parse(src)
			blobTree, _ := gotreesitter.NewParser(blobLang).Parse(src)

			directSexp := directTree.RootNode().SExpr(directLang)
			blobSexp := blobTree.RootNode().SExpr(blobLang)

			if directSexp != blobSexp {
				t.Errorf("blob round-trip mismatch:\n  direct: %s\n  blob:   %s", directSexp, blobSexp)
			}
		})
	}
}

// ── Parity: Validate + Generate coherence ───────────────────────────────────

func TestParityValidateBeforeGenerate(t *testing.T) {
	// All built-in grammars should validate cleanly before generation.
	grammars := []struct {
		name string
		fn   func() *Grammar
	}{
		{"json", JSONGrammar},
		{"calc", CalcGrammar},
		{"glr", GLRGrammar},
		{"keyword", KeywordGrammar},
		{"ext", ExtScannerGrammar},
		{"alias", AliasSuperGrammar},
	}

	for _, g := range grammars {
		t.Run(g.name, func(t *testing.T) {
			warnings := Validate(g.fn())
			if len(warnings) > 0 {
				t.Errorf("validation warnings for %s: %v", g.name, warnings)
			}
		})
	}
}

// ── Parity: Report coherence ────────────────────────────────────────────────

func TestParityReportProperties(t *testing.T) {
	// GenerateWithReport should produce a usable Language with correct counts.
	grammars := []struct {
		name string
		fn   func() *Grammar
	}{
		{"json", JSONGrammar},
		{"calc", CalcGrammar},
	}

	for _, g := range grammars {
		t.Run(g.name, func(t *testing.T) {
			report, err := GenerateWithReport(g.fn())
			if err != nil {
				t.Fatalf("GenerateWithReport failed: %v", err)
			}

			// Report counts should match Language fields.
			if report.SymbolCount != int(report.Language.SymbolCount) {
				t.Errorf("SymbolCount mismatch: report=%d lang=%d",
					report.SymbolCount, report.Language.SymbolCount)
			}
			if report.StateCount != int(report.Language.StateCount) {
				t.Errorf("StateCount mismatch: report=%d lang=%d",
					report.StateCount, report.Language.StateCount)
			}
			if report.TokenCount != int(report.Language.TokenCount) {
				t.Errorf("TokenCount mismatch: report=%d lang=%d",
					report.TokenCount, report.Language.TokenCount)
			}

			// Blob should be non-empty.
			if len(report.Blob) == 0 {
				t.Error("report blob is empty")
			}
		})
	}
}
