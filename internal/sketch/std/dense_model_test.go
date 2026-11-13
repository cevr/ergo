package std_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// The model property of std dense (docs/spec/stdlib.md, Dense stores 5):
// seeded command sequences over a Table, a Count with a Col, a Side and a
// Pool run on std dense and on a reference model of plain Go slices and a
// map, and each command prints one line that both must agree on. The same
// sequences run on a copy of dense.ergo in a user module, which must check
// and print the same lines (TestDenseCopyAgrees).

// denseSeeds is the count of seeds one run checks; denseSeedVariable
// replays one.
const (
	denseSeeds        = 40
	denseSeedVariable = "ERGO_DENSE_SEED"
	denseBig          = 72 // rows of the table whose ids reach past the end of the others
)

// denseModel is the reference: what each store holds.
type denseModel struct {
	rows  []int
	col   []int
	side  map[int]int
	zero  int
	lists [][]int
	items int
}

// denseCase is one seed: its Ergo statements (with P for the package) and
// the lines the model expects.
type denseCase struct {
	seed  int
	body  []string
	lines []string
}

// denseOps are the generator's commands, by name: each must be reached.
var denseOps = []string{"put", "at", "get", "set", "each", "ids", "rows", "opt", "next", "of", "setof", "sideset", "sideof", "putlist", "list"}

