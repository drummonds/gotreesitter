package gotreesitter

import "testing"

func TestTreeEditShiftsNodes(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	// Parse "1+2"
	tree := mustParse(t, parser, []byte("1+2"))
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}

	// Simulate inserting "0" before "1": "01+2"
	// Edit: at byte 0, old end 0, new end 1 (inserted 1 byte)
	tree.Edit(InputEdit{
		StartByte:   0,
		OldEndByte:  0,
		NewEndByte:  1,
		StartPoint:  Point{0, 0},
		OldEndPoint: Point{0, 0},
		NewEndPoint: Point{0, 1},
	})

	// After edit, the root's end should shift by 1.
	if root.EndByte() != 4 {
		t.Errorf("root EndByte after edit = %d, want 4", root.EndByte())
	}

	// The edit should be recorded.
	if len(tree.Edits()) != 1 {
		t.Fatalf("expected 1 edit recorded, got %d", len(tree.Edits()))
	}
}

func TestParseIncremental(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	// Parse "1+2"
	tree := mustParse(t, parser, []byte("1+2"))

	// Edit: change to "1+3"
	tree.Edit(InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{0, 2},
		OldEndPoint: Point{0, 3},
		NewEndPoint: Point{0, 3},
	})

	// Incremental re-parse with new source.
	newTree := mustParseIncremental(t, parser, []byte("1+3"), tree)
	root := newTree.RootNode()
	if root == nil {
		t.Fatal("incremental parse returned nil root")
	}

	// Should have the same structure: expression(expression(NUMBER), +, NUMBER)
	if root.ChildCount() != 3 {
		t.Fatalf("root child count = %d, want 3", root.ChildCount())
	}

	num := root.Child(2)
	if num.Text(newTree.Source()) != "3" {
		t.Errorf("changed NUMBER text = %q, want %q", num.Text(newTree.Source()), "3")
	}
}

func TestHighlightIncremental(t *testing.T) {
	lang := buildArithmeticLanguage()

	// Simple highlight query: capture NUMBER nodes.
	h, err := NewHighlighter(lang, `(NUMBER) @number`)
	if err != nil {
		t.Fatal(err)
	}

	// Initial highlight.
	source1 := []byte("1+2")
	ranges1 := h.Highlight(source1)
	if len(ranges1) < 2 {
		t.Fatalf("expected at least 2 highlight ranges, got %d", len(ranges1))
	}

	// Parse for incremental use.
	parser := NewParser(lang)
	tree := mustParse(t, parser, source1)

	// Edit: "1+2" -> "1+3"
	tree.Edit(InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{0, 2},
		OldEndPoint: Point{0, 3},
		NewEndPoint: Point{0, 3},
	})

	source2 := []byte("1+3")
	ranges2, newTree := h.HighlightIncremental(source2, tree)
	if newTree == nil {
		t.Fatal("HighlightIncremental returned nil tree")
	}

	// Should still have at least 2 number ranges.
	if len(ranges2) < 2 {
		t.Fatalf("expected at least 2 incremental highlight ranges, got %d", len(ranges2))
	}

	// Verify the captures are "number".
	for _, r := range ranges2 {
		if r.Capture != "number" {
			t.Errorf("unexpected capture %q, want %q", r.Capture, "number")
		}
	}
}

func TestParseIncrementalReusesUnchangedLeaf(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	oldSource := []byte("1+2+3")
	tree := mustParse(t, parser, oldSource)
	root := tree.RootNode()
	if root == nil {
		t.Fatal("initial parse returned nil root")
	}
	oldRight := root.Child(2)
	if oldRight == nil {
		t.Fatal("missing right child in initial tree")
	}

	// Edit the middle number: "1+2+3" -> "1+4+3"
	tree.Edit(InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{0, 2},
		OldEndPoint: Point{0, 3},
		NewEndPoint: Point{0, 3},
	})

	newSource := []byte("1+4+3")
	newTree := mustParseIncremental(t, parser, newSource, tree)
	newRoot := newTree.RootNode()
	if newRoot == nil {
		t.Fatal("incremental parse returned nil root")
	}
	newRight := newRoot.Child(2)
	if newRight == nil {
		t.Fatal("missing right child in incremental tree")
	}

	if newRight != oldRight {
		t.Fatal("expected unchanged right leaf node to be reused")
	}
	if got := newRight.Text(newTree.Source()); got != "3" {
		t.Fatalf("reused leaf text = %q, want %q", got, "3")
	}
}

func TestParseIncrementalReusesRootWhenUnchanged(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	source := []byte("1+2")
	tree := mustParse(t, parser, source)
	if tree.RootNode() == nil {
		t.Fatal("initial parse returned nil root")
	}

	// No edits: incremental parse should be able to reuse the whole root subtree.
	newTree := mustParseIncremental(t, parser, source, tree)
	if newTree.RootNode() == nil {
		t.Fatal("incremental parse returned nil root")
	}

	if newTree.RootNode() != tree.RootNode() {
		t.Fatal("expected root node to be reused when there are no edits")
	}
}

