package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// lw-get-commaok (G-ex-f-6): a match on m.Get(k) whose Option is used
// nowhere else reads the Map with Go's comma-ok form; the reference is the
// type switch on the Option (getLowering "switch").

// getCall is a call of the prelude's Get wrapper on m and k in emitted Go.
var getCall = regexp.MustCompile(`\bGet(\[[^\]]*\])?\(m, k\)`)

// okTest is the comma-ok lowering's branch on ok.
var okTest = regexp.MustCompile(`if ok(_[0-9]+)? \{`)

// lowerGetMode lowers src with the comma-ok lowering, or with the reference.
func lowerGetMode(t *testing.T, name, src string, commaOk bool) *lowered {
	t.Helper()
	saved := getLowering
	defer func() { getLowering = saved }()
	getLowering = ""
	if !commaOk {
		getLowering = "switch"
	}
	return mustLower(t, name, src)
}

var getMatchSeed = flag.Uint64("getmatch.seed", 0, "run TestGetMatchAgrees on this seed alone")

// The shapes the property draws. A context places the match: as the tail,
// in a let, with its subject in parentheses, in a loop whose arms break and
// continue, with the Option bound first and read after (no fusion), or with
// a constant inside Some (no fusion). Some's arm binds the variant, binds
// its value, matches it with no binding, or ("both") takes None too in one
// case; None's arm is None, None bound, a wildcard, a test of the Option,
// or a binding of the whole Option (no fusion).
var (
	getContexts = []string{"tail", "let", "paren", "loop", "after", "nested"}
	getSomes    = []string{"as", "record", "recordWild", "test", "both"}
	getNones    = []string{"none", "noneAs", "wild", "option", "bind"}
)

type getShape struct {
	context, some, none string
	someFirst           bool
}

// fused reports whether the comma-ok lowering takes the shape.
func (s getShape) fused() bool {
	return s.context != "after" && s.context != "nested" && (s.some == "both" || s.none != "bind")
}

func (g *getGen) shape() getShape {
	s := getShape{
		context: getContexts[g.r.IntN(len(getContexts))],
		some:    getSomes[g.r.IntN(len(getSomes))],
		none:    getNones[g.r.IntN(len(getNones))],
	}
	// A wildcard, a test of the Option or a binding takes both variants,
	// so it comes last.
	s.someFirst = s.none == "wild" || s.none == "option" || s.none == "bind" || g.r.IntN(2) == 0
	return s
}

type getGen struct{ r *rand.Rand }

// arms is the match's arms; loop arms are statements, the others values.
func (s getShape) arms(ind string) string {
	loop := s.context == "loop"
	var some, none string
	val := map[string]string{"as": "s.Value", "record": "x", "recordWild": "1", "test": "1", "both": "4"}[s.some]
	head := map[string]string{"as": "case Some[int] as s:", "record": "case Some{x}:", "recordWild": "case Some{_}:", "test": "case Some[int]:", "both": "case Some[int], None:"}[s.some]
	switch {
	case loop && s.some == "both":
		some = head + "\n" + ind + "\ttotal = total + " + val + " + i\n"
	case loop:
		some = head + "\n" + ind + "\tif " + val + " > 1 {\n" + ind + "\t\tbreak\n" + ind + "\t}\n" + ind + "\ttotal = total + " + val + " + i\n"
	default:
		some = head + "\n" + ind + "\t" + val + " * 3 + 1\n"
	}
	nhead := map[string]string{"none": "case None:", "noneAs": "case None as n:", "wild": "case _:", "option": "case Option[int]:", "bind": "case opt:"}[s.none]
	bound := map[string]string{"noneAs": "n", "bind": "opt"}[s.none]
	switch {
	case loop && bound != "":
		none = nhead + "\n" + ind + "\tif " + bound + " == None {\n" + ind + "\t\ttotal = total + 100\n" + ind + "\t}\n" + ind + "\tcontinue\n"
	case loop:
		none = nhead + "\n" + ind + "\ttotal = total + 100\n" + ind + "\tcontinue\n"
	case bound != "":
		none = nhead + "\n" + ind + "\tif " + bound + " == None {\n" + ind + "\t\t-2\n" + ind + "\t} else {\n" + ind + "\t\t-3\n" + ind + "\t}\n"
	default:
		none = nhead + "\n" + ind + "\t-" + fmt.Sprint(1+slices.Index(getNones, s.none)) + "\n"
	}
	nested := ""
	if s.context == "nested" {
		nested = ind + "case Some{Value: 1}:\n" + ind + "\t50\n"
	}
	if s.some == "both" {
		return nested + ind + some
	}
	if s.someFirst {
		return nested + ind + some + ind + none
	}
	return nested + ind + none + ind + some
}