// genDense builds the case of seed, and marks in reach each command and
// each branch it reached.
func genDense(seed int, reach map[string]bool) denseCase {
	r := rand.New(rand.NewPCG(uint64(seed), 0xd15e))
	m := denseModel{side: map[int]int{}, zero: r.IntN(5) - 2}
	c := denseCase{seed: seed}
	c.body = append(c.body,
		"let mut t = P.New[int]()",
		"let mut ids = []P.ID[int]{}",
		"let mut big = P.New[int]()",
		"let mut far = []P.ID[int]{}",
		fmt.Sprintf("for i in 0..%d {\n\t\tfar = far.Append(big.Put(i))\n\t}", denseBig),
		"let mut c = P.NewCount[int]()",
		"let mut col = P.NewCol[int, int]()",
		"let mut cids = []P.ID[int]{}",
		fmt.Sprintf("let mut side = P.NewSide[int, int](%d)", m.zero),
		"let mut pool = P.NewPool[int]()",
		"let mut runs = []P.Run[int]{}",
	)
	stores := len(c.body)
	say := func(stmt, line string) {
		c.body = append(c.body, stmt)
		c.lines = append(c.lines, line)
	}
	n := 10 + r.IntN(50)
	for step := 0; step < n; step++ {
		op := denseOps[r.IntN(len(denseOps))]
		v := r.IntN(200) - 100
		if op != "put" && op != "next" && op != "sideset" && op != "sideof" && op != "putlist" && op != "get" && op != "each" && op != "ids" && op != "rows" {
			switch {
			case (op == "at" || op == "set" || op == "opt") && len(m.rows) == 0,
				(op == "of" || op == "setof") && len(m.col) == 0,
				op == "list" && len(m.lists) == 0:
				op = "put"
			}
		}
		reach[op] = true
		s := strconv.Itoa(step)
		switch op {
		case "put":
			m.rows = append(m.rows, v)
			say(fmt.Sprintf("ids = ids.Append(t.Put(%d))\n\tConsole.Println(\"put \\(P.Index(ids[len(ids)-1])) \\(P.Len(t))\")", v),
				fmt.Sprintf("put %d %d", len(m.rows)-1, len(m.rows)))
		case "at":
			i := r.IntN(len(m.rows))
			say(fmt.Sprintf("Console.Println(\"at \\(P.At(t, ids[%d]))\")", i), fmt.Sprintf("at %d", m.rows[i]))
		case "get":
			j := r.IntN(len(m.rows) + 3)
			want := "none false"
			if j < len(m.rows) {
				want = fmt.Sprintf("%d true", m.rows[j])
				reach["get hit"] = true
			} else {
				reach["get past the end"] = true
			}
			say(fmt.Sprintf("match P.Get(t, far[%d]) {\n\tcase Some[int] as v%s:\n\t\tConsole.Println(\"get \\(v%s.Value) \\(P.Has(t, far[%d]))\")\n\tcase None:\n\t\tConsole.Println(\"get none \\(P.Has(t, far[%d]))\")\n\t}", j, s, s, j, j),
				"get "+want)
		case "set":
			i := r.IntN(len(m.rows))
			m.rows[i] = v
			say(fmt.Sprintf("t.Set(ids[%d], %d)\n\tConsole.Println(\"set \\(P.At(t, ids[%d]))\")", i, v, i), fmt.Sprintf("set %d", v))
		case "each", "ids":
			k := r.IntN(len(m.rows) + 2)
			sum, count := 0, 0
			for i, row := range m.rows {
				if i >= k {
					reach[op+" stops"] = true
					break
				}
				if op == "each" {
					sum += row * (i + 1)
				} else {
					sum += i
				}
				count++
			}
			loop := fmt.Sprintf("for e in P.Each(t) {\n\t\tif P.Index(e.ID) >= %d {\n\t\t\tbreak\n\t\t}\n\t\ts%s += e.Row * (P.Index(e.ID) + 1)\n\t\tn%s += 1\n\t}", k, s, s)
			if op == "ids" {
				loop = fmt.Sprintf("for id in P.IDs(t) {\n\t\tif P.Index(id) >= %d {\n\t\t\tbreak\n\t\t}\n\t\ts%s += P.Index(id)\n\t\tn%s += 1\n\t}", k, s, s)
			}
			say(fmt.Sprintf("let mut s%s = 0\n\tlet mut n%s = 0\n\t%s\n\tConsole.Println(\"%s \\(s%s) \\(n%s)\")", s, s, loop, op, s, s),
				fmt.Sprintf("%s %d %d", op, sum, count))
		case "rows":
			sum := 0
			for i, row := range m.rows {
				sum += row * (i + 2)
			}
			say(fmt.Sprintf("let mut s%s = 0\n\tfor i, row in P.Rows(t) {\n\t\ts%s += row * (P.Index(P.IDOf(t, i)) + 2)\n\t}\n\tConsole.Println(\"rows \\(s%s) \\(len(P.Rows(t)))\")", s, s, s),
				fmt.Sprintf("rows %d %d", sum, len(m.rows)))
		case "opt":
			i := r.IntN(len(m.rows))
			say(fmt.Sprintf("let o%s = P.OptOf(ids[%d])\n\tlet u%s = match P.Unpack(o%s) {\n\tcase Some[P.ID[int]] as x:\n\t\tP.Index(x.Value)\n\tcase None:\n\t\t-1\n\t}\n\tlet w%s = match P.Unpack(P.NoID[int]()) {\n\tcase Some[P.ID[int]]:\n\t\t1\n\tcase None:\n\t\t0\n\t}\n\tConsole.Println(\"opt \\(P.Index(P.Or(o%s, ids[0]))) \\(P.Index(P.Or(P.NoID[int](), ids[0]))) \\(u%s) \\(w%s)\")", s, i, s, s, s, s, s, s),
				fmt.Sprintf("opt %d 0 %d 0", i, i))
		case "next":
			m.col = append(m.col, v)
			say(fmt.Sprintf("cids = cids.Append(c.Next())\n\tcol.Push(%d)\n\tConsole.Println(\"next \\(P.Index(cids[len(cids)-1])) \\(P.Total(c))\")", v),
				fmt.Sprintf("next %d %d", len(m.col)-1, len(m.col)))
		case "of":
			i := r.IntN(len(m.col))
			say(fmt.Sprintf("Console.Println(\"of \\(P.Of(col, cids[%d])) \\(P.In(col, cids[%d])) \\(P.In(col, far[%d]))\")", i, i, len(m.col)),
				fmt.Sprintf("of %d true false", m.col[i]))
		case "setof":
			i := r.IntN(len(m.col))
			m.col[i] = v
			say(fmt.Sprintf("col.SetOf(cids[%d], %d)\n\tConsole.Println(\"setof \\(P.Of(col, cids[%d]))\")", i, v, i), fmt.Sprintf("setof %d", v))
		case "sideset":
			j := r.IntN(16)
			m.side[j] = v
			say(fmt.Sprintf("side.SideSet(far[%d], %d)\n\tConsole.Println(\"sideset \\(P.SideOf(side, far[%d]))\")", j, v, j), fmt.Sprintf("sideset %d", v))
		case "sideof":
			j := r.IntN(24)
			want, ok := m.side[j]
			if !ok {
				want = m.zero
				reach["side default"] = true
			}
			say(fmt.Sprintf("Console.Println(\"sideof \\(P.SideOf(side, far[%d]))\")", j), fmt.Sprintf("sideof %d", want))
		case "putlist":
			xs := make([]string, r.IntN(5))
			list := []int{}
			for i := range xs {
				x := r.IntN(100)
				xs[i] = strconv.Itoa(x)
				list = append(list, x)
			}
			if len(list) == 0 {
				reach["empty list"] = true
			}
			m.lists = append(m.lists, list)
			m.items += len(list)
			say(fmt.Sprintf("runs = runs.Append(pool.PutList([]int{%s}))\n\tConsole.Println(\"putlist \\(P.RunLen(runs[len(runs)-1])) \\(P.Items(pool))\")", strings.Join(xs, ", ")),
				fmt.Sprintf("putlist %d %d", len(list), m.items))
		case "list":
			i := r.IntN(len(m.lists))
			sum := 0
			for k, x := range m.lists[i] {
				sum += x * (k + 1)
			}
			say(fmt.Sprintf("let mut s%s = 0\n\tfor k, x in P.List(pool, runs[%d]) {\n\t\ts%s += x * (k + 1)\n\t}\n\tConsole.Println(\"list \\(s%s) \\(len(P.List(pool, runs[%d])))\")", s, i, s, s, i),
				fmt.Sprintf("list %d %d", sum, len(m.lists[i])))
		}
	}
	// The last line reads every store, so no seed leaves one unused.
	sideFirst, ok := m.side[0]
	if !ok {
		sideFirst = m.zero
	}
	say("Console.Println(\"end \\(len(ids)) \\(P.Len(big)) \\(P.Total(c)) \\(len(cids)) \\(P.In(col, far[0])) \\(P.SideOf(side, far[0])) \\(P.Items(pool)) \\(len(runs))\")",
		fmt.Sprintf("end %d %d %d %d %t %d %d %d", len(m.rows), denseBig, len(m.col), len(m.col), len(m.col) > 0, sideFirst, m.items, len(m.lists)))
	// A store no command changes is a plain let (S110).
	var uses []string
	for i, stmt := range c.body {
		if i >= stores || !strings.HasPrefix(stmt, "let mut ") {
			uses = append(uses, stmt)
		}
	}
	rest := strings.Join(uses, "\n")
	for i, decl := range c.body[:stores] {
		name, ok := strings.CutPrefix(decl, "let mut ")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, " ")
		if !regexp.MustCompile(`\b` + name + `( = |\.(Put|Set|Next|Push|SetOf|SideSet|PutList)\()`).MatchString(rest) {
			c.body[i] = "let " + strings.TrimPrefix(decl, "let mut ")
		}
	}
	return c
}

