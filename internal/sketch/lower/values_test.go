package lower

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The value rule's elisions, its -race guard, its mutants and its
// benchmark (values.go).

const valueElideSource = `import maps

type Box { M Map[int, int], S []int }

func bump(old Option[int]) int {
	match old {
	case Some[int] as v:
		v.Value + 1
	case None:
		1
	}
}

func put(m Map[int, int], k int) Map[int, int] {
	let mut c = m
	c[k] = k
	c
}

func count(n int) Map[int, int] {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = maps.Update(m, i % 7, bump)
	}
	m
}

func build(n int) Map[int, int] {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m[i] = i
	}
	m
}

func collect(n int) []int {
	let mut out = []int{}
	for i in 0..n {
		out = out.Append(i)
	}
	out
}

func drop(n int) Map[int, int] {
	let mut m = build(n)
	for i in 0..n {
		m = m.Delete(i)
	}
	m
}

func puts(n int) Map[int, int] {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = m.Put(i, i)
	}
	m
}

func boxed(n int) Box {
	let mut b = Box{M: build(n), S: collect(n)}
	b.S[0] = 7
	b.M[0] = 7
	b.S = b.S.Append(n)
	b.M = put(b.M, n)
	b
}

func moved(n int) []int {
	let xs = collect(n)
	let mut ys = xs
	ys[0] = 5
	ys
}

func kept(xs []int) int {
	let mut c = xs
	c[0] = 1
	let snap = c
	c[1] = 2
	snap[1] + c[0]
}

func main() needs Console {
	let mut m = build(3)
	m[9] = 9
	Console.Println("\(len(m)) \(len(drop(3))) \(len(boxed(2).S)) \(moved(2)[0]) \(kept([]int{4, 4})) \(len(count(9))) \(len(puts(4)))")
}
`

// TestValueRuleElides: a fresh value enters a root with no copy (a
// literal, a call whose result is fresh, a local dead after the bind) and
// writes in place (m[k] = v, out = out.Append(x), m = m.Delete(k),
// m = m.Put(k, v), a field's Append); a read kept while the root is
// written later is copied.
// An owned parameter (V5) copies nothing on a rebinding: m =
// maps.Update(m, k, f) in a loop and b.M = put(b.M, n) call the owned
// bodies with no copy, as hand Go writes m[k] = f(m[k]) (integration r1
// F2), and the parameter's copy moves to the wrapper, which only an
// indirect call reaches. With a named callback the rebinding is written at
// its site with the callback called directly (update.go), so no Option
// escapes; with updateLowering "call" it calls the owned body, uncopied.
// It reads the emitted Go only: the copies' meaning
// is TestValueRuleAgrees', which passes with every copy in place, so only
// the Go text tells this rule from the one that copies everywhere and from
// the copying lowering "copy" (the mutant "never elide"); the
// zero-allocation claims of BenchmarkValueRule rest on it.
func TestValueRuleElides(t *testing.T) {
	p := lowerValueMode(t, "velide", valueElideSource, true)
	code := p.mainGo()
	for _, fn := range []string{"build", "collect", "drop", "puts", "boxed", "moved", "count", "putOwned"} {
		if body := goFunc(t, code, fn); strings.Contains(body, "Clone") || strings.Contains(body, "Clip(") {
			t.Errorf("%s copies a fresh value:\n%s", fn, body)
		}
	}
	if body := goFunc(t, code, "keptOwned"); strings.Count(body, "slices.Clone(") != 1 {
		t.Errorf("kept should copy the kept read once:\n%s", body)
	}
	if body := goFunc(t, code, "main_1"); strings.Contains(body, "Clone") {
		t.Errorf("main copies build's fresh result:\n%s", body)
	}
	// m = maps.Update(m, k, bump) is written at its site with bump's
	// comma-ok variant called directly, no Option built (update.go); with
	// updateLowering "option" it calls bump with the Option; called
	// instead, it calls the owned body.
	if body := goFunc(t, code, "count"); strings.Contains(body, "Update") || !strings.Contains(body, "bumpOk(") || strings.Contains(body, "Some") {
		t.Errorf("m = maps.Update(m, k, bump) is not written at its site with bump's comma-ok variant:\n%s", body)
	}
	option := lowerValueMode(t, "velideoption", valueElideSource, true, "update-option").mainGo()
	if body := goFunc(t, option, "count"); strings.Contains(body, "Update") || !strings.Contains(body, "bump(") || strings.Contains(option, "bumpOk") {
		t.Errorf("updateLowering option does not call bump with the Option:\n%s", body)
	}
	called := lowerValueMode(t, "velidecall", valueElideSource, true, "update-call").mainGo()
	update := regexp.MustCompile(`(?s)\nfunc UpdateOwned\[.*?\n}\n`).FindString(called)
	if update == "" || strings.Contains(update, "Clone") || !strings.Contains(goFunc(t, called, "count"), "UpdateOwned[") || strings.Contains(goFunc(t, called, "count"), "Clone") {
		t.Errorf("m = maps.Update(m, k, f) copies the Map:\n%s\n%s", update, goFunc(t, called, "count"))
	}
	// The copying lowering keeps a copy on the same rebinding.
	ref := lowerValueMode(t, "velidecopy", valueElideSource, true, "copy").mainGo()
	if body := goFunc(t, ref, "count"); !strings.Contains(body, "Clone") || strings.Contains(ref, "Owned") {
		t.Errorf("the copying lowering elides the copy of m = maps.Update(m, k, f):\n%s", body)
	}
}