// fn is a function f<i>(m, k) int whose body is the shape's match.
func (s getShape) fn(i int) string {
	body := ""
	switch s.context {
	case "tail":
		body = "\tmatch m.Get(k) {\n" + s.arms("\t") + "\t}\n"
	case "let":
		body = "\tlet r = match m.Get(k) {\n" + s.arms("\t") + "\t}\n\tr * 2\n"
	case "paren":
		body = "\tmatch (m.Get(k)) {\n" + s.arms("\t") + "\t}\n"
	case "loop":
		body = "\tlet mut total = 0\n\tfor i in 0..3 {\n\t\tmatch m.Get(k) {\n" + s.arms("\t\t") + "\t\t}\n\t\ttotal = total + 1\n\t}\n\ttotal\n"
	case "after":
		body = "\tlet o = m.Get(k)\n\tlet r = match o {\n" + s.arms("\t") + "\t}\n\tif o == None {\n\t\tr + 1000\n\t} else {\n\t\tr\n\t}\n"
	case "nested":
		body = "\tmatch m.Get(k) {\n" + s.arms("\t") + "\t}\n"
	}
	return fmt.Sprintf("func f%d(m Map[string, int], k string) int {\n%s}\n", i, body)
}

const getFuncs = 12

// lowerCopyMode lowers src with the element copy of a comma-ok read, or
// with the reference copy of the whole receiver (getCopy "receiver").
func lowerCopyMode(t *testing.T, name, src string, element bool) *lowered {
	t.Helper()
	saved := getCopy
	defer func() { getCopy = saved }()
	getCopy = ""
	if !element {
		getCopy = "receiver"
	}
	return mustLower(t, name, src)
}

// TestGetMatchCopiesTheValueNotTheMap is the speed promise of the comma-ok
// read (wf10 GP frame cost): where the value rule copies a Map that a
// match on m.Get(k) reads, the read takes the value in place and copies
// that value only, so a read costs one value and not the whole Map (std
// ui's composer read its sibling index this way on every node: 13.9M
// instructions a dash-gui frame, 3.84M with the element copy). The
// reference copies the receiver.
func TestGetMatchCopiesTheValueNotTheMap(t *testing.T) {
	src := `type Box { m Map[int, []int] }

func look(b *Box, k int) int {
	match b.m.Get(k) {
	case Some[[]int] as s:
		len(s.Value)
	case None:
		0
	}
}

func main() needs Console {
	let mut b = Box{m: Map[int, []int]{1: []int{4, 5}}}
	Console.Println("\(look(&b, 1))")
}
`
	read := regexp.MustCompile(`(?m)^\s*v(_[0-9]+)?, ok(_[0-9]+)? := (.*)\[k\]$`)
	for _, element := range []bool{true, false} {
		fn := goFuncBody(t, lowerCopyMode(t, fmt.Sprintf("gc%v", element), src, element).mainGo(), "look")
		m := read.FindStringSubmatch(fn)
		if m == nil {
			t.Fatalf("element %v: no comma-ok read in\n%s", element, fn)
		}
		copied := strings.Contains(m[3], "Clone")
		if copied == element {
			t.Errorf("element %v: the read's receiver is %s (want a copy only in the reference)\n%s", element, m[3], fn)
		}
		if element && !strings.Contains(fn, "slices.Clone(v") {
			t.Errorf("the element read does not copy the value it binds:\n%s", fn)
		}
	}
}

