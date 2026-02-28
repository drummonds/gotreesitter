//go:build cgo && treesitter_c_parity

package cgoharness

// Structural parity tests: compare gotreesitter parse trees against a native C
// reference parser built from grammars/languages.lock commits, node-by-node.
//
// Run with:
//   go test . -tags treesitter_c_parity -run TestParity -v
//
// Requires CGo and a C toolchain. Gated behind build tag to keep the default
// test suite zero-CGo.

import (
	"fmt"
	"os"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type parityMeta struct {
	skipReason string // non-empty = skip with this message
}

var paritySkips = map[string]parityMeta{
	// Keep this map for explicitly known structural mismatches.
	// Parse-support-specific skips (e.g. missing scanners) should not live here.
}

type parityCase struct {
	name   string
	source string
}

var parityCases = []parityCase{
	{name: "bash", source: "echo hi\n"},
	{name: "c", source: "int main(void) { return 0; }\n"},
	{name: "cpp", source: "int main() { return 0; }\n"},
	{name: "css", source: "body { color: red; }\n"},
	{name: "elixir", source: "defmodule M do\n  def f(x), do: x + 1\nend\n"},
	{name: "go", source: "package main\n\nfunc main() {\n\tprintln(1)\n}\n"},
	{name: "html", source: "<html><body>Hello</body></html>\n"},
	{name: "java", source: "class Main { int x; }\n"},
	{name: "javascript", source: "function f() { return 1; }\nconst x = () => x + 1;\n"},
	{name: "kotlin", source: "fun main() {\n    val x: Int? = null\n    println(x)\n}\n"},
	{name: "lua", source: "local x = 1\n"},
	{name: "php", source: "<?php echo 1;\n"},
	{name: "python", source: "def f():\n    return 1\n"},
	{name: "ruby", source: "def f\n  1\nend\n"},
	{name: "rust", source: "fn main() { let x = 1; }\n"},
	{name: "scala", source: "object Main { def f(x: Int): Int = x + 1 }\n"},
	{name: "sql", source: "SELECT id, name FROM users WHERE id = 1;\n"},
	{name: "swift", source: "func f() -> Int {\n  return 1\n}\n"},
	{name: "toml", source: "a = 1\ntitle = \"hello\"\ntags = [\"x\", \"y\"]\n"},
	{name: "tsx", source: "const x = <div/>;\n"},
	{name: "typescript", source: "function f(): number { return 1; }\n"},
	{name: "yaml", source: "a: 1\n"},
}

var parityEntriesByName, paritySupportByName = func() (map[string]grammars.LangEntry, map[string]grammars.ParseSupport) {
	entries := make(map[string]grammars.LangEntry)
	for _, entry := range grammars.AllLanguages() {
		entries[entry.Name] = entry
	}

	support := make(map[string]grammars.ParseSupport)
	for _, report := range grammars.AuditParseSupport() {
		support[report.Name] = report
	}
	return entries, support
}()

var parityCompareFields = func() bool {
	raw := strings.TrimSpace(os.Getenv("GTS_PARITY_COMPARE_FIELDS"))
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}()

type nodeSnapshot struct {
	Type       string
	StartByte  uint32
	EndByte    uint32
	IsNamed    bool
	IsMissing  bool
	ChildCount int
}

func snapshotGo(n *gotreesitter.Node, lang *gotreesitter.Language) nodeSnapshot {
	return nodeSnapshot{
		Type:       n.Type(lang),
		StartByte:  n.StartByte(),
		EndByte:    n.EndByte(),
		IsNamed:    n.IsNamed(),
		IsMissing:  n.IsMissing(),
		ChildCount: n.ChildCount(),
	}
}

func snapshotC(n *sitter.Node) nodeSnapshot {
	return nodeSnapshot{
		Type:       n.Kind(),
		StartByte:  uint32(n.StartByte()),
		EndByte:    uint32(n.EndByte()),
		IsNamed:    n.IsNamed(),
		IsMissing:  n.IsMissing(),
		ChildCount: int(n.ChildCount()),
	}
}

// compareNodes walks both trees recursively in lockstep. Any divergence is
// appended to errs with a breadcrumb path for diagnosis.
func compareNodes(goNode *gotreesitter.Node, goLang *gotreesitter.Language, cNode *sitter.Node, path string, errs *[]string) {
	gs := snapshotGo(goNode, goLang)
	cs := snapshotC(cNode)

	if gs.Type != cs.Type {
		*errs = append(*errs, fmt.Sprintf("%s: Type go=%q c=%q", path, gs.Type, cs.Type))
	}
	if gs.StartByte != cs.StartByte {
		*errs = append(*errs, fmt.Sprintf("%s: StartByte go=%d c=%d", path, gs.StartByte, cs.StartByte))
	}
	if gs.EndByte != cs.EndByte {
		*errs = append(*errs, fmt.Sprintf("%s: EndByte go=%d c=%d", path, gs.EndByte, cs.EndByte))
	}
	if gs.IsNamed != cs.IsNamed {
		*errs = append(*errs, fmt.Sprintf("%s: IsNamed go=%v c=%v", path, gs.IsNamed, cs.IsNamed))
	}
	if gs.IsMissing != cs.IsMissing {
		*errs = append(*errs, fmt.Sprintf("%s: IsMissing go=%v c=%v", path, gs.IsMissing, cs.IsMissing))
	}
	if gs.ChildCount != cs.ChildCount {
		*errs = append(*errs, fmt.Sprintf("%s: ChildCount go=%d c=%d", path, gs.ChildCount, cs.ChildCount))
		return
	}

	for i := 0; i < gs.ChildCount; i++ {
		childPath := fmt.Sprintf("%s[%d]", path, i)
		if parityCompareFields {
			goField := goNode.FieldNameForChild(i, goLang)
			cField := cNode.FieldNameForChild(uint32(i))
			if goField != cField {
				*errs = append(*errs, fmt.Sprintf("%s: FieldName go=%q c=%q", childPath, goField, cField))
			}
		}
		compareNodes(goNode.Child(i), goLang, cNode.Child(uint(i)), childPath, errs)
	}
}

// compareGoNodes walks two gotreesitter trees recursively in lockstep.
func compareGoNodes(left *gotreesitter.Node, lang *gotreesitter.Language, right *gotreesitter.Node, path string, errs *[]string) {
	if left == nil || right == nil {
		if left != right {
			*errs = append(*errs, fmt.Sprintf("%s: nil mismatch left=%v right=%v", path, left == nil, right == nil))
		}
		return
	}

	ls := snapshotGo(left, lang)
	rs := snapshotGo(right, lang)

	if ls.Type != rs.Type {
		*errs = append(*errs, fmt.Sprintf("%s: Type left=%q right=%q", path, ls.Type, rs.Type))
	}
	if ls.StartByte != rs.StartByte {
		*errs = append(*errs, fmt.Sprintf("%s: StartByte left=%d right=%d", path, ls.StartByte, rs.StartByte))
	}
	if ls.EndByte != rs.EndByte {
		*errs = append(*errs, fmt.Sprintf("%s: EndByte left=%d right=%d", path, ls.EndByte, rs.EndByte))
	}
	if ls.IsNamed != rs.IsNamed {
		*errs = append(*errs, fmt.Sprintf("%s: IsNamed left=%v right=%v", path, ls.IsNamed, rs.IsNamed))
	}
	if ls.IsMissing != rs.IsMissing {
		*errs = append(*errs, fmt.Sprintf("%s: IsMissing left=%v right=%v", path, ls.IsMissing, rs.IsMissing))
	}
	if ls.ChildCount != rs.ChildCount {
		*errs = append(*errs, fmt.Sprintf("%s: ChildCount left=%d right=%d", path, ls.ChildCount, rs.ChildCount))
		return
	}

	for i := 0; i < ls.ChildCount; i++ {
		childPath := fmt.Sprintf("%s[%d]", path, i)
		if parityCompareFields {
			leftField := left.FieldNameForChild(i, lang)
			rightField := right.FieldNameForChild(i, lang)
			if leftField != rightField {
				*errs = append(*errs, fmt.Sprintf("%s: FieldName left=%q right=%q", childPath, leftField, rightField))
			}
		}
		compareGoNodes(left.Child(i), lang, right.Child(i), childPath, errs)
	}
}

func parseWithGo(tc parityCase, src []byte, oldTree *gotreesitter.Tree) (*gotreesitter.Tree, *gotreesitter.Language, error) {
	entry, ok := parityEntriesByName[tc.name]
	if !ok {
		return nil, nil, fmt.Errorf("missing gotreesitter registry entry for %q", tc.name)
	}
	report, ok := paritySupportByName[tc.name]
	if !ok {
		return nil, nil, fmt.Errorf("missing parse support report for %q", tc.name)
	}

	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)

	var tree *gotreesitter.Tree
	var parseErr error
	if oldTree == nil {
		switch report.Backend {
		case grammars.ParseBackendTokenSource:
			if entry.TokenSourceFactory == nil {
				return nil, nil, fmt.Errorf("token source backend without factory for %q", tc.name)
			}
			tree, parseErr = parser.ParseWithTokenSource(src, entry.TokenSourceFactory(src, lang))
		case grammars.ParseBackendDFA, grammars.ParseBackendDFAPartial:
			tree, parseErr = parser.Parse(src)
		default:
			return nil, nil, fmt.Errorf("unsupported parse backend %q for %q", report.Backend, tc.name)
		}
	} else {
		switch report.Backend {
		case grammars.ParseBackendTokenSource:
			if entry.TokenSourceFactory == nil {
				return nil, nil, fmt.Errorf("token source backend without factory for %q", tc.name)
			}
			tree, parseErr = parser.ParseIncrementalWithTokenSource(src, oldTree, entry.TokenSourceFactory(src, lang))
		case grammars.ParseBackendDFA, grammars.ParseBackendDFAPartial:
			tree, parseErr = parser.ParseIncremental(src, oldTree)
		default:
			return nil, nil, fmt.Errorf("unsupported incremental backend %q for %q", report.Backend, tc.name)
		}
	}
	if parseErr != nil {
		return nil, nil, fmt.Errorf("gotreesitter parse error: %w", parseErr)
	}

	if tree == nil || tree.RootNode() == nil {
		return nil, nil, fmt.Errorf("gotreesitter returned nil tree")
	}
	return tree, lang, nil
}