// TestValueRuleRace: the probe that sends a value to two tasks, built with
// -race, reports no race and prints the values (map-alias P6: a DATA RACE
// in 3 of 3 runs before the rule). Only Go's race detector, in a built
// binary, observes the race. A task that captures a let mut written after
// it is S108 now (types.md, Function values 3).
//
// It builds Go because only Go's race detector, in a built binary, sees
// the race.
func TestValueRuleRace(t *testing.T) {
	needsBuild(t)
	if testing.Short() {
		t.Skip("-race build")
	}
	for _, p := range valueProbes {
		if !strings.HasPrefix(p.name, "P6") {
			continue
		}
		o := lowerValueMode(t, "vrace"+p.name, p.src, true)
		root := t.TempDir()
		write := func(rel string, data []byte) {
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", []byte(goMod))
		for _, f := range Runtime() {
			write(f.Path, f.Data)
		}
		write("p/main.go", []byte(o.mainGo()))
		exe := filepath.Join(root, "race")
		build := exec.Command("go", "build", "-race", "-trimpath", "-o", exe, "./p")
		build.Dir = root
		build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "CGO_ENABLED=1")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%s: go build -race: %v\n%s", p.name, err, out)
		}
		for i := range 3 {
			run := exec.Command(exe)
			run.Env = append(os.Environ(), "GORACE=halt_on_error=1", "GOMAXPROCS=4")
			var stdout, stderr bytes.Buffer
			run.Stdout, run.Stderr = &stdout, &stderr
			err := run.Run()
			if err != nil || strings.Contains(stderr.String(), "DATA RACE") || stdout.String() != p.want {
				t.Errorf("%s run %d: %v, got %q, want %q\n%s", p.name, i, err, stdout.String(), p.want, firstLines(stderr.String(), 40))
			}
		}
	}
}

var valueMutants = flag.Bool("value.mutants", false, "run TestValueRuleMutants")

// TestValueRuleMutants switches off each rule of the analysis in turn
// (valueMutant) and requires the probes or the property's shared programs
// to print something else: each rule has a guard that fails without it.
// The never-elide mutant is the copying lowering "copy", which every
// run-based check passes; TestValueRuleElides guards it.
// The never-fuse mutant is updateLowering "call", which every run-based
// check passes; TestValueRuleElides guards it too.
// It builds eleven sets of programs, so it runs only with -value.mutants:
// go test -run '^TestValueRuleMutants$' ./internal/sketch/lower -args -value.mutants
//
// It builds Go because a guard of the value rule is a probe's output at
// run time (Go's map and slice sharing), which no in-process check of the
// emitted text observes.
func TestValueRuleMutants(t *testing.T) {
	if !*valueMutants {
		t.Skip("run with -value.mutants")
	}
	for _, m := range []string{"never-copy", "shallow", "elide-live", "elide-escape", "in-place", "consume-live", "consume-captured", "own-live", "update-none", "update-no-write", "update-ok-none", "update-ok-swap", "hand-nonliteral", "store-unfresh", "share-blind", "return-blind"} {
		t.Run(m, func(t *testing.T) {
			saved := valueMutant
			valueMutant = m
			defer func() { valueMutant = saved }()
			type want struct{ name, out, src string }
			var wants []want
			var programs []*lowered
			for _, p := range valueProbes {
				programs = append(programs, mustLower(t, "vm"+p.name, p.src))
				wants = append(wants, want{"vm" + p.name, p.want, p.src})
			}
			for _, seed := range []uint64{1, 2, 3, 4, 5, 6} {
				src, out := valueProgram(seed, true, map[string]bool{})
				name := fmt.Sprintf("vm%dshared", seed)
				programs = append(programs, mustLower(t, name, src))
				wants = append(wants, want{name, out, src})
			}
			exes := buildAll(t, programs, true)
			if exes == nil {
				t.Fatal("the mutant's programs do not build")
			}
			var killed []string
			for _, w := range wants {
				if out, _, code := runExe(t, exes[w.name]); out != w.out || code != 0 {
					killed = append(killed, w.name)
				}
			}
			if len(killed) == 0 {
				t.Errorf("mutant %s survives every probe and seed", m)
			}
			t.Logf("mutant %s killed by %v", m, killed)
		})
	}
}

