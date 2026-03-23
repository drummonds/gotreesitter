//go:build cgo && treesitter_c_parity

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gotreesitter "codeberg.org/hum3/gotreesitter"
	cgoharness "codeberg.org/hum3/gotreesitter/cgo_harness"
	"codeberg.org/hum3/gotreesitter/grammars"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

var topParityLanguages = []string{
	"go",
	"javascript",
	"typescript",
	"python",
	"rust",
	"c",
	"cpp",
	"java",
	"json",
	"yaml",
	"html",
	"css",
	"markdown",
}

type parityResult struct {
	Language      string                       `json:"language"`
	FileID        string                       `json:"file_id"`
	FilePath      string                       `json:"file_path"`
	SourceSHA256  string                       `json:"source_sha256"`
	DumpVersion   string                       `json:"dump_version"`
	Pass          bool                         `json:"pass"`
	Category      string                       `json:"category,omitempty"`
	FirstMismatch *cgoharness.DumpV1Divergence `json:"first_divergence,omitempty"`
	GoDumpPath    string                       `json:"go_dump_path,omitempty"`
	CDumpPath     string                       `json:"c_dump_path,omitempty"`
	Error         string                       `json:"error,omitempty"`
}

type languageRunner struct {
	name     string
	entry    grammars.LangEntry
	support  grammars.ParseSupport
	goLang   *gotreesitter.Language
	goParser *gotreesitter.Parser
	cParser  *sitter.Parser
}

type score struct {
	total int
	pass  int
}

func main() {
	var (
		langFlag     string
		corpusFlag   string
		outFlag      string
		artifactFlag string
		scoreboardMD string
		oracleFlag   string
	)

	flag.StringVar(&langFlag, "lang", "top10", "language name, comma list, or top10")
	flag.StringVar(&corpusFlag, "corpus", "", "corpus root directory")
	flag.StringVar(&outFlag, "out", "parity_out/results.jsonl", "JSONL output path")
	flag.StringVar(&artifactFlag, "artifact-dir", "parity_out/dump_v1", "directory for dump.v1 artifacts")
	flag.StringVar(&scoreboardMD, "scoreboard", "PARITY.md", "scoreboard markdown output path")
	flag.StringVar(&oracleFlag, "oracle", "c", "oracle runtime (only 'c' is supported)")
	flag.Parse()

	if corpusFlag == "" {
		fatalf("--corpus is required")
	}
	if oracleFlag != "c" {
		fatalf("--oracle=%s is not supported; use --oracle c", oracleFlag)
	}

	langs := parseLangs(langFlag)
	if len(langs) == 0 {
		fatalf("no languages selected")
	}

	entriesByName := make(map[string]grammars.LangEntry)
	for _, entry := range grammars.AllLanguages() {
		entriesByName[entry.Name] = entry
	}
	supportByName := make(map[string]grammars.ParseSupport)
	for _, report := range grammars.AuditParseSupport() {
		supportByName[report.Name] = report
	}

	if err := os.MkdirAll(filepath.Dir(outFlag), 0o755); err != nil {
		fatalf("create out dir: %v", err)
	}
	if err := os.MkdirAll(artifactFlag, 0o755); err != nil {
		fatalf("create artifact dir: %v", err)
	}

	outFile, err := os.Create(outFlag)
	if err != nil {
		fatalf("create %s: %v", outFlag, err)
	}
	defer outFile.Close()
	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)

	scores := make(map[string]score, len(langs))
	seenFiles := 0

	for _, lang := range langs {
		runner, err := buildRunner(lang, entriesByName, supportByName)
		if err != nil {
			fatalf("init %s runner: %v", lang, err)
		}
		files, root, err := collectFilesForLanguage(corpusFlag, lang, len(langs) == 1)
		if err != nil {
			fatalf("%s corpus: %v", lang, err)
		}
		if len(files) == 0 {
			fmt.Fprintf(os.Stderr, "[%s] no files under %s\n", lang, root)
			continue
		}

		langArtifacts := filepath.Join(artifactFlag, lang)
		if err := os.MkdirAll(langArtifacts, 0o755); err != nil {
			fatalf("create artifact lang dir %s: %v", langArtifacts, err)
		}

		for _, abs := range files {
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				rel = filepath.Base(abs)
			}
			src, err := os.ReadFile(abs)
			if err != nil {
				res := parityResult{
					Language:    lang,
					FileID:      rel,
					FilePath:    abs,
					DumpVersion: cgoharness.DumpV1Version,
					Pass:        false,
					Category:    "io",
					Error:       err.Error(),
				}
				_ = enc.Encode(res)
				updateScore(scores, lang, false)
				continue
			}

			result := runFileParity(runner, langArtifacts, abs, rel, src)
			if err := enc.Encode(result); err != nil {
				fatalf("write jsonl for %s: %v", abs, err)
			}
			updateScore(scores, lang, result.Pass)
			seenFiles++
		}
		runner.Close()
	}

	if err := writer.Flush(); err != nil {
		fatalf("flush %s: %v", outFlag, err)
	}
	if err := writeScoreboard(scoreboardMD, scores); err != nil {
		fatalf("write scoreboard: %v", err)
	}

	fmt.Printf("wrote %d results to %s\n", seenFiles, outFlag)
	fmt.Printf("updated scoreboard: %s\n", scoreboardMD)
}

