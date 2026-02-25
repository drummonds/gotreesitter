# gotreesitter

Pure-Go [tree-sitter](https://tree-sitter.github.io/) runtime. No CGo, no C toolchain, cross-compiles everywhere including WASM.

```sh
go get github.com/odvcencio/gotreesitter
```

Uses the same parse-table format as tree-sitter, so existing grammars work without recompilation. 205 languages ship in the registry.

## Why pure Go?

Every existing Go tree-sitter binding requires CGo:

- Cross-compilation breaks (`GOOS=wasip1`, `GOARCH=arm64` from Linux, Windows without MSYS2)
- CI pipelines need a C toolchain in every build image
- `go install` fails for end users without `gcc`
- Race detector, fuzzing, and coverage tools work poorly across the CGo boundary

gotreesitter is `go get` and build, on any target.

## Quick start

```go
import (
    "fmt"

    "github.com/odvcencio/gotreesitter"
    "github.com/odvcencio/gotreesitter/grammars"
)

func main() {
    src := []byte(`package main

func main() {}
`)

    lang := grammars.GoLanguage()
    parser := gotreesitter.NewParser(lang)

    tree := parser.Parse(src)
    fmt.Println(tree.RootNode())
}
```

Use `grammars.DetectLanguage("main.go")` to pick the right grammar by filename.

### Queries

```go
q, _ := gotreesitter.NewQuery(`(function_declaration name: (identifier) @fn)`, lang)
cursor := q.Exec(tree.RootNode(), lang, src)

for {
    match, ok := cursor.NextMatch()
    if !ok {
        break
    }
    for _, cap := range match.Captures {
        fmt.Println(cap.Node.Text(src))
    }
}
```

S-expression queries, predicates (`#eq?`, `#match?`, `#any-of?`, `#has-ancestor?`, etc.), quantifiers, alternation, and field matching are all supported. See [Query API](#query-api) for the full list.

### Incremental editing

Re-parse only the changed region after an edit. Unchanged subtrees are reused.

```go
tree := parser.Parse(src)

// User types "x" at byte offset 42
src = append(src[:42], append([]byte("x"), src[42:]...)...)

tree.Edit(gotreesitter.InputEdit{
    StartByte:   42,
    OldEndByte:  42,
    NewEndByte:  43,
    StartPoint:  gotreesitter.Point{Row: 3, Column: 10},
    OldEndPoint: gotreesitter.Point{Row: 3, Column: 10},
    NewEndPoint: gotreesitter.Point{Row: 3, Column: 11},
})

tree2 := parser.ParseIncremental(src, tree)
```

### Tree cursor

`TreeCursor` gives O(1) parent, child, and sibling movement with zero allocations. It tracks `(node, childIndex)` frames on a stack, so sibling movement indexes directly into `parent.children[]`.

```go
c := gotreesitter.NewTreeCursorFromTree(tree)

c.GotoFirstChild()              // program -> first child
c.GotoChildByFieldName("body")  // jump to "body" field

for ok := c.GotoFirstNamedChild(); ok; ok = c.GotoNextNamedSibling() {
    fmt.Printf("%s at %d\n", c.CurrentNodeType(), c.CurrentNode().StartByte())
}
```

Navigation methods: `GotoFirstChild`, `GotoLastChild`, `GotoNextSibling`, `GotoPrevSibling`, `GotoParent`, plus named-only variants (`GotoFirstNamedChild`, etc.), field-based (`GotoChildByFieldName`, `GotoChildByFieldID`), and position-based (`GotoFirstChildForByte`, `GotoFirstChildForPoint`).

### Highlighting

```go
hl, _ := gotreesitter.NewHighlighter(lang, highlightQuery)
ranges := hl.Highlight(src)

for _, r := range ranges {
    fmt.Printf("%s: %q\n", r.Capture, src[r.StartByte:r.EndByte])
}
```

Text predicates (`#eq?`, `#match?`, `#any-of?`, `#not-eq?`) require `source []byte`. Pass `nil` to skip predicate evaluation.

### Tagging

Extract definitions and references:

```go
entry := grammars.DetectLanguage("main.go")
lang := entry.Language()

tagger, _ := gotreesitter.NewTagger(lang, entry.TagsQuery)
tags := tagger.Tag(src)

for _, tag := range tags {
    fmt.Printf("%s %s at %d:%d\n", tag.Kind, tag.Name,
        tag.NameRange.StartPoint.Row, tag.NameRange.StartPoint.Column)
}
```

### Parse quality

Each `LangEntry` has a `Quality` field:

| Quality | Meaning |
|---|---|
| `full` | Token source or DFA with external scanner — full fidelity |
| `partial` | DFA-partial — missing external scanner, tree may have gaps |
| `none` | Cannot parse |

## Benchmarks

Measured against [`go-tree-sitter`](https://github.com/smacker/go-tree-sitter) (CGo binding), parsing a Go file with 500 function definitions.

```
goos: linux / goarch: amd64 / cpu: Intel(R) Core(TM) Ultra 9 285
```

| Workload | gotreesitter | CGo binding | Speedup |
|---|---:|---:|---|
| Full parse | 1,330 μs | 2,058 μs | 1.5x |
| Incremental (1-byte edit) | 1.38 μs | 124 μs | 90x |
| Incremental (no-op) | 8.6 ns | 121 μs | 14,000x |

The incremental path reuses subtrees aggressively. A single-byte edit reparses in microseconds; a no-op reparse exits on a nil-check in single-digit nanoseconds.

<details>
<summary>Raw benchmark output</summary>

```
go test -run '^$' -bench 'BenchmarkGoParse' -benchmem -count=3

# CGo baseline (cgo_harness module):
cd cgo_harness
go test . -run '^$' -tags treesitter_c_bench -bench 'BenchmarkCTreeSitterGoParse' -benchmem -count=3
```

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `CTreeSitterGoParseFull` | 2,058,000 | 600 | 6 |
| `CTreeSitterGoParseIncrementalSingleByteEdit` | 124,100 | 648 | 7 |
| `CTreeSitterGoParseIncrementalNoEdit` | 121,100 | 600 | 6 |
| `GoParseFull` | 1,330,000 | 10,842 | 2,495 |
| `GoParseIncrementalSingleByteEdit` | 1,381 | 361 | 9 |
| `GoParseIncrementalNoEdit` | 8.63 | 0 | 0 |

</details>

## Supported languages

205 grammars ship in the registry. Run `go run ./cmd/parity_report` for live status.

- 204 full, 1 partial (`norg` — external scanner not yet ported)
- 195 DFA-backed, 9 hand-written token sources, 1 DFA-partial
- 111 languages have hand-written Go external scanners

<details>
<summary>Full language list</summary>

`ada`, `agda`, `angular`, `apex`, `arduino`, `asm`, `astro`, `authzed`, `awk`, `bash`, `bass`, `beancount`, `bibtex`, `bicep`, `bitbake`, `blade`, `brightscript`, `c`, `c_sharp`, `caddy`, `cairo`, `capnp`, `chatito`, `circom`, `clojure`, `cmake`, `cobol`, `comment`, `commonlisp`, `cooklang`, `corn`, `cpon`, `cpp`, `crystal`, `css`, `csv`, `cuda`, `cue`, `cylc`, `d`, `dart`, `desktop`, `devicetree`, `dhall`, `diff`, `disassembly`, `djot`, `dockerfile`, `dot`, `doxygen`, `dtd`, `earthfile`, `ebnf`, `editorconfig`, `eds`, `eex`, `elisp`, `elixir`, `elm`, `elsa`, `embedded_template`, `enforce`, `erlang`, `facility`, `faust`, `fennel`, `fidl`, `firrtl`, `fish`, `foam`, `forth`, `fortran`, `fsharp`, `gdscript`, `git_config`, `git_rebase`, `gitattributes`, `gitcommit`, `gitignore`, `gleam`, `glsl`, `gn`, `go`, `godot_resource`, `gomod`, `graphql`, `groovy`, `hack`, `hare`, `haskell`, `haxe`, `hcl`, `heex`, `hlsl`, `html`, `http`, `hurl`, `hyprlang`, `ini`, `janet`, `java`, `javascript`, `jinja2`, `jq`, `jsdoc`, `json`, `json5`, `jsonnet`, `julia`, `just`, `kconfig`, `kdl`, `kotlin`, `ledger`, `less`, `linkerscript`, `liquid`, `llvm`, `lua`, `luau`, `make`, `markdown`, `markdown_inline`, `matlab`, `mermaid`, `meson`, `mojo`, `move`, `nginx`, `nickel`, `nim`, `ninja`, `nix`, `norg`, `nushell`, `objc`, `ocaml`, `odin`, `org`, `pascal`, `pem`, `perl`, `php`, `pkl`, `powershell`, `prisma`, `prolog`, `promql`, `properties`, `proto`, `pug`, `puppet`, `purescript`, `python`, `ql`, `r`, `racket`, `regex`, `rego`, `requirements`, `rescript`, `robot`, `ron`, `rst`, `ruby`, `rust`, `scala`, `scheme`, `scss`, `smithy`, `solidity`, `sparql`, `sql`, `squirrel`, `ssh_config`, `starlark`, `svelte`, `swift`, `tablegen`, `tcl`, `teal`, `templ`, `textproto`, `thrift`, `tlaplus`, `tmux`, `todotxt`, `toml`, `tsx`, `turtle`, `twig`, `typescript`, `typst`, `uxntal`, `v`, `verilog`, `vhdl`, `vimdoc`, `vue`, `wgsl`, `wolfram`, `xml`, `yaml`, `yuck`, `zig`

</details>

## Query API

| Feature | Status |
|---|---|
| Compile + execute (`NewQuery`, `Execute`, `ExecuteNode`) | supported |
| Cursor streaming (`Exec`, `NextMatch`, `NextCapture`) | supported |
| Structural quantifiers (`?`, `*`, `+`) | supported |
| Alternation (`[...]`) | supported |
| Field matching (`name: (identifier)`) | supported |
| `#eq?` / `#not-eq?` | supported |
| `#match?` / `#not-match?` | supported |
| `#any-of?` / `#not-any-of?` | supported |
| `#lua-match?` | supported |
| `#has-ancestor?` / `#not-has-ancestor?` | supported |
| `#not-has-parent?` | supported |
| `#is?` / `#is-not?` | supported |
| `#set!` / `#offset!` directives | parsed and accepted |

All shipped highlight and tags queries compile (`156/156` highlight, `69/69` tags).

## Known limitations

`norg` requires an external scanner (122 tokens) that hasn't been ported. It parses with the DFA lexer alone; tokens needing the scanner are skipped. The tree is structurally valid but may have gaps. Check `entry.Quality`.

## Adding a language

1. Add the grammar to `grammars/languages.manifest`
2. Generate: `go run ./cmd/ts2go -manifest grammars/languages.manifest -outdir ./grammars -package grammars -compact=true`
3. Add smoke samples to `cmd/parity_report/main.go` and `grammars/parse_support_test.go`
4. Verify: `go run ./cmd/parity_report && go test ./grammars/...`

## Architecture

gotreesitter reimplements the tree-sitter runtime in pure Go:

- Table-driven LR(1) parser with GLR for ambiguous grammars
- Incremental subtree reuse — unchanged regions skip reparsing
- Slab-based arena allocator to minimize GC pressure
- DFA lexer generated from grammar tables via `ts2go`
- External scanner VM (bytecode interpreter for Python indentation, etc.)
- S-expression query engine with predicate evaluation and streaming cursors
- Tree cursor for O(1) stateful navigation
- Query-based syntax highlighting and symbol tagging

Grammar tables come from upstream tree-sitter `parser.c` files, extracted by `ts2go`, serialized to compressed binary blobs, and lazy-loaded on first use.

### Build tags and environment

**External grammar blobs** (avoid embedding in the binary):

```sh
go build -tags grammar_blobs_external
GOTREESITTER_GRAMMAR_BLOB_DIR=/path/to/blobs  # required
GOTREESITTER_GRAMMAR_BLOB_MMAP=false           # disable mmap (Unix only)
```

**Curated language set** (smaller binary):

```sh
go build -tags grammar_set_core  # c, go, java, javascript, python, rust, typescript, etc.
GOTREESITTER_GRAMMAR_SET=go,json,python  # runtime restriction
```

**Grammar cache tuning** (long-lived processes):

```go
grammars.SetEmbeddedLanguageCacheLimit(8)    // LRU cap
grammars.UnloadEmbeddedLanguage("rust.bin")  // drop one
grammars.PurgeEmbeddedLanguageCache()        // drop all
```

```sh
GOTREESITTER_GRAMMAR_CACHE_LIMIT=8       # LRU cap via env
GOTREESITTER_GRAMMAR_IDLE_TTL=5m         # evict after idle
GOTREESITTER_GRAMMAR_IDLE_SWEEP=30s      # sweep interval
GOTREESITTER_GRAMMAR_COMPACT=true        # loader compaction (default)
GOTREESITTER_GRAMMAR_STRING_INTERN_LIMIT=200000
GOTREESITTER_GRAMMAR_TRANSITION_INTERN_LIMIT=20000
```

## Testing

```sh
go test ./... -race -count=1
```

Covers smoke tests (205 grammars), golden S-expression snapshots (20 languages), highlight validation, query matching, incremental reparsing, error recovery, GLR ambiguity, and fuzz testing.

## Roadmap

v0.4.0 — 205 grammars, stable parser, incremental reparsing, query engine, tree cursor, highlighting, tagging.

Next:
- Query parity hardening (field-negation semantics, metadata directives)
- More external scanners for `dfa-partial` languages
- `Parse() (*Tree, error)` — return errors instead of silent nil trees
- Automated parity testing against C tree-sitter output
- Fuzz coverage expansion

## License

[MIT](LICENSE)
