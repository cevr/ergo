package lower

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

// cellHeader is the generated programs' sealed operation type and its value
// provider: Push appends, Put writes an element and Bump a scalar through
// *Box; Get and Sum read. A Box starts with two elements, so Get and Put
// at 0 and 1 are in range.
const cellHeader = `type Bag {
	Push(n int),
	Put(i int, n int),
	Bump(n int),
	Get(i int) int,
	Sum() int,
	seal(),
}

type Box { xs []int, n int }

func NewBox() Box { Box{xs: []int{1, 2}, n: 0} }
func Push(b *Box, n int) { b.xs = b.xs.Append(n) }
func Put(b *Box, i int, n int) { b.xs[i] = n }
func Bump(b *Box, n int) { b.n = b.n + n }
func Get(b Box, i int) int { b.xs[i] }
func Sum(b Box) int {
	let mut s = b.n * 1000
	for x in b.xs {
		s = s + x
	}
	s
}
func seal(b Box) {}

func use(c Bag) {
	c.Put(0, 77)
	c.Push(5)
	c.Bump(9)
}

func wrap(b Box) Bag { b }

func show(name string, c Bag) needs Console {
	Console.Println("\(name) \(c.Get(0)) \(c.Get(1)) \(c.Sum())")
}

`

// cellBox is the interpreter's Box: a value, copied at every conversion.
type cellBox struct {
	xs []int
	n  int
}

func (b cellBox) clone() cellBox { return cellBox{xs: append([]int(nil), b.xs...), n: b.n} }

func (b *cellBox) op(name string, args []int) {
	switch name {
	case "Push":
		b.xs = append(b.xs, args[0])
	case "Put":
		b.xs[args[0]] = args[1]
	case "Bump":
		b.n += args[0]
	}
}

func (b cellBox) line(name string) string {
	s := b.n * 1000
	for _, x := range b.xs {
		s += x
	}
	return fmt.Sprintf("%s %d %d %d\n", name, b.xs[0], b.xs[1], s)
}

// cellProgram draws a program of a live source w (a let mut Box), cells
// made from it by a let, by a call's result and from fresh sources that are
// dead after the conversion, operations on all of them, and passes of w to
// a Bag parameter. It returns the source and what the interpreter prints.
func cellProgram(rng *rand.Rand) (string, string) {
	var src, out strings.Builder
	src.WriteString(cellHeader + "func main() needs Console {\n\tlet mut w = NewBox()\n")
	w := cellBox{xs: []int{1, 2}}
	var cells []string
	model := map[string]*cellBox{}
	op := func() (string, []int) {
		switch rng.Intn(3) {
		case 0:
			return "Push", []int{rng.Intn(9) + 1}
		case 1:
			return "Put", []int{rng.Intn(2), rng.Intn(90) + 10}
		}
		return "Bump", []int{rng.Intn(9) + 1}
	}
	call := func(recv, name string, args []int) {
		var as []string
		for _, a := range args {
			as = append(as, strconv.Itoa(a))
		}
		fmt.Fprintf(&src, "\t%s.%s(%s)\n", recv, name, strings.Join(as, ", "))
	}
	for range 8 + rng.Intn(10) {
		switch r := rng.Intn(10); {
		case r < 3:
			name, args := op()
			call("w", name, args)
			w.op(name, args)
		case r < 5 && len(cells) > 0:
			c := cells[rng.Intn(len(cells))]
			name, args := op()
			call(c, name, args)
			model[c].op(name, args)
		case r < 6:
			c := "c" + strconv.Itoa(len(cells))
			fmt.Fprintf(&src, "\tlet %s Bag = w\n", c)
			b := w.clone()
			cells, model[c] = append(cells, c), &b
		case r < 7:
			c := "c" + strconv.Itoa(len(cells))
			fmt.Fprintf(&src, "\tlet %s = wrap(w)\n", c)
			b := w.clone()
			cells, model[c] = append(cells, c), &b
		case r < 8:
			// A fresh source, dead after its conversion: it moves.
			c := "c" + strconv.Itoa(len(cells))
			v := "v" + strconv.Itoa(len(cells))
			fmt.Fprintf(&src, "\tlet mut %s = NewBox()\n", v)
			b := cellBox{xs: []int{1, 2}}
			name, args := op()
			call(v, name, args)
			b.op(name, args)
			fmt.Fprintf(&src, "\tlet %s Bag = %s\n", c, v)
			cells, model[c] = append(cells, c), &b
		case r < 9:
			src.WriteString("\tuse(w)\n")
		default:
			fmt.Fprintf(&src, "\tshow(\"w\", w)\n")
			out.WriteString(w.line("w"))
		}
	}
	fmt.Fprintf(&src, "\tshow(\"w\", w)\n")
	out.WriteString(w.line("w"))
	for _, c := range cells {
		fmt.Fprintf(&src, "\tshow(%q, %s)\n", c, c)
		out.WriteString(model[c].line(c))
	}
	src.WriteString("}\n")
	return src.String(), out.String()
}

