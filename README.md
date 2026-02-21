# gotreesitter

Pure-Go [tree-sitter](https://tree-sitter.github.io/) runtime — no CGo, no C toolchain, WASM-ready.

```sh
go get github.com/odvcencio/gotreesitter
```

Implements the same parse-table format tree-sitter uses, so existing grammars work without recompilation. Outperforms the CGo binding on every workload — incremental edits (the dominant operation in editors and language servers) are **90x faster** than the C implementation.

## Why Not CGo?

Every existing Go tree-sitter binding requires CGo. That means:

- Cross-compilation breaks (`GOOS=wasip1`, `GOARCH=arm64` from Linux, Windows without MSYS2)
- CI pipelines need a C toolchain in every build image
- `go install` fails for end users without `gcc`
- Race detector, fuzzing, and coverage tools work poorly across the CGo boundary

gotreesitter is pure Go. `go get` and build — on any target, any platform.

## Quick Start

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

    // After editing source, reparse incrementally:
    //   tree.Edit(edit)
    //   tree2 := parser.ParseIncremental(newSrc, tree)
}
```

### Queries

Tree-sitter's S-expression query language is fully supported, including predicates and cursor-based streaming.

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

### Incremental Editing

After the initial parse, re-parse only the changed region — unchanged subtrees are reused automatically.

```go
// Initial parse
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

// Incremental reparse — ~1.35 μs vs 122 μs for the CGo binding (90x faster)
tree2 := parser.ParseIncremental(src, tree)
```

> **Tip:** Use `grammars.DetectLanguage("main.go")` to pick the right grammar by filename — useful for editor integration.

### Syntax Highlighting

```go
hl, _ := gotreesitter.NewHighlighter(lang, highlightQuery)
ranges := hl.Highlight(src)

for _, r := range ranges {
    fmt.Printf("%s: %q\n", r.HighlightName, src[r.StartByte:r.EndByte])
}
```

> **Note:** Text predicates (`#eq?`, `#match?`, `#any-of?`, `#not-eq?`) require `source []byte` to evaluate. Passing `nil` disables predicate checks.

---

## Benchmarks

Measured against [`go-tree-sitter`](https://github.com/smacker/go-tree-sitter) (the standard CGo binding), parsing a Go source file with 500 function definitions.

```
goos: linux / goarch: amd64 / cpu: Intel(R) Core(TM) Ultra 9 285
go test -run '^$' -tags treesitter_c_bench \
  -bench 'Benchmark(GoParse|CTreeSitterGoParse)' -benchmem -count=3
```

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkCTreeSitterGoParseFull` | 2,071,000 | 600 | 6 |
| `BenchmarkCTreeSitterGoParseIncrementalSingleByteEdit` | 121,700 | 648 | 7 |
| `BenchmarkCTreeSitterGoParseIncrementalNoEdit` | 119,100 | 600 | 6 |
| `BenchmarkGoParseFull` | 1,284,000 | 14,858 | 2,495 |
| `BenchmarkGoParseIncrementalSingleByteEdit` | 1,349 | 361 | 9 |
| `BenchmarkGoParseIncrementalNoEdit` | 8.73 | 0 | 0 |

**Summary:**

| Workload | gotreesitter | CGo binding | Ratio |
|---|---:|---:|---|
| Full parse | 1,284 μs | 2,071 μs | **~1.6x faster** |
| Incremental (single-byte edit) | 1.35 μs | 122 μs | **~90x faster** |
| Incremental (no-op reparse) | 8.7 ns | 119 μs | **~13,700x faster** |

The incremental hot path reuses subtrees aggressively — a single-byte edit reparses in microseconds while the CGo binding pays full C-runtime and call overhead. The no-edit fast path exits on a single nil-check: zero allocations, single-digit nanoseconds.

---

## Supported Languages

33 languages. Run `go run ./cmd/parity_report` to verify.

Current registry:
`agda`, `bash`, `c`, `c_sharp`, `cpp`, `css`, `elixir`, `embedded_template`, `go`, `haskell`, `html`, `java`, `javascript`, `json`, `julia`, `kotlin`, `lua`, `nix`, `ocaml`, `php`, `python`, `regex`, `ruby`, `rust`, `scala`, `sql`, `swift`, `toml`, `tsx`, `typescript`, `verilog`, `yaml`, `zig`

Current parity-report summary:
- `parseable=33`
- `total=33`
- `unsupported=0`

**Backend types:**
- **`dfa`** — lexer fully generated from grammar tables
- **`dfa-partial`** — generated DFA with partial external-scanner coverage; runtime synthesizes remaining tokens
- **`token_source`** — hand-written pure-Go lexer bridge

**`degraded`** means the language parses and produces a tree, but the smoke parse still reports recoverable syntax errors (currently: `agda`, `c_sharp`, `elixir`, `haskell`, `swift`, `yaml`).

---

## Query API

| Feature | Status |
|---|---|
| Compile + execute (`NewQuery`, `Execute`, `ExecuteNode`) | supported |
| Cursor streaming (`Exec`, `NextMatch`, `NextCapture`) | supported |
| Structural quantifiers (`?`, `*`, `+`) | supported |
| `#eq?` / `#not-eq?` | supported |
| `#match?` | supported |
| `#any-of?` | supported |

---

## Adding a Language

**1.** Add the grammar to `grammars/languages.manifest`.

**2.** Generate bindings:

```sh
go run ./cmd/ts2go -manifest grammars/languages.manifest -outdir ./grammars -package grammars
```

**3.** Add smoke samples to `cmd/parity_report/main.go` and `grammars/parse_support_test.go`.

**4.** Verify:

```sh
go run ./cmd/parity_report
go test ./grammars/...
```

`graphql` and `hcl` have generated bindings but are missing highlight query stubs from upstream — PRs welcome.

---

## Architecture

gotreesitter reimplements the tree-sitter runtime in pure Go:

- **Parser** — table-driven LR(1) with GLR support for ambiguous grammars
- **Incremental reuse** — cursor-based subtree reuse; unchanged regions skip reparsing entirely
- **Arena allocator** — slab-based node allocation with ref counting, minimizing GC pressure
- **DFA lexer** — generated from grammar tables via `ts2go`, with hand-written bridges where needed
- **External scanner VM** — bytecode interpreter for language-specific scanning (Python indentation, etc.)
- **Query engine** — full S-expression pattern matching with predicate evaluation and streaming cursors

Grammar tables are extracted from upstream tree-sitter `parser.c` files by the `ts2go` tool and compiled into Go source. No C code runs at parse time.

---

## Roadmap

**Goal: 100+ languages.**

Most tree-sitter grammars can be added with zero hand-written code. The effort depends on which tier a grammar falls into:

| Tier | Description | Manual code | Examples |
|---|---|---|---|
| `dfa` | Lexer fully generated from grammar tables | None | `swift`, `zig` |
| `dfa-partial` | Generated DFA + synthesized scanner tokens | None (auto) | `python`, `bash`, `ruby` |
| `full` | Hand-written external scanner required | Yes | `go`, `c`, `rust` |

The `ts2go` generator handles `dfa` and `dfa-partial` grammars automatically — add the grammar URL to `languages.manifest` and run `go generate`. Most new languages land in one of these tiers.

**What's next:**

- Scanner VM improvements to cover more external-scanner patterns automatically
- Community-contributed scanners for languages that need `full` tier support
- Automated parity testing against the C tree-sitter output for every supported grammar
- Continuous expansion toward 100+ languages with each release

---

## Status

Pre-v0.1.0. The API is stabilizing. Breaking changes will be noted in releases.
