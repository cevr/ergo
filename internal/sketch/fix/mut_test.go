package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
)

// TestMutSpellsTheBorrow (MODES-1; SIMPLIFY U2, owner q169, q223) is a
// seeded property of the write-in-place mode `mut T`. Each seed writes
// one program twice, with the borrow spelled `*P` and spelled `mut P`, in
// every position a type is written: a declaration's parameter groups, a
// function type's parameter (named and unnamed) and result, a function
// literal's parameters, an operation's parameters, a law's parameter, a
// record field (refused in both spellings), `own` before the mode, and an
// indirection `*out = v` in a body, which is no type and stays. A Go
// binding keeps `*P`. Then: ergo fix(old) is byte-equal to the new
// spelling; a second fix writes nothing; format leaves the new spelling
// as written; and both spellings check with the same checked facts
// (every declared function's and variable's type, each operation's
// signature) and the same diagnostics (code and line), but for the old
// spelling's S108 naming mut (MODES-2): one on each line the fix rewrote
// that the new spelling does not refuse, and none elsewhere (a Go
// binding's *P is Go's pointer). ERGO_SEED=<n> replays one
// seed; a failure prints both programs.
//
// Mutants it kills that no other test does (ledgers: syntax, fix): the
// parser drops mut's mark, so format writes *; own does not read before
// mut; fix rewrites a Go binding's *T; fix misses a function type's
// borrow; fix rewrites an indirection in a body.
func TestMutSpellsTheBorrow(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(200) {
			seeds = append(seeds, i+1)
		}
	}
	kinds := map[string]int{}
	for _, seed := range seeds {
		oldSrc, newSrc, reached := mutProgram(rand.New(rand.NewPCG(seed, 0x3a7)))
		for k := range reached {
			kinds[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestMutSpellsTheBorrow$' ./internal/sketch/fix\nold:\n%s\nnew:\n%s", seed, fmt.Sprintf(format, args...), seed, oldSrc, newSrc)
		}
		fixed, n, err := fix.Mut(oldSrc)
		if err != nil {
			fail("fix: %v", err)
		}
		if fixed != newSrc {
			fail("fix(old) is not the new spelling:\n%s", fixed)
		}
		if want := strings.Count(newSrc, "mut "); n != want {
			fail("fix counts %d borrows, the new spelling writes %d", n, want)
		}
		if again, k, _ := fix.Mut(newSrc); k != 0 || again != newSrc {
			fail("a second fix rewrites %d borrows", k)
		}
		if out, err := format.Source(newSrc); err != nil || out != newSrc {
			fail("format gives (%v):\n%s", err, out)
		}
		oldFacts, oldDiags, oldStars := mutFacts(oldSrc)
		newFacts, newDiags, newStars := mutFacts(newSrc)
		if len(newStars) > 0 {
			fail("the new spelling is refused as *T on lines %v", newStars)
		}
		oldLines, newLines := strings.Split(oldSrc, "\n"), strings.Split(newSrc, "\n")
		refused := map[int]bool{}
		for _, d := range strings.Fields(newDiags) {
			_, l, _ := strings.Cut(d, ":")
			n, _ := strconv.Atoi(l)
			refused[n] = true
		}
		for i := range oldLines {
			changed := oldLines[i] != newLines[i]
			if changed && !refused[i+1] && !oldStars[i+1] {
				fail("line %d writes *T and the old spelling is not refused there", i+1)
			}
			if !changed && oldStars[i+1] {
				fail("line %d is refused as *T, but fix rewrites nothing there", i+1)
			}
		}
		if len(oldStars) > 0 {
			kinds["star refused"]++
		}
		if oldDiags != newDiags {
			fail("diagnostics differ:\nold %s\nnew %s", oldDiags, newDiags)
		}
		if oldFacts != newFacts {
			fail("facts differ:\nold %s\nnew %s", oldFacts, newFacts)
		}
		if strings.Contains(oldFacts, "mut main.P") {
			kinds["borrow facts"]++
		}
		if newDiags == "" {
			kinds["checks clean"]++
		}
	}
	t.Logf("%d seeds reach: %v", len(seeds), kinds)
	if len(seeds) > 1 {
		for _, k := range []string{"decl", "group", "functype", "unnamed", "result", "lit", "op", "law", "field", "own", "deref", "binding", "plain", "borrow facts", "checks clean", "star refused"} {
			if kinds[k] == 0 {
				t.Errorf("no seed reaches %s", k)
			}
		}
	}
}

