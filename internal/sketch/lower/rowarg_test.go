package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var raSeed = flag.Uint64("ra.seed", 0, "run TestRowParamBindsFromTypeArg on this seed alone")

// raShape is one generated program of TestRowParamBindsFromTypeArg: a
// generic union Box whose failure parameter F holds a row of one to three
// error types, read back by a generic function whose own failure row is F
// (types.md, Generics 6; rows.md, Error rows 8; CK-rowarg).
type raShape struct {
	errs    int    // error types E0 … E(errs-1)
	hasID   []bool // Ek has a field ID
	row     []int  // the row's members, in the order written
	fFirst  bool   // Box is declared Box[F, T], else Box[T, F]
	getFT   bool   // get is declared get[F, T], else get[T, F]
	written string // the row as written: "E0 | E2"
}

func newRAShape(r *rand.Rand) raShape {
	s := raShape{errs: 1 + r.IntN(3), fFirst: r.IntN(2) == 0, getFT: r.IntN(2) == 0}
	for range s.errs {
		s.hasID = append(s.hasID, r.IntN(3) > 0)
	}
	s.row = r.Perm(s.errs)[:1+r.IntN(s.errs)]
	var names []string
	for _, m := range s.row {
		names = append(names, fmt.Sprintf("E%d", m))
	}
	s.written = strings.Join(names, " | ")
	return s
}

// box is the type Box with T and F given, in Box's declared order.
func (s raShape) box(t, f string) string {
	if s.fFirst {
		return "Box[" + f + ", " + t + "]"
	}
	return "Box[" + t + ", " + f + "]"
}

// failure is what a caller prints for the error type Ek holding id.
func (s raShape) failure(k, id int) string {
	if s.hasID[k] {
		return fmt.Sprintf("e%d %d", k, id)
	}
	return fmt.Sprintf("e%d", k)
}

// errValue is an Ergo value of Ek holding id.
func (s raShape) errValue(k, id int) string {
	if s.hasID[k] {
		return fmt.Sprintf("E%d{ID: %d}", k, id)
	}
	return fmt.Sprintf("E%d", k)
}

