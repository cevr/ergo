package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The entry rule's seeded properties (V8, values.go). Each seed writes a
// program of functions over a store {m Map[int, E], s []int, out [][]int},
// whose entry E is int, []int, Map[int, int] or a Box of a Map and a
// slice, read through a borrow (r *st) or through a let mut local. Each
// function is one shape of the copy rules:
//
//   - get keep, get match, get none: Get reads an entry, and a write
//     through the root follows. No copy: no write reaches an entry;
//   - get in flight: the key's evaluation writes the root (bump), so the
//     receiver is copied once, as a table (maps.Clone);
//   - window: slices.Slice of a field that is written after it is kept
//     in another field: one copy, of the window;
//   - chain param, chain loop, chain put, chain delete: Append, Put and
//     Delete on the result of another: one copy of a shared source, none
//     of a let mut root's;
//   - copy root: a let mut copy of the Map, written, while the root is
//     read after: one copy, of the table;
//   - entry rmw: an entry moved into a let mut, written and stored back:
//     one copy of the entry (uniqueness is not known: values.go, V8);
//   - ref modify: the X1 shape, one key of a Map in a Ref changed in the
//     owned callback: no copy but the cell's copy-if-shared function.
//
// copyShape's model is the number of copies the semantics need in each
// function: TestCopyCountsMatchModel counts the copies the lowered Go of
// each function makes (maps.Clone, slices.Clone, slices.Clip, a generated
// copy function) and holds them equal, and holds that no Map is copied
// deep (no generated copy function for a Map type: values.go, V8).
// TestCopyRuleAgrees builds the same programs under the rule and under
// the reference (copyLowering "whole") and holds their outputs equal.
//
// A failure prints the seed, the Ergo source and the Go of the function.
// Replay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=N

var copySeed = flag.Uint64("copy.seed", 0, "run the copy-rule properties on this seed alone")

func copySeeds() []uint64 {
	if *copySeed != 0 {
		return []uint64{*copySeed}
	}
	return []uint64{1, 2, 3, 4}
}

// copyEntry is an entry kind: its suffix, its Ergo type, a zero literal,
// a sample literal and whether it holds a Map or slice.
type copyEntry struct {
	k, typ, zero, sample string
	holds                bool
}

var copyEntries = []copyEntry{
	{"I", "int", "0", "5", false},
	{"S", "[]int", "[]int{}", "[]int{1, 2}", true},
	{"M", "Map[int, int]", "Map[int, int]{}", "Map[int, int]{1: 2}", true},
	{"B", "Box", "Box{M: Map[int, int]{}, S: []int{}}", "Box{M: Map[int, int]{3: 4}, S: []int{5}}", true},
}

const copyPrelude = `import slices
import sync

type Box { M Map[int, int], S []int }

func szI(v int) int { v }

func szS(v []int) int {
	let mut t = len(v) * 10
	for x in v {
		t = t + x
	}
	t
}

func szM(v Map[int, int]) int {
	let mut t = len(v) * 100
	for k, x in v {
		t = t + k * 3 + x
	}
	t
}

func szB(v Box) int { szM(v.M) * 7 + szS(v.S) }

// head and cell keep both imports used in every seed.
func head(s []int) []int { slices.Slice(s, 0, 1) }

func cell(m Map[int, int]) sync.Ref[Map[int, int]] { sync.NewRef[Map[int, int]](m) }

`

// copyStore is the store type of entry e, its constructor, its digest and
// its bump, which writes the root through a borrow.
func copyStore(e copyEntry) string {
	k := e.k
	return fmt.Sprintf(`type st%[1]s { m Map[int, %[2]s], s []int, out [][]int }

func new%[1]s() st%[1]s { st%[1]s{m: Map[int, %[2]s]{1: %[3]s, 2: %[4]s}, s: []int{1, 2, 3, 4}, out: [][]int{}} }

func d%[1]s(r st%[1]s) int {
	let mut t = len(r.m) * 1000 + szS(r.s) + len(r.out) * 7
	for k, v in r.m {
		t = t + k + sz%[1]s(v)
	}
	for o in r.out {
		t = t + szS(o)
	}
	t
}

func dm%[1]s(m Map[int, %[2]s]) int {
	let mut t = len(m) * 1000
	for k, v in m {
		t = t + k + sz%[1]s(v)
	}
	t
}

func bump%[1]s(r *st%[1]s, k int) int {
	r.m[k] = %[3]s
	k
}

`, k, e.typ, e.zero, e.sample)
}

// copyShapes is the number of shapes copyGen.shape draws from.
const copyShapes = 9

