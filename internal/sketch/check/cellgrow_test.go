package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// cgType is a type expression of the recursive-records model: a leaf
// (int, string, the cell type Bag, or a type parameter), a slice, an
// Option, or an instance of one of the program's records T0..Tn-1.
type cgType struct {
	kind string // "int", "string", "Bag", "param", "slice", "Option", "T"
	name string // the parameter's name, or the record's
	rec  int    // the record of a "T"
	args []cgType
}

func (t cgType) String() string {
	switch t.kind {
	case "param":
		return t.name
	case "slice":
		return "[]" + t.args[0].String()
	case "Option", "T":
		name := t.name
		if t.kind == "Option" {
			name = "Option"
		}
		if len(t.args) == 0 {
			return name
		}
		parts := make([]string, len(t.args))
		for i, a := range t.args {
			parts[i] = a.String()
		}
		return name + "[" + strings.Join(parts, ", ") + "]"
	}
	return t.kind
}

// cgProgram is a drawn program: records with zero to two type parameters
// whose fields name each other with type arguments that nest and swap
// parameters (CK-MAPGROW's shape), so some programs are instantiation
// cycles; Bag is a sealed provider cell, so some fields hold one.
type cgProgram struct {
	params [][]string // each record's type parameters
	fields [][]cgType // each record's field types
	lines  [][]int    // the line of each field
	src    string     // the Ergo text
	nodes  int        // the written type expressions
}

func cgDraw(rng *rand.Rand) cgProgram {
	var p cgProgram
	n := 1 + rng.Intn(3)
	for range n {
		p.params = append(p.params, []string{"A", "B"}[:rng.Intn(3)])
	}
	var expr func(ps []string, d int) cgType
	expr = func(ps []string, d int) cgType {
		p.nodes++
		if d >= 3 || rng.Intn(3) == 0 {
			switch k := rng.Intn(8); {
			case k == 0:
				return cgType{kind: "Bag"}
			case k < 4 && len(ps) > 0:
				return cgType{kind: "param", name: ps[rng.Intn(len(ps))]}
			case k < 6:
				return cgType{kind: "int"}
			default:
				return cgType{kind: "string"}
			}
		}
		switch rng.Intn(4) {
		case 0:
			return cgType{kind: "slice", args: []cgType{expr(ps, d+1)}}
		case 1:
			return cgType{kind: "Option", args: []cgType{expr(ps, d+1)}}
		}
		r := rng.Intn(n)
		t := cgType{kind: "T", name: "T" + strconv.Itoa(r), rec: r}
		for range p.params[r] {
			t.args = append(t.args, expr(ps, d+1))
		}
		return t
	}
	var b strings.Builder
	b.WriteString(`type Bag {
	mut Add(n int),
	seal(),
}

type Box { n int } implements Bag

func Add(b mut Box, n int) { b.n = b.n + n }
func seal(b Box) {}

func main() {}
`)
	line := strings.Count(b.String(), "\n") + 1
	p.fields = make([][]cgType, n)
	p.lines = make([][]int, n)
	for r := range n {
		head := "T" + strconv.Itoa(r)
		if len(p.params[r]) > 0 {
			head += "[" + strings.Join(p.params[r], ", ") + "]"
		}
		fmt.Fprintf(&b, "\ntype %s {\n", head)
		line += 2
		for f := range 1 + rng.Intn(3) {
			t := expr(p.params[r], 0)
			p.fields[r] = append(p.fields[r], t)
			p.lines[r] = append(p.lines[r], line)
			fmt.Fprintf(&b, "\tf%d %s,\n", f, t)
			line++
		}
		b.WriteString("}\n")
		line++
	}
	p.src = b.String()
	return p
}

