package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFixLenProperty is a seeded property test of the len(e) -> e.Len() pass
// (NO-PRELUDE §6, §9 row 4, Risk (b)). Each seed generates an Ergo program
// combining:
//   - simple operands (ident, string literal, selector, index, slice, call,
//     already-parenthesised, interpolated string) that must NOT get redundant
//     parentheses;
//   - lower-precedence operands (*p, binary +, conversion string(bs), if,
//     match, try, composite literal including in an if header) that MUST be
//     parenthesised;
//   - surrounding precedence contexts (-len(x), -len(*p), len(a) - 1,
//     0..len(xs), "\(len(f(x)))", "\(len(*p))", "\(len(a + b))");
//   - nested len calls (len(xs[0:len(xs)-1]), len("n=\(len(xs))"),
//     len(len(xs).Tag())), multiline calls with trailing commas;
//   - all four built types ([]T, Map[K, V], string, [N]T) and compile-time
//     const len expressions;
//   - local and package-level user `len` shadows (PRELUDE SHADOW), which must
//     remain untouched.
//
// For every seed:
//  1. Before and after sources check with 0 diagnostics.
//  2. The fixed source formats to a fixpoint (format.Source(fixed) == fixed).
//  3. Compile-time constant len values and evaluated stdout are unchanged.
//  4. Running fix.LenSource a second time is a no-op (idempotent).
//  5. Shadowed `len(...)` calls are never rewritten.
//
// Replay a single seed with ERGO_SEED=<n>.
func TestFixLenProperty(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(30) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1e4f1a))
		src, wantExact, wantAbsent := genLenProgram(r)
		fail := func(msg string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestFixLenProperty$' ./internal/sketch/fix\nsource:\n%s",
				seed, fmt.Sprintf(msg, args...), seed, src)
		}
		if formatted, err := format.Source(src); err != nil || formatted != src {
			fail("generated source is not formatted (%v):\n%s", err, formatted)
		}
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			fail("generated source does not check: %v", beforeDiags)
		}
		beforeConsts := constValues(beforePkg)
		if len(beforeConsts) != 3 {
			fail("expected 3 folded const len values, got %v", beforeConsts)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			fail("eval before fix: %v", err)
		}

		fixed, rewrites, err := fix.LenSource(src, "gen.ergo")
		if err != nil {
			fail("LenSource: %v", err)
		}
		if len(rewrites) == 0 {
			fail("expected len rewrites, got 0")
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			fail("fixed source does not check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		if formatted, err := format.Source(fixed); err != nil || formatted != fixed {
			fail("fixed source is not a formatter fixpoint (%v):\nfixed:\n%s\nformatted:\n%s", err, fixed, formatted)
		}
		afterConsts := constValues(afterPkg)
		for name, want := range beforeConsts {
			if got, ok := afterConsts[name]; !ok || got != want {
				fail("const %s changed: before %q, after %q\nfixed:\n%s", name, want, got, fixed)
			}
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			fail("eval after fix: %v\nfixed:\n%s", err, fixed)
		}
		if afterOut.Stdout != beforeOut.Stdout {
			fail("eval stdout changed:\nbefore: %q\nafter:  %q\nfixed:\n%s", beforeOut.Stdout, afterOut.Stdout, fixed)
		}
		for _, sub := range wantExact {
			if !strings.Contains(fixed, sub) {
				fail("fixed source missing expected spelling %q\nfixed:\n%s", sub, fixed)
			}
		}
		for _, sub := range wantAbsent {
			if strings.Contains(fixed, sub) {
				fail("fixed source must not contain %q\nfixed:\n%s", sub, fixed)
			}
		}
		again, more, err := fix.LenSource(fixed, "gen.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			fail("second LenSource is not a no-op: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	}

	t.Run("package shadow untouched", func(t *testing.T) {
		const src = `func len(x int) int { x + 1 }

func main() needs Console {
	Console.Println("\(len(41))")
}
`
		fixed, rewrites, err := fix.LenSource(src, "shadow.ergo")
		if err != nil {
			t.Fatalf("LenSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("package-level len shadow must stay untouched: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
		if ds := sketchfront.Check(fixed).Diagnostics; len(ds) > 0 {
			t.Fatalf("shadowed source must check clean: %v", ds)
		}
	})

	t.Run("comments inside len preserved", func(t *testing.T) {
		const src = `func count(xs []int) int {
	len( // keep note
		xs,
	)
}
`
		fixed, rewrites, err := fix.LenSource(src, "comment.ergo")
		if err != nil {
			t.Fatalf("LenSource: %v", err)
		}
		if len(rewrites) != 1 {
			t.Fatalf("expected 1 rewrite, got %d", len(rewrites))
		}
		if !strings.Contains(fixed, "// keep note") || !strings.Contains(fixed, ").Len()") {
			t.Fatalf("comment inside len was not preserved cleanly:\n%s", fixed)
		}
		if ds := sketchfront.Check(fixed).Diagnostics; len(ds) > 0 {
			t.Fatalf("rewritten comment source does not check: %v\n%s", ds, fixed)
		}
		if formatted, err := format.Source(fixed); err != nil {
			t.Fatalf("format: %v\n%s", err, fixed)
		} else if again, err := format.Source(formatted); err != nil || again != formatted {
			t.Fatalf("format is not a fixpoint: %v", err)
		}
	})

	t.Run("RunLen writes package and is idempotent", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module lenpkg\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		const src = `func Count(xs []int, p mut []int) int {
	len(xs) + len(*p)
}
`
		file := filepath.Join(dir, "count.ergo")
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		dry, err := fix.RunLen(fix.Options{Paths: []string{dir}, Write: false})
		if err != nil || len(dry.Errs) != 0 || len(dry.Rewrites) != 2 {
			t.Fatalf("dry RunLen: err=%v errs=%v rewrites=%d", err, dry.Errs, len(dry.Rewrites))
		}
		if onDisk, _ := os.ReadFile(file); string(onDisk) != src {
			t.Fatalf("dry RunLen modified disk:\n%s", onDisk)
		}
		wrote, err := fix.RunLen(fix.Options{Paths: []string{dir}, Write: true})
		if err != nil || len(wrote.Errs) != 0 || len(wrote.Rewrites) != 2 {
			t.Fatalf("write RunLen: err=%v errs=%v rewrites=%d", err, wrote.Errs, len(wrote.Rewrites))
		}
		got, _ := os.ReadFile(file)
		if !strings.Contains(string(got), "xs.Len() + (*p).Len()") {
			t.Fatalf("unexpected written output:\n%s", got)
		}
		again, err := fix.RunLen(fix.Options{Paths: []string{dir}, Write: false})
		if err != nil || len(again.Errs) != 0 || len(again.Rewrites) != 0 || len(again.Files) != 0 {
			t.Fatalf("second RunLen must be a no-op: err=%v errs=%v rewrites=%d", err, again.Errs, len(again.Rewrites))
		}
	})
}

func constValues(pkg *check.Package) map[string]string {
	out := map[string]string{}
	if pkg == nil || pkg.Info == nil {
		return out
	}
	want := map[string]bool{"ConstLit": true, "ConstConcat": true, "ConstArr": true}
	for _, f := range pkg.Files() {
		for _, d := range f.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, spec := range vd.Specs {
				if len(spec.Names) == 1 && len(spec.Values) == 1 && want[spec.Names[0].Name] {
					if v, ok := pkg.Info.Values[spec.Values[0]]; ok && v != nil {
						out[spec.Names[0].Name] = v.ExactString()
					}
				}
			}
		}
	}
	return out
}

