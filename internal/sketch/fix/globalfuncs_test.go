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
	"ergo.local/prototype/internal/sketchfront"
)

// TestFixGlobalFuncsProperty is a seeded property test of the --global-funcs
// pass (NO-PRELUDE §3.2, Unit 5a) covering all 10 global functions (11 names):
//   - hash -> maps.Hash
//   - encode -> codec.Encode
//   - decode[T] -> codec.Decode[T]
//   - embedded[T] -> embed.File[T]
//   - MakeSlice[T] -> slices.WithCapacity[T]
//   - MakeMap[K, V, H] -> maps.Using[K, V, H]
//   - InForce[R] -> scope.InForce[R]
//   - AsWritten[T] -> proof.AsWritten[T]
//   - TestFolder / TestFolderHandle -> fstest.TestFolder / fstest.TestFolderHandle
//   - NewLiveScheduler -> time.NewLiveScheduler
//
// Each seed varies existing imports (none, pre-existing plain imports, aliased
// imports), exercises generic type arguments, checks user shadows (local and
// package-level), verifies 0 diagnostics before & after, formatter fixpoint,
// idempotence, and identical evaluated stdout.
func TestFixGlobalFuncsProperty(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(24) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x910ba1))
		src, wantExact, wantAbsent := genGlobalFuncsProgram(r)
		fail := func(msg string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestFixGlobalFuncsProperty$' ./internal/sketch/fix\nsource:\n%s",
				seed, fmt.Sprintf(msg, args...), seed, src)
		}
		if formatted, err := format.Source(src); err != nil || formatted != src {
			fail("generated source is not formatted (%v):\n%s", err, formatted)
		}
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			fail("generated source does not check: %v", beforeDiags)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			fail("eval before fix: %v", err)
		}

		fixed, rewrites, err := fix.GlobalFuncsSource(src, "gen.ergo")
		if err != nil {
			fail("GlobalFuncsSource: %v", err)
		}
		if len(rewrites) == 0 {
			fail("expected global-func rewrites, got 0")
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			fail("fixed source does not check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		if formatted, err := format.Source(fixed); err != nil || formatted != fixed {
			fail("fixed source is not a formatter fixpoint (%v):\nfixed:\n%s\nformatted:\n%s", err, fixed, formatted)
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
		// Ensure no duplicate import lines exist.
		seenImp := map[string]bool{}
		for _, line := range strings.Split(fixed, "\n") {
			if strings.HasPrefix(line, "import ") {
				if seenImp[line] {
					fail("duplicate import line %q\nfixed:\n%s", line, fixed)
				}
				seenImp[line] = true
			}
		}
		again, more, err := fix.GlobalFuncsSource(fixed, "gen.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			fail("second GlobalFuncsSource is not a no-op: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	}

	t.Run("embedded package rewrite", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module embpkg\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		const src = `let msg = embedded[string]("hello.txt")

func Msg() string { msg }
`
		file := filepath.Join(dir, "emb.ergo")
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		rep, err := fix.RunGlobalFuncs(fix.Options{Paths: []string{dir}, Write: true})
		if err != nil || len(rep.Errs) != 0 || len(rep.Rewrites) != 1 {
			t.Fatalf("RunGlobalFuncs embed: err=%v errs=%v rewrites=%d", err, rep.Errs, len(rep.Rewrites))
		}
		got, _ := os.ReadFile(file)
		const want = "import embed\n\nlet msg = embed.File[string](\"hello.txt\")\n\nfunc Msg() string { msg }\n"
		if string(got) != want {
			t.Fatalf("embed rewrite:\ngot:\n%s\nwant:\n%s", got, want)
		}
		a, err := sketchfront.CheckDir(dir)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("rewritten embed package must check cleanly: %v %v", err, a.Diagnostics)
		}
		again, err := fix.RunGlobalFuncs(fix.Options{Paths: []string{dir}, Write: false})
		if err != nil || len(again.Errs) != 0 || len(again.Rewrites) != 0 {
			t.Fatalf("second RunGlobalFuncs must be a no-op: err=%v errs=%v rewrites=%d", err, again.Errs, len(again.Rewrites))
		}
	})

	t.Run("aliased import reused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module aliaspkg\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "maps"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "maps", "maps.ergo"), []byte("func Helper() int { 1 }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		const src = `import aliaspkg/maps
import stdmaps maps

func Main(s string) uint64 {
	let _ = maps.Helper()
	let _ = stdmaps.Clone(Map[string, int]{"a": 1})
	hash(s)
}
`
		file := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		rep, err := fix.RunGlobalFuncs(fix.Options{Paths: []string{dir}, Write: true})
		if err != nil || len(rep.Errs) != 0 || len(rep.Rewrites) != 1 {
			t.Fatalf("RunGlobalFuncs alias: err=%v errs=%v rewrites=%d", err, rep.Errs, len(rep.Rewrites))
		}
		got, _ := os.ReadFile(file)
		if !strings.Contains(string(got), "stdmaps.Hash(s)") || strings.Contains(string(got), "\nimport maps\n") {
			t.Fatalf("aliased import must be reused without duplicating import maps:\n%s", got)
		}
		a, err := sketchfront.CheckDir(dir)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("rewritten alias package must check cleanly: %v %v", err, a.Diagnostics)
		}
	})

	t.Run("package shadow untouched", func(t *testing.T) {
		const src = `func hash(x int) int { x + 7 }

func MakeSlice(n int) int { n * 2 }

func main() needs Console {
	Console.Println("\(hash(5)) \(MakeSlice(3))")
}
`
		fixed, rewrites, err := fix.GlobalFuncsSource(src, "shadow.ergo")
		if err != nil {
			t.Fatalf("GlobalFuncsSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("package-level shadows must stay untouched: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})

	t.Run("package name clash auto-aliases import", func(t *testing.T) {
		src := `func maps() string { "mine" }

func main() needs Console {
	Console.Println("\(maps()) \(hash("k") == hash("k"))")
}
`
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			t.Fatalf("before check: %v", beforeDiags)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("eval before: %v", err)
		}
		fixed, rewrites, err := fix.GlobalFuncsSource(src, "clash.ergo")
		if err != nil {
			t.Fatalf("GlobalFuncsSource: %v", err)
		}
		if len(rewrites) != 2 {
			t.Fatalf("expected 2 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		if !strings.Contains(fixed, "import stdmaps maps\n") || !strings.Contains(fixed, "stdmaps.Hash(") {
			t.Fatalf("expected auto-aliased import stdmaps maps:\n%s", fixed)
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			t.Fatalf("after check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil || afterOut.Stdout != beforeOut.Stdout {
			t.Fatalf("eval mismatch: err=%v before=%q after=%q", err, beforeOut.Stdout, afterOut.Stdout)
		}
		again, more, err := fix.GlobalFuncsSource(fixed, "clash.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			t.Fatalf("second pass not idempotent: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	})
}

func genGlobalFuncsProgram(r *rand.Rand) (src string, wantExact, wantAbsent []string) {
	// Randomly pre-import subsets of the target std packages so sorted
	// insertion before, between, and after existing imports is exercised, and
	// existing imports are never duplicated.
	preMaps := r.IntN(2) == 1
	preScope := r.IntN(2) == 1
	preTime := r.IntN(2) == 1

	imps := []string{"import codec"}
	if preMaps {
		imps = append(imps, "import maps")
	}
	if preScope {
		imps = append(imps, "import scope")
	}
	if preTime {
		imps = append(imps, "import time")
	}

	n := 1 + r.IntN(4)
	words := []string{"alpha", "beta", "gamma", "delta"}
	w := words[r.IntN(len(words))]

	var b strings.Builder
	for _, im := range imps {
		b.WriteString(im + "\n")
	}
	b.WriteString("\n")
	b.WriteString("type Item { Name string, Count int }\n\n")
	b.WriteString("type Greet { Say() string }\n\n")
	b.WriteString("type Plain {} implements Greet\n\n")
	b.WriteString("func Say(_ Plain) string { \"hi\" }\n\n")
	b.WriteString("type KeyHash {} implements Hashing[string]\n\n")
	b.WriteString("func Hash(_ KeyHash, k string) uint64 { hash(k) }\n\n")
	b.WriteString("func Equal(_ KeyHash, a string, b string) bool { a == b }\n\n")
	b.WriteString("func localShadow(hash func(x int) int, encode func(x int) int) int {\n")
	b.WriteString("\thash(10) + encode(20)\n")
	b.WriteString("}\n\n")
	b.WriteString("func capture() Context[Greet] needs Greet {\n")
	if preScope {
		b.WriteString("\tscope.OnExit() {}\n")
	}
	b.WriteString("\tInForce[Greet]()\n")
	b.WriteString("}\n\n")
	b.WriteString("func folderName() string needs TestAmbient {\n")
	b.WriteString("\tlet h = TestFolderHandle()\n")
	b.WriteString("\tTestFolder() + h.Name()\n")
	b.WriteString("}\n\n")
	b.WriteString("func makeSched() LiveScheduler needs Ambient {\n")
	if preTime {
		b.WriteString("\tlet _ = time.NewManual(0)\n")
	}
	b.WriteString("\tNewLiveScheduler()\n")
	b.WriteString("}\n\n")
	b.WriteString("func main() needs Console {\n")
	fmt.Fprintf(&b, "\tlet item = Item{Name: %q, Count: %d}\n", w, n)
	b.WriteString("\tlet ev = codec.NewEvents()\n")
	b.WriteString("\tencode(item, ev)\n")
	b.WriteString("\tlet dec = decode[Item](codec.NewReplay(ev.Recorded())) catch {\n")
	b.WriteString("\tcase WireWrongKind, WireMissingField:\n\t\tItem{Name: \"err\", Count: 0}\n\t}\n")
	fmt.Fprintf(&b, "\tlet xs = MakeSlice[int](%d).Append(dec.Count)\n", n)
	b.WriteString("\tlet mut m = MakeMap[string, int, KeyHash](KeyHash)\n")
	b.WriteString("\tm[dec.Name] = dec.Count\n")
	if preMaps {
		b.WriteString("\tlet _ = maps.Clone(Map[string, int]{\"k\": 1})\n")
	}
	b.WriteString("\tlet kept = AsWritten[int](xs[0])\n")
	b.WriteString("\tlet same = hash(dec.Name) == hash(item.Name)\n")
	b.WriteString("\tlet ctx = with context { Greet: Plain } { capture() }\n")
	b.WriteString("\tlet said = with ctx { Greet.Say() }\n")
	b.WriteString("\tlet sh = localShadow(func(x int) int { x + 1 }, func(x int) int { x + 2 })\n")
	b.WriteString("\tConsole.Println(\"\\(dec.Name) \\(kept) \\(same) \\(said) \\(sh) \\(m.Len())\")\n")
	b.WriteString("}\n")

	wantExact = []string{
		"maps.Hash(k)",
		"maps.Hash(dec.Name) == maps.Hash(item.Name)",
		"codec.Encode(item, ev)",
		"codec.Decode[Item](codec.NewReplay(ev.Recorded()))",
		fmt.Sprintf("slices.WithCapacity[int](%d)", n),
		"maps.Using[string, int, KeyHash](KeyHash)",
		"scope.InForce[Greet]()",
		"proof.AsWritten[int](xs[0])",
		"fstest.TestFolderHandle()",
		"fstest.TestFolder()",
		"time.NewLiveScheduler()",
		"hash(10) + encode(20)",
		"import codec\nimport fstest\nimport maps\nimport proof\nimport scope\nimport slices\nimport time\n",
	}
	wantAbsent = []string{
		"MakeSlice[",
		"MakeMap[",
		"= AsWritten[int](xs",
		"\tInForce[Greet]()",
		"\tTestFolder()",
		"\tNewLiveScheduler()",
		"maps.Hash(10)",
		"codec.Encode(20)",
	}
	return b.String(), wantExact, wantAbsent
}