// function is the case as an Ergo function over package pkg.
func (c denseCase) function(pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func seed%d%s() needs Console {\n", c.seed, pkg)
	for _, stmt := range c.body {
		b.WriteString("\t" + strings.ReplaceAll(stmt, "P.", pkg+".") + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// denseRun is the one build and run of every case, on std dense and on
// the copy, shared by the two tests.
var denseRun struct {
	once  sync.Once
	cases []denseCase
	reach map[string]bool
	src   string
	out   map[string][]string // by "<seed> <pkg>"
	err   error
}

// denseRoot is the module root: the directory above the test's that
// holds go.mod.
func denseRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = up
	}
}

// runBound runs cmd in a process group of its own under proclimit's
// memory bound, kills the group when it ends, and returns its output.
func runBound(cmd *exec.Cmd) (string, error) {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := proclimit.Start(cmd, proclimit.DefaultLimit)
	if err == nil {
		err = cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out.String(), err
}

// denseBuildAndRun writes the user module (std dense's copy as package
// local, and one program that runs each case on dense and on local),
// builds it with the ergo CLI and runs it.
//
// It builds Go because the stores' writes borrow, and the check-time
// evaluator does not run a borrow: only the built program runs them.
func denseBuildAndRun(t *testing.T) {
	root := denseRoot(t)
	only := -1
	if v := os.Getenv(denseSeedVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			denseRun.err = fmt.Errorf("%s=%q is not a seed", denseSeedVariable, v)
			return
		}
		only = n
	}
	denseRun.reach = map[string]bool{}
	var prog strings.Builder
	prog.WriteString("import dense\nimport m/local\n\n")
	var calls strings.Builder
	for seed := 0; seed < denseSeeds; seed++ {
		if only >= 0 && seed != only {
			continue
		}
		c := genDense(seed, denseRun.reach)
		denseRun.cases = append(denseRun.cases, c)
		for _, pkg := range []string{"dense", "local"} {
			prog.WriteString(c.function(pkg) + "\n")
			fmt.Fprintf(&calls, "\tConsole.Println(\"== %d %s\")\n\tseed%d%s()\n", seed, pkg, seed, pkg)
		}
	}
	prog.WriteString("func main() needs Console {\n" + calls.String() + "}\n")
	denseRun.src = prog.String()
	copied, err := os.ReadFile(filepath.Join(root, "internal/sketch/std/dense/dense.ergo"))
	if err != nil {
		denseRun.err = err
		return
	}
	dir, err := os.MkdirTemp("", "ergo-dense-model-")
	if err != nil {
		denseRun.err = err
		return
	}
	defer os.RemoveAll(dir)
	files := map[string][]byte{
		"mod/ergo.mod":            []byte("module m\n"),
		"mod/local/dense.ergo":    copied,
		"mod/cmd/agree/main.ergo": []byte(denseRun.src),
		"bin/.keep":               nil,
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			denseRun.err = err
			return
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			denseRun.err = err
			return
		}
	}
	ergo := filepath.Join(dir, "bin", "ergo")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", ergo, "./cmd/ergo")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		denseRun.err = fmt.Errorf("build the ergo CLI: %v\n%s", err, out)
		return
	}
	exe := filepath.Join(dir, "bin", "agree")
	compile := exec.Command(ergo, "build", "./cmd/agree", "-o", exe)
	compile.Dir = filepath.Join(dir, "mod")
	if out, err := runBound(compile); err != nil {
		denseRun.err = fmt.Errorf("ergo build of the model program: %v\n%s", err, out)
		return
	}
	out, err := runBound(exec.Command(exe))
	if err != nil {
		denseRun.err = fmt.Errorf("the model program failed: %v\n%s", err, out)
		return
	}
	denseRun.out = map[string][]string{}
	key := ""
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if rest, ok := strings.CutPrefix(line, "== "); ok {
			key = rest
			continue
		}
		denseRun.out[key] = append(denseRun.out[key], line)
	}
}