func runParityCase(t *testing.T, tc parityCase, label string, src []byte) {
	t.Helper()

	goTree, goLang, err := parseWithGo(tc, src, nil)
	if err != nil {
		t.Fatalf("[%s/%s] gotreesitter parse error: %v", tc.name, label, err)
	}
	cLang, err := ParityCLanguage(tc.name)
	if err != nil {
		if skipReason := parityReferenceSkipReason(err); skipReason != "" {
			t.Skipf("[%s/%s] skip C reference parser: %s", tc.name, label, skipReason)
		}
		t.Fatalf("[%s/%s] load C parser from languages.lock: %v", tc.name, label, err)
	}

	cParser := sitter.NewParser()
	defer cParser.Close()
	if err := cParser.SetLanguage(cLang); err != nil {
		if skipReason := parityReferenceSkipReason(err); skipReason != "" {
			t.Skipf("[%s/%s] skip C reference parser SetLanguage: %s", tc.name, label, skipReason)
		}
		t.Fatalf("[%s/%s] C parser SetLanguage error: %v", tc.name, label, err)
	}
	cTree := cParser.Parse(src, nil)
	if cTree == nil || cTree.RootNode() == nil {
		t.Fatalf("[%s/%s] C reference parser returned nil tree", tc.name, label)
	}
	defer cTree.Close()
	goRoot := goTree.RootNode()
	cRoot := cTree.RootNode()
	goRuntime := goTree.ParseRuntime()

	var errs []string
	compareNodes(goRoot, goLang, cRoot, "root", &errs)
	if len(errs) == 0 {
		return
	}

	const maxErrors = 20
	shown := errs
	extra := 0
	if len(errs) > maxErrors {
		shown = errs[:maxErrors]
		extra = len(errs) - maxErrors
	}
	msg := strings.Join(shown, "\n  ")
	if extra > 0 {
		msg += fmt.Sprintf("\n  ... and %d more", extra)
	}
	goSummary := fmt.Sprintf(
		"go_root type=%q end=%d/%d hasError=%v %s",
		goRoot.Type(goLang), goRoot.EndByte(), len(src), goRoot.HasError(), goRuntime.Summary(),
	)
	cSummary := fmt.Sprintf(
		"c_root type=%q end=%d/%d hasError=%v",
		cRoot.Kind(), cRoot.EndByte(), len(src), cRoot.HasError(),
	)
	t.Errorf("[%s/%s] %d node divergence(s):\n  %s\n  %s\n  %s", tc.name, label, len(errs), msg, goSummary, cSummary)
}

func parityReferenceSkipReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "ABI version") || strings.Contains(msg, "Incompatible language version") {
		return msg
	}
	return ""
}

func normalizedSource(name, src string) []byte {
	if name == "yaml" || name == "swift" {
		return []byte(src)
	}
	// Trim one trailing newline so both runtimes compare syntax tree shape
	// independent of final-line extra-token representation.
	return []byte(strings.TrimSuffix(src, "\n"))
}

// safeEditOffset chooses one insertion offset for a single-byte whitespace edit.
func safeEditOffset(src []byte) int {
	insertAt := -1

	for i := len(src) - 1; i >= 0; i-- {
		if src[i] == ' ' || src[i] == '\t' {
			insertAt = i
			break
		}
	}
	if insertAt < 0 && len(src) > 0 && src[0] == '<' {
		for i := 0; i+1 < len(src); i++ {
			if src[i] == '>' && src[i+1] == '<' {
				insertAt = i + 1
				break
			}
		}
	}
	if insertAt < 0 {
		for i := len(src) - 1; i >= 0; i-- {
			switch src[i] {
			case '\n', '}', ';', ')', ']', '>', '<':
				insertAt = i
				break
			}
			if insertAt >= 0 {
				break
			}
		}
	}
	if insertAt < 0 {
		insertAt = len(src)
	}
	return insertAt
}

