package check_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// hostFn is a Go function of an embedded binding that the generator can
// call with literals: basic parameters and one basic result. foreign and
// fails are the reference model's, read independently of the Ergo rows the
// checker reads: the tier from v1.PureImports and v1.PureImportsExcept
// (docs/spec/interop.md, Purity), and fails from the Go declaration's own
// text (its last result is error).
type hostFn struct {
	pkg, name string
	params    []string
	foreign   bool
	fails     bool
}

var rowsModelPackages = []string{"strconv", "strings", "unicode/utf8", "math", "path/filepath", "net/url", "html", "mime"}

func hostFns(t *testing.T) []hostFn {
	t.Helper()
	basic := map[string]bool{"string": true, "int": true, "bool": true, "float64": true, "rune": true, "int64": true}
	var out []hostFn
	for _, path := range rowsModelPackages {
		src, ok := gostd.Binding(path)
		if !ok {
			t.Fatalf("no binding for %s", path)
		}
		goText := map[string]string{}
		for _, line := range strings.Split(src, "\n") {
			if rest, ok := strings.CutPrefix(line, "// ergo:go func "); ok && !strings.HasPrefix(rest, "(") {
				name := rest[:strings.Index(rest, "(")]
				goText[name] = "func" + rest[len(name):]
			}
		}
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*syntax.FuncDecl)
			if !ok || fd.Recv != nil || fd.TypeParams != nil || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fd.Type.Results.List[0].Type.(*syntax.Ident); !ok || !basic[id.Name] {
				continue
			}
			var params []string
			fine := true
			for _, p := range fd.Type.Params.List {
				id, ok := p.Type.(*syntax.Ident)
				if !ok || !basic[id.Name] {
					fine = false
					break
				}
				for range max(1, len(p.Names)) {
					params = append(params, id.Name)
				}
			}
			gt, ok := goText[fd.Name.Name]
			if !fine || !ok {
				continue
			}
			ft, err := parser.ParseExpr(gt)
			if err != nil {
				t.Fatalf("%s.%s: %v", path, fd.Name.Name, err)
			}
			res := ft.(*ast.FuncType).Results
			last := res.List[len(res.List)-1].Type
			id, _ := last.(*ast.Ident)
			out = append(out, hostFn{
				pkg: path, name: fd.Name.Name, params: params,
				foreign: !slices.Contains(v1.PureImports, path) || slices.Contains(v1.PureImportsExcept[path], fd.Name.Name),
				fails:   id != nil && id.Name == "error",
			})
		}
	}
	return out
}

// rowsProgram is one generated program and the model's row of each function.
type rowsProgram struct {
	src     string
	names   []string
	foreign []bool
	fails   []bool
	export  []bool
}

func literal(t string) string {
	switch t {
	case "string":
		return `"a/b c"`
	case "bool":
		return "true"
	case "float64":
		return "1.5"
	case "rune":
		return "'x'"
	}
	return "2"
}

// genRows writes a chain of functions f0…fn; each calls host functions and
// earlier functions, propagating a failure with try or handling it with a
// catch. The model: a function needs go.Foreign iff a callee does, and
// fails go.Error iff a callee fails and the call is a try. An exported
// function writes the model's row exactly; drop removes one part of
// function drop's written row (-1 for none).
func genRows(r *rand.Rand, fns []hostFn, dropFn int, dropForeign bool) rowsProgram {
	n := 2 + r.IntN(5)
	p := rowsProgram{names: make([]string, n), foreign: make([]bool, n), fails: make([]bool, n), export: make([]bool, n)}
	imports := map[string]bool{}
	var b strings.Builder
	bodies := make([]string, n)
	for i := range n {
		p.export[i] = r.IntN(2) == 0
		p.names[i] = fmt.Sprintf("f%d", i)
		if p.export[i] {
			p.names[i] = fmt.Sprintf("F%d", i)
		}
		var body strings.Builder
		for range 1 + r.IntN(3) {
			var call string
			var foreign, fails bool
			if i > 0 && r.IntN(3) == 0 {
				j := r.IntN(i)
				call, foreign, fails = p.names[j]+"()", p.foreign[j], p.fails[j]
			} else {
				h := fns[r.IntN(len(fns))]
				var args []string
				for _, pt := range h.params {
					args = append(args, literal(pt))
				}
				local := h.pkg[strings.LastIndex(h.pkg, "/")+1:]
				imports[h.pkg] = true
				call, foreign, fails = local+"."+h.name+"("+strings.Join(args, ", ")+")", h.foreign, h.fails
			}
			p.foreign[i] = p.foreign[i] || foreign
			switch {
			case fails && r.IntN(2) == 0:
				fmt.Fprintf(&body, "\tlet _ = try %s\n", call)
				p.fails[i] = true
			case fails:
				fmt.Fprintf(&body, "\tlet _ = %s catch {\n\t\tcase go.Error: %s\n\t}\n", call, zeroOf(call, fns, p))
			default:
				fmt.Fprintf(&body, "\tlet _ = %s\n", call)
			}
		}
		bodies[i] = body.String()
	}
	var paths []string
	for path := range imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Fprintf(&b, "import go %s\n", path)
	}
	for i := range n {
		row := ""
		if p.export[i] {
			fails, foreign := p.fails[i], p.foreign[i]
			if i == dropFn {
				if dropForeign {
					foreign = false
				} else {
					fails = false
				}
			}
			if fails {
				row += " fails go.Error"
			}
			if foreign {
				row += " needs go.Foreign"
			}
		}
		fmt.Fprintf(&b, "\nfunc %s() int%s {\n%s\t0\n}\n", p.names[i], row, bodies[i])
	}
	p.src = b.String()
	return p
}

