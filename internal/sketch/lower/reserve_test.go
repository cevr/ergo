package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var reserveSeed = flag.Uint64("reserve.seed", 0, "run TestReserveAgrees on this seed alone")

// The shapes TestReserveAgrees draws. A domain is what the loop ranges
// over: the slice parameter, a field of a local record, a package let,
// the Map parameter, a Range, a string, a call, the target itself, or a
// field reached through a pointer. An
// init is the target's first value: empty, two elements, or the slice
// parameter (sharing its array until the value rule copies). A body is
// the loop's body: the append alone, after a let, inside an if, after a
// conditional break, continue or return, twice, followed by an element
// write of the target, or with a reassignment of it.
var (
	reserveDomains = []string{"slice", "field", "pkg", "map", "range", "string", "call", "self", "pointer"}
	reserveInits   = []string{"empty", "pre", "copy"}
	reserveBodies  = []string{"plain", "before", "cond", "break", "continue", "return", "twice", "write", "reassign"}
)

type reserveShape struct{ domain, init, body string }

// reserves is the lowering's promise (reserve.go): a slice or Map path
// domain that does not mention the target, and a body that appends once
// per iteration.
func (s reserveShape) reserves() bool {
	switch s.domain {
	case "slice", "field", "pkg", "map":
	default:
		return false
	}
	return s.body == "plain" || s.body == "before"
}