// BenchmarkValueRule measures the value rule against hand Go: a loop that
// builds a Map (the copy is elided, so no allocation beyond Go's), the same
// loop with m = m.Put(i, i) (PutsErgo, in place: against BuildGo), a loop
// that appends (in place), and a real shared write, a parameter written
// through a let mut copy (one maps.Clone, as hand Go writes it), and
// m = maps.Update(m, k, f) over 1e5 new keys (Upsert, f sees None) and
// over 64 keys again and again (Again, f sees Some) against hand Go's
// v, ok := m[k]; m[k] = f(v, ok) (owned parameters, V5: no copy; the call
// written at its site, update.go: no Option escapes, so allocs/op match).
// go test -run '^$' -bench '^BenchmarkValueRule$' -benchmem ./internal/sketch/lower
func BenchmarkValueRule(b *testing.B) {
	const src = `import maps

func inc(old Option[int]) int {
	match old {
	case Some[int] as v:
		v.Value + 1
	case None:
		1
	}
}

func Upsert(n int) int {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = maps.Update(m, i, inc)
	}
	len(m)
}

func Again(n int) int {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = maps.Update(m, i % 64, inc)
	}
	len(m)
}

func Build(n int) int {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m[i] = i
	}
	len(m)
}

func Puts(n int) int {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = m.Put(i, i)
	}
	len(m)
}

func Collect(n int) int {
	let mut out = []int{}
	for i in 0..n {
		out = out.Append(i)
	}
	len(out)
}

func Bump(m Map[int, int], k int) int {
	let mut c = m
	c[k] = k
	len(c)
}

func main() needs Console {
	Console.Println("\(Build(3)) \(Puts(3)) \(Collect(3)) \(Bump(Map[int, int]{}, 1)) \(Upsert(3)) \(Again(3))")
}
`
	emittedBench(b, "valuebench", src, func(mainGo string) string {
		return `package main

import (
	"maps"
	"testing"
)

var sink int
var shared = func() map[int]int {
	m := map[int]int{}
	for i := range 64 {
		m[i] = i
	}
	return m
}()

func BenchmarkBuildErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Build", "64") + `
	}
}

func BenchmarkBuildGo(b *testing.B) {
	for range b.N {
		m := map[int]int{}
		for i := range 64 {
			m[i] = i
		}
		sink = len(m)
	}
}

func BenchmarkPutsErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Puts", "64") + `
	}
}

func BenchmarkCollectErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Collect", "64") + `
	}
}

func BenchmarkCollectGo(b *testing.B) {
	for range b.N {
		var out []int
		out = []int{}
		for i := range 64 {
			out = append(out, i)
		}
		sink = len(out)
	}
}

func BenchmarkBumpErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Bump", "shared, 99") + `
	}
}

func BenchmarkBumpGo(b *testing.B) {
	for range b.N {
		c := maps.Clone(shared)
		c[99] = 99
		sink = len(c)
	}
}

func BenchmarkUpsertErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Upsert", "100000") + `
	}
}

//go:noinline
func incGo(v int, ok bool) int {
	if ok {
		return v + 1
	}
	return 1
}

func BenchmarkUpsertGo(b *testing.B) {
	for range b.N {
		m := map[int]int{}
		for i := range 100000 {
			v, ok := m[i]
			m[i] = incGo(v, ok)
		}
		sink = len(m)
	}
}

func BenchmarkAgainErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Again", "100000") + `
	}
}

func BenchmarkAgainGo(b *testing.B) {
	for range b.N {
		m := map[int]int{}
		for i := range 100000 {
			v, ok := m[i%64]
			m[i%64] = incGo(v, ok)
		}
		sink = len(m)
	}
}
`
	}, "BuildErgo", "BuildGo", "CollectErgo", "CollectGo", "BumpErgo", "BumpGo", "UpsertErgo", "UpsertGo", "AgainErgo", "AgainGo")
}
