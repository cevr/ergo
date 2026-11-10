package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestDropGlueModel is the model-based run test of the generated release
// (#197 L8 B2; resources R3, R37, R39, Gap 36): a value whose type holds
// resources in its parts is released part by part when its owner's scope
// ends. Each seed generates a few types over a resource Res (records,
// some with a written Close of their own, slices, Maps, Option, a tuple, a
// union and a recursive record) and an owned value of one of them, built
// by a literal; some seeds take a field in place first (`v.f.Close()`),
// and some resources' Close is a defect. A Go reference model predicts
// the release order: the type's own Close, then the fields last declared
// first; a slice from the back; every Map value once (compared as a
// multiset); the active variant; a taken field skipped; after a defect the
// other parts still run, and the program ends with the defect, whose
// cause names every failing Close. The programs of a run are linked into
// one binary (buildAll). A failing seed prints the Ergo source, both
// traces and the replay command (ERGO_SEED=n).
//
// It builds Go because its subject is the emitted release code on its
// defect path: go/types cannot run it, and Ergo has no reference
// interpreter yet (test-speed T8).
//
// Mutants it catches that nothing else does (internal/sketch/lower
// mutants.json and check mutants.json, L8-B2): fields released in
// declaration order; the taken-field mask ignored; a Map value skipped;
// a slice released from the front; the first defect stops the rest; the
// type's own Close after its fields; a variant not released.
func TestDropGlueModel(t *testing.T) {
	needsBuild(t)
	seeds, first := 40, 1
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ERGO_SEED %q: %v", s, err)
		}
		first, seeds = n, 1
	}
	var cases []*dgCase
	var programs []*lowered
	for seed := first; seed < first+seeds; seed++ {
		c := genDropGlue(seed)
		cases = append(cases, c)
		programs = append(programs, mustLower(t, fmt.Sprintf("dropglue_%d", seed), c.src))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for i, c := range cases {
		seed := first + i
		cmd := exec.Command(exes[fmt.Sprintf("dropglue_%d", seed)])
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := runProgram(t, cmd)
		got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		if err != nil {
			got = append(got, "defect")
		}
		bad := !c.same(got)
		for _, id := range c.defects {
			if !strings.Contains(stderr.String(), fmt.Sprintf("close %d", id)) {
				bad = true
				t.Errorf("seed %d: the exit cause does not name the defect of close %d", seed, id)
			}
		}
		if bad {
			t.Errorf("seed %d (ERGO_SEED=%d replays)\n%s\ngot:\n%s\nwant:\n%s\nstderr:\n%s", seed, seed, numbered(c.src), strings.Join(got, "\n"), strings.Join(c.want, "\n"), stderr.String())
		}
	}
}

// dgCase is one seed's program and its expected output. Lines of ids in
// unordered (the values of a Map) are compared as a multiset; every other
// line in order.
type dgCase struct {
	src       string
	want      []string
	unordered map[string]bool
	defects   []int
}

func (c *dgCase) same(got []string) bool {
	if len(got) != len(c.want) {
		return false
	}
	var gm, wm []string
	var g, w []string
	for _, l := range got {
		if c.unordered[l] {
			gm = append(gm, l)
		} else {
			g = append(g, l)
		}
	}
	for _, l := range c.want {
		if c.unordered[l] {
			wm = append(wm, l)
		} else {
			w = append(w, l)
		}
	}
	slices.Sort(gm)
	slices.Sort(wm)
	return slices.Equal(g, w) && slices.Equal(gm, wm)
}

type dgKind uint8

const (
	dgRes dgKind = iota
	dgInt
	dgRec
	dgSlice
	dgMap
	dgOpt
	dgTuple
	dgUnion
	dgNode // the recursive record Node { id int, r Res, next []Node }
)

type dgType struct {
	kind   dgKind
	name   string    // a record's or union's name
	fields []*dgType // a record's fields f0…, a tuple's elements
	elem   *dgType   // slice, Map value, Option value, union variant
	closes bool      // a record with a written Close of its own
}