type copyGen struct {
	r       *rand.Rand
	fns     strings.Builder
	main    strings.Builder
	model   map[string]int // function name -> the copies its Go makes
	order   []string
	reached map[string]bool
	n       int
}

// shape writes one function, its call in main and its model count.
func (g *copyGen) shape(combo int) {
	e := copyEntries[combo%len(copyEntries)]
	ptr := combo/len(copyEntries)%2 == 0
	pick := combo / (2 * len(copyEntries))
	g.n++
	name := fmt.Sprintf("f%d", g.n)
	k := 1 + g.r.IntN(3)
	sz := "sz" + e.k
	// head opens the function over the store; root is how the body
	// names it; call is main's call of it, with the store digest after.
	head := fmt.Sprintf("func %s(r *st%s, k int) int {\n", name, e.k)
	if !ptr {
		head = fmt.Sprintf("func %s(k int) int {\n\tlet mut r = new%s()\n", name, e.k)
	}
	call := func() {
		if ptr {
			g.main.WriteString(fmt.Sprintf("\tlet mut r%d = new%s()\n\tConsole.Println(\"\\(%s(&r%d, %d)) \\(d%s(r%d))\")\n", g.n, e.k, name, g.n, k, e.k, g.n))
		} else {
			g.main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s(%d))\")\n", name, k))
		}
	}
	tail := ""
	if !ptr {
		tail = fmt.Sprintf(" + d%s(r)", e.k)
	}
	match := func(x string) string {
		return fmt.Sprintf("match %s {\n\tcase Some[%s] as v:\n\t\t%s(v.Value)\n\tcase None:\n\t\t0\n\t}", x, e.typ, sz)
	}
	access := map[bool]string{true: "borrow", false: "local"}[ptr]
	var body string
	want := 0
	shape := ""
	switch pick {
	case 0:
		shape = "get keep"
		body = fmt.Sprintf("\tlet o = r.m.Get(k)\n\tr.m[k] = %s\n\tlet n = %s\n\tn%s\n", e.zero, match("o"), tail)
	case 1:
		shape = "get match"
		body = fmt.Sprintf("\tlet n = %s\n\tr.m[k + 1] = %s\n\tn%s\n", match("r.m.Get(k)"), e.zero, tail)
	case 2:
		shape = "get none"
		body = fmt.Sprintf("\tlet b = r.m.Get(k) == None\n\tr.m[k] = %s\n\tlet n = if b { 1 } else { 2 }\n\tn%s\n", e.zero, tail)
	case 3:
		shape = "get in flight"
		bump := "bump" + e.k + "(r, k)"
		if !ptr {
			bump = "bump" + e.k + "(&r, k)"
		}
		body = fmt.Sprintf("\tlet o = r.m.Get(%s)\n\tlet n = %s\n\tn%s\n", bump, match("o"), tail)
		want = 1 // the receiver, before bump writes the root
	case 4:
		shape = "window"
		body = fmt.Sprintf("\tr.out = r.out.Append(slices.Slice(r.s, 1, 3))\n\tr.s[1] = 70\n\tlen(r.out)%s\n", tail)
		want = 1 // the window
	case 5:
		shape = "copy root"
		body = fmt.Sprintf("\tlet mut c = r.m\n\tc[k] = %s\n\tr.s[0] = 9\n\tdm%s(c) + len(r.m)%s\n", e.zero, e.k, tail)
		want = 1 // the table
	case 6:
		if !e.holds {
			shape = "entry rmw"
			body = fmt.Sprintf("\tlet mut s = 0\n\tmatch r.m.Get(k) {\n\tcase Some[int] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\ts = s + 1\n\tr.m[k] = s\n\ts%s\n", tail)
			break
		}
		shape = "entry rmw"
		write := map[string]string{"S": "s = s.Append(9)", "M": "s[8] = 9", "B": "s.M[8] = 9"}[e.k]
		body = fmt.Sprintf("\tlet mut s = %s\n\tmatch r.m.Get(k) {\n\tcase Some[%s] as v:\n\t\ts = v.Value\n\tcase None:\n\t}\n\t%s\n\tr.m[k] = s\n\tlen(r.m)%s\n", e.zero, e.typ, write, tail)
		want = 1 // the entry: it may be shared
	case 7:
		shape = "ref modify"
		head = fmt.Sprintf("func %s(c sync.Ref[Map[int, %s]], k int) int {\n", name, e.typ)
		body = fmt.Sprintf("\tsync.Modify(c, func(all Map[int, %[1]s]) (int, Map[int, %[1]s]) {\n\t\tlet mut m = all\n\t\tlet n = %[2]s\n\t\tm[k] = %[3]s\n\t\t(n, m)\n\t})\n", e.typ, strings.ReplaceAll(match("m.Get(k)"), "\n", "\n\t"), e.sample)
		want = 1 // the cell's copy, made only when its value is shared
		call = func() {
			g.main.WriteString(fmt.Sprintf("\tlet c%d = sync.NewRef[Map[int, %s]](Map[int, %s]{1: %s})\n\tConsole.Println(\"\\(%s(c%d, %d)) \\(%s(c%d, %d)) \\(dm%s(c%d.Get()))\")\n", g.n, e.typ, e.typ, e.sample, name, g.n, k, name, g.n, k+1, e.k, g.n))
		}
	default:
		// The chains, over a slice or a Map of int.
		switch g.r.IntN(4) {
		case 0:
			shape = "chain param"
			head = fmt.Sprintf("func %s(x []int, a int) int {\n", name)
			body = "\tszS(x.Append(a).Append(a + 1).Append(a + 2))\n"
			want = 1 // x is shared: the first Append copies it
			call = func() {
				g.main.WriteString(fmt.Sprintf("\tlet x%d = []int{1, 2}\n\tConsole.Println(\"\\(%s(x%d, %d)) \\(szS(x%d))\")\n", g.n, name, g.n, k, g.n))
			}
		case 1:
			shape = "chain loop"
			head = fmt.Sprintf("func %s(n int) int {\n", name)
			body = "\tlet mut x = []int{}\n\tfor i in 0..n {\n\t\tx = x.Append(i).Append(i * 2)\n\t}\n\tszS(x)\n"
			call = func() { g.main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s(%d))\")\n", name, k+3)) }
		case 2:
			shape = "chain put"
			head = fmt.Sprintf("func %s(m Map[int, int], a int) int {\n", name)
			body = "\tszM(m.Put(a, 1).Put(a + 1, 2).Put(a + 2, 3))\n"
			want = 1
			call = func() {
				g.main.WriteString(fmt.Sprintf("\tlet m%d = Map[int, int]{1: 1}\n\tConsole.Println(\"\\(%s(m%d, %d)) \\(szM(m%d))\")\n", g.n, name, g.n, k, g.n))
			}
		default:
			shape = "chain delete"
			head = fmt.Sprintf("func %s(m Map[int, int], a int) int {\n", name)
			body = "\tszM(m.Delete(a).Delete(a + 1))\n"
			want = 1
			call = func() {
				g.main.WriteString(fmt.Sprintf("\tlet m%d = Map[int, int]{1: 1, 2: 2, 3: 3}\n\tConsole.Println(\"\\(%s(m%d, %d)) \\(szM(m%d))\")\n", g.n, name, g.n, k, g.n))
			}
		}
		access = "value"
	}
	switch {
	case shape == "window" || shape == "copy root":
		// Entries take no part: one kind is enough for the path.
		g.reached[shape+" "+access] = true
	case strings.HasPrefix(shape, "chain"):
		g.reached[shape] = true
	case shape == "ref modify":
		g.reached[shape+" "+e.k] = true // a Ref site has no store
	default:
		g.reached[shape+" "+access+" "+e.k] = true
	}
	g.fns.WriteString(head + body + "}\n\n")
	call()
	g.model[name] = want
	g.order = append(g.order, name)
}