// cellCounterSource is the counter of TestProvedRunsModel with its
// operation type sealed (seal), used through a cell local.
var cellCounterSource = strings.NewReplacer(
	"\tTotal() Digit,\n", "\tTotal() Digit,\n\tseal(),\n",
	"func Total(w *Wheel) Digit { w.at }\n", "func Total(w *Wheel) Digit { w.at }\nfunc seal(w Wheel) {}\n",
	"\tlet mut w = NewWheel()\n\tw.Add(1)\n\tw.Add(2)\n\tw.Add(4)\n\tw.Total()\n", "\tlet c Counter = NewWheel()\n\tc.Add(1)\n\tc.Add(2)\n\tc.Add(4)\n\tc.Total()\n",
).Replace(wheelSource)

// cellReleaseSource releases a cell of a provider that holds a resource
// once, with the cell's own state: the log reads 8 (one release of n = 7),
// not 1 (the source's state), 0 (no release) or 808 (two).
const cellReleaseSource = `import sync

type Res { id int }
func Close(r Res) {}

type Bag {
	Add(n int),
	Close(),
	seal(),
}

type Kept { r Res, n int, log sync.Ref[int] }

func Add(k *Kept, n int) { k.n = k.n + n }
func Close(k Kept) { k.log.Set(k.log.Get() * 100 + k.n + 1) }
func seal(k Kept) {}

func scope(log sync.Ref[int]) {
	let k = Kept{r: Res{id: 1}, n: 0, log: log}
	let c Bag = k
	c.Add(3)
	c.Add(4)
}

func main() needs Console {
	let log = sync.NewRef[int](0)
	scope(log)
	Console.Println("\(log.Get())")
}
`

// TestProviderCellsAgree: a value that moves into a cell is its own value.
// Each generated program prints what an interpreter that copies the value
// at every conversion prints, under the default lowering (which copies
// only a live source) and the copy reference (cellLowering "copy"). A
// cell's resource is released once, with the cell's state. And a window on
// a cell local is rewritten by the provider's proved law, as on the
// provider: one Add under the build's evidence, three under the reference,
// both printing 7. ERGO_CELL_SEED=N replays one seed.
//
// It builds Go because the claim is what the lowered programs print: the
// copy at the conversion is Go slice aliasing, which no in-process check
// of the Go text decides.
func TestProviderCellsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []int64{}
	if s := os.Getenv("ERGO_CELL_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 30; i++ {
			seeds = append(seeds, i)
		}
	}
	type run struct {
		seed      int64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := cellProgram(rand.New(rand.NewSource(seed)))
		for _, mode := range []string{"", "copy"} {
			saved := cellLowering
			cellLowering = mode
			programs = append(programs, mustLower(t, fmt.Sprintf("cell%d%s", seed, mode), src))
			cellLowering = saved
			runs = append(runs, run{seed, src, want})
		}
	}
	programs = append(programs, mustLower(t, "cellrelease", cellReleaseSource))
	runs = append(runs, run{0, cellReleaseSource, "8\n"})
	programs = append(programs, cellCounter(t)...)
	runs = append(runs, run{0, cellCounterSource, "7\n"}, run{0, cellCounterSource, "7\n"})
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the cell programs do not build")
	}
	for i, p := range programs {
		out, stderr, code := runExe(t, exes[p.name])
		if out != runs[i].want || code != 0 {
			t.Errorf("seed %d, %s: the program and the interpreter differ\nreplay: ERGO_CELL_SEED=%d go test -run '^TestProviderCellsAgree$' ./internal/sketch/lower\n%s\n--- program (exit %d)\n%s%s\n--- interpreter\n%s", runs[i].seed, p.name, runs[i].seed, runs[i].src, code, out, firstLines(stderr, 8), runs[i].want)
		}
	}
}

// cellCounter lowers cellCounterSource with its proof run's evidence and
// with the reference rewrite lowering, and checks the promised form: the
// cell window's three Adds are one under the evidence.
func cellCounter(t *testing.T) []*lowered {
	t.Helper()
	saved := inlineLowering
	inlineLowering = "call"
	defer func() { inlineLowering = saved }()
	src := cellCounterSource
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v\n%s", diags, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	var receipt string
	ev, err := RewriteChecks(u, func(files []File, laws []string) (string, error) {
		exes := buildAll(t, []*lowered{{name: "cellproof", src: src, file: file, pkg: pkg, files: files}}, true)
		out, stderr, code := runExeArgs(t, exes["cellproof"], "--law", "Counter.AddJoins")
		if code != 0 {
			t.Fatalf("proof exit %d:\n%s\n%s", code, out, stderr)
		}
		receipt = out
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt, "ok law Counter.AddJoins for Wheel (proved:") {
		t.Fatalf("missing proved receipt:\n%s", receipt)
	}
	u.Rewrite = ev
	fast, err := Lower(u)
	if err != nil {
		t.Fatal(err)
	}
	rewriteLowering = "reference"
	ref, err := Lower(u)
	rewriteLowering = ""
	if err != nil {
		t.Fatal(err)
	}
	progs := []*lowered{{name: "cellfast", src: src, file: file, pkg: pkg, files: fast}, {name: "cellref", src: src, file: file, pkg: pkg, files: ref}}
	for _, p := range progs {
		body := goFunc(t, p.mainGo(), "site")
		want := 3
		if p.name == "cellfast" {
			want = 1
		}
		if got := strings.Count(body, ".Add("); got != want {
			t.Fatalf("%s has %d Add calls, want %d:\n%s", p.name, got, want, body)
		}
	}
	return progs
}