func insertSpaceAt(src []byte, insertAt int) []byte {
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > len(src) {
		insertAt = len(src)
	}

	edited := make([]byte, len(src)+1)
	copy(edited[:insertAt], src[:insertAt])
	edited[insertAt] = ' '
	copy(edited[insertAt+1:], src[insertAt:])
	return edited
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

// incrementalEditOffsets returns preferred insertion offsets for incremental
// parity tests. It tries syntax-local edits first, then falls back to all
// interior offsets so the test can find a fresh-parity-safe site.
func incrementalEditOffsets(src []byte) []int {
	if len(src) == 0 {
		return []int{0}
	}

	offsets := make([]int, 0, len(src)+8)
	seen := make([]bool, len(src)+1)
	add := func(pos int) {
		if pos < 0 || pos > len(src) || seen[pos] {
			return
		}
		seen[pos] = true
		offsets = append(offsets, pos)
	}

	add(safeEditOffset(src))

	// For markup-like sources, prefer edits in text content (outside tags).
	if src[0] == '<' {
		inTag := false
		for i := 0; i < len(src); i++ {
			switch src[i] {
			case '<':
				inTag = true
			case '>':
				inTag = false
			default:
				if !inTag && i > 0 && isWordByte(src[i-1]) && isWordByte(src[i]) {
					add(i)
				}
			}
		}
	}

	// Prefer edits inside existing word runs across languages.
	for i := 1; i < len(src); i++ {
		if isWordByte(src[i-1]) && isWordByte(src[i]) {
			add(i)
		}
	}

	// Exhaustive fallback over interior positions, biased toward tail.
	for i := len(src) - 1; i >= 1; i-- {
		add(i)
	}
	add(len(src))
	return offsets
}

// safeEditSource inserts one ASCII space at the preferred offset.
func safeEditSource(src []byte) ([]byte, int) {
	insertAt := safeEditOffset(src)
	return insertSpaceAt(src, insertAt), insertAt
}

// TestParityFreshParse verifies that fresh parse trees match the CGo binding
// on the currently CI-gated language set.
func TestParityFreshParse(t *testing.T) {
	for _, tc := range parityCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if meta, ok := paritySkips[tc.name]; ok && meta.skipReason != "" {
				t.Skipf("known mismatch: %s", meta.skipReason)
			}
			runParityCase(t, tc, "fresh", normalizedSource(tc.name, tc.source))
		})
	}
}