// denseOutputs builds and runs the cases once per test process.
func denseOutputs(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	denseRun.once.Do(func() { denseBuildAndRun(t) })
	if denseRun.err != nil {
		t.Fatal(denseRun.err)
	}
}

// denseDiff reports the case of c whose lines differ from want, with its
// Ergo source, both outputs and the replay command.
func denseDiff(t *testing.T, c denseCase, pkg, wantName string, want []string) {
	t.Helper()
	got := denseRun.out[fmt.Sprintf("%d %s", c.seed, pkg)]
	if strings.Join(got, "\n") == strings.Join(want, "\n") {
		return
	}
	t.Errorf("seed %d: %s disagrees with %s\nErgo source:\n%s\n%s:\n%s\n%s:\n%s\nreplay: %s=%d go test ./internal/sketch/std -run %s",
		c.seed, pkg, wantName, c.function(pkg), pkg, strings.Join(got, "\n"), wantName, strings.Join(want, "\n"), denseSeedVariable, c.seed, t.Name())
}

// TestDenseAgreesWithModel runs each seed's commands on std dense and
// holds every line equal to the model's. It kills the dense-* mutants of
// the std mutant ledger that name it, one per operation.
//
// It builds Go because Put, Set, Next, Push, SideSet and PutList borrow
// their store, and the check-time evaluator skips every call that borrows
// (internal/sketch/eval, preflight): no in-process run of them exists.
func TestDenseAgreesWithModel(t *testing.T) {
	denseOutputs(t)
	for _, c := range denseRun.cases {
		denseDiff(t, c, "dense", "the model", c.lines)
	}
	if os.Getenv(denseSeedVariable) == "" {
		for _, want := range append(append([]string{}, denseOps...), "get hit", "get past the end", "each stops", "ids stops", "side default", "empty list") {
			if !denseRun.reach[want] {
				t.Errorf("no seed reached %s", want)
			}
		}
	}
}

// TestDenseCopyAgrees holds that dense.ergo copied into a user module, as
// package local, checks and builds (std dense uses nothing a user module
// cannot) and prints, seed by seed, what std dense prints.
//
// It builds Go because it shares TestDenseAgreesWithModel's build and run:
// the copy's output is the output of the built program.
func TestDenseCopyAgrees(t *testing.T) {
	denseOutputs(t)
	for _, c := range denseRun.cases {
		denseDiff(t, c, "local", "std dense", denseRun.out[fmt.Sprintf("%d dense", c.seed)])
	}
}