var getCopySeed = flag.Uint64("getcopy.seed", 0, "run TestGetMatchElementCopyAgrees on this seed alone")

// TestGetMatchElementCopyAgrees is the seeded property of the element
// copy: each seed writes functions that read a Map of slices through a
// pointer with a match on Get and then return the value, keep it in the
// Box, keep it and write to the kept copy, append to it, or only read it;
// main writes to what they return and prints the Map after each. Built
// with the element copy and with the reference copy of the receiver, the
// two programs must print the same. A failure prints the seed, the Ergo
// source and both outputs.
// Replay: go test -run '^TestGetMatchElementCopyAgrees$' ./internal/sketch/lower -args -getcopy.seed=N
//
// It builds Go because the question is whether a value read from the Map
// stays apart from it at run time, through every later copy decision the
// value rule makes; no in-process check observes that, and Ergo has no
// reference interpreter yet (test-speed T8).
func TestGetMatchElementCopyAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if *getCopySeed != 0 {
		seeds = []uint64{*getCopySeed}
	}
	uses := []string{"return", "keep", "keepWrite", "append", "read"}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	const funcs = 8
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6763))
		var b strings.Builder
		b.WriteString("type Box { m Map[int, []int], kept []int }\n\n")
		var calls strings.Builder
		for i := range funcs {
			use := uses[r.IntN(len(uses))]
			reached[use] = true
			arm := map[string]string{
				"return":    "\t\ts.Value\n",
				"keep":      "\t\tb.kept = s.Value\n\t\ts.Value\n",
				"keepWrite": "\t\tb.kept = s.Value\n\t\tb.kept[0] = 77\n\t\ts.Value\n",
				"append":    "\t\ts.Value.Append(9)\n",
				"read":      "\t\t[]int{len(s.Value), s.Value[0]}\n",
			}[use]
			fmt.Fprintf(&b, "func f%d(b *Box, k int) []int {\n\tmatch b.m.Get(k) {\n\tcase Some[[]int] as s:\n%s\tcase None:\n\t\t[]int{0}\n\t}\n}\n\n", i, arm)
			fmt.Fprintf(&calls, "\tlet mut r%d = f%d(&b, %d)\n\tr%d[0] = %d\n\tConsole.Println(\"f%d \\(list(r%d)) \\(list(b.kept)) \\(show(b.m))\")\n", i, i, 1+r.IntN(2), i, 100+i, i, i)
		}
		b.WriteString("func show(m Map[int, []int]) string {\n\tlet mut out = \"\"\n\tfor k in 1..3 {\n\t\tmatch m.Get(k) {\n\t\tcase Some[[]int] as s:\n\t\t\tout = out + \"\\(k)=\" + list(s.Value) + \" \"\n\t\tcase None:\n\t\t}\n\t}\n\tout\n}\n\n")
		b.WriteString("func list(xs []int) string {\n\tlet mut out = \"[\"\n\tfor x in xs {\n\t\tout = out + \"\\(x) \"\n\t}\n\tout + \"]\"\n}\n\n")
		b.WriteString("func main() needs Console {\n\tlet mut b = Box{m: Map[int, []int]{1: []int{1, 2}, 2: slices.WithCapacity[int](8).Append(3)}, kept: []int{}}\n")
		b.WriteString(calls.String())
		b.WriteString("}\n")
		src := "import slices\n\n" + b.String()
		sources[seed] = src
		programs = append(programs,
			lowerCopyMode(t, fmt.Sprintf("gc%don", seed), src, true),
			lowerCopyMode(t, fmt.Sprintf("gc%doff", seed), src, false))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the Get copy programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("gc%don", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("gc%doff", seed)])
		if on != off || onCode != 0 || offCode != 0 || strings.Count(on, "\n") != funcs {
			t.Errorf("seed %d: element copy and receiver copy differ\nReplay: go test -run '^TestGetMatchElementCopyAgrees$' ./internal/sketch/lower -args -getcopy.seed=%d\n%s\n--- element (exit %d)\n%s%s\n--- receiver (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *getCopySeed == 0 && len(reached) != len(uses) {
		t.Errorf("the fixed seeds reach %d of %d uses: %v", len(reached), len(uses), reached)
	}
}