// mutProgram writes one program with the borrow spelled *P (old) and
// mut P (new), and the kinds of site it reached.
func mutProgram(r *rand.Rand) (string, string, map[string]bool) {
	reached := map[string]bool{}
	var o, n strings.Builder
	both := func(s string) { o.WriteString(s); n.WriteString(s) }
	// pair writes a fragment whose every `*` is a borrow type.
	pair := func(s string) { o.WriteString(s); n.WriteString(strings.ReplaceAll(s, "*", "mut ")) }
	both("type P { n int }\n\n")
	both("type R { n int } implements Closer\n\nfunc Close(r own R) {}\n\n")
	// typ draws a parameter type: a borrow, or plain data, or a function
	// type that takes or returns one.
	typ := func() string {
		switch r.IntN(6) {
		case 0, 1:
			return "*P"
		case 2:
			reached["plain"] = true
			return []string{"P", "int", "R"}[r.IntN(3)]
		case 3:
			reached["functype"] = true
			return "func(v *P) int"
		case 4:
			reached["functype"], reached["unnamed"] = true, true
			return "func(*P, int)"
		default:
			reached["result"] = true
			return "func() *P"
		}
	}
	params := func() string {
		var gs []string
		k := 0
		for range 1 + r.IntN(3) {
			var names []string
			for range 1 + r.IntN(3) {
				names = append(names, fmt.Sprintf("p%d", k))
				k++
			}
			if len(names) > 1 {
				reached["group"] = true
			}
			t := typ()
			if strings.HasPrefix(t, "*") && r.IntN(4) == 0 {
				reached["own"] = true
				t = "own " + t
			}
			gs = append(gs, strings.Join(names, ", ")+" "+t)
		}
		return "(" + strings.Join(gs, ", ") + ")"
	}
	if r.IntN(2) == 0 {
		reached["binding"] = true
		both("extern go example.com/p.Touch func touch(p *P)\n\n")
	}
	if r.IntN(2) == 0 {
		reached["field"] = true
		pair("type Hold { at *P }\n\n")
	}
	if r.IntN(2) == 0 {
		reached["law"] = true
		pair("law Kept(p *P) { p.n == p.n }\n\n")
	}
	if r.IntN(2) == 0 {
		reached["deref"] = true
		pair("func put(out *P, v P) {\n\t")
		both("*out = v\n}\n\n")
	}
	both("type Ops {\n")
	for i := range 1 + r.IntN(3) {
		reached["op"] = true
		if r.IntN(2) == 0 {
			reached["unnamed"] = true
			pair(fmt.Sprintf("\tM%d(*P, int),\n", i))
		} else {
			pair(fmt.Sprintf("\tM%d%s,\n", i, params()))
		}
	}
	both("}\n\n")
	var lits []string
	for i := range 1 + r.IntN(3) {
		if r.IntN(2) == 0 {
			reached["decl"] = true
			pair(fmt.Sprintf("func f%d%s {}\n\n", i, params()))
		} else {
			reached["lit"] = true
			lits = append(lits, fmt.Sprintf("\tlet l%d = func%s {}\n\t_ = l%d\n", i, params(), i))
		}
	}
	both("func main() {\n")
	for _, l := range lits {
		pair(l)
	}
	both("}\n")
	return o.String(), n.String(), reached
}

// mutFacts are a program's checked facts (each declared function's and
// variable's type, each operation's signature) and its diagnostics as
// code and line.
func mutFacts(src string) (string, string, map[int]bool) {
	pkg, diags := check.CheckSource(src)
	var ds []string
	stars := map[int]bool{}
	for _, d := range diags {
		if d.Code == "S108" && strings.HasPrefix(d.Message, "write the borrow as mut ") {
			stars[d.Line] = true
			continue
		}
		ds = append(ds, fmt.Sprintf("%s:%d", d.Code, d.Line))
	}
	sort.Strings(ds)
	if pkg == nil {
		return "", strings.Join(ds, " "), stars
	}
	var facts []string
	for id, obj := range pkg.Info.Defs {
		line := strings.Count(src[:min(int(id.Pos()), len(src))], "\n")
		switch x := obj.(type) {
		case *check.Func:
			facts = append(facts, fmt.Sprintf("%05d %s %s", line, x.Name, mutSig(x.Sig)))
		case *check.Var:
			facts = append(facts, fmt.Sprintf("%05d %s %s", line, x.Name, mutType(x.Type)))
		case *check.TypeName:
			if nt, ok := x.Type.(*check.Named); ok {
				if it, ok := nt.Underlying().(*check.Interface); ok {
					for _, m := range it.Methods {
						facts = append(facts, fmt.Sprintf("%05d %s.%s %s", line, x.Name, m.Name, mutSig(m.Sig)))
					}
				}
			}
		}
	}
	sort.Strings(facts)
	return strings.Join(facts, "\n"), strings.Join(ds, " "), stars
}

func mutSig(s *check.Signature) string {
	if s == nil {
		return "nil"
	}
	var ps []string
	for _, p := range s.Params {
		ps = append(ps, mutType(p.Type))
	}
	return "(" + strings.Join(ps, ", ") + ")"
}

func mutType(t check.Type) string {
	if s, ok := t.(*check.Signature); ok {
		return "func " + mutSig(s)
	}
	if t == nil {
		return "nil"
	}
	return t.String()
}

// TestMutGoLeavesGoSource (MODES-2): ergo fix --go respells the borrows of
// the Go string literals that hold an Ergo source, and leaves a string that
// also parses as Ergo but is Go source (a Go test function naming
// testing.T, which it imports no package for) as it is. The fixture is
// testdata/mutgo/lits.go; its .want is the file after the fix.
//
// Mutant it kills that no other test does (ledger: fix): goSource answers
// false, so a Go test function's *testing.T becomes mut testing.T.
func TestMutGoLeavesGoSource(t *testing.T) {
	src, err := os.ReadFile("testdata/mutgo/lits.go")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/mutgo/lits.go.want")
	if err != nil {
		t.Fatal(err)
	}
	file := t.TempDir() + "/lits.go"
	if err := os.WriteFile(file, src, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := fix.RunMut(fix.Options{Paths: []string{file}, Write: true, Go: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("ergo fix --go wrote:\n%s\nwant:\n%s", got, want)
	}
	if rep.Edits[file] != 2 {
		t.Errorf("%d borrows rewritten, want 2", rep.Edits[file])
	}
}
