package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"codeberg.org/hum3/gotreesitter"
	"codeberg.org/hum3/gotreesitter/grammars"
)

type editSite struct {
	offset int
	start  gotreesitter.Point
	end    gotreesitter.Point
}

type report struct {
	GeneratedAtUTC   string  `json:"generated_at_utc"`
	Edits            int     `json:"edits"`
	FuncCount        int     `json:"func_count"`
	Seed             uint32  `json:"seed"`
	MeanEditPathNs   float64 `json:"mean_edit_path_ns"`
	P50EditPathNs    int64   `json:"p50_edit_path_ns"`
	P95EditPathNs    int64   `json:"p95_edit_path_ns"`
	P99EditPathNs    int64   `json:"p99_edit_path_ns"`
	MinEditPathNs    int64   `json:"min_edit_path_ns"`
	MaxEditPathNs    int64   `json:"max_edit_path_ns"`
	EditTotalNs      int64   `json:"edit_total_ns"`
	ReuseTotalNs     int64   `json:"reuse_total_ns"`
	ParseTotalNs     int64   `json:"parse_total_ns"`
	ReusedSubtrees   uint64  `json:"reused_subtrees"`
	ReusedBytes      uint64  `json:"reused_bytes"`
	NewNodes         uint64  `json:"new_nodes_allocated"`
	MaxStacksSeen    int     `json:"max_stacks_seen"`
	EntryScratchPeak uint64  `json:"entry_scratch_peak_entries"`
	EditSharePct     float64 `json:"edit_share_pct"`
	ReuseSharePct    float64 `json:"reuse_share_pct"`
	ParseSharePct    float64 `json:"parse_share_pct"`
}

func main() {
	var (
		funcCount int
		edits     int
		seed      uint
		outPath   string
	)
	flag.IntVar(&funcCount, "func-count", benchmarkFuncCount(), "number of synthetic functions in generated source")
	flag.IntVar(&edits, "edits", 10000, "number of incremental edits to apply in one process")
	flag.UintVar(&seed, "seed", 0x9e3779b9, "random seed")
	flag.StringVar(&outPath, "out", "", "optional output json path")
	flag.Parse()

	if edits <= 0 {
		fatalf("--edits must be > 0")
	}
	if funcCount <= 0 {
		fatalf("--func-count must be > 0")
	}

	src := makeGoBenchmarkSource(funcCount)
	sites := makeGoBenchmarkEditSites(src)
	if len(sites) == 0 {
		fatalf("could not find edit sites")
	}

	lang := grammars.GoLanguage()
	parser := gotreesitter.NewParser(lang)
	tree, err := parser.Parse(src)
	if err != nil {
		fatalf("initial parse failed: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		fatalf("initial parse returned nil root")
	}
	defer tree.Release()

	latency := make([]int64, 0, edits)
	var editTotalNs int64
	var reuseTotalNs int64
	var parseTotalNs int64
	var reusedSubtrees uint64
	var reusedBytes uint64
	var newNodes uint64
	maxStacksSeen := 0
	var entryScratchPeak uint64
	state := uint32(seed)
	for i := 0; i < edits; i++ {
		state = state*1664525 + 1013904223
		site := sites[int(state%uint32(len(sites)))]
		toggleDigitAt(src, site.offset)

		edit := gotreesitter.InputEdit{
			StartByte:   uint32(site.offset),
			OldEndByte:  uint32(site.offset + 1),
			NewEndByte:  uint32(site.offset + 1),
			StartPoint:  site.start,
			OldEndPoint: site.end,
			NewEndPoint: site.end,
		}
		editStart := time.Now()
		tree.Edit(edit)
		editNs := time.Since(editStart).Nanoseconds()
		editTotalNs += editNs

		old := tree
		tree, prof, err := parser.ParseIncrementalProfiled(src, tree)
		if err != nil {
			fatalf("incremental parse failed at edit %d: %v", i, err)
		}
		reuseTotalNs += prof.ReuseCursorNanos
		parseTotalNs += prof.ReparseNanos
		reusedSubtrees += prof.ReusedSubtrees
		reusedBytes += prof.ReusedBytes
		newNodes += prof.NewNodesAllocated
		if prof.MaxStacksSeen > maxStacksSeen {
			maxStacksSeen = prof.MaxStacksSeen
		}
		if prof.EntryScratchPeak > entryScratchPeak {
			entryScratchPeak = prof.EntryScratchPeak
		}
		latency = append(latency, editNs+prof.ReuseCursorNanos+prof.ReparseNanos)
		if old != tree {
			old.Release()
		}
	}
	measuredTotalNs := editTotalNs + reuseTotalNs + parseTotalNs

	out := report{
		GeneratedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		Edits:            edits,
		FuncCount:        funcCount,
		Seed:             uint32(seed),
		MeanEditPathNs:   meanInt64(latency),
		P50EditPathNs:    percentileInt64(latency, 50),
		P95EditPathNs:    percentileInt64(latency, 95),
		P99EditPathNs:    percentileInt64(latency, 99),
		MinEditPathNs:    minInt64(latency),
		MaxEditPathNs:    maxInt64(latency),
		EditTotalNs:      editTotalNs,
		ReuseTotalNs:     reuseTotalNs,
		ParseTotalNs:     parseTotalNs,
		ReusedSubtrees:   reusedSubtrees,
		ReusedBytes:      reusedBytes,
		NewNodes:         newNodes,
		MaxStacksSeen:    maxStacksSeen,
		EntryScratchPeak: entryScratchPeak,
		EditSharePct:     sharePct(editTotalNs, measuredTotalNs),
		ReuseSharePct:    sharePct(reuseTotalNs, measuredTotalNs),
		ParseSharePct:    sharePct(parseTotalNs, measuredTotalNs),
	}
	fmt.Printf(
		"STATS edits=%d edit_ns=%d reuse_ns=%d parse_ns=%d total_ns=%d reused_subtrees=%d reused_bytes=%d new_nodes=%d max_stacks=%d scratch_peak_entries=%d\n",
		edits, editTotalNs, reuseTotalNs, parseTotalNs, measuredTotalNs, reusedSubtrees, reusedBytes, newNodes, maxStacksSeen, entryScratchPeak,
	)

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatalf("marshal json: %v", err)
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			fatalf("write %s: %v", outPath, err)
		}
		fmt.Printf("perfprobe: wrote %s\n", outPath)
	}
	fmt.Println(string(data))
}