func TestParseIncrementalReusesRootAfterUndo(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	source := []byte("1+2+3")
	tree := mustParse(t, parser, source)
	oldRoot := tree.RootNode()
	if oldRoot == nil {
		t.Fatal("initial parse returned nil root")
	}

	// Edit and undo before reparsing: "1+2+3" -> "1+4+3" -> "1+2+3".
	edit := InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{0, 2},
		OldEndPoint: Point{0, 3},
		NewEndPoint: Point{0, 3},
	}
	tree.Edit(edit)
	tree.Edit(edit)

	newTree := mustParseIncremental(t, parser, source, tree)
	if newTree.RootNode() == nil {
		t.Fatal("incremental parse returned nil root")
	}
	if newTree.RootNode() != oldRoot {
		t.Fatal("expected root node to be reused after undo")
	}
	if newTree.RootNode().dirty {
		t.Fatal("expected reused root to have dirty flag cleared after undo reuse")
	}
}

func TestTreeEditNodesAfterEdit(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	tree := mustParse(t, parser, []byte("1+2+3"))
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}

	origEnd := root.EndByte()

	// Delete the "+3" at end: "1+2+3" -> "1+2"
	// Edit: start=3, oldEnd=5, newEnd=3
	tree.Edit(InputEdit{
		StartByte:   3,
		OldEndByte:  5,
		NewEndByte:  3,
		StartPoint:  Point{0, 3},
		OldEndPoint: Point{0, 5},
		NewEndPoint: Point{0, 3},
	})

	// Root should shrink.
	if root.EndByte() != 3 {
		t.Errorf("root EndByte after deletion = %d, want 3 (was %d)", root.EndByte(), origEnd)
	}
}

func TestParseIncrementalReleaseKeepsBorrowedNodesAlive(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)

	oldSrc := []byte("1+2+3")
	oldTree := mustParse(t, parser, oldSrc)
	oldRoot := oldTree.RootNode()
	if oldRoot == nil {
		t.Fatal("initial parse returned nil root")
	}
	oldRight := oldRoot.Child(2)
	if oldRight == nil {
		t.Fatal("missing right leaf in initial tree")
	}
	oldArena := oldRight.ownerArena
	if oldArena == nil {
		t.Fatal("expected reused leaf to have an owning arena")
	}

	oldTree.Edit(InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{0, 2},
		OldEndPoint: Point{0, 3},
		NewEndPoint: Point{0, 3},
	})

	newSrc := []byte("1+4+3")
	newTree := mustParseIncremental(t, parser, newSrc, oldTree)
	newRight := newTree.RootNode().Child(2)
	if newRight == nil {
		t.Fatal("missing right leaf in incremental tree")
	}
	if newRight != oldRight {
		t.Fatal("expected right leaf to be reused")
	}
	if oldArena.refs.Load() < 2 {
		t.Fatalf("expected borrowed arena to be retained by new tree, refs=%d", oldArena.refs.Load())
	}
	foundBorrowed := false
	for _, a := range newTree.borrowedArena {
		if a == oldArena {
			foundBorrowed = true
			break
		}
	}
	if !foundBorrowed {
		t.Fatal("expected new tree to track reused node arena in borrowedArena")
	}

	oldTree.Release()
	oldTree.Release() // idempotent
	if oldArena.refs.Load() < 1 {
		t.Fatalf("borrowed arena refcount dropped too far after old tree release: %d", oldArena.refs.Load())
	}

	// Force arena churn to validate that borrowed nodes are retained correctly.
	for i := 0; i < 2000; i++ {
		tmp := mustParse(t, parser, []byte("7+8"))
		if tmp.RootNode() == nil {
			t.Fatalf("tmp parse %d returned nil root", i)
		}
		tmp.Release()
	}

	if got := newRight.Text(newTree.Source()); got != "3" {
		t.Fatalf("reused right leaf text after old release = %q, want %q", got, "3")
	}

	newTree.Release()
	newTree.Release() // idempotent
	if oldArena.refs.Load() != 0 {
		t.Fatalf("borrowed arena should be fully released after new tree release, refs=%d", oldArena.refs.Load())
	}
}