func (t *dgType) text() string {
	switch t.kind {
	case dgRes:
		return "Res"
	case dgInt:
		return "int"
	case dgRec, dgUnion:
		return t.name
	case dgNode:
		return "Node"
	case dgSlice:
		return "[]" + t.elem.text()
	case dgMap:
		return "Map[string, " + t.elem.text() + "]"
	case dgOpt:
		return "Option[" + t.elem.text() + "]"
	case dgTuple:
		return "(" + t.fields[0].text() + ", int, " + t.fields[1].text() + ")"
	}
	panic("dgType")
}

// holds reports a type that holds a Res.
func (t *dgType) holds() bool {
	switch t.kind {
	case dgRes, dgNode:
		return true
	case dgRec, dgTuple:
		for _, f := range t.fields {
			if f.holds() {
				return true
			}
		}
		return t.closes
	case dgSlice, dgMap, dgOpt, dgUnion:
		return t.elem.holds()
	}
	return false
}

type dgGen struct {
	r      *rand.Rand
	types  []*dgType
	decls  strings.Builder
	nextID int
	// unordered are the close lines of values inside a Map.
	unordered map[string]bool
	defects   []int
	// top is the closes of each field of the top record value.
	top [][]int
}

// typ picks a type for a part, from the plain int, Res, the types made so
// far and a fresh container of one of them.
func (g *dgGen) typ(depth int) *dgType {
	pick := g.r.IntN(9)
	if depth > 2 {
		pick = g.r.IntN(2)
	}
	switch pick {
	case 0:
		return &dgType{kind: dgInt}
	case 1, 2:
		return &dgType{kind: dgRes}
	case 3:
		if len(g.types) > 0 {
			return g.types[g.r.IntN(len(g.types))]
		}
		return &dgType{kind: dgRes}
	case 4:
		return &dgType{kind: dgSlice, elem: g.typ(depth + 1)}
	case 5:
		return &dgType{kind: dgMap, elem: g.typ(depth + 1)}
	case 6:
		return &dgType{kind: dgOpt, elem: g.typ(depth + 1)}
	case 7:
		return &dgType{kind: dgOpt, elem: &dgType{kind: dgRes}}
	default:
		return &dgType{kind: dgNode}
	}
}

// declare adds a record type (some with a Close of their own) or a union
// of a record and an empty variant.
func (g *dgGen) declare(i int) {
	if i > 0 && g.r.IntN(4) == 0 {
		v := g.types[g.r.IntN(len(g.types))]
		for v.kind != dgRec {
			v = g.types[g.r.IntN(len(g.types))]
		}
		t := &dgType{kind: dgUnion, name: fmt.Sprintf("U%d", i), elem: v}
		fmt.Fprintf(&g.decls, "type %s = %s | E%d{}\n\n", t.name, v.name, i)
		g.types = append(g.types, t)
		return
	}
	t := &dgType{kind: dgRec, name: fmt.Sprintf("T%d", i), closes: g.r.IntN(3) == 0}
	n := 1 + g.r.IntN(3)
	fields := []string{"id int"}
	for k := 0; k < n; k++ {
		f := g.typ(1)
		t.fields = append(t.fields, f)
		fields = append(fields, fmt.Sprintf("f%d %s", k, f.text()))
	}
	fmt.Fprintf(&g.decls, "type %s { %s }\n\n", t.name, strings.Join(fields, ", "))
	if t.closes {
		fmt.Fprintf(&g.decls, "func Close(x %s) needs Console { Console.Println(\"close \\(x.id)\") }\n\n", t.name)
	}
	g.types = append(g.types, t)
}