// TestGetMatchAgrees is the seeded property of lw-get-commaok: each seed
// writes a program of getFuncs generated matches on m.Get(k) (every
// context, arm shape and arm order), lowers it with comma-ok and with the
// reference type switch, and the two programs must print the same for
// present and absent keys. The comma-ok build has no Get call in a fused
// shape and keeps it in the others; the reference keeps it everywhere. A
// failure prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestGetMatchAgrees$' ./internal/sketch/lower -args -getmatch.seed=N
//
// It builds Go because it compares the runs of built programs with a
// reference (another lowering, a fold, or Go's own package), and the
// question is behaviour at run time; no in-process check observes that, and
// Ergo has no reference interpreter yet (test-speed T8).
func TestGetMatchAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *getMatchSeed != 0 {
		seeds = []uint64{*getMatchSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	for _, seed := range seeds {
		g := &getGen{r: rand.New(rand.NewPCG(seed, 0x6765))}
		var b strings.Builder
		var shapes []getShape
		for i := range getFuncs {
			s := g.shape()
			shapes = append(shapes, s)
			reached["context "+s.context] = true
			reached["some "+s.some] = true
			if s.some != "both" {
				reached["none "+s.none] = true
			}
			reached[fmt.Sprintf("someFirst %v", s.someFirst)] = true
			b.WriteString(s.fn(i) + "\n")
		}
		b.WriteString("func main() needs Console {\n\tlet m = Map[string, int]{\"a\": 1, \"b\": 2, \"c\": 0}\n\tfor k in []string{\"a\", \"b\", \"c\", \"z\"} {\n")
		for i := range getFuncs {
			fmt.Fprintf(&b, "\t\tConsole.Println(\"\\(k) %d \\(f%d(m, k))\")\n", i, i)
		}
		b.WriteString("\t}\n}\n")
		src := b.String()
		sources[seed] = src
		on := lowerGetMode(t, fmt.Sprintf("gm%don", seed), src, true)
		off := lowerGetMode(t, fmt.Sprintf("gm%doff", seed), src, false)
		for i, s := range shapes {
			name := fmt.Sprintf("f%d", i)
			fn := goFunc(t, on.mainGo(), name)
			if s.fused() != (strings.Contains(fn, ":= m[k]") && !getCall.MatchString(fn)) {
				t.Errorf("seed %d, %s (%+v): fused %v, but the comma-ok build emits\n%s", seed, name, s, s.fused(), fn)
			}
			// One case takes both variants: one block, no test of ok.
			if s.fused() && (s.some == "both") == okTest.MatchString(fn) {
				t.Errorf("seed %d, %s (%+v): the test of ok does not match the arms:\n%s", seed, name, s, fn)
			}
			if ref := goFunc(t, off.mainGo(), name); !getCall.MatchString(ref) {
				t.Errorf("seed %d, %s: the reference does not call Get:\n%s", seed, name, ref)
			}
		}
		programs = append(programs, on, off)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the Get programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("gm%don", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("gm%doff", seed)])
		if on != off || onCode != 0 || offCode != 0 || strings.Count(on, "\n") != 4*getFuncs {
			t.Errorf("seed %d: comma-ok and reference differ\nReplay: go test -run '^TestGetMatchAgrees$' ./internal/sketch/lower -args -getmatch.seed=%d\n%s\n--- comma-ok (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	t.Logf("seeds %v: %d functions, %d shapes reached", seeds, getFuncs*len(seeds), len(reached))
	if *getMatchSeed == 0 {
		want := len(getContexts) + len(getSomes) + len(getNones) + 2
		if len(reached) != want {
			t.Errorf("the fixed seeds reach %d of %d shapes: %v", len(reached), want, reached)
		}
	}
}