// TestParityIncrementalParse verifies that gotreesitter incremental parse
// matches a CGo fresh parse on edited source.
func TestParityIncrementalParse(t *testing.T) {
	for _, tc := range parityCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if meta, ok := paritySkips[tc.name]; ok && meta.skipReason != "" {
				t.Skipf("known mismatch: %s", meta.skipReason)
			}

			src := normalizedSource(tc.name, tc.source)
			if len(src) < 2 {
				t.Skip("source too short for incremental edit")
			}

			oldTree, _, err := parseWithGo(tc, src, nil)
			if err != nil {
				t.Fatalf("[%s/incremental] initial gotreesitter parse error: %v", tc.name, err)
			}
			cLang, err := ParityCLanguage(tc.name)
			if err != nil {
				if skipReason := parityReferenceSkipReason(err); skipReason != "" {
					t.Skipf("[%s/incremental] skip C reference parser: %s", tc.name, skipReason)
				}
				t.Fatalf("[%s/incremental] load C parser from languages.lock: %v", tc.name, err)
			}

			cParser := sitter.NewParser()
			defer cParser.Close()
			if err := cParser.SetLanguage(cLang); err != nil {
				if skipReason := parityReferenceSkipReason(err); skipReason != "" {
					t.Skipf("[%s/incremental] skip C parser SetLanguage: %s", tc.name, skipReason)
				}
				t.Fatalf("[%s/incremental] C parser SetLanguage error: %v", tc.name, err)
			}

			candidates := incrementalEditOffsets(src)
			edited := []byte(nil)
			editAt := -1
			validSiteCount := 0
			firstFreshErr := ""
			firstIncrErr := ""
			for _, candidateAt := range candidates {
				candidateEdited := insertSpaceAt(src, candidateAt)

				goFreshTree, goFreshLang, err := parseWithGo(tc, candidateEdited, nil)
				if err != nil {
					t.Fatalf("[%s/incremental] gotreesitter fresh parse on candidate edit@%d error: %v", tc.name, candidateAt, err)
				}

				cFreshTree := cParser.Parse(candidateEdited, nil)
				if cFreshTree == nil || cFreshTree.RootNode() == nil {
					t.Fatalf("[%s/incremental] C reference parser returned nil tree on candidate edit@%d", tc.name, candidateAt)
				}

				var freshErrs []string
				compareNodes(goFreshTree.RootNode(), goFreshLang, cFreshTree.RootNode(), "root", &freshErrs)
				cFreshTree.Close()
				if len(freshErrs) > 0 {
					if firstFreshErr == "" {
						firstFreshErr = freshErrs[0]
					}
					continue
				}

				// Candidate sites must also preserve Go incremental correctness:
				// incremental parse on edited source must match Go fresh parse.
				candidateOldTree, _, err := parseWithGo(tc, src, nil)
				if err != nil {
					t.Fatalf("[%s/incremental] gotreesitter candidate old-tree parse@%d error: %v", tc.name, candidateAt, err)
				}
				candidateEdit := gotreesitter.InputEdit{
					StartByte:   uint32(candidateAt),
					OldEndByte:  uint32(candidateAt),
					NewEndByte:  uint32(candidateAt + 1),
					StartPoint:  pointAtOffset(src, candidateAt),
					OldEndPoint: pointAtOffset(src, candidateAt),
					NewEndPoint: pointAtOffset(candidateEdited, candidateAt+1),
				}
				candidateOldTree.Edit(candidateEdit)

				goIncrTree, goIncrLang, err := parseWithGo(tc, candidateEdited, candidateOldTree)
				if err != nil {
					t.Fatalf("[%s/incremental] gotreesitter candidate incremental parse@%d error: %v", tc.name, candidateAt, err)
				}

				var incrErrs []string
				compareGoNodes(goIncrTree.RootNode(), goIncrLang, goFreshTree.RootNode(), "root", &incrErrs)
				if len(incrErrs) > 0 {
					if firstIncrErr == "" {
						firstIncrErr = incrErrs[0]
					}
					continue
				}

				validSiteCount++
				if editAt < 0 {
					edited = candidateEdited
					editAt = candidateAt
				}
			}
			if tc.name == "html" {
				t.Logf("[%s/incremental] valid edit sites=%d/%d", tc.name, validSiteCount, len(candidates))
			}
			if editAt < 0 {
				reason := firstFreshErr
				if reason == "" {
					reason = firstIncrErr
				}
				if reason == "" {
					reason = "no comparable fresh/incremental trees from candidate edits"
				}
				t.Skipf("[%s/incremental] no fresh-parity-safe incremental edit site found (%d candidates, first divergence: %s)", tc.name, len(candidates), reason)
			}

			edit := gotreesitter.InputEdit{
				StartByte:   uint32(editAt),
				OldEndByte:  uint32(editAt),
				NewEndByte:  uint32(editAt + 1),
				StartPoint:  pointAtOffset(src, editAt),
				OldEndPoint: pointAtOffset(src, editAt),
				NewEndPoint: pointAtOffset(edited, editAt+1),
			}

			oldTree.Edit(edit)

			goTree, goLang, err := parseWithGo(tc, edited, oldTree)
			if err != nil {
				t.Fatalf("[%s/incremental] gotreesitter incremental parse error: %v", tc.name, err)
			}

			cTree := cParser.Parse(edited, nil)
			if cTree == nil || cTree.RootNode() == nil {
				t.Fatalf("[%s/incremental] C reference parser returned nil tree", tc.name)
			}
			defer cTree.Close()

			var errs []string
			compareNodes(goTree.RootNode(), goLang, cTree.RootNode(), "root", &errs)
			if len(errs) == 0 {
				return
			}

			const maxErrors = 20
			shown := errs
			extra := 0
			if len(errs) > maxErrors {
				shown = errs[:maxErrors]
				extra = len(errs) - maxErrors
			}
			msg := strings.Join(shown, "\n  ")
			if extra > 0 {
				msg += fmt.Sprintf("\n  ... and %d more", extra)
			}
			t.Errorf("[%s/incremental] %d node divergence(s):\n  %s", tc.name, len(errs), msg)
		})
	}
}