// program is the generated source and, line by line, the output the model
// gives it: each value read by the call without type arguments, the call
// with written ones and a function written without generics.
func (s raShape) program() (string, string) {
	var b, want strings.Builder
	bt := s.box("int", s.written)
	for k := range s.errs {
		if s.hasID[k] {
			fmt.Fprintf(&b, "type E%d { ID int }\n", k)
		} else {
			fmt.Fprintf(&b, "type E%d {}\n", k)
		}
	}
	b.WriteString("type Pend {}\n\n")
	if s.fFirst {
		b.WriteString("type Box[F, T] = Idle{} | Got{Value T} | Bad{Error F}\n\n")
	} else {
		b.WriteString("type Box[T, F] = Idle{} | Got{Value T} | Bad{Error F}\n\n")
	}
	tps := "[T, F]"
	if s.getFT {
		tps = "[F, T]"
	}
	fmt.Fprintf(&b, "func get%s(b %s) T fails F | Pend {\n\tmatch b {\n\tcase Idle:\n\t\treturn Pend\n\tcase Got as g:\n\t\treturn g.Value\n\tcase Bad as e:\n\t\treturn e.Error\n\t}\n}\n\n", tps, s.box("T", "F"))
	// put writes a caught failure into the box: the Slot row's value goes
	// into a field of its Go type parameter.
	fmt.Fprintf(&b, "func put%s(f func() T fails F) %s {\n\tlet v = f() catch {\n\tcase err:\n\t\treturn Bad[F]{Error: err}\n\t}\n\treturn Got[T]{Value: v}\n}\n\n", tps, s.box("T", "F"))
	// src fails with the row's i-th member, or returns i*10 past them.
	fmt.Fprintf(&b, "func src(i int) int fails %s {\n", s.written)
	for i, m := range s.row {
		fmt.Fprintf(&b, "\tif i == %d {\n\t\treturn %s\n\t}\n", i, s.errValue(m, 100+i))
	}
	b.WriteString("\treturn i * 10\n}\n\n")
	written := "[int, " + s.written + "]"
	if s.getFT {
		written = "[" + s.written + ", int]"
	}
	fmt.Fprintf(&b, "func viaInfer(b %s) int fails %s | Pend { try get(b) }\n\n", bt, s.written)
	fmt.Fprintf(&b, "func viaWritten(b %s) int fails %s | Pend { try get%s(b) }\n\n", bt, s.written, written)
	fmt.Fprintf(&b, "func viaMono(b %s) int fails %s | Pend {\n\tmatch b {\n\tcase Idle:\n\t\treturn Pend\n\tcase Got as g:\n\t\treturn g.Value\n\tcase Bad as e:\n\t\treturn e.Error\n\t}\n}\n\n", bt, s.written)
	arms := "\tcase Pend:\n\t\t\"pending\"\n"
	for _, m := range s.row {
		if s.hasID[m] {
			arms += fmt.Sprintf("\tcase E%d as e:\n\t\t\"e%d \\(e.ID)\"\n", m, m)
		} else {
			arms += fmt.Sprintf("\tcase E%d:\n\t\t\"e%d\"\n", m, m)
		}
	}
	for _, form := range []string{"Infer", "Written", "Mono"} {
		fmt.Fprintf(&b, "func text%s(b %s) string fails %s | Pend {\n\tlet n = try via%s(b)\n\treturn \"\\(n)\"\n}\n\n", form, bt, s.written, form)
	}
	fmt.Fprintf(&b, "func report(b %s) needs Console {\n", bt)
	for _, form := range []string{"Infer", "Written", "Mono"} {
		fmt.Fprintf(&b, "\tlet s%s = text%s(b) catch {\n%s\t}\n", form, form, arms)
		fmt.Fprintf(&b, "\tConsole.Println(\"%s \\(s%s)\")\n", strings.ToLower(form), form)
	}
	one := len(s.row) == 1 && s.hasID[s.row[0]]
	if one {
		// A row of one member is its member's type: its field reads.
		b.WriteString("\tmatch b {\n\tcase Bad as e:\n\t\tConsole.Println(\"field \\(e.Error.ID)\")\n\tcase _:\n\t}\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("func main() needs Console {\n")
	emit := func(val string, out string, id int, bad bool) {
		fmt.Fprintf(&b, "\treport(%s)\n", val)
		for _, form := range []string{"infer", "written", "mono"} {
			fmt.Fprintf(&want, "%s %s\n", form, out)
		}
		if one && bad {
			fmt.Fprintf(&want, "field %d\n", id)
		}
	}
	emit("Idle", "pending", 0, false)
	emit("Got[int]{Value: 7}", "7", 0, false)
	for i, m := range s.row {
		emit("Bad["+s.written+"]{Error: "+s.errValue(m, i)+"}", s.failure(m, i), i, true)
	}
	for i := range len(s.row) + 1 {
		if i < len(s.row) {
			emit(fmt.Sprintf("put() { try src(%d) }", i), s.failure(s.row[i], 100+i), 100+i, true)
			// The box put builds is the one written as a literal.
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(put() { try src(%d) } == Bad[%s]{Error: %s})\")\n", i, s.written, s.errValue(s.row[i], 100+i))
			want.WriteString("true\n")
		} else {
			emit(fmt.Sprintf("put() { try src(%d) }", i), fmt.Sprint(i*10), 0, false)
		}
	}
	b.WriteString("}\n")
	return b.String(), want.String()
}

// raProgram is one lowered program of the property, with its model output.
type raProgram struct {
	o       *lowered
	seed    uint64
	members int
	src     string
	want    string
}

// raPrograms generates and lowers the property's programs, failing the
// test with the source and the replay command for one that does not check
// or lower.
func raPrograms(t *testing.T, test string) []raProgram {
	seeds := make([]uint64, 0, 40)
	for i := range uint64(40) {
		seeds = append(seeds, i+1)
	}
	if *raSeed != 0 {
		seeds = []uint64{*raSeed}
	}
	var out []raProgram
	reached := map[string]bool{}
	for _, seed := range seeds {
		s := newRAShape(rand.New(rand.NewPCG(seed, 0x7261)))
		reached[fmt.Sprintf("members %d", len(s.row))] = true
		reached[fmt.Sprintf("fFirst %v", s.fFirst)] = true
		reached[fmt.Sprintf("getFT %v", s.getFT)] = true
		src, want := s.program()
		o := lowerSource(fmt.Sprintf("ra%d", seed), src, true)
		if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
			t.Fatalf("seed %d: does not lower: %v %v %v %+v\n%s\nReplay: go test -run '^%s$' ./internal/sketch/lower -args -ra.seed=%d", seed, o.err, o.diags, o.panicked, o.refused, src, test, seed)
		}
		out = append(out, raProgram{o: o, seed: seed, members: len(s.row), src: src, want: want})
	}
	if *raSeed == 0 {
		for _, k := range []string{"members 1", "members 2", "members 3", "fFirst true", "fFirst false", "getFT true", "getFT false"} {
			if !reached[k] {
				t.Errorf("the seeds never reach %s (reached %v)", k, reached)
			}
		}
	}
	return out
}

// TestRowParamBindsFromTypeArg is CK-rowarg's property (types.md,
// Generics 6 and 8; rows.md, Error rows 8): over generated generic unions
// whose failure parameter holds a row of one to three error types, in
// either parameter order, the call of a generic reader with no type
// arguments, the call with written ones and a reader written without
// generics all check, and lower to Go that type-checks. A row of one
// member is its member's type: Bad[E0] is one type whether E0 is written
// as a type or bound as a row, so its field reads, and a box built through
// a row parameter is compared with ==. TestRowParamBindsFromTypeArgRuns
// runs them. A failing seed prints the source and the replay command.
func TestRowParamBindsFromTypeArg(t *testing.T) {
	var programs []*lowered
	for _, p := range raPrograms(t, "TestRowParamBindsFromTypeArg") {
		programs = append(programs, p.o)
	}
	typecheckGo(t, programs)
}

// TestRowParamBindsFromTypeArgRuns runs the first program of each row size
// of TestRowParamBindsFromTypeArg and compares its output with the model:
// each value read the three ways gives the same line, and a box put builds
// equals the literal.
//
// Builds Go because the defect it guards is only seen running: a match in
// a body whose Go instance names error where the caller's names the
// member type-checks and finds no case (the inlined slot row).
func TestRowParamBindsFromTypeArgRuns(t *testing.T) {
	needsBuild(t)
	var run []raProgram
	sizes := map[int]bool{}
	for _, p := range raPrograms(t, "TestRowParamBindsFromTypeArgRuns") {
		if !sizes[p.members] {
			sizes[p.members] = true
			run = append(run, p)
		}
	}
	var programs []*lowered
	for _, p := range run {
		programs = append(programs, p.o)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the row argument programs do not build")
	}
	for _, p := range run {
		stdout, stderr, code := runExe(t, exes[p.o.name])
		if code != 0 || stdout != p.want {
			t.Errorf("seed %d: exit %d\n%s\ngot:\n%s\nwant:\n%s\n%s\nReplay: go test -run '^TestRowParamBindsFromTypeArgRuns$' ./internal/sketch/lower -args -ra.seed=%d", p.seed, code, firstLines(stderr, 8), stdout, p.want, p.src, p.seed)
		}
	}
}