// value is a literal of t and the closes its release prints, in order.
func (g *dgGen) value(t *dgType, inMap bool, depth int) (string, []int) {
	id := func() int {
		g.nextID++
		if inMap {
			g.unordered[fmt.Sprintf("close %d", g.nextID)] = true
		}
		return g.nextID
	}
	switch t.kind {
	case dgInt:
		return strconv.Itoa(g.r.IntN(9)), nil
	case dgRes:
		n := id()
		bad := g.r.IntN(10) == 0
		if bad {
			g.defects = append(g.defects, n)
		}
		return fmt.Sprintf("Res{id: %d, bad: %t}", n, bad), []int{n}
	case dgRec:
		n := id()
		parts := []string{fmt.Sprintf("id: %d", n)}
		var rel []int
		if t.closes {
			rel = append(rel, n)
		}
		var fields [][]int
		for k, f := range t.fields {
			v, r := g.value(f, inMap, depth+1)
			parts = append(parts, fmt.Sprintf("f%d: %s", k, v))
			fields = append(fields, r)
		}
		for k := len(fields) - 1; k >= 0; k-- {
			rel = append(rel, fields[k]...)
		}
		if depth == 0 {
			g.top = fields
		}
		return t.name + "{" + strings.Join(parts, ", ") + "}", rel
	case dgNode:
		n := id()
		r, rr := g.value(&dgType{kind: dgRes}, inMap, depth+1)
		var next []string
		var rel []int
		if depth < 3 {
			for k := g.r.IntN(3); k > 0; k-- {
				v, vr := g.value(t, inMap, depth+1)
				next = append(next, v)
				rel = append(vr, rel...) // released from the back
			}
		}
		_ = n
		return fmt.Sprintf("Node{id: %d, r: %s, next: []Node{%s}}", n, r, strings.Join(next, ", ")), append(rel, rr...)
	case dgSlice:
		var xs []string
		var rel []int
		for k := g.r.IntN(4); k > 0; k-- {
			v, r := g.value(t.elem, inMap, depth+1)
			xs = append(xs, v)
			rel = append(r, rel...)
		}
		return t.text() + "{" + strings.Join(xs, ", ") + "}", rel
	case dgMap:
		var xs []string
		var rel []int
		for k := g.r.IntN(3); k > 0; k-- {
			v, r := g.value(t.elem, true, depth+1)
			xs = append(xs, fmt.Sprintf("\"k%d\": %s", k, v))
			rel = append(rel, r...)
		}
		return t.text() + "{" + strings.Join(xs, ", ") + "}", rel
	case dgOpt:
		if g.r.IntN(3) == 0 {
			return "None", nil
		}
		v, r := g.value(t.elem, inMap, depth+1)
		return "Some{Value: " + v + "}", r
	case dgTuple:
		a, ar := g.value(t.fields[0], inMap, depth+1)
		b, br := g.value(t.fields[1], inMap, depth+1)
		return "(" + a + ", 0, " + b + ")", append(br, ar...)
	case dgUnion:
		if g.r.IntN(3) == 0 {
			return strings.Replace(t.name, "U", "E", 1), nil
		}
		return g.value(t.elem, inMap, depth)
	}
	panic("dgType")
}

const dgPrelude = `type Res { id int, bad bool }

func Close(r Res) needs Console {
	Console.Println("close \(r.id)")
	if r.bad {
		Unreachable("close \(r.id)")
	}
}

type Node { id int, r Res, next []Node }

`