// fn is function f<i> of shape s: it builds out from its domain and
// returns it.
func (s reserveShape) fn(i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func f%d(xs []int, m Map[string, int], n int) []int {\n", i)
	switch s.init {
	case "empty":
		b.WriteString("\tlet mut out = []int{}\n")
	case "pre":
		b.WriteString("\tlet mut out = []int{7, 8}\n")
	case "copy":
		b.WriteString("\tlet mut out = xs\n")
	}
	head, elem := "", "v"
	switch s.domain {
	case "slice":
		head = "for v in xs {"
	case "field":
		b.WriteString("\tlet r = Box{xs: xs}\n")
		head = "for v in r.xs {"
	case "pkg":
		head = "for v in Base {"
	case "map":
		head = "for _, v in m {"
	case "range":
		head = "for v in 0..n {"
	case "string":
		head, elem = "for c in \"aé\" {", "int(c)"
	case "call":
		head = "for v in twice(xs) {"
	case "self":
		head = "for v in out {"
	case "pointer":
		b.WriteString("\tlet mut box = Box{xs: xs}\n\tlet p = &box\n")
		head = "for v in p.xs {"
	}
	b.WriteString("\t" + head + "\n")
	if elem != "v" {
		b.WriteString("\t\tlet v = " + elem + "\n")
	}
	add := "\t\tout = out.Append(v * 3 + 1)\n"
	switch s.body {
	case "plain":
		b.WriteString(add)
	case "before":
		b.WriteString("\t\tlet w = v + 2\n\t\tout = out.Append(w)\n")
	case "cond":
		b.WriteString("\t\tif v % 2 == 0 {\n\t" + add + "\t\t}\n")
	case "break":
		b.WriteString("\t\tif v > 4 {\n\t\t\tbreak\n\t\t}\n" + add)
	case "continue":
		b.WriteString("\t\tif v == 1 {\n\t\t\tcontinue\n\t\t}\n" + add)
	case "return":
		b.WriteString("\t\tif v == 9 {\n\t\t\treturn out\n\t\t}\n" + add)
	case "twice":
		b.WriteString(add + "\t\tout = out.Append(v)\n")
	case "write":
		b.WriteString(add + "\t\tout[0] = out[0] + v\n")
	case "reassign":
		b.WriteString("\t\tif v == 5 {\n\t\t\tout = []int{v}\n\t\t}\n" + add)
	}
	b.WriteString("\t}\n\tout\n}\n")
	return b.String()
}

const reserveFuncs = 12

const reservePrelude = `type Box { xs []int }

let Base = []int{2, 7, 1, 8}

func twice(xs []int) []int {
	let mut out = []int{}
	for x in xs {
		out = out.Append(x)
		out = out.Append(x)
	}
	out
}

// digest is xs's length, sum and, when order counts, an order-sensitive
// hash: a Map's range order is not fixed, so its loops print the sum.
func digest(xs []int, inOrder bool) string {
	let mut sum = 0
	let mut h = 0
	for x in xs {
		sum = sum + x
		h = h * 31 + x
	}
	if inOrder { "\(len(xs)) \(sum) \(h)" } else { "\(len(xs)) \(sum)" }
}
`

// TestReserveAgrees: a for-in that appends to a local once per iteration
// reserves its room first (reserve.go) exactly when its shape qualifies,
// and the program prints the same lines as under the reference lowering,
// which reserves nothing. Each seed draws reserveFuncs functions over
// every domain, init and body; main runs each on a slice, a Map and a
// count, and on empty ones. A failing seed prints the source, both
// outputs and the replay command.
//
// It builds Go because the question is what the reservation changes at
// run time (an element write seen through a shared array, a slice the
// value rule copies), which go/types cannot answer; each program runs
// under the kit's memory limit (runProgram).
//
// Mutants it kills (testdata/mutants.json): gx2-reserve-any-domain,
// gx2-reserve-escapes, gx2-reserve-twice, gx2-reserve-other-assign,
// gx2-reserve-self-domain, gx2-reserve-call-domain, gx2-reserve-never.
func TestReserveAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *reserveSeed != 0 {
		seeds = []uint64{*reserveSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7265))
		var b strings.Builder
		b.WriteString(reservePrelude)
		var shapes []reserveShape
		for i := range reserveFuncs {
			s := reserveShape{
				domain: reserveDomains[r.IntN(len(reserveDomains))],
				init:   reserveInits[r.IntN(len(reserveInits))],
				body:   reserveBodies[r.IntN(len(reserveBodies))],
			}
			// Half the functions take a qualifying body, so both
			// verdicts are reached at every seed.
			if i%2 == 0 {
				s.body = []string{"plain", "before"}[r.IntN(2)]
			}
			shapes = append(shapes, s)
			reached["domain "+s.domain], reached["init "+s.init], reached["body "+s.body] = true, true, true
			reached[fmt.Sprintf("reserves %v", s.reserves())] = true
			b.WriteString("\n" + s.fn(i))
		}
		b.WriteString("\nfunc main() needs Console {\n\tlet xs = []int{3, 1, 4, 1, 5, 9, 2, 6}\n\tlet m = Map[string, int]{\"a\": 1, \"b\": 2, \"c\": 3}\n")
		for i, s := range shapes {
			ordered := s.domain != "map"
			fmt.Fprintf(&b, "\tConsole.Println(\"f%d \\(digest(f%d(xs, m, 5), %v))\")\n", i, i, ordered)
			fmt.Fprintf(&b, "\tConsole.Println(\"f%d \\(digest(f%d([]int{}, Map[string, int]{}, 0), %v))\")\n", i, i, ordered)
		}
		b.WriteString("}\n")
		src := b.String()
		sources[seed] = src
		on := lowerReserveMode(t, fmt.Sprintf("rs%don", seed), src, true)
		off := lowerReserveMode(t, fmt.Sprintf("rs%doff", seed), src, false)
		for i, s := range shapes {
			name := fmt.Sprintf("f%d", i)
			if got := strings.Contains(reserveBody(t, on.mainGo(), name), ".Reserve("); got != s.reserves() {
				t.Errorf("seed %d, %s (%+v): reserves %v, want %v:\n%s", seed, name, s, got, s.reserves(), reserveBody(t, on.mainGo(), name))
			}
			if strings.Contains(reserveBody(t, off.mainGo(), name), ".Reserve(") {
				t.Errorf("seed %d, %s: the reference reserves", seed, name)
			}
		}
		programs = append(programs, on, off)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the reserve programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("rs%don", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("rs%doff", seed)])
		if on != off || onCode != 0 || offCode != 0 || strings.Count(on, "\n") != 2*reserveFuncs {
			t.Errorf("seed %d: reserving and reference differ\nReplay: go test -run '^TestReserveAgrees$' ./internal/sketch/lower -args -reserve.seed=%d\n%s\n--- reserving (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if len(seeds) > 1 {
		for p, k := range map[string][]string{"domain ": reserveDomains, "init ": reserveInits, "body ": reserveBodies, "reserves ": {"true", "false"}} {
			for _, v := range k {
				if !reached[p+v] {
					t.Errorf("seeds %v reach no %s%s", seeds, p, v)
				}
			}
		}
	}
	t.Logf("seeds %v: %d functions, %d shapes reached", seeds, reserveFuncs*len(seeds), len(reached))
}

// reserveBody is the Go body of f<i>: its Owned form when the lowering
// split it for an owned parameter (V5), else the function itself.
func reserveBody(t *testing.T, code, name string) string {
	t.Helper()
	if strings.Contains(code, "\nfunc "+name+"Owned(") {
		return goFunc(t, code, name+"Owned")
	}
	return goFunc(t, code, name)
}

func lowerReserveMode(t *testing.T, name, src string, on bool) *lowered {
	t.Helper()
	saved := reserveLowering
	defer func() { reserveLowering = saved }()
	reserveLowering = ""
	if !on {
		reserveLowering = "off"
	}
	return mustLower(t, name, src)
}