func benchmarkFuncCount() int {
	if raw := strings.TrimSpace(os.Getenv("GOT_BENCH_FUNC_COUNT")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err == nil && n > 0 {
			return n
		}
	}
	return 5000
}

func makeGoBenchmarkSource(funcCount int) []byte {
	var sb strings.Builder
	sb.Grow(funcCount * 48)
	sb.WriteString("package main\n\n")
	for i := 0; i < funcCount; i++ {
		fmt.Fprintf(&sb, "func f%d() int { v := %d; return v }\n", i, i)
	}
	return []byte(sb.String())
}

func makeGoBenchmarkEditSites(src []byte) []editSite {
	const marker = "v := "
	needle := []byte(marker)
	sites := make([]editSite, 0, 64)
	from := 0
	for from < len(src) {
		idx := bytes.Index(src[from:], needle)
		if idx < 0 {
			break
		}
		offset := from + idx + len(marker)
		if offset >= len(src) {
			break
		}
		sites = append(sites, editSite{
			offset: offset,
			start:  pointAtOffset(src, offset),
			end:    pointAtOffset(src, offset+1),
		})
		from = offset + 1
	}
	return sites
}

func pointAtOffset(src []byte, offset int) gotreesitter.Point {
	var row uint32
	var col uint32
	for i := 0; i < offset && i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		if r == '\n' {
			row++
			col = 0
		} else {
			col++
		}
		i += size
	}
	return gotreesitter.Point{Row: row, Column: col}
}

func toggleDigitAt(src []byte, offset int) {
	if offset < 0 || offset >= len(src) {
		return
	}
	if src[offset] == '0' {
		src[offset] = '1'
		return
	}
	src[offset] = '0'
}

func meanInt64(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

func percentileInt64(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	ys := make([]int64, len(xs))
	copy(ys, xs)
	sort.Slice(ys, func(i, j int) bool { return ys[i] < ys[j] })
	if len(ys) == 1 {
		return ys[0]
	}
	rank := (p / 100.0) * float64(len(ys)-1)
	idx := int(rank + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ys) {
		idx = len(ys) - 1
	}
	return ys[idx]
}

func minInt64(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	min := xs[0]
	for _, x := range xs[1:] {
		if x < min {
			min = x
		}
	}
	return min
}

func maxInt64(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	max := xs[0]
	for _, x := range xs[1:] {
		if x > max {
			max = x
		}
	}
	return max
}

func sharePct(part, total int64) float64 {
	if total <= 0 || part <= 0 {
		return 0
	}
	return (float64(part) * 100.0) / float64(total)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
