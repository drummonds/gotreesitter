# Changelog

All notable changes to this project are documented in this file.

This project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
for tags and release notes while still in `0.x`.
## [0.6.7] - 2026-03-24

 - Update yabnf grammar to v0.1.2 and fix CHANGELOG

## [0.6.6] - 2026-03-23

- Changing to codeberg
- Adding yabnf grammar

## [0.6.5] - 2026-03-23

- Changing to codeberg
- Updating gitignore

## [0.6.4] - 2026-03-16

- Fixing issues 2 and 3

## [0.6.3] - 2026-03-15

- Fixing --index bug in docs dir
- Updating goluca grammar
- Add ABNF grammar
- Updating license

## [0.6.2] - 2026-03-15

- Updating linter
- Prune unused tree functions
- Add docs build pipeline and statichost deployment config
- Add task-plus.yml, links section, update gitignore

## [0.6.1] - 2026-03-04

- Add beancount highlight query and file extension to registry
- Add goluca and pta grammars, update module path
- Prune unused parser functions

## [0.6.0] - 2026-03-01

### Added
- `ParseWith` functional options API (`WithOldTree`, `WithTokenSource`, `WithProfiling`) and `ParseResult`.
- Parser runtime diagnostics surfaced on `Tree` (`ParseRuntime`, stop-reason/truncation metadata).
- Top-50 grammar smoke correctness gate and expanded cgo parity suites (fresh parse, no-error corpus checks, issue repros, GLR canary).
- Grammar lock update automation (`cmd/grammar_updater` + CI workflow integration).
- Configurable injection parser nesting depth.

### Changed
- Full-parse GLR behavior tuned for correctness-first performance:
  - lower default global GLR stack cap with better top-K retention behavior,
  - improved merge/pruning hot paths and profiling counters,
  - benchmark harness tightened to avoid truncated-parse results.
- Significant parser/query maintainability refactors:
  - parser/query monoliths split into focused files (`parser_*`, `query_compile_*`).
- README benchmark and gate documentation refreshed to match current numbers and commands.

### Fixed
- Multiple parity/correctness regressions in HTML/YAML/disassembly paths and grammar support wiring.
- Query predicate parsing and generated query edge cases.
- Rewriter multi-edit coordinate handling and parser profile availability signaling.

## [0.5.2] - 2026-02-24

### Fixed
- Simplified asm register-label query pattern fix in bundled grammar queries.

## [0.5.1] - 2026-02-24

### Fixed
- Corrected tree-sitter query node types in bundled grammar queries.

## [0.4.0] - 2026-02-24

### Fixed
- Parser span-calculation correctness fixes.
- `ts2go` GOTO/action detection fixes.

## [0.3.0] - 2026-02-23

### Added
- Benchmark suite for parser/query/highlighter/tagger paths.
- Fuzzing targets and stress-test coverage.

## [0.2.0] - 2026-02-23

### Added
- Broad grammar expansion with external-scanner support across 80+ grammars.

## [0.1.0] - 2026-02-19

### Added
- Initial standalone pure-Go runtime module.
- External scanner VM foundation and base parser/lexer/tree infrastructure.

[0.6.6]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.5...v0.6.6
[0.6.5]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.4...v0.6.5
[0.6.4]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.3...v0.6.4
[0.6.3]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.2...v0.6.3
[0.6.2]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.1...v0.6.2
[0.6.1]: https://codeberg.org/hum3/gotreesitter/compare/v0.6.0...v0.6.1
[0.6.0]: https://codeberg.org/hum3/gotreesitter/compare/v0.5.2...v0.6.0
[0.5.2]: https://codeberg.org/hum3/gotreesitter/compare/v0.5.1...v0.5.2
[0.5.1]: https://codeberg.org/hum3/gotreesitter/compare/v0.4.0...v0.5.1
[0.4.0]: https://codeberg.org/hum3/gotreesitter/compare/v0.3.0...v0.4.0
[0.3.0]: https://codeberg.org/hum3/gotreesitter/compare/v0.2.0...v0.3.0
[0.2.0]: https://codeberg.org/hum3/gotreesitter/compare/v0.1.0...v0.2.0
