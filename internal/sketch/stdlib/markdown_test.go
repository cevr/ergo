package stdlib_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdMarkdownLawsRun runs the laws of std markdown at check time in process
// and holds every markdown row of runners.txt to a pass.
func TestStdMarkdownLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("markdown")
	if err != nil {
		t.Fatalf("import markdown: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 100_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate markdown: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		name := strings.TrimPrefix(r.Name, "law ")
		if idx := strings.Index(name, " for "); idx > 0 {
			name = name[:idx]
		}
		results["markdown "+name] = r
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			switch {
			case !ok:
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
			case r.Status != eval.Passed || (r.Evidence != "tested" && r.Evidence != "proved"):
				t.Errorf("law %s: %s (%s, evidence=%q) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Evidence, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const markdownSpecDriverSrc = `import io
import markdown
import os
import strings

func opts(flags string) markdown.Options {
	let mut o = markdown.CommonMark()
	if strings.Contains(flags, "g") {
		o = markdown.GFM()
	}
	if strings.Contains(flags, "a") {
		o = markdown.Options{..o, Autolinks: true}
	}
	if strings.Contains(flags, "f") {
		o = markdown.Options{..o, TagFilter: true}
	}
	o
}

func main() fails os.Exit needs Console | Ambient {
	let text = io.NewStdin().ReadAll() catch {
	case io.ReadFailed as r:
		return os.Exit{Code: 1, Message: "drv: \(r.Reason)"}
	}
	let cases = strings.Split(text, "\x01")
	let mut out = strings.NewBuilder()
	for i, c in cases {
		let nl = strings.IndexByte(c, '\n')
		let flags = strings.Slice(c, 0, nl)
		let src = strings.Slice(c, nl+1, c.Len())
		let d = markdown.Parse(src, opts(flags)) catch {
		case markdown.TooLarge:
			return os.Exit{Code: 1, Message: "too large"}
		}
		if i > 0 {
			out.WriteString("\x01")
		}
		out.WriteString(markdown.ToHTML(d))
	}
	Console.Println(out.String())
}
`

type markdownSpecCase struct {
	Example   int    `json:"example"`
	Section   string `json:"section"`
	Extension string `json:"extension"`
	Markdown  string `json:"markdown"`
	HTML      string `json:"html"`
}

func loadMarkdownCorpus(t *testing.T) ([]markdownSpecCase, []markdownSpecCase) {
	t.Helper()
	root := moduleRootDir(t)
	read := func(rel string) []markdownSpecCase {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var cases []markdownSpecCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return cases
	}
	return read("internal/sketch/std/markdown/testdata/spec.json"), read("internal/sketch/std/markdown/testdata/gfm.json")
}

func buildMarkdownBatchInput(cm, gfm []markdownSpecCase) (string, []markdownSpecCase) {
	all := make([]markdownSpecCase, 0, len(cm)+len(gfm)+1)
	var parts []string
	for _, c := range cm {
		all = append(all, c)
		parts = append(parts, "c\n"+c.Markdown)
	}
	for _, c := range gfm {
		all = append(all, c)
		flags := "g"
		if c.Extension == "autolink" {
			flags += "a"
		}
		if c.Extension == "tagfilter" {
			flags += "f"
		}
		parts = append(parts, flags+"\n"+c.Markdown)
	}
	nul := markdownSpecCase{Section: "Insecure characters", Markdown: "a\x00b", HTML: "<p>a\ufffdb</p>\n"}
	all = append(all, nul)
	parts = append(parts, "c\n"+nul.Markdown)
	return strings.Join(parts, "\x01"), all
}

func verifyMarkdownOutputs(t *testing.T, stdout string, cases []markdownSpecCase) {
	t.Helper()
	outs := strings.Split(strings.TrimSuffix(stdout, "\n"), "\x01")
	if len(outs) != len(cases) {
		t.Fatalf("got %d outputs, want %d", len(outs), len(cases))
	}
	fails := 0
	for i, c := range cases {
		if outs[i] != c.HTML {
			fails++
			if fails <= 10 {
				label := c.Section
				if c.Extension != "" {
					label = c.Extension
				}
				t.Errorf("example #%d (%s):\nmd:   %q\nwant: %q\ngot:  %q", c.Example, label, c.Markdown, c.HTML, outs[i])
			}
		}
	}
	if fails > 10 {
		t.Errorf("total mismatches: %d of %d", fails, len(cases))
	}
}

// TestStdMarkdownSpec runs all 652 CommonMark 0.31.2 examples and all 24 GFM
// extension examples in process in the Ergo evaluator.
func TestStdMarkdownSpec(t *testing.T) {
	cm, gfm := loadMarkdownCorpus(t)
	input, all := buildMarkdownBatchInput(cm, gfm)
	pkg, diags := check.CheckSource(markdownSpecDriverSrc)
	if len(diags) > 0 {
		t.Fatalf("check spec driver: %v", diags)
	}
	b := eval.DefaultBudget()
	b.Steps = 500_000_000
	b.Bytes = 1 << 30
	exit, err := eval.RunMain(pkg, b, eval.Process{Stdin: input})
	if err != nil {
		t.Fatalf("eval spec driver: %v", err)
	}
	if exit.Status != 0 || exit.Skipped != "" || exit.Incomplete != "" {
		t.Fatalf("spec driver exit: status=%d skipped=%q incomplete=%q stderr=%q", exit.Status, exit.Skipped, exit.Incomplete, exit.Stderr)
	}
	verifyMarkdownOutputs(t, exit.Stdout, all)
}

const markdownNativeTestSrc = `import html
import markdown
import option
import strings
import term
import text

test MarkdownNativeSuite {
	let src = "# Title\n\n> Quote **bold** [safe](https://example.com) [bad](javascript:alert(1))\n\n- [x] done\n- [ ] todo\n\n| left | right |\n| :--- | ---: |\n| a | 42 |\n\n~~~go\nlet x = 1\n~~~\n\n<script>alert(1)</script>\n"
	let d = try markdown.Parse(src, markdown.GFM())
	assert markdown.Rows(d) > 1
	assert markdown.End(d, 0) == markdown.Rows(d)
	assert markdown.Work(d) > 0 && markdown.Work(d) <= 8*src.Len()+64

	let h = markdown.ToHTML(d)
	assert strings.Contains(h, "<h1>Title</h1>")
	assert strings.Contains(h, "<blockquote>")
	assert strings.Contains(h, "<table>")
	assert strings.Contains(h, "<input type=\"checkbox\"")

	let mut hb = strings.NewBuilder()
	markdown.WriteHTML(&hb, d)
	assert hb.String() == h

	let h2 = try markdown.HTML(src, markdown.GFM())
	assert h2 == h

	let nul = try markdown.HTML("a\x00b", markdown.CommonMark())
	assert nul == "<p>a\ufffdb</p>\n"

	let safeStr = html.Render(markdown.Safe(d))
	assert !strings.Contains(safeStr, "javascript:")
	assert !strings.Contains(safeStr, "<script>")
	assert strings.Contains(safeStr, "&lt;script&gt;")
	assert strings.Contains(safeStr, "href=\"https://example.com\"")

	let plain = with context { term.Colors: term.Never } {
		markdown.ToANSI(d, markdown.DefaultTheme())
	}
	assert strings.IndexByte(plain, 0x1b) < 0
	assert strings.Contains(plain, "Title")
	assert strings.Contains(plain, "===")
	assert strings.Contains(plain, "[x]")

	let styled = with context { term.Colors: term.Always } {
		markdown.ToANSI(d, markdown.Theme{Columns: 40, Hyperlinks: true, Light: true})
	}
	assert strings.IndexByte(styled, 0x1b) >= 0
	assert strings.Contains(styled, "\x1b]8;;https://example.com\x1b\\")
	for line in strings.Split(plain, "\n") {
		assert text.Cells(line) <= 80
	}

	let hostile = "hello \x1b[31mworld\x01\x7f\n\n~~~\ncode\x1b]0;secret\x07\n~~~\n"
	let hd = try markdown.Parse(hostile, markdown.CommonMark())
	let hPlain = with context { term.Colors: term.Never } { markdown.ToANSI(hd, markdown.DefaultTheme()) }
	let hStyled = with context { term.Colors: term.Always } { markdown.ToANSI(hd, markdown.DefaultTheme()) }
	assert strings.IndexByte(hPlain, 0x1b) < 0 && strings.IndexByte(hPlain, 0x01) < 0 && strings.IndexByte(hPlain, 0x7f) < 0
	assert !strings.Contains(hPlain, "secret")
	assert !strings.Contains(term.Strip(hStyled), "secret")

	let det = term.Decide(term.Env{NoColor: option.Some[string]{Value: "1"}, ForceColor: option.None, Term: option.Some[string]{Value: "xterm-256color"}, ColorTerm: option.None, Terminal: true})
	let off = with context { term.Colors: det } { markdown.ToANSI(d, markdown.DefaultTheme()) }
	assert off == plain

	let cm = try markdown.HTML("| a | b |\n| - | - |\n| 1 | 2 |\n", markdown.CommonMark())
	assert !strings.Contains(cm, "<table>")

	assert match markdown.View(d, 0) {
	case markdown.Document:
		true
	case _:
		false
	}
}
`

// TestStdMarkdownNative compiles and runs the CommonMark 0.31.2 (652 cases),
// GFM (24 cases), Safe, and ToANSI tests natively with ERGO_FALLBACK=off.
func TestStdMarkdownNative(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo binary")
	}
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}

	modDir := filepath.Join(tmp, "mdmod")
	drvDir := filepath.Join(modDir, "cmd", "drv")
	if err := os.MkdirAll(drvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module mdmod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drvDir, "main.ergo"), []byte(markdownSpecDriverSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "markdown_native_test.ergo"), []byte(markdownNativeTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	drvBin := filepath.Join(tmp, "bin", "md-spec-drv")
	compile := exec.Command(ergoBin, "build", "./cmd/drv", "-o", drvBin)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build spec driver (ERGO_FALLBACK=off): %v\n%s", err, out)
	}

	cm, gfm := loadMarkdownCorpus(t)
	input, all := buildMarkdownBatchInput(cm, gfm)
	runDrv := exec.Command(drvBin)
	runDrv.Stdin = strings.NewReader(input)
	runDrv.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, err := runBound(runDrv)
	if err != nil {
		t.Fatalf("run native spec driver: %v\n%s", err, out)
	}
	verifyMarkdownOutputs(t, out, all)

	testCmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
	testCmd.Dir = modDir
	testCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	testOut, err := runBound(testCmd)
	if err != nil {
		t.Fatalf("ergo test --native failed: %v\n%s", err, testOut)
	}
	if !strings.Contains(testOut, "ok MarkdownNativeSuite") {
		t.Fatalf("expected ok MarkdownNativeSuite in output:\n%s", testOut)
	}
}