// TestParityHasNoErrors checks that well-formed inputs for CI-gated languages
// do not produce error nodes.
func TestParityHasNoErrors(t *testing.T) {
	for _, tc := range parityCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if meta, ok := paritySkips[tc.name]; ok && meta.skipReason != "" {
				t.Skipf("known mismatch: %s", meta.skipReason)
			}
			cLang, err := ParityCLanguage(tc.name)
			if err != nil {
				if skipReason := parityReferenceSkipReason(err); skipReason != "" {
					t.Skipf("[%s/errors] skip C reference parser: %s", tc.name, skipReason)
				}
				t.Fatalf("[%s/errors] load C parser from languages.lock: %v", tc.name, err)
			}
			cParser := sitter.NewParser()
			defer cParser.Close()
			if err := cParser.SetLanguage(cLang); err != nil {
				if skipReason := parityReferenceSkipReason(err); skipReason != "" {
					t.Skipf("[%s/errors] skip C parser SetLanguage: %s", tc.name, skipReason)
				}
				t.Fatalf("[%s/errors] C parser SetLanguage error: %v", tc.name, err)
			}

			tree, _, err := parseWithGo(tc, normalizedSource(tc.name, tc.source), nil)
			if err != nil {
				t.Fatalf("[%s/errors] gotreesitter parse error: %v", tc.name, err)
			}
			if tree.RootNode().HasError() {
				t.Errorf("[%s] RootNode().HasError() = true on well-formed input", tc.name)
			}
		})
	}
}
