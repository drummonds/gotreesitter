//go:build cgo && treesitter_c_parity

package cgoharness

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

var unstableParityCorpusLangs = map[string]string{
	"html": "html corpus parity is under active scanner investigation",
	"yaml": "yaml corpus parity is under active scanner investigation",
}

type parityCorpusDoc struct {
	lang   string
	label  string
	source []byte
}

func parityCorpusScale() int {
	raw := strings.TrimSpace(os.Getenv("GTS_PARITY_CORPUS_SCALE"))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

func parityCorpusLangFilter() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("GTS_PARITY_CORPUS_ONLY"))
	if raw == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func includeUnstableParityCorpusLangs() bool {
	raw := strings.TrimSpace(os.Getenv("GTS_PARITY_CORPUS_INCLUDE_UNSTABLE"))
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func allowCorpusLang(filter map[string]struct{}, name string) bool {
	if filter == nil {
		return true
	}
	_, ok := filter[name]
	return ok
}

func goCorpus(funcCount int) string {
	var b strings.Builder
	b.Grow(funcCount * 72)
	b.WriteString("package p\n\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&b, "func f%d() int { v := %d; return v }\n", i, i)
	}
	return b.String()
}

func cCorpus(funcCount int) string {
	var b strings.Builder
	b.Grow(funcCount * 64)
	b.WriteString("int main(void) { return f0(); }\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&b, "int f%d(void) { return %d; }\n", i, i)
	}
	return b.String()
}

func javaCorpus(classCount int) string {
	var b strings.Builder
	b.Grow(classCount * 48)
	for i := 0; i < classCount; i++ {
		fmt.Fprintf(&b, "class C%d { int x = %d; }\n", i, i)
	}
	return b.String()
}

func htmlCorpus(divCount int) string {
	var b strings.Builder
	b.Grow(divCount * 40)
	b.WriteString("<html><body>\n")
	for i := 0; i < divCount; i++ {
		fmt.Fprintf(&b, "<div id=\"d%d\">hello %d</div>\n", i, i)
	}
	b.WriteString("</body></html>\n")
	return b.String()
}

func luaCorpus(lineCount int) string {
	var b strings.Builder
	b.Grow(lineCount * 32)
	for i := 0; i < lineCount; i++ {
		fmt.Fprintf(&b, "local x%d = %d\n", i, i)
	}
	return b.String()
}

func tomlCorpus(keyCount int) string {
	var b strings.Builder
	b.Grow(keyCount * 24)
	for i := 0; i < keyCount; i++ {
		fmt.Fprintf(&b, "k%d = %d\n", i, i)
	}
	return b.String()
}

func yamlCorpus(keyCount int) string {
	var b strings.Builder
	b.Grow(keyCount * 20)
	for i := 0; i < keyCount; i++ {
		fmt.Fprintf(&b, "k%d: %d\n", i, i)
	}
	return b.String()
}

func buildParityCorpusDocs() []parityCorpusDoc {
	scale := parityCorpusScale()
	filter := parityCorpusLangFilter()
	includeUnstable := includeUnstableParityCorpusLangs()

	docs := make([]parityCorpusDoc, 0, 32)
	add := func(lang, label string, src string) {
		if !allowCorpusLang(filter, lang) {
			return
		}
		if !includeUnstable {
			if _, unstable := unstableParityCorpusLangs[lang]; unstable {
				return
			}
		}
		docs = append(docs, parityCorpusDoc{
			lang:   lang,
			label:  label,
			source: normalizedSource(lang, src),
		})
	}

	add("go", "corpus-small", goCorpus(128*scale))
	add("go", "corpus-medium", goCorpus(512*scale))
	add("go", "corpus-large", goCorpus(2048*scale))

	add("c", "corpus-small", cCorpus(128*scale))
	add("c", "corpus-medium", cCorpus(512*scale))
	add("c", "corpus-large", cCorpus(2048*scale))

	add("java", "corpus-small", javaCorpus(128*scale))
	add("java", "corpus-medium", javaCorpus(512*scale))
	add("java", "corpus-large", javaCorpus(2048*scale))

	add("html", "corpus-small", htmlCorpus(128*scale))
	add("html", "corpus-medium", htmlCorpus(512*scale))
	add("html", "corpus-large", htmlCorpus(2048*scale))

	add("lua", "corpus-small", luaCorpus(128*scale))
	add("lua", "corpus-medium", luaCorpus(512*scale))
	add("lua", "corpus-large", luaCorpus(2048*scale))

	add("toml", "corpus-small", tomlCorpus(128*scale))
	add("toml", "corpus-medium", tomlCorpus(512*scale))
	add("toml", "corpus-large", tomlCorpus(2048*scale))

	add("yaml", "corpus-small", yamlCorpus(128*scale))
	add("yaml", "corpus-medium", yamlCorpus(512*scale))
	add("yaml", "corpus-large", yamlCorpus(2048*scale))

	return docs
}

// TestParityCorpusFreshParse runs larger generated corpora through both
// gotreesitter and the upstream C parser and compares tree structure.
func TestParityCorpusFreshParse(t *testing.T) {
	docs := buildParityCorpusDocs()
	if len(docs) == 0 {
		t.Skip("no corpus docs selected")
	}
	for _, doc := range docs {
		doc := doc
		name := fmt.Sprintf("%s/%s", doc.lang, doc.label)
		t.Run(name, func(t *testing.T) {
			if meta, ok := paritySkips[doc.lang]; ok && meta.skipReason != "" {
				t.Skipf("known mismatch: %s", meta.skipReason)
			}
			if reason, unstable := unstableParityCorpusLangs[doc.lang]; unstable && !includeUnstableParityCorpusLangs() {
				t.Skipf("unstable corpus parity disabled by default: %s", reason)
			}
			runParityCase(t, parityCase{name: doc.lang, source: string(doc.source)}, doc.label, doc.source)
		})
	}
}
