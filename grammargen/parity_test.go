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
	"os"
	"strings"
	"testing"
	"time"

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

// ── Multi-Grammar Import Pipeline Parity ─────────────────────────────────────
//
// Tests the full pipeline: grammar.js → ImportGrammarJS → GenerateLanguage →
// parse → compare against reference .bin blob. Each grammar tracks metrics
// at four stages:
//   Stage 1 (import):   grammar.js → Grammar IR
//   Stage 2 (generate): Grammar IR → Language
//   Stage 3 (parse):    Language → parse samples without ERROR
//   Stage 4 (parity):   S-expressions match reference blob exactly

// importParityGrammar describes a real-world grammar to test against.
type importParityGrammar struct {
	name     string
	path     string                            // path to grammar.js (ImportGrammarJS)
	jsonPath string                            // path to grammar.json (ImportGrammarJSON) — preferred over path
	blobFunc func() *gotreesitter.Language      // reference blob loader
	samples  []string                           // representative parse inputs
	genTimeout time.Duration                   // per-grammar generation timeout (0 = default 30s)
	// Expected pass counts at each stage (regression floor — can only increase).
	expectImport   bool // import should succeed
	expectGenerate bool // generate should succeed
	expectNoErrors int  // minimum samples that parse without ERROR
	expectParity   int  // minimum samples with exact S-expression match
}