// cgRefused is the model's answer: the lines whose field type instantiates
// a generic type (Option or a record with parameters) with an argument
// that holds the cell. A type holds it when it is Bag, or an element, an
// argument or its record's declared fields (parameters holding nothing)
// hold it: a least fixpoint over the records.
func cgRefused(p cgProgram) []int {
	decl := make([]bool, len(p.fields))
	var holds func(t cgType) bool
	holds = func(t cgType) bool {
		switch t.kind {
		case "Bag":
			return true
		case "slice", "Option":
			return holds(t.args[0])
		case "T":
			return decl[t.rec] || slices.ContainsFunc(t.args, holds)
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		for r, fs := range p.fields {
			if !decl[r] && slices.ContainsFunc(fs, holds) {
				decl[r], changed = true, true
			}
		}
	}
	var refused func(t cgType) bool
	refused = func(t cgType) bool {
		if (t.kind == "Option" || t.kind == "T") && slices.ContainsFunc(t.args, holds) {
			return true
		}
		return slices.ContainsFunc(t.args, refused)
	}
	var out []int
	for r, fs := range p.fields {
		for i, t := range fs {
			if refused(t) {
				out = append(out, p.lines[r][i])
			}
		}
	}
	return out
}

// TestCellWalkRecursiveRecords (CK-MAPGROW): over drawn recursive generic
// records, the check ends with the cell walk (holdsCell) within a counted
// budget proportional to the written types, and refuses exactly the type
// arguments the model says hold a cell. It counts steps, never time; the
// checker stops the walk past the budget, so a walk that grows fails here
// instead of hanging. ERGO_CELLGROW_SEED=N replays one seed.
func TestCellWalkRecursiveRecords(t *testing.T) {
	seeds := []int64{}
	if s := os.Getenv("ERGO_CELLGROW_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 400; i++ {
			seeds = append(seeds, i)
		}
	}
	worst := 0
	var reached struct{ cycles, compared, refused int }
	for _, seed := range seeds {
		p := cgDraw(rand.New(rand.NewSource(seed)))
		budget := cgStepsPerNode * p.nodes
		type result struct {
			steps int
			ds    []check.Diagnostic
		}
		done := make(chan result, 1)
		go func() {
			steps, ds := check.CellSteps(p.src, budget)
			done <- result{steps, ds}
		}()
		var r result
		select {
		case r = <-done:
		case <-testwait.After(t):
			t.Fatalf("seed %d: the check did not end by the test's deadline\nreplay: ERGO_CELLGROW_SEED=%d go test -run TestCellWalkRecursiveRecords ./internal/sketch/check\n%s", seed, seed, p.src)
		}
		worst = max(worst, (r.steps+p.nodes-1)/p.nodes)
		replay := fmt.Sprintf("replay: ERGO_CELLGROW_SEED=%d go test -run TestCellWalkRecursiveRecords ./internal/sketch/check\n%s", seed, p.src)
		if r.steps > budget {
			t.Fatalf("seed %d: the cell walk took over %d steps (%d per written type) for %d written types\n%s", seed, budget, cgStepsPerNode, p.nodes, replay)
		}
		var got []int
		other := false
		for _, d := range r.ds {
			switch {
			case strings.Contains(d.Message, "holds the provider cell Bag"):
				if !slices.Contains(got, d.Line) {
					got = append(got, d.Line)
				}
			case strings.Contains(d.Message, "instantiation cycle"):
				reached.cycles++
				other = true
			default:
				other = true
			}
		}
		if other {
			// An instantiation cycle or a record that contains itself
			// by value is refused as one; what its arguments hold is
			// not decided.
			continue
		}
		reached.compared++
		slices.Sort(got)
		want := cgRefused(p)
		if len(want) > 0 {
			reached.refused++
		}
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: refused lines %v, want %v\ndiagnostics: %v\n%s", seed, got, want, r.ds, replay)
		}
	}
	t.Logf("worst: %d steps per written type; reached %+v", worst, reached)
	if len(seeds) > 1 && (reached.cycles == 0 || reached.compared == 0 || reached.refused == 0) {
		t.Fatalf("the draws reach no instantiation cycle, no compared program or no refusal: %+v", reached)
	}
}

// cgStepsPerNode is the walk's budget per written type expression.
const cgStepsPerNode = 64