// genDropGlue is the program of one seed and its expected output.
func genDropGlue(seed int) *dgCase {
	r := rand.New(rand.NewPCG(uint64(seed), 2))
	g := &dgGen{r: r, unordered: map[string]bool{}}
	for i := 0; i < 1+r.IntN(4); i++ {
		g.declare(i)
	}
	top := g.types[len(g.types)-1]
	if top.kind == dgUnion && r.IntN(2) == 0 {
		top = &dgType{kind: dgSlice, elem: top}
	}
	if r.IntN(4) == 0 || !top.holds() {
		// A tuple (a local, not a record field: a field type is no tuple).
		top = &dgType{kind: dgTuple, fields: []*dgType{top, {kind: dgRes}}}
	}
	if r.IntN(4) == 0 {
		return g.queueCase(top)
	}
	lit, rel := g.value(top, false, 0)
	var body strings.Builder
	fmt.Fprintf(&body, "\tlet v %s = %s\n", top.text(), lit)
	var want []string
	// A field of the top record taken in place: closed now, skipped by the
	// release.
	if top.kind == dgRec {
		for k, f := range top.fields {
			if f.kind != dgRes || r.IntN(2) == 0 {
				continue
			}
			taken := g.top[k][0]
			fmt.Fprintf(&body, "\tv.f%d.Close()\n", k)
			want = append(want, fmt.Sprintf("close %d", taken))
			rel = slices.DeleteFunc(rel, func(n int) bool { return n == taken })
			if slices.Contains(g.defects, taken) {
				// The take's defect ends main; the release still runs.
				for _, n := range rel {
					want = append(want, fmt.Sprintf("close %d", n))
				}
				want = append(want, "defect")
				return &dgCase{src: dgSource(g, body.String()), want: want, unordered: g.unordered, defects: g.reached(want)}
			}
			break
		}
	}
	want = append(want, "end")
	for _, n := range rel {
		want = append(want, fmt.Sprintf("close %d", n))
	}
	if len(g.reached(want)) > 0 {
		want = append(want, "defect")
	}
	return &dgCase{src: dgSource(g, body.String()), want: want, unordered: g.unordered, defects: g.reached(want)}
}

// reached are the defects whose Close the output runs.
func (g *dgGen) reached(want []string) []int {
	var out []int
	for _, n := range g.defects {
		if slices.Contains(want, fmt.Sprintf("close %d", n)) {
			out = append(out, n)
		}
	}
	return out
}

func dgSource(g *dgGen, body string) string {
	imports := ""
	if strings.Contains(body, "queue.") {
		imports = "import queue\n\n"
	}
	return imports + dgPrelude + g.decls.String() + "func main() needs Console {\n" + body + "\tConsole.Println(\"end\")\n}\n"
}

// queueCase is a program that owns a queue of values of type t (#197 L8
// B3; Q7, R13): each value is pushed (Push takes it); some seeds end the
// queue and push once more, and that failed Push releases its value at
// once; the queue's release ends admission and releases the values left,
// last entered first.
func (g *dgGen) queueCase(t *dgType) *dgCase {
	n := g.r.IntN(4)
	var body strings.Builder
	fmt.Fprintf(&body, "\tlet q = queue.New[%s](%d)\n", t.text(), n+1)
	var left [][]int
	for k := 0; k < n; k++ {
		lit, rel := g.value(t, false, 1)
		fmt.Fprintf(&body, "\tlet e%d %s = %s\n\tq.Push(e%d) catch {\n\tcase queue.Closed:\n\t\tConsole.Println(\"closed\")\n\t}\n", k, t.text(), lit, k)
		left = append(left, rel)
	}
	var want []string
	closes := func(rel []int) {
		for _, id := range rel {
			want = append(want, fmt.Sprintf("close %d", id))
		}
	}
	if g.r.IntN(2) == 0 {
		lit, rel := g.value(t, false, 1)
		fmt.Fprintf(&body, "\tq.End()\n\tlet late %s = %s\n\tq.Push(late) catch {\n\tcase queue.Closed:\n\t\tConsole.Println(\"closed\")\n\t}\n", t.text(), lit)
		closes(rel)
		if len(g.reached(want)) > 0 {
			// The failed Push's release had a defect: it ends main, and
			// the queue's release still runs.
			for k := len(left) - 1; k >= 0; k-- {
				closes(left[k])
			}
			want = append(want, "defect")
			return &dgCase{src: dgSource(g, body.String()), want: want, unordered: g.unordered, defects: g.reached(want)}
		}
		want = append(want, "closed")
	}
	want = append(want, "end")
	for k := len(left) - 1; k >= 0; k-- {
		closes(left[k])
	}
	if len(g.reached(want)) > 0 {
		want = append(want, "defect")
	}
	return &dgCase{src: dgSource(g, body.String()), want: want, unordered: g.unordered, defects: g.reached(want)}
}