func TestTryReuseSubtreeReusesFirstEligibleNonLeafCandidate(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	oldSource := []byte("1+2+3+4")
	newSource := []byte("9+2+3+4")
	oldTree := mustParse(t, parser, oldSource)

	var reuseScratch reuseScratch
	reuse := (&reuseCursor{}).reset(oldTree, newSource, &reuseScratch)
	if reuse == nil {
		t.Fatal("reuse cursor reset returned nil")
	}

	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	stack := newGLRStackWithScratch(lang.InitialState, &entryScratch)

	// Force the non-leaf fallback path by using a non-matching lookahead symbol.
	lookahead := Token{
		Symbol:     2,
		StartByte:  0,
		EndByte:    1,
		StartPoint: Point{Row: 0, Column: 0},
		EndPoint:   Point{Row: 0, Column: 1},
	}
	candidates := reuse.candidates(lookahead.StartByte)
	var expected *Node
	var expectedState StateID
	var expectedSpan uint32
	for _, n := range candidates {
		if n == nil || n.ChildCount() == 0 || n.Parent() == nil {
			continue
		}
		span := n.EndByte() - n.StartByte()
		if span == 0 || span > 2048 {
			continue
		}
		if _, ok := parser.reuseTargetState(stack.top().state, n, lookahead); !ok {
			continue
		}
		expected = n
		expectedState, _ = parser.reuseTargetState(stack.top().state, n, lookahead)
		expectedSpan = span
		break
	}
	if expected == nil {
		t.Fatal("expected at least one eligible non-leaf reuse candidate")
	}

	ts := &stubTokenSource{
		tokens: []Token{
			{Symbol: 2, StartByte: expected.EndByte(), EndByte: expected.EndByte() + 1},
			{Symbol: 0, StartByte: uint32(len(newSource)), EndByte: uint32(len(newSource))},
		},
	}
	nextTok, reusedBytes, ok := parser.tryReuseSubtree(&stack, lookahead, ts, reuse, &entryScratch, &gssScratch)
	if !ok {
		t.Fatal("expected non-leaf fallback reuse to succeed")
	}
	if stack.top().node != expected {
		t.Fatalf("reused wrong non-leaf candidate: got span=%d want span=%d", stack.top().node.EndByte()-stack.top().node.StartByte(), expectedSpan)
	}
	if stack.top().state != expectedState {
		t.Fatalf("stack top state = %d, want %d", stack.top().state, expectedState)
	}
	if reusedBytes != expectedSpan {
		t.Fatalf("reusedBytes = %d, want %d", reusedBytes, expectedSpan)
	}
	if nextTok.StartByte < expected.EndByte() {
		t.Fatalf("next token did not advance past reused subtree: next=%d reusedEnd=%d", nextTok.StartByte, expected.EndByte())
	}
}

func TestTryReuseSubtreeSkipsLargeNonLeafCandidate(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	oldSource := make([]byte, 3000)
	newSource := make([]byte, len(oldSource))
	copy(newSource, oldSource)
	newSource[0] = 1

	leaf := NewLeafNode(1, true, 0, 1, Point{Row: 0, Column: 0}, Point{Row: 0, Column: 1})
	leaf.parseState = 1
	large := NewParentNode(3, true, []*Node{leaf}, nil, 0)
	large.parseState = 2
	large.endByte = uint32(len(oldSource))
	large.endPoint = Point{Row: 0, Column: uint32(len(oldSource))}
	root := NewParentNode(3, true, []*Node{large}, nil, 0)
	root.parseState = 2
	root.endByte = uint32(len(oldSource))
	root.endPoint = Point{Row: 0, Column: uint32(len(oldSource))}
	oldTree := NewTree(root, oldSource, lang)

	var reuseScratch reuseScratch
	reuse := (&reuseCursor{}).reset(oldTree, newSource, &reuseScratch)
	if reuse == nil {
		t.Fatal("reuse cursor reset returned nil")
	}
	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	stack := newGLRStackWithScratch(lang.InitialState, &entryScratch)

	lookahead := Token{
		Symbol:     2,
		StartByte:  0,
		EndByte:    1,
		StartPoint: Point{Row: 0, Column: 0},
		EndPoint:   Point{Row: 0, Column: 1},
	}
	ts := &stubTokenSource{tokens: []Token{{Symbol: 0, StartByte: uint32(len(newSource)), EndByte: uint32(len(newSource))}}}
	nextTok, reusedBytes, ok := parser.tryReuseSubtree(&stack, lookahead, ts, reuse, &entryScratch, &gssScratch)
	if ok {
		t.Fatalf("expected large non-leaf candidate to be rejected by span cutoff, reusedBytes=%d nextTok=%+v", reusedBytes, nextTok)
	}
	if stack.top().node != nil {
		t.Fatal("stack should remain unchanged when reuse fails")
	}
}

func TestReuseTargetStateAmbiguousShiftMustMatchNodeState(t *testing.T) {
	lang := buildArithmeticLanguage()
	ambiguousActionIdx := uint16(len(lang.ParseActions))
	lang.ParseActions = append(lang.ParseActions, ParseActionEntry{
		Actions: []ParseAction{
			{Type: ParseActionShift, State: 7},
			{Type: ParseActionShift, State: 9},
		},
	})
	lang.ParseTable[0][1] = ambiguousActionIdx
	parser := NewParser(lang)

	lookahead := Token{Symbol: 1}
	leaf := &Node{symbol: 1, parseState: 9}
	nextState, ok := parser.reuseTargetState(0, leaf, lookahead)
	if !ok {
		t.Fatal("expected reuseTargetState to accept matching shift state in ambiguous set")
	}
	if nextState != 9 {
		t.Fatalf("reuseTargetState returned state %d, want 9", nextState)
	}

	leaf.parseState = 8
	if _, ok := parser.reuseTargetState(0, leaf, lookahead); ok {
		t.Fatal("expected reuseTargetState to reject ambiguous shift when node parseState does not match any action")
	}
}
