package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Provider cells (types.md, Interface assignability 6; cells.go).

// cellLib is a package that seals Bag (seal is unexported) and provides it
// through Box, whose Add takes *Box; Open has no unexported operation.
const cellLib = `type Bag {
	..Open,
	// seal seals Bag to this package.
	seal(),
}

type Open {
	Add(n int),
	Total() int,
}

type Box { n int } implements Bag, Open

func NewBox() Box { Box{n: 0} }
func Add(b *Box, n int) { b.n = b.n + n }
func Total(b Box) int { b.n }
func seal(b Box) {}
`

// cellLibImporter serves package lib and the standard library.
type cellLibImporter struct{ lib *check.Package }

func (i cellLibImporter) Import(path string) (*check.Package, error) {
	if path == "lib" {
		return i.lib, nil
	}
	return check.Std.Import(path)
}

func checkCellSrc(t *testing.T, path, src string, imp check.Importer) []check.Diagnostic {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	_, ds := check.Check(path, []*syntax.File{f}, imp)
	return ds
}

// TestProviderCellDiagnostics: the text and position of each refusal of a
// cell conversion, and the forms a cell admits. Rows: diagnostic text.
func TestProviderCellDiagnostics(t *testing.T) {
	f, err := syntax.Parse(cellLib)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{f}, nil)
	if len(ds) > 0 {
		t.Fatalf("lib: %v", ds)
	}
	// Kept holds a resource; Bag with Close releases it, Bag without does
	// not (R22, R23). Each is lines 1 to 12.
	resource := func(close bool) string {
		op, fn := "", ""
		if close {
			op, fn = "\t..Closer,\n", "func Close(k Kept) takes k {}\n"
		} else {
			op, fn = "\tTotal() int,\n", "func Total(k Kept) int { k.n }\n"
		}
		return "type Bag {\n\tAdd(n int),\n" + op + "\tseal(),\n}\ntype Res { id int } implements Closer\nfunc Close(r Res) {}\ntype Kept { r Res, n int } implements Bag\nfunc Add(k *Kept, n int) { k.n = k.n + n }\n" + fn + "func seal(k Kept) {}\n"
	}
	for _, row := range []struct {
		name, src string
		lib       bool   // imports lib; otherwise cellLib is the package itself
		line      int    // of the one diagnostic; 0: none
		want      string // a part of its message
	}{
		// The forms a cell admits: a let, an argument and a result.
		{name: "admitted", src: cellLib + "func use(b Bag) int { b.Add(1)\n b.Total() }\nfunc make() Bag { NewBox() }\nfunc main() { let c Bag = NewBox()\n c.Add(2)\n let _ = use(NewBox()) + c.Total() + make().Total() }\n"},
		{name: "open type", src: cellLib + "func main() { let o Open = NewBox()\n let _ = o }\n", line: 18, want: "a cell needs an operation type sealed in its provider's package (an unexported operation); Open has none"},
		{name: "foreign site", lib: true, src: "import lib\nfunc main() { let c lib.Bag = lib.NewBox()\n let _ = c }\n", line: 2, want: "a cell of lib.Bag is made only in package lib, which seals it"},
		{name: "context provider", src: cellLib + "let W = context { Bag: NewBox() }\nfunc main() { }\n", line: 18, want: "the provider of a with is shared by the tasks of its block"},
		{name: "hidden in another operation type", src: cellLib + "func main() { let c Bag = NewBox()\n let o Open = c\n let _ = o }\n", line: 19, want: "Bag holds the provider cell Bag, and Open is no cell type"},
		{name: "package let", src: cellLib + "let shared Bag = NewBox()\nfunc main() { }\n", line: 18, want: "the package let shared holds the provider cell Bag"},
		{name: "resource into no Close", src: resource(false) + "func main() { let c Bag = Kept{r: Res{id: 1}, n: 0}\n let _ = c }\n", line: 12, want: "Kept is a resource and Bag has no Close"},
		{name: "use after move", src: resource(true) + "func main() { let k = Kept{r: Res{id: 1}, n: 0}\n let c Bag = k\n let n = k.n\n let _ = n\n c.Add(1) }\n", line: 14, want: "k was handed off"},
	} {
		t.Run(row.name, func(t *testing.T) {
			var ds []check.Diagnostic
			if row.lib {
				ds = checkCellSrc(t, "main", row.src, cellLibImporter{lib})
			} else {
				ds = checkCellSrc(t, "main", row.src, nil)
			}
			if row.line == 0 {
				if len(ds) > 0 {
					t.Fatalf("want no diagnostic, got %v\n%s", ds, row.src)
				}
				return
			}
			if len(ds) != 1 || ds[0].Line != row.line || !strings.Contains(ds[0].Message, row.want) {
				t.Fatalf("want one diagnostic on line %d containing %q, got %v\n%s", row.line, row.want, ds, row.src)
			}
		})
	}
}

