package stdlib_test

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdTextWidthLaws is the statement of each display-width law of std text
// (width.ergo), by ledger key.
var stdTextWidthLaws = map[string]string{
	"text CellsAddOverClusters": `CellsAddOverClusters(xs []int, ys []int) { let a = clusterText(xs) let b = clusterText(ys) Cells(a+b) == Cells(a)+Cells(b) }`,
	"text StylingTakesNoCells":  `StylingTakesNoCells(xs []int, k int) { let t = clusterText(xs) let open = escapes[(k%len(escapes)+len(escapes))%len(escapes)] Cells(open+t+"\x1b[0m") == Cells(t) && Cells(open+t+"\x1b]8;;\x07") == Cells(t) }`,
	"text PadIsWidth":           `PadIsWidth(xs []int, w int) { let s = clusterText(xs) let width = w % 32 let p = Pad(s, width) let want = if Cells(s) > width { Cells(s) } else { width } strings.HasPrefix(p, s) && Cells(p) == want }`,
	"text TruncateFits":         `TruncateFits(xs []int, w int) { let s = clusterText(xs) let width = (w%24 + 24) % 24 let t = Truncate(s, width, "\u2026") if Cells(s) <= width { return t == s } let head = t[:len(t)-len("\u2026")] strings.HasSuffix(t, "\u2026") && strings.HasPrefix(s, head) && (Cells(t) <= width || head == "") && Cells(head)+Cells(s[len(head):]) == Cells(s) }`,
	"text WrapFits":             `WrapFits(xs []int, w int) { let s = clusterText(xs) let width = 2 + (w%14+14)%14 let mut ok = true for line in Wrap(s, width) { ok = ok && Cells(line) <= width } ok }`,
	"text WrapLosesNoRune":      `WrapLosesNoRune(xs []int, w int) { let s = clusterText(xs) let width = (w%16 + 16) % 16 strings.ReplaceAll(strings.Join(Wrap(s, width), ""), " ", "") == strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\n", "") }`,
	"text ColumnsAlign":         `ColumnsAlign(xs []int, k int) { let n = 1 + (k%3+3)%3 let mut rows = [][]string{} let mut row = []string{} let m = if len(xs) > 9 { 9 } else { len(xs) } for i in 0..m { row = row.Append(strings.ReplaceAll(strings.ReplaceAll(clusterText([]int{xs[i], xs[i] / 7}), "\n", ""), "\t", "")) if len(row) == n { rows = rows.Append(row) row = []string{} } } let mut lead = n - 1 for j in 0..n-1 { let mut widest = 0 for r in rows { widest = if Cells(r[j]) > widest { Cells(r[j]) } else { widest } } lead = lead + widest } let lines = Columns(rows, "|") let mut ok = len(lines) == len(rows) for i, line in lines { let last = rows[i][n-1] ok = ok && strings.HasSuffix(line, last) && Cells(line)-Cells(last) == lead } ok }`,
}

// TestStdTextWidthLaws runs the display-width laws of std text in
// process: it checks the package's own files as the root package, so the
// laws read its unexported helpers (clusterText, escapes), and evaluates
// them; lawledger.Run holds that the ledger's rows are these laws, each
// with the source's statement, and that each passed.
func TestStdTextWidthLaws(t *testing.T) {
	sources, ok := stdlib.Package("text")
	if !ok {
		t.Fatal("no std package text")
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	var files []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files = append(files, f)
	}
	for key, statement := range stdTextWidthLaws {
		law := textLawSource(t, sources, strings.TrimPrefix(key, "text "))
		if !lawledger.Same(law, statement) {
			t.Errorf("%s: the source law %q is not the restated %q", key, law, statement)
		}
	}
	pkg, diags := check.Check("text", files, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results[r.Name] = r
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range stdTextWidthLaws {
		name := "law " + strings.TrimPrefix(key, "text ")
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			r, ok := results[name]
			if !ok {
				t.Fatalf("the evaluator ran no %s", name)
			}
			if r.Status != eval.Passed || r.Cases == 0 {
				t.Errorf("%s: %s after %d cases: %s", name, r.Status, r.Cases, r.Reason)
			}
		}}
	}
	lawledger.Run(t, laws)
}

// textLawSource is the text of the law name in one of the files of
// sources, from its name to its closing brace.
func textLawSource(t *testing.T, sources map[string]string, name string) string {
	t.Helper()
	for _, src := range sources {
		at := strings.Index(src, "\nlaw "+name+"(")
		if at < 0 {
			continue
		}
		at += len("\nlaw ")
		depth := 0
		for i := strings.IndexByte(src[at:], '{') + at; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return src[at : i+1]
				}
			}
		}
	}
	t.Fatalf("no law %s in std text", name)
	return ""
}