var importParityGrammars = []importParityGrammar{
	{
		name: "json", path: "/tmp/grammar_parity/json/grammar.js", jsonPath: "/tmp/grammar_parity/json/src/grammar.json",
		blobFunc: grammars.JsonLanguage,
		samples: []string{
			`{}`, `{"a": 1}`, `[1, 2, 3]`, `"hello"`, `42`, `true`, `null`,
			`{"a": {"b": [1, null, "x"]}}`,
			`{"key": "value", "arr": [1, 2.5, -3, true, false, null]}`,
			`[]`, `[null]`, `[[]]`, `[{}]`,
			`{"":""}`, `{"a":true,"b":false}`,
			`-0`, `1e10`, `1.5e-3`, `0.0`,
			`{"nested":{"deep":{"arr":[1,2,3]}}}`,
			`"\u0041"`, `"line1\nline2"`, `"tab\there"`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 23, expectParity: 23,
	},
	{
		name: "ini", path: "/tmp/grammar_parity/ini/grammar.js", jsonPath: "/tmp/grammar_parity/ini/src/grammar.json",
		blobFunc: grammars.IniLanguage,
		samples: []string{
			"[section]\nkey=value\n",
			"key=value\n",
			"[main]\nhost=localhost\nport=8080\n",
			"; comment\n[section]\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 4, expectParity: 3,
	},
	{
		name: "properties", path: "/tmp/grammar_parity/properties/grammar.js", jsonPath: "/tmp/grammar_parity/properties/src/grammar.json",
		blobFunc: grammars.PropertiesLanguage,
		samples: []string{
			"key=value\n",
			"key = value\n",
			"# comment\nkey=value\n",
			"key1=v1\nkey2=v2\n",
			"key = value with spaces\n",
			"! alternative comment\nkey=val\n",
			"multi.level.key = true\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 7, expectParity: 7,
	},
	{
		name: "requirements", path: "/tmp/grammar_parity/requirements/grammar.js", jsonPath: "/tmp/grammar_parity/requirements/src/grammar.json",
		blobFunc: grammars.RequirementsLanguage,
		samples: []string{
			"flask==2.0",
			"numpy",
			"requests>=2.0\nflask",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 3, expectParity: 2,
	},
	{
		name: "jsdoc", path: "/tmp/grammar_parity/jsdoc/grammar.js", jsonPath: "/tmp/grammar_parity/jsdoc/src/grammar.json",
		blobFunc: grammars.JsdocLanguage,
		samples: []string{
			"@param {string} name",
			"@returns {number}",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 2, expectParity: 0,
	},
	{
		name: "css", path: "/tmp/grammar_parity/css/grammar.js", jsonPath: "/tmp/grammar_parity/css/src/grammar.json",
		blobFunc: grammars.CssLanguage,
		samples: []string{
			"body { color: red; }",
			".class { margin: 0; }",
		},
		expectImport: true, expectGenerate: false, expectNoErrors: 0, expectParity: 0,
	},
	{
		name: "html", path: "/tmp/grammar_parity/html/grammar.js", jsonPath: "/tmp/grammar_parity/html/src/grammar.json",
		blobFunc: grammars.HtmlLanguage,
		samples: []string{
			"<div></div>",
			"<p>hello</p>",
		},
		// Imports and generates, but parsing requires external scanner (9 externals).
		expectImport: true, expectGenerate: true, expectNoErrors: 0, expectParity: 0,
	},
	{
		name: "scala", path: "/tmp/grammar_parity/scala/grammar.js",
		blobFunc: grammars.ScalaLanguage,
		samples: []string{
			"val x = 1",
			"object Main { def main(args: Array[String]): Unit = {} }",
		},
		// 208 rules — imports but too large for LR generation in 30s.
		expectImport: true, expectGenerate: false, expectNoErrors: 0, expectParity: 0,
	},
	// ── grammar.json imports (canonical resolved form) ──
	{
		name: "csv", jsonPath: "/tmp/grammar_parity/csv/csv/src/grammar.json",
		blobFunc: grammars.CsvLanguage,
		samples: []string{
			"a,b,c\n1,2,3\n",
			"hello,world\n",
			"1,2.5,true\n",
			"\"quoted,field\",plain\n",
			"a\nb\nc\n",
			"single\n",
			"a,b\nc,d\ne,f\n",
			"a,b,c\n1,2,3\n4,5,6\n",
			"\"quoted\",\"with,comma\",plain\n",
			"\"has \"\"double\"\" quotes\"\n",
			"x\n",
			"a,b,c,d,e,f\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 12, expectParity: 12,
	},
	{
		name: "json5", jsonPath: "/tmp/grammar_parity/json5/src/grammar.json",
		blobFunc: grammars.Json5Language,
		samples: []string{
			`null`, `true`, `false`,
			`42`, `-3.14`, `0xFF`,
			`"hello"`, `'single'`, `""`, `''`,
			`[]`, `[1, 2, 3]`, `[1, "two", true]`,
			`{}`, `{"key": "value"}`,
			`{a: 1}`, `{$key: 1}`, `{_key: 1}`,
			`{"nested": {"a": [1, 2]}}`,
			`[1,]`, `{a: 1,}`,
			`Infinity`, `-Infinity`, `NaN`,
			`.5`, `10.`,
			`{unquoted: 'single'}`,
			`{trailing: 1,}`,
			`[1,2,3,]`,
			`{a: Infinity}`,
			`{a: NaN}`,
			`{a: +1}`,
			`{a: .5}`,
			`{a: 0xff}`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 34, expectParity: 34,
	},
	{
		name: "diff", jsonPath: "/tmp/grammar_parity/diff/src/grammar.json",
		blobFunc: grammars.DiffLanguage,
		samples: []string{
			"--- a/file.txt\n+++ b/file.txt\n@@ -1,3 +1,3 @@\n-old\n+new\n",
			"diff --git a/file b/file\n",
			"+added line\n",
			"-removed line\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 4, expectParity: 1,
	},
	{
		name: "gitcommit", jsonPath: "/tmp/grammar_parity/gitcommit/src/grammar.json",
		blobFunc: grammars.GitcommitLanguage,
		samples: []string{
			"Initial commit\n",
			"Fix bug\n\nDetails here\n",
			"feat: add new feature\n",
			"# comment only\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 4, expectParity: 2,
	},
	{
		name: "graphql", jsonPath: "/tmp/grammar_parity/graphql/src/grammar.json",
		blobFunc: grammars.GraphqlLanguage,
		samples: []string{
			`{ hero { name } }`,
			`query { user(id: 1) { name email } }`,
			`type Query { users: [User] }`,
			`mutation { createUser(name: "test") { id } }`,
			`fragment F on User { name email }`,
			`input CreateUserInput { name: String! email: String }`,
			`type Query { user(id: ID!): User }`,
			`enum Role { ADMIN USER GUEST }`,
			`interface Node { id: ID! }`,
			`union SearchResult = User | Post`,
			`scalar DateTime`,
			`extend type Query { newField: String }`,
			`schema { query: Query mutation: Mutation }`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 13, expectParity: 13,
	},
	{
		name: "dot", jsonPath: "/tmp/grammar_parity/dot/src/grammar.json",
		blobFunc: grammars.DotLanguage,
		samples: []string{
			"graph {}",
			"digraph {}",
			"strict graph {}",
			"digraph { a -> b }",
			"graph { a -- b }",
			"digraph G { a -> b; b -> c; }",
			"graph G { a -- b; b -- c; }",
			"digraph { node [shape=box]; a -> b [label=\"edge\"]; }",
			"digraph { a -> b -> c }",
			"digraph { rank=same; a; b; }",
			"digraph { subgraph cluster_0 { a; b; } }",
			"graph { a [color=red, style=bold] }",
			"digraph { a -> b; a -> c; b -> d; c -> d; }",
			"digraph { \"node with spaces\" -> other }",
			"strict digraph { a -> b; a -> b; }",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 15, expectParity: 14,
	},
	{
		name: "ron", jsonPath: "/tmp/grammar_parity/ron/src/grammar.json",
		blobFunc: grammars.RonLanguage,
		samples: []string{
			"(x: 1, y: 2)",
			"[1, 2, 3]",
			"true",
			"false",
			"42",
			"()",
			"[]",
			"(a: true, b: false, c: 42)",
			"[[1, 2], [3, 4]]",
			`"hello"`,
			"(x: 1, y: 2, z: 3)",
			"Some(42)",
			"None",
			"[[], [1], [1, 2]]",
			"(field: [1, 2, 3])",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 15, expectParity: 14,
	},
	{
		name: "toml", jsonPath: "/tmp/grammar_parity/toml/src/grammar.json",
		blobFunc: grammars.TomlLanguage,
		samples: []string{
			"key = \"value\"\n",
			"[section]\nkey = 1\n",
			"arr = [1, 2, 3]\n",
			"[server]\nhost = \"localhost\"\nport = 8080\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 4, expectParity: 2,
	},
	{
		name: "proto", jsonPath: "/tmp/grammar_parity/proto/src/grammar.json",
		blobFunc: grammars.ProtoLanguage,
		samples: []string{
			`syntax = "proto3";`,
			"syntax = \"proto3\";\nmessage Foo {\n  string name = 1;\n}",
			"syntax = \"proto3\";\nenum Color {\n  RED = 0;\n  GREEN = 1;\n}",
			"syntax = \"proto3\";\npackage mypackage;",
			"syntax = \"proto3\";\nimport \"other.proto\";",
			"syntax = \"proto3\";\nmessage Nested {\n  message Inner {\n    int32 x = 1;\n  }\n  Inner inner = 1;\n}",
			"syntax = \"proto3\";\nservice MyService {\n  rpc GetUser (GetUserRequest) returns (User) {}\n}",
			"syntax = \"proto3\";\nmessage Foo {\n  repeated string tags = 1;\n  map<string, int32> metadata = 2;\n}",
			"syntax = \"proto3\";\nmessage Foo {\n  oneof value {\n    string text = 1;\n    int32 number = 2;\n  }\n}",
			"syntax = \"proto3\";\noption java_package = \"com.example\";\nmessage Empty {}",
			"syntax = \"proto3\";\nmessage Foo {\n  reserved 1, 2, 5 to 10;\n  reserved \"old_field\";\n}",
			"syntax = \"proto3\";\nmessage Foo {\n  optional string name = 1;\n  bytes data = 2;\n  bool flag = 3;\n}",
			"syntax = \"proto3\";\nmessage Foo {\n  double lat = 1;\n  float lng = 2;\n  fixed64 big = 3;\n}",
			"syntax = \"proto2\";\nmessage Foo {\n  required string name = 1;\n  optional int32 age = 2;\n}",
			"syntax = \"proto3\";\nimport public \"common.proto\";\nimport weak \"deprecated.proto\";",
			"syntax = \"proto3\";\nservice Greeter {\n  rpc SayHello (HelloRequest) returns (stream HelloReply) {}\n}",
			"syntax = \"proto3\";\nmessage Foo {\n  option deprecated = true;\n  int32 x = 1 [deprecated = true];\n}",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 17, expectParity: 16,
	},
	{
		name: "comment", jsonPath: "/tmp/grammar_parity/comment/src/grammar.json",
		blobFunc: grammars.CommentLanguage,
		samples: []string{
			"just text",
			"some random words",
			"x = 42",
			"a+b",
			"",
			"hello world",
			"line1\nline2",
			"  indented text",
			"foo bar baz qux",
			"12345",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 10, expectParity: 10,
	},
	{
		name: "pem", jsonPath: "/tmp/grammar_parity/pem/src/grammar.json",
		blobFunc: grammars.PemLanguage,
		samples: []string{
			"",
			"random text",
			"BEGIN",
			"  spaces  ",
			"multi\nline\ntext",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 5, expectParity: 5,
	},
	{
		name: "dockerfile", jsonPath: "/tmp/grammar_parity/dockerfile/src/grammar.json",
		blobFunc: grammars.DockerfileLanguage,
		samples: []string{
			"FROM ubuntu\n",
			"RUN echo hello\n",
			"COPY . /app\n",
			"EXPOSE 8080\n",
			"ENV FOO=bar\n",
			"WORKDIR /app\n",
			"LABEL version=\"1.0\"\n",
			"USER root\n",
			"HEALTHCHECK CMD curl -f http://localhost/ || exit 1\n",
			"# just a comment\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 10, expectParity: 5,
	},
	{
		name: "gitattributes", jsonPath: "/tmp/grammar_parity/gitattributes/src/grammar.json",
		blobFunc: grammars.GitattributesLanguage,
		samples: []string{
			"# a comment\n",
			"\n",
			"",
			"# first\n# second\n",
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 4, expectParity: 3,
	},
	{
		name: "nix", jsonPath: "/tmp/grammar_parity/nix/src/grammar.json",
		blobFunc: grammars.NixLanguage,
		samples: []string{
			"42",
			"true",
			"null",
			"{ x = 1; }",
			`"hello"`,
			`let x = 1; in x`,
			`if true then 1 else 2`,
			`[ 1 2 3 ]`,
			`x: x + 1`,
			`{ a = 1; b = 2; }`,
			`rec { a = b; b = 1; }`,
			`with import ./foo.nix; x`,
			`assert true; 42`,
			`a.b.c`,
			`a // b`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 15, expectParity: 13,
	},
	{
		name: "jq", jsonPath: "/tmp/grammar_parity/jq/src/grammar.json",
		blobFunc: grammars.JqLanguage,
		genTimeout: 60 * time.Second,
		samples: []string{
			`.`, `.foo`, `.foo.bar`, `.[] | .name`, `[.[] | .+1]`,
			`{a: 1, b: 2}`, `if .x then .y else .z end`, `def f: . + 1; f`,
			`null`, `"hello"`, `42`, `.foo | select(. > 2)`,
			`[range(10)]`, `.a as $x | $x + 1`, `try .foo catch "default"`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 15, expectParity: 0,
	},
	{
		name: "hcl", jsonPath: "/tmp/grammar_parity/hcl/src/grammar.json",
		blobFunc: grammars.HclLanguage,
		genTimeout: 60 * time.Second,
		samples: []string{
			`x = 1`, `x = "hello"`, `x = true`,
			`resource "aws_instance" "example" {}`,
			`resource "aws_instance" "example" { ami = "abc" }`,
			`variable "name" { type = string }`,
			`output "result" { value = var.name }`,
			`x = [1, 2, 3]`, `x = { a = 1 }`, `locals { x = 1 }`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 10, expectParity: 3,
	},
	{
		name: "regex", jsonPath: "/tmp/grammar_parity/regex/src/grammar.json",
		blobFunc: grammars.RegexLanguage,
		genTimeout: 90 * time.Second,
		samples: []string{
			`a`, `abc`, `a|b`, `a*`, `a+`, `a?`,
			`[abc]`, `[a-z]`, `[^abc]`, `(abc)`,
			`\d`, `\w`, `\s`, `.`,
			`^abc$`, `a{3}`, `a{1,3}`,
		},
		expectImport: true, expectGenerate: true, expectNoErrors: 17, expectParity: 17,
	},
}

// generateWithTimeout runs GenerateLanguage with a deadline. Returns nil, err
// if the generation exceeds the timeout (e.g., LR table construction hangs).
func generateWithTimeout(gram *Grammar, timeout time.Duration) (*gotreesitter.Language, error) {
	type result struct {
		lang *gotreesitter.Language
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		lang, err := GenerateLanguage(gram)
		ch <- result{lang, err}
	}()
	select {
	case r := <-ch:
		return r.lang, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("generation timed out after %v", timeout)
	}
}

func TestMultiGrammarImportPipeline(t *testing.T) {
	// Summary metrics.
	var totalGrammars, importOK, generateOK, totalSamples, noErrorSamples, paritySamples int

	for _, g := range importParityGrammars {
		t.Run(g.name, func(t *testing.T) {
			totalGrammars++

			// Stage 1: Import — prefer grammar.json over grammar.js.
			var gram *Grammar
			var importErr error
			if g.jsonPath != "" {
				source, err := os.ReadFile(g.jsonPath)
				if err != nil {
					t.Skipf("grammar.json not available: %v", err)
					return
				}
				gram, importErr = ImportGrammarJSON(source)
			} else {
				source, err := os.ReadFile(g.path)
				if err != nil {
					t.Skipf("grammar.js not available: %v (clone repos to /tmp/grammar_parity/)", err)
					return
				}
				gram, importErr = ImportGrammarJS(source)
			}
			if importErr != nil {
				if g.expectImport {
					t.Errorf("REGRESSION: import should succeed but failed: %v", importErr)
				} else {
					t.Logf("import failed (expected): %v", importErr)
				}
				return
			}
			importOK++
			t.Logf("import: %d rules, %d extras, %d externals", len(gram.Rules), len(gram.Extras), len(gram.Externals))

			// Stage 2: Generate (with timeout to avoid LR table hangs)
			timeout := g.genTimeout
			if timeout == 0 {
				timeout = 30 * time.Second
			}
			genLang, err := generateWithTimeout(gram, timeout)
			if err != nil {
				if g.expectGenerate {
					t.Errorf("REGRESSION: generate should succeed but failed: %v", err)
				} else {
					t.Logf("generate failed (expected): %v", err)
				}
				return
			}
			generateOK++
			t.Logf("generate: %d symbols, %d states, %d tokens",
				genLang.SymbolCount, genLang.StateCount, genLang.TokenCount)

			// Stage 3 + 4: Parse and compare
			refLang := g.blobFunc()
			genParser := gotreesitter.NewParser(genLang)
			refParser := gotreesitter.NewParser(refLang)

			noErrCount := 0
			parityCount := 0

			for _, sample := range g.samples {
				totalSamples++
				genTree, _ := genParser.Parse([]byte(sample))
				refTree, _ := refParser.Parse([]byte(sample))

				genSexp := genTree.RootNode().SExpr(genLang)
				refSexp := refTree.RootNode().SExpr(refLang)

				genHasError := strings.Contains(genSexp, "ERROR") || strings.Contains(genSexp, "MISSING")

				if !genHasError {
					noErrCount++
					noErrorSamples++
				}

				if genSexp == refSexp {
					parityCount++
					paritySamples++
				}
			}

			t.Logf("parse: %d/%d no-error, %d/%d parity",
				noErrCount, len(g.samples), parityCount, len(g.samples))

			// Regression gates: counts can only improve.
			if noErrCount < g.expectNoErrors {
				t.Errorf("REGRESSION: no-error count %d < floor %d", noErrCount, g.expectNoErrors)
			}
			if parityCount < g.expectParity {
				t.Errorf("REGRESSION: parity count %d < floor %d", parityCount, g.expectParity)
			}
		})
	}

	// Log summary.
	t.Logf("PIPELINE SUMMARY: %d/%d import, %d/%d generate, %d/%d no-error, %d/%d parity",
		importOK, totalGrammars, generateOK, totalGrammars,
		noErrorSamples, totalSamples, paritySamples, totalSamples)
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