func parseLangs(raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" || value == "top10" || value == "top" {
		out := make([]string, len(topParityLanguages))
		copy(out, topParityLanguages)
		return out
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func buildRunner(lang string, entries map[string]grammars.LangEntry, support map[string]grammars.ParseSupport) (*languageRunner, error) {
	entry, ok := entries[lang]
	if !ok {
		return nil, fmt.Errorf("language %q is not in grammars registry", lang)
	}
	report, ok := support[lang]
	if !ok {
		return nil, fmt.Errorf("language %q has no parse support report", lang)
	}
	if report.Backend == grammars.ParseBackendUnsupported {
		return nil, fmt.Errorf("language %q parse backend is unsupported", lang)
	}
	goLang := entry.Language()
	goParser := gotreesitter.NewParser(goLang)
	cLang, err := cgoharness.ParityCLanguage(lang)
	if err != nil {
		return nil, fmt.Errorf("load C oracle language: %w", err)
	}
	cParser := sitter.NewParser()
	if err := cParser.SetLanguage(cLang); err != nil {
		cParser.Close()
		return nil, fmt.Errorf("set C language: %w", err)
	}
	return &languageRunner{
		name:     lang,
		entry:    entry,
		support:  report,
		goLang:   goLang,
		goParser: goParser,
		cParser:  cParser,
	}, nil
}

func (r *languageRunner) Close() {
	if r == nil || r.cParser == nil {
		return
	}
	r.cParser.Close()
}

func collectFilesForLanguage(corpusRoot, lang string, allowRawRoot bool) ([]string, string, error) {
	root := filepath.Join(corpusRoot, lang)
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		return collectFiles(root)
	}
	if allowRawRoot {
		return collectFiles(corpusRoot)
	}
	return nil, "", fmt.Errorf("missing language directory %s", root)
}

func collectFiles(root string) ([]string, string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return []string{root}, filepath.Dir(root), nil
	}

	files := make([]string, 0, 128)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Strings(files)
	return files, root, nil
}