// copyProgram writes the program of seed and the model of its copies.
func copyProgram(seed uint64, reached map[string]bool) (string, *copyGen) {
	g := &copyGen{r: rand.New(rand.NewPCG(seed, 0x636f7079)), model: map[string]int{}, reached: reached}
	// Every shape, entry and access once, in the seed's order (copyShapes
	// shapes, the last drawing one of the four chains).
	for _, combo := range g.r.Perm(copyShapes * 2 * len(copyEntries)) {
		g.shape(combo)
	}
	var src strings.Builder
	src.WriteString(copyPrelude)
	for _, e := range copyEntries {
		src.WriteString(copyStore(e))
	}
	src.WriteString(g.fns.String())
	src.WriteString("func main() needs Console {\n" + g.main.String() + "}\n")
	return src.String(), g
}

var copyOps = regexp.MustCompile(`\bmaps\.Clone\(|\bslices\.Clone\(|\bslices\.Clip\(|\bergoClone\w*\(`)

// goFuncsOf is the Go text of the function name: its body, and the body
// of its owned form (values.go, V5) when it has one.
func goFuncsOf(t *testing.T, mainGo, name string) string {
	t.Helper()
	text := goFuncBody(t, mainGo, name)
	if regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + name + `Owned\(`).MatchString(mainGo) {
		text += goFuncBody(t, mainGo, name+"Owned")
	}
	return text
}