func genLenProgram(r *rand.Rand) (src string, wantExact, wantAbsent []string) {
	words := []string{"a", "hi", "ergo", "hello", "abcdef", "01234567"}
	w1 := words[r.IntN(len(words))]
	w2 := words[r.IntN(len(words))]
	flag := r.IntN(2) == 0
	k := 2 + r.IntN(3)
	var nums []string
	for i := range k {
		nums = append(nums, fmt.Sprintf("%d", i+1+r.IntN(5)))
	}
	sliceLit := "[]int{" + strings.Join(nums, ", ") + "}"
	flagLit := "false"
	if flag {
		flagLit = "true"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "let ConstLit = len(%q)\n", w1)
	fmt.Fprintf(&b, "let ConstConcat = len(%q + %q)\n", w1, w2)
	b.WriteString("let ConstArr = len([3]int{1, 2, 3})\n\n")
	b.WriteString("type Box { items []int }\n\n")
	b.WriteString("type Boom {}\n\n")
	b.WriteString("type Label string\n\n")
	b.WriteString("type Tagger {\n\tTag() string,\n}\n\n")
	b.WriteString("func makeSlice(n int) []int {\n\tif n > 0 {\n\t\t[]int{1, 2, 3}\n\t} else {\n\t\t[]int{1}\n\t}\n}\n\n")
	b.WriteString("func fallible(ok bool) string fails Boom {\n\tif !ok {\n\t\treturn Boom\n\t}\n\t\"ok\"\n}\n\n")
	b.WriteString("func Tag(n int) string implements Tagger { \"t\\(n)\" }\n\n")
	b.WriteString("func fromBorrow(p mut []int) int {\n")
	b.WriteString("\tlet a = len(*p)\n")
	b.WriteString("\tlet b = -len(*p)\n")
	b.WriteString("\tlet s = \"\\(len(*p))\"\n")
	b.WriteString("\ta + b + len(s)\n")
	b.WriteString("}\n\n")
	b.WriteString("func withLocalShadow(len func(x int) int, xs []int) int {\n")
	b.WriteString("\tlen(41) + xs.Len()\n")
	b.WriteString("}\n\n")
	b.WriteString("func run(flag bool) string fails Boom {\n")
	fmt.Fprintf(&b, "\tlet mut xs = %s\n", sliceLit)
	b.WriteString("\tlet box = Box{items: xs}\n")
	b.WriteString("\tlet grid = [][]int{xs}\n")
	fmt.Fprintf(&b, "\tlet a = %q\n", w1)
	fmt.Fprintf(&b, "\tlet b = %q\n", w2)
	b.WriteString("\tlet lbl = Label(\"abc\")\n")
	b.WriteString("\tlet arr = [3]int{7, 8, 9}\n")
	b.WriteString("\tlet m = Map[string, int]{\"x\": 1, \"y\": 2}\n")
	b.WriteString("\tlet r1 = len(xs) - 1\n")
	b.WriteString("\tlet r2 = -len(xs)\n")
	b.WriteString("\tlet r3 = len(\"abc\") + len(box.items) + len(grid[0]) + len(xs[0:1])\n")
	b.WriteString("\tlet r4 = len(makeSlice(1)) + len((a + b)) + len(\"hi \\(r1)\")\n")
	b.WriteString("\tlet r5 = len(a+b) + len(string(lbl)) + len(arr) + len(m)\n")
	b.WriteString("\tlet r6 = len(if flag {\n\t\ta\n\t} else {\n\t\tb\n\t})\n")
	b.WriteString("\tlet r7 = len(match flag {\n\tcase true:\n\t\ta\n\tcase false:\n\t\tb\n\t})\n")
	b.WriteString("\tlet r8 = len(try fallible(true)) + len([]int{1, 2})\n")
	b.WriteString("\tlet r9 = len(xs[0:len(xs)-1]) + len(\"n=\\(len(xs))\") + len(len(xs).Tag())\n")
	b.WriteString("\tlet mut r10 = 0\n")
	b.WriteString("\tfor i in 0..len(xs) {\n\t\tr10 = r10 + i\n\t}\n")
	b.WriteString("\tif len(Map[string, int]{\"k\": 1}) > 0 {\n\t\tr10 = r10 + 1\n\t}\n")
	b.WriteString("\tlet r11 = fromBorrow(&xs)\n")
	b.WriteString("\tlet r12 = withLocalShadow(func(x int) int { x + 1 }, xs)\n")
	b.WriteString("\t\"\\(r1) \\(r2) \\(r3) \\(r4) \\(r5) \\(r6) \\(r7) \\(r8) \\(r9) \\(r10) \\(r11) \\(r12) \\(len(makeSlice(0))) \\(len(a + b))\"\n")
	b.WriteString("}\n\n")
	b.WriteString("func main() needs Console {\n")
	fmt.Fprintf(&b, "\tlet out = run(%s) catch {\n\tcase Boom:\n\t\t\"boom\"\n\t}\n", flagLit)
	b.WriteString("\tConsole.Println(\"\\(ConstLit) \\(ConstConcat) \\(ConstArr) \\(out)\")\n")
	b.WriteString("}\n")

	wantExact = []string{
		fmt.Sprintf("let ConstLit = %q.Len()", w1),
		fmt.Sprintf("let ConstConcat = (%q + %q).Len()", w1, w2),
		"let ConstArr = ([3]int{1, 2, 3}).Len()",
		"let a = (*p).Len()",
		"let b = -(*p).Len()",
		`let s = "\((*p).Len())"`,
		"a + b + s.Len()",
		"len(41) + xs.Len()",
		"let r1 = xs.Len() - 1",
		"let r2 = -xs.Len()",
		`let r3 = "abc".Len() + box.items.Len() + grid[0].Len() + xs[0:1].Len()`,
		`let r4 = makeSlice(1).Len() + (a + b).Len() + "hi \(r1)".Len()`,
		"let r5 = (a + b).Len() + (string(lbl)).Len() + arr.Len() + m.Len()",
		"let r8 = (try fallible(true)).Len() + ([]int{1, 2}).Len()",
		`let r9 = xs[0:xs.Len()-1].Len() + "n=\(xs.Len())".Len() + xs.Len().Tag().Len()`,
		"for i in 0..xs.Len() {",
		`if (Map[string, int]{"k": 1}).Len() > 0 {`,
		`\(makeSlice(0).Len()) \((a + b).Len())`,
	}
	wantAbsent = []string{
		"(xs).Len()",
		`("abc").Len()`,
		"(box.items).Len()",
		"(grid[0]).Len()",
		"(makeSlice(1)).Len()",
		`("hi \(r1)").Len()`,
		"((a + b)).Len()",
		"41.Len()",
	}
	return b.String(), wantExact, wantAbsent
}