// zeroOf is a value of the result type of call, for its catch arm.
func zeroOf(call string, fns []hostFn, p rowsProgram) string {
	for _, h := range fns {
		local := h.pkg[strings.LastIndex(h.pkg, "/")+1:]
		if strings.HasPrefix(call, local+"."+h.name+"(") {
			return resultZero(h)
		}
	}
	return "0"
}

// resultZero reads the result type of h from its binding.
func resultZero(h hostFn) string {
	src, _ := gostd.Binding(h.pkg)
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "extern go "+h.pkg+"."+h.name+" func ") {
			sig := line[strings.LastIndex(line, ")")+2:]
			res, _, _ := strings.Cut(sig, " ")
			return literal(res)
		}
	}
	return "0"
}

// TestImportGoRowsModel (IG2): generated programs call random functions of
// Go packages through chains of functions, exported and not, with try and
// catch; the verdict equals a reference model of rows (a call adds
// go.Foreign iff the Go function is foreign, adds go.Error iff its last Go
// result is error and the call is a try). Each seed checks the program with
// every exported row exact (clean), then drops one part of one exported
// row that the model needs (S102 for go.Foreign, S101 for go.Error, naming
// the function). A failing seed prints the source; ERGO_ROWS_SEED replays.
//
// Mutants it alone catches: a foreign call charges nothing; a call into a
// reviewed-pure package charges go.Foreign; a catch keeps go.Error; a
// try drops it; the tier read without PureImportsExcept (filepath.Abs).
func TestImportGoRowsModel(t *testing.T) {
	fns := hostFns(t)
	seeds := 400
	first := uint64(1)
	if s := os.Getenv("ERGO_ROWS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, 1
	}
	negatives := 0
	defer func() { t.Logf("%d host functions, %d seeds, %d dropped rows", len(fns), seeds, negatives) }()
	for seed := first; seed < first+uint64(seeds); seed++ {
		p := genRows(rand.New(rand.NewPCG(seed, 0x16202)), fns, -1, false)
		if ds := checkRows(t, p.src); len(ds) > 0 {
			t.Errorf("seed %d (ERGO_ROWS_SEED=%d): want clean, got:\n%s\n--- source:\n%s", seed, seed, strings.Join(ds, "\n"), p.src)
			continue
		}
		var cands [][2]int // function, 1 drops go.Foreign, 0 go.Error
		for i := range p.names {
			if p.export[i] && p.foreign[i] {
				cands = append(cands, [2]int{i, 1})
			}
			if p.export[i] && p.fails[i] {
				cands = append(cands, [2]int{i, 0})
			}
		}
		if len(cands) == 0 {
			continue
		}
		pick := cands[int(seed)%len(cands)]
		q := genRows(rand.New(rand.NewPCG(seed, 0x16202)), fns, pick[0], pick[1] == 1)
		code, what := "S101", "escapes function "+q.names[pick[0]]
		if pick[1] == 1 {
			code, what = "S102", "go.Foreign, which is not in the capability row of function "+q.names[pick[0]]
		}
		ds := checkRows(t, q.src)
		negatives++
		found := false
		for _, d := range ds {
			found = found || strings.Contains(d, code) && strings.Contains(d, what)
		}
		if !found {
			t.Errorf("seed %d (ERGO_ROWS_SEED=%d): want %s %q, got:\n%s\n--- source:\n%s", seed, seed, code, what, strings.Join(ds, "\n"), q.src)
		}
	}
}

func checkRows(t *testing.T, src string) []string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}
	_, ds := check.Check("example.test/rows", []*syntax.File{f}, nil)
	var out []string
	for _, d := range ds {
		out = append(out, d.String())
	}
	return out
}