// copyPaths are the paths the fixed seeds must reach.
func copyPaths() []string {
	var out []string
	for _, e := range copyEntries {
		for _, s := range []string{"get keep", "get match", "get none", "get in flight", "entry rmw"} {
			for _, a := range []string{"borrow", "local"} {
				out = append(out, s+" "+a+" "+e.k)
			}
		}
		out = append(out, "ref modify "+e.k)
	}
	out = append(out, "window borrow", "window local", "copy root borrow", "copy root local", "chain param", "chain loop", "chain put", "chain delete")
	return out
}

// TestCopyCountsMatchModel is the copy-count property (above), in process
// on the lowered Go.
func TestCopyCountsMatchModel(t *testing.T) {
	reached := map[string]bool{}
	for _, seed := range copySeeds() {
		src, g := copyProgram(seed, reached)
		p := mustLower(t, fmt.Sprintf("copycount%d", seed), src)
		mainGo := p.mainGo()
		for _, name := range g.order {
			text := goFuncsOf(t, mainGo, name)
			if got := len(copyOps.FindAllString(text, -1)); got != g.model[name] {
				t.Errorf("seed %d, %s: the lowering makes %d copies, the model %d\nReplay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=%d\n--- Go\n%s\n--- source\n%s", seed, name, got, g.model[name], seed, text, src)
			}
		}
		// A Map is copied as a table, never entry by entry (V8): no
		// generated copy function of a Map type.
		if m := regexp.MustCompile(`(?m)^func ergoCloneMap\w*\(`).FindString(mainGo); m != "" {
			t.Errorf("seed %d: a Map is copied entry by entry (%s)\nReplay: go test -run '^TestCopyCountsMatchModel$' ./internal/sketch/lower -args -copy.seed=%d\n%s", seed, m, seed, src)
		}
	}
	if *copySeed == 0 {
		for _, path := range copyPaths() {
			if !reached[path] {
				t.Errorf("the fixed seeds never reach %q", path)
			}
		}
	}
}

// TestGoGetsEntryCopies: a Map whose entries hold slices goes to Go as a
// copy of each entry (V8, cloneDeep), since Go may write an entry two Ergo
// Maps share; the same Map's Ergo copy is a table. The promised fact is the
// shape: Go's operand goes through a copy function that ranges over the
// entries. Mutant: cloneDeep copies the table only.
func TestGoGetsEntryCopies(t *testing.T) {
	src := "import go net/url\n\nfunc enc(m Map[string, []string]) string needs go.Foreign {\n\tlet mut c = m\n\tc[\"b\"] = []string{\"2\"}\n\tlet s = url.Values(c).Encode()\n\ts + \"\\(len(c))\"\n}\n\n" +
		"func main() needs Console | go.Foreign {\n\tConsole.Println(enc(Map[string, []string]{\"a\": []string{\"1\"}}))\n}\n"
	mainGo := mustLower(t, "goentries", src).mainGo()
	call := regexp.MustCompile(`(ergoClone\w*)\(url\.Values\(c\)\)\.Encode\(\)`).FindStringSubmatch(mainGo)
	if call == nil {
		t.Fatalf("Go's operand is not copied by a copy function:\n%s", mainGo)
	}
	if body := goFuncBody(t, mainGo, call[1]); !strings.Contains(body, "range") || !strings.Contains(body, "slices.Clone(") {
		t.Errorf("Go's operand is not copied entry by entry:\n%s", body)
	}
}

// TestCopyRuleAgrees runs the copy-count programs under the entry rule and
// under its reference (copyLowering "whole", the rule before V8): both
// print the same lines.
//
// It builds Go because the question is what the programs print at run
// time, which only a run of both lowerings shows.
func TestCopyRuleAgrees(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed uint64
		src  string
		rule *lowered
		ref  *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range copySeeds() {
		src, _ := copyProgram(seed, map[string]bool{})
		rule := mustLower(t, fmt.Sprintf("copyrule%d", seed), src)
		saved := copyLowering
		copyLowering = "whole"
		ref := mustLower(t, fmt.Sprintf("copywhole%d", seed), src)
		copyLowering = saved
		runs = append(runs, run{seed, src, rule, ref})
		programs = append(programs, rule, ref)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the copy programs do not build")
	}
	for _, r := range runs {
		got, gerr, gcode := runExe(t, exes[r.rule.name])
		want, werr, wcode := runExe(t, exes[r.ref.name])
		if got != want || gcode != wcode || gcode != 0 {
			t.Errorf("seed %d: the rule and the reference differ\nReplay: go test -run '^TestCopyRuleAgrees$' ./internal/sketch/lower -args -copy.seed=%d\n%s\n--- rule (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- diff\n%s", r.seed, r.seed, r.src, gcode, got, gerr, wcode, want, werr, diffLines(got, want))
		}
	}
}