// cellHolder is a type of the confinement model: its Ergo text, a value of
// it written from c (a cell) and b (a provider value), and whether it
// holds a cell.
type cellHolder struct {
	typ, value string
	holds      bool
}

var cellHolders = []cellHolder{
	{"Bag", "c", true},
	{"Holder", "Holder{c: c, n: 1}", true},
	{"[]Bag", "[]Bag{c}", true},
	{"(Bag, int)", "(c, 1)", true},
	{"Shape", "Round{c: c}", true},
	{"[]Holder", "[]Holder{Holder{c: c, n: 2}}", true},
	{"Box", "b", false},
	{"Plain", "Plain{b: b}", false},
	{"[]int", "[]int{1}", false},
	{"int", "3", false},
}

// cellUse is a use of a local of the model: its text around the local x,
// and whether it may take the value to another task.
type cellUse struct {
	form    string
	crosses bool
}

var cellUses = []cellUse{
	{"let t%d = task.Go() {\n\t\tlet _ = x%d\n\t}\n\tt%d.Wait()", true},
	{"keep(func() {\n\t\tlet _ = x%d\n\t})", true},
	{"let _ = id(x%d)", true},
	{"inPlace(func() {\n\t\tlet _ = x%d\n\t})", false},
	{"let g%d = func() {\n\t\tlet _ = x%d\n\t}\n\tg%d()", false},
	{"let _ = x%d", false},
}

// cellConfinementProgram draws a program of the model: lets of drawn
// holder types and drawn uses of them. want maps each line that a correct
// checker refuses to the local it names.
func cellConfinementProgram(rng *rand.Rand) (src string, want map[int]string) {
	var b strings.Builder
	b.WriteString(`import task

type Bag {
	Add(n int),
	seal(),
}

type Box { n int } implements Bag

func Add(b *Box, n int) { b.n = b.n + n }
func seal(b Box) {}

type Holder { c Bag, n int }
type Round { c Bag }
type Square { n int }
type Shape = Round | Square
type Plain { b Box }

func id[T](x T) T { x }
func keep(f escaping func()) { f() }
func inPlace(f func()) { f() }

func main() {
	let b = Box{n: 0}
	let c Bag = Box{n: 1}
`)
	line := strings.Count(b.String(), "\n") + 1
	want = map[int]string{}
	emit := func(s string) {
		b.WriteString("\t" + s + "\n")
		line += strings.Count(s, "\n") + 1
	}
	for i := range 3 + rng.Intn(5) {
		h := cellHolders[rng.Intn(len(cellHolders))]
		emit(fmt.Sprintf("let x%d %s = %s", i, h.typ, h.value))
		u := cellUses[rng.Intn(len(cellUses))]
		text := strings.ReplaceAll(u.form, "%d", strconv.Itoa(i))
		if h.holds && u.crosses {
			// The diagnostic is at the use of x: the line that names it.
			at := line
			for j, l := range strings.Split(text, "\n") {
				if strings.Contains(l, "x"+strconv.Itoa(i)) {
					at = line + j
				}
			}
			want[at] = "x" + strconv.Itoa(i)
		}
		emit(text)
	}
	// A task that captures only the provider value: no cell, no refusal.
	emit("let tb = task.Go() {\n\t\tlet _ = b\n\t}\n\ttb.Wait()\n\tlet _ = c")
	b.WriteString("}\n")
	return b.String(), want
}

// TestProviderCellConfinementModel: a cell, and a value that holds one in
// a field, element, variant or tuple, is refused where it may reach
// another task (a task.Go block, an escaping literal, a type argument) and
// nowhere else: not in a non-escaping literal, a local frame or a let.
// The model is the table of holders and uses above. ERGO_CELL_SEED=N
// replays one seed.
func TestProviderCellConfinementModel(t *testing.T) {
	seeds := []int64{}
	if s := os.Getenv("ERGO_CELL_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 40; i++ {
			seeds = append(seeds, i)
		}
	}
	for _, seed := range seeds {
		src, want := cellConfinementProgram(rand.New(rand.NewSource(seed)))
		ds := checkCellSrc(t, "main", src, nil)
		got := map[int]string{}
		bad := false
		for _, d := range ds {
			if d.Code != "S108" || !strings.Contains(d.Message, "holds the provider cell Bag") {
				bad = true
				continue
			}
			got[d.Line] = d.Message
		}
		ok := !bad && len(got) == len(want)
		for l := range want {
			if _, hit := got[l]; !hit {
				ok = false
			}
		}
		if !ok {
			t.Fatalf("seed %d: refused lines %v, want %v\ndiagnostics: %v\nreplay: ERGO_CELL_SEED=%d go test -run TestProviderCellConfinementModel ./internal/sketch/check\n%s", seed, cellLines(got), cellLines(want), ds, seed, src)
		}
	}
}

func cellLines(m map[int]string) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	return out
}