func runFileParity(runner *languageRunner, artifactDir, absPath, fileID string, src []byte) parityResult {
	hash := sha256.Sum256(src)
	sourceHash := hex.EncodeToString(hash[:])

	res := parityResult{
		Language:     runner.name,
		FileID:       fileID,
		FilePath:     absPath,
		SourceSHA256: sourceHash,
		DumpVersion:  cgoharness.DumpV1Version,
	}

	goTree, goLang, err := parseWithGoRunner(runner, src)
	if err != nil {
		res.Pass = false
		res.Category = "go_parse"
		res.Error = err.Error()
		return res
	}
	if goTree == nil || goTree.RootNode() == nil {
		res.Pass = false
		res.Category = "go_parse"
		res.Error = "gotreesitter returned nil tree"
		return res
	}

	cTree := runner.cParser.Parse(src, nil)
	if cTree == nil || cTree.RootNode() == nil {
		res.Pass = false
		res.Category = "c_parse"
		res.Error = "C oracle returned nil tree"
		return res
	}
	defer cTree.Close()

	goDump := cgoharness.DumpV1FromGo(goTree.RootNode(), goLang)
	cDump := cgoharness.DumpV1FromC(cTree.RootNode())

	safeID := safeArtifactID(fileID)
	goDumpPath := filepath.Join(artifactDir, safeID+".go.dump.v1.json")
	cDumpPath := filepath.Join(artifactDir, safeID+".c.dump.v1.json")

	if err := writeJSON(goDumpPath, goDump); err != nil {
		res.Pass = false
		res.Category = "artifact"
		res.Error = fmt.Sprintf("write %s: %v", goDumpPath, err)
		return res
	}
	if err := writeJSON(cDumpPath, cDump); err != nil {
		res.Pass = false
		res.Category = "artifact"
		res.Error = fmt.Sprintf("write %s: %v", cDumpPath, err)
		return res
	}
	res.GoDumpPath = goDumpPath
	res.CDumpPath = cDumpPath

	diff := cgoharness.FirstDivergenceDumpV1(goTree.RootNode(), goLang, cTree.RootNode())
	if diff != nil {
		res.Pass = false
		res.Category = diff.Category
		res.FirstMismatch = diff
		return res
	}

	res.Pass = true
	return res
}

func parseWithGoRunner(runner *languageRunner, src []byte) (*gotreesitter.Tree, *gotreesitter.Language, error) {
	switch runner.support.Backend {
	case grammars.ParseBackendTokenSource:
		if runner.entry.TokenSourceFactory == nil {
			return nil, nil, fmt.Errorf("token_source backend without TokenSourceFactory")
		}
		tree, err := runner.goParser.ParseWithTokenSource(src, runner.entry.TokenSourceFactory(src, runner.goLang))
		return tree, runner.goLang, err
	case grammars.ParseBackendDFA, grammars.ParseBackendDFAPartial:
		tree, err := runner.goParser.Parse(src)
		return tree, runner.goLang, err
	default:
		return nil, nil, fmt.Errorf("unsupported backend %q", runner.support.Backend)
	}
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func safeArtifactID(fileID string) string {
	s := strings.ReplaceAll(fileID, string(os.PathSeparator), "__")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}

func updateScore(scores map[string]score, lang string, pass bool) {
	s := scores[lang]
	s.total++
	if pass {
		s.pass++
	}
	scores[lang] = s
}

func writeScoreboard(path string, scores map[string]score) error {
	langs := make([]string, 0, len(scores))
	for lang := range scores {
		langs = append(langs, lang)
	}
	sort.Strings(langs)

	totalPass := 0
	total := 0
	var b strings.Builder
	b.WriteString("# PARITY\n\n")
	b.WriteString(fmt.Sprintf("_Generated: %s_\n\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString("| Language | Fresh Parse Parity |\n")
	b.WriteString("| --- | --- |\n")
	for _, lang := range langs {
		s := scores[lang]
		totalPass += s.pass
		total += s.total
		b.WriteString(fmt.Sprintf("| %s | %d/%d |\n", lang, s.pass, s.total))
	}
	b.WriteString(fmt.Sprintf("| **TOTAL** | **%d/%d** |\n", totalPass, total))
	b.WriteString("\n")
	b.WriteString("- Dump artifact: `dump.v1`\n")
	b.WriteString("- Incremental parity scoreboard: pending integration in this command\n")
	b.WriteString("- Query parity scoreboard: pending integration in this command\n")

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
