package lower

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEveryGoBoundFunctionLowers is the property over std's binding
// inventory (FU-readdir): for every Go-bound function and method of every
// Go standard-library binding (gostd.Paths), a minimal program that calls
// it and imports only the packages its parameters name checks and lowers
// with no internal compiler error, and the lowered Go type-checks
// (typecheckGo). The program writes nothing of the result's type, so a Go
// package that only the result names (io/fs of os.ReadDir's []DirEntry)
// is one the program does not import: the lowering must name it itself.
//
// Each use binds the call's result through an if, a form whose lowering
// declares the result's Go type. A use the checker refuses, or the lowering
// refuses at its source, falls back to a plain `let _ =` call and then
// drops out: only an internal error or Go that does not type-check fails.
// os.ReadDir must keep its full form, so a broken generator cannot drop
// every use unseen; and a diagnostic of the generator's own text (a takes
// clause that names no parameter of the use) fails, so a change of the
// binding format cannot starve the property unseen (goBoundOwnFault).
//
// A method (an extern with a receiver, or an operation of a Go interface
// type) has a second use, its method value called: `let f = recv.M`. A Go
// method is a call-only extern (grammar.md, Extern), so that form is S108
// and steps down to the literal the diagnostic names, which must lower;
// os.File.ReadDir's must reach it.
func TestEveryGoBoundFunctionLowers(t *testing.T) {
	var programs []*lowered
	kept, plain, dropped := 0, 0, 0
	sawReadDir, sawMethodValue := false, false
	// One set of Go bindings for every program: each binding is parsed
	// once, not once per program (they import only Go packages).
	imp := hresImporter{check.NewGoBindings(nil)}
	for _, path := range gostd.Paths() {
		// One program for each use, so no use reaches a package that only
		// another use reaches (os.DirFS's fs.FS would name io/fs for
		// os.ReadDir).
		for i, u := range goBoundUses(t, path) {
			o, k, p, d := lowerGoBoundUses(t, imp, fmt.Sprintf("%s#%d", path, i), []goBoundUse{u})
			kept, plain, dropped = kept+k, plain+p, dropped+d
			if o == nil {
				continue
			}
			programs = append(programs, o)
			if path == "os" && strings.Contains(o.src, "\tlet r = if c { try os.ReadDir(") && !strings.Contains(o.src, "import go io/fs") {
				sawReadDir = true
			}
			if path == "os" && strings.Contains(o.src, "\tlet f = func(p0 int) []fs.DirEntry fails go.Error needs go.Foreign { try recv.ReadDir(p0) }\n\tlet r = if c { try f(") {
				sawMethodValue = true
			}
		}
	}
	t.Logf("Go-bound functions: %d in full form, %d plain, %d the checker or lowering refuses", kept, plain, dropped)
	if !sawReadDir {
		t.Errorf("the os program lost its full-form os.ReadDir use: the generator no longer reaches the case the property guards")
	}
	if !sawMethodValue {
		// A Go method is an extern: its method value is S108, and the
		// literal that diagnostic names lowers (grammar.md, Extern).
		t.Errorf("the method value recv.ReadDir of os.File did not step down to the literal the extern rule names")
	}
	typecheckGo(t, programs)
}

// goSeqProbe holds Go's iter.Seq at the import go boundary, where the
// binding makes it the prelude Seq (gobind): a Seq a Go call returns is an
// Ergo Seq (seqFromGo), and an Ergo Seq argument is Go's (goSeqArgs).
// GoSeqRoundTrips passes a Go-made Seq back to Go; GoSeqStopsAtBreak
// breaks out of a for-in over one, which Go's range-over-func checks: an
// iterator that calls yield after it returned false panics.
const goSeqProbe = `import go slices

func firstN(xs []int, n int) int {
	let mut seen = 0
	for _ in slices.Values(xs) {
		if seen == n {
			break
		}
		seen = seen + 1
	}
	seen
}

test GoSeqRoundTrips(xs []int) {
	let got = slices.Collect(slices.Values(xs))
	assert len(got) == len(xs)
	for i in 0..len(xs) {
		assert got[i] == xs[i]
	}
}

test GoSeqStopsAtBreak(xs []int, n int) {
	let k = if n < 0 { 0 - (n + 1) } else { n }
	let want = if k < len(xs) { k } else { len(xs) }
	assert firstN(xs, k) == want
}
`

// TestGoSeqCrossesTheBoundary runs goSeqProbe's properties. It builds Go
// because the break's guarantee is Go's range-over-func check at run time:
// go/types accepts an iterator that ignores yield's false.
func TestGoSeqCrossesTheBoundary(t *testing.T) {
	needsBuild(t)
	out, errout, code := runExeArgs(t, tableExe(t, "goseq"), "--seed", "1")
	if code != 0 {
		t.Fatalf("Go Seq properties: exit %d\n%s\n%s", code, out, firstLines(errout, 20))
	}
	for _, name := range []string{"GoSeqRoundTrips", "GoSeqStopsAtBreak"} {
		if !strings.Contains(out, name) {
			t.Errorf("the probe did not report %s:\n%s", name, out)
		}
	}
}

// goBoundUse is one Go-bound function's use: a function of the program
// that calls it in the full form, or the plain form.
type goBoundUse struct {
	target  string // the extern's Go target
	full    string
	short   string
	imports []string // the import lines of the packages the use names
	// plainImports, when set, are the plain form's import lines: a method
	// value's literal writes the result type.
	plainImports []string
}

var (
	goBoundQualifier = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\.[A-Z]`)
	goBoundIdent     = regexp.MustCompile(`(^|[^.\w])([A-Z]\w*)`)
)

// goBoundUses are the uses of every function and method the binding of
// path declares with extern go, in source order.
func goBoundUses(t *testing.T, path string) []goBoundUse {
	t.Helper()
	src, ok := gostd.Binding(path)
	if !ok {
		t.Fatalf("no binding for %s", path)
	}
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s: the binding does not parse: %v", path, err)
	}
	// imports are the import lines of the binding's packages by the name
	// the binding qualifies with, and of path itself, under an alias
	// where its name is a keyword (context).
	importLine := func(name, p string) string {
		if name != check.GoPackageName(p) {
			return "import go " + name + " " + p
		}
		return "import go " + p
	}
	name := check.GoPackageName(path)
	if syntax.IsKeyword(name) {
		name = "go" + name
	}
	own := importLine(name, path)
	imports := map[string]string{name: own}
	types := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.ImportDecl:
			for _, spec := range d.Specs {
				p := spec.Path.Value
				if spec.Path.Kind != syntax.PATH {
					p, _ = strconv.Unquote(p)
				}
				n := check.GoPackageName(p)
				if spec.Name != nil {
					n = spec.Name.Name
				}
				imports[n] = importLine(n, p)
			}
		case *syntax.TypeDecl:
			types[d.Name.Name] = true
		}
	}
	text := func(n syntax.Node) string { return src[n.Pos():n.End()] }
	var out []goBoundUse
	// add adds the uses of the function or method named fn, of the
	// signature ft whose text ends at end: its call, and for a method
	// (recv, its receiver type's text) its method value.
	add := func(target, fn, recv string, tps *syntax.FieldList, ft *syntax.FuncType, end syntax.Pos) {
		tparams := map[string]bool{}
		tpText := ""
		if tps != nil {
			for _, fl := range tps.List {
				for _, n := range fl.Names {
					tparams[n.Name] = true
				}
			}
			tpText = src[tps.Opening : tps.Closing+1]
		}
		qualify := func(s string) string {
			return goBoundIdent.ReplaceAllStringFunc(s, func(m string) string {
				sub := goBoundIdent.FindStringSubmatch(m)
				if types[sub[2]] && !tparams[sub[2]] {
					return sub[1] + name + "." + sub[2]
				}
				return m
			})
		}
		// The literal of a method value takes its own parameters, p0…,
		// since a name of the function's own is not rebound (196v).
		var params, litParams, litArgs, args []string
		params = append(params, "c bool")
		callee := name + "." + fn
		if recv != "" {
			params = append(params, "recv "+goBoundOwned(qualify(recv)))
			callee = "recv." + fn
		}
		if ft.Params != nil {
			for _, fl := range ft.Params.List {
				n := max(1, len(fl.Names))
				for range n {
					a := fmt.Sprintf("a%d", len(args))
					p := fmt.Sprintf("p%d", len(args))
					args, litArgs = append(args, a), append(litArgs, p)
					pt := goBoundOwned(qualify(text(fl.Type)))
					params = append(params, a+" "+pt)
					litParams = append(litParams, p+" "+pt)
				}
			}
		}
		results := 0
		resEnd := ft.Params.End()
		if r := ft.Results; r != nil {
			// The result type, then the error row as a field of its own.
			if res := r.List[0].Type; !isErrorRow(res) {
				resEnd = res.End()
				results = 1
				if tup, ok := res.(*syntax.TupleExpr); ok {
					results = len(tup.Elts)
				}
			}
		}
		rows := goBoundRows(qualify(src[resEnd:end]))
		sig := qualify(tpText) + "(" + strings.Join(params, ", ") + ") " + rows
		try := ""
		if strings.Contains(rows, "fails") {
			try = "try "
		}
		forms := func(callee string) (full, short string) {
			call := try + callee + "(" + strings.Join(args, ", ") + ")"
			switch results {
			case 0:
				return call, call
			case 1:
				return "let r = if c { " + call + " } else { " + call + " }\n\tlet _ = r", "let _ = " + call
			}
			names := make([]string, results)
			blanks := make([]string, results)
			for i := range names {
				names[i], blanks[i] = fmt.Sprintf("r%d", i), "_"
			}
			full = "let (" + strings.Join(names, ", ") + ") = if c { " + call + " } else { " + call + " }"
			for _, n := range names {
				full += "\n\tlet _ = " + n
			}
			return full, "let (" + strings.Join(blanks, ", ") + ") = " + call
		}
		var paths []string
		for _, m := range goBoundQualifier.FindAllStringSubmatch(sig, -1) {
			if p, ok := imports[m[1]]; ok && !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
		if !slices.Contains(paths, own) {
			paths = append(paths, own)
		}
		sort.Strings(paths)
		full, short := forms(callee)
		out = append(out, goBoundUse{
			target:  target,
			full:    sig + " {\n\t" + full + "\n}\n",
			short:   sig + " {\n\t" + short + "\n}\n",
			imports: paths,
		})
		if recv == "" {
			return
		}
		// The method value: in full form recv.M itself, called; in plain
		// form the literal a program passes where the method value is
		// refused (grammar.md, Extern), called.
		// The literal writes the result type, so it imports what that
		// names.
		lit := "func(" + strings.Join(litParams, ", ") + ") " + goBoundRows(qualify(src[ft.Params.End():end])) + " { " + try + callee + "(" + strings.Join(litArgs, ", ") + ") }"
		litPaths := slices.Clone(paths)
		for _, m := range goBoundQualifier.FindAllStringSubmatch(lit, -1) {
			if p, ok := imports[m[1]]; ok && !slices.Contains(litPaths, p) {
				litPaths = append(litPaths, p)
			}
		}
		sort.Strings(litPaths)
		fullV, _ := forms("f")
		out = append(out, goBoundUse{
			target:       target + " (method value)",
			full:         sig + " {\n\tlet f = " + callee + "\n\t" + fullV + "\n}\n",
			short:        sig + " {\n\tlet f = " + lit + "\n\t" + fullV + "\n}\n",
			imports:      paths,
			plainImports: litPaths,
		})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Extern == nil {
				continue
			}
			recv := ""
			if d.Recv != nil {
				recv = text(d.Recv.List[0].Type)
			}
			add(d.Extern.Target.Value, d.Name.Name, recv, d.TypeParams, d.Type, d.End())
		case *syntax.TypeDecl:
			// The operations of a Go interface type (io/fs.DirEntry's
			// Name), each a method of a value of the type.
			it, ok := d.Spec.(*syntax.InterfaceType)
			if !ok || d.Extern == nil || d.TypeParams != nil {
				continue
			}
			for _, m := range it.Methods {
				ft, ok := m.Type.(*syntax.FuncType)
				if !ok {
					continue
				}
				for _, n := range m.Names {
					add(d.Extern.Target.Value+"."+n.Name, n.Name, d.Name.Name, nil, ft, m.Type.End())
				}
			}
		}
	}
	return out
}

// goBoundTakes is a signature's takes clause, to its end.
var goBoundTakes = regexp.MustCompile(`(^|\s)takes\b.*$`)

// goBoundRows is the rows text of a binding's signature, trimmed, with its
// takes clause dropped. The clause names the binding's parameters (r) or,
// bare, an interface operation's receiver; the use renames them (a0…, p0…,
// recv) and adds c, so a copied clause names no parameter of the use. A use
// is unexported and has a body, so the checker infers its clause (grammar.md
// Declarations 13, A3).
func goBoundRows(s string) string {
	return strings.TrimSpace(goBoundTakes.ReplaceAllString(strings.TrimSpace(s), ""))
}

// goBoundOwned is the type a use declares for a parameter of the binding's
// type t: a Go pointer `*S` is the owned handle go.Ref[S], since Go keeps
// its arguments by default and a borrow cannot be passed to a kept Go
// argument (interop.md Resources 5; a go.Ref is accepted where Go takes
// `*S`, q38). Any other type is t.
func goBoundOwned(t string) string {
	if s, ok := strings.CutPrefix(t, "*"); ok {
		return "go.Ref[" + s + "]"
	}
	return t
}

// goBoundOwnFault marks a diagnostic that the generator's own text causes:
// a takes clause that names no parameter of the use. Such a use steps down
// on the generator, not on the binding, so it fails the property: a change
// of the binding format must not starve the property unseen (batch 17 M2).
var goBoundOwnFault = regexp.MustCompile(`takes \S+ names no parameter|takes names \S+ twice|a bare takes names|takes \S+ has no effect`)

// isErrorRow reports the `fails …` field of a signature's results.
func isErrorRow(x syntax.Expr) bool {
	_, ok := x.(*syntax.ErrorRow)
	return ok
}

// goBoundDebug logs why each use steps down a form.
var goBoundDebug = os.Getenv("ERGO_GOBOUND_DEBUG") != ""

// goBoundForm is a use's state: its full form, its plain form, or dropped.
type goBoundForm int

const (
	goBoundFull goBoundForm = iota
	goBoundPlain
	goBoundDropped
)

// lowerGoBoundUses checks and lowers one program of the uses of path's
// binding, stepping a use down a form at each diagnostic or refusal in it,
// until the program lowers. An internal error fails the test and drops its
// use. It returns the lowered program (nil when every use dropped) and the
// number of uses in each form.
func lowerGoBoundUses(t *testing.T, imp check.Importer, path string, uses []goBoundUse) (o *lowered, full, plain, dropped int) {
	t.Helper()
	forms := make([]goBoundForm, len(uses))
	name := "gobound_" + strings.NewReplacer("/", "_", ".", "_", "#", "_g").Replace(path)
	for {
		src, starts := goBoundProgram(uses, forms)
		o = lowerSourceWith(name, src, false, imp)
		if o.err != nil {
			t.Fatalf("%s: the generated program does not parse: %v\n%s", path, o.err, src)
		}
		if o.panicked != nil {
			t.Errorf("%s: lowering panicked: %v", path, o.panicked)
		}
		if o.panicked == nil && len(o.diags) == 0 && len(o.refused) == 0 {
			for _, f := range forms {
				switch f {
				case goBoundFull:
					full++
				case goBoundPlain:
					plain++
				default:
					dropped++
				}
			}
			return o, full, plain, dropped
		}
		// Step down each use a diagnostic or refusal is in; an internal
		// error drops its use. One outside every use (an internal error
		// the naming pass meets at the file's start) is found by lowering
		// each use alone.
		stepped := false
		seen := map[int]bool{}
		for _, l := range goBoundFaults(o) {
			if goBoundOwnFault.MatchString(l.text) {
				t.Errorf("%s: the generator's own text refuses a use: %s\n%s", path, l.text, src)
			}
			i := -1
			for j, s := range starts {
				if s != 0 && s <= l.line {
					i = j
				}
			}
			if i >= 0 && !l.internal && !seen[i] {
				seen[i] = true
				if goBoundDebug {
					t.Logf("%s: %s steps from form %d: %s", path, uses[i].target, forms[i], l.text)
				}
				forms[i]++
				stepped = true
			}
		}
		if !stepped {
			for i := range uses {
				if forms[i] == goBoundDropped {
					continue
				}
				alone := slices.Repeat([]goBoundForm{goBoundDropped}, len(uses))
				alone[i] = forms[i]
				one, _ := goBoundProgram(uses, alone)
				lo := lowerSourceWith(name, one, false, imp)
				faults := goBoundFaults(lo)
				if len(faults) == 0 {
					continue
				}
				if slices.ContainsFunc(faults, func(f goBoundFault) bool { return f.internal }) {
					t.Errorf("%s: %s: an internal compiler error: %v %+v\n%s", path, uses[i].target, lo.panicked, lo.refused, one)
					forms[i] = goBoundDropped
				} else {
					forms[i]++
				}
				stepped = true
			}
		}
		if !stepped {
			t.Fatalf("%s: the program does not lower, and no use alone fails: %v %v %+v\n%s", path, o.panicked, o.diags, o.refused, src)
		}
		if !slices.ContainsFunc(forms, func(f goBoundForm) bool { return f != goBoundDropped }) {
			return nil, 0, 0, len(uses)
		}
	}
}

// goBoundFault is a diagnostic, a refusal or a panic of a lowering, at its
// line (0 for a panic); internal marks an internal compiler error.
type goBoundFault struct {
	line     int
	internal bool
	text     string
}

// goBoundFaults are o's faults.
func goBoundFaults(o *lowered) []goBoundFault {
	var out []goBoundFault
	if o.panicked != nil {
		out = append(out, goBoundFault{internal: true, text: fmt.Sprint(o.panicked)})
	}
	for _, d := range o.diags {
		l, _ := strconv.Atoi(strings.SplitN(d.String(), ":", 2)[0])
		out = append(out, goBoundFault{line: l, text: d.String()})
	}
	for _, r := range o.refused {
		out = append(out, goBoundFault{line: r.Line, internal: strings.Contains(r.Message, "internal compiler error"), text: r.Message})
	}
	return out
}

// goBoundProgram is the program of the uses in their forms, with each
// use's first line (0 for a dropped use).
func goBoundProgram(uses []goBoundUse, forms []goBoundForm) (string, []int) {
	var b strings.Builder
	var imports []string
	for i, u := range uses {
		if forms[i] != goBoundDropped {
			lines := u.imports
			if forms[i] == goBoundPlain && u.plainImports != nil {
				lines = u.plainImports
			}
			for _, p := range lines {
				if !slices.Contains(imports, p) {
					imports = append(imports, p)
				}
			}
		}
	}
	sort.Strings(imports)
	for _, p := range imports {
		b.WriteString(p + "\n")
	}
	b.WriteString("\n")
	line := strings.Count(b.String(), "\n") + 1
	starts := make([]int, len(uses))
	for i, u := range uses {
		text := u.full
		switch forms[i] {
		case goBoundPlain:
			text = u.short
		case goBoundDropped:
			continue
		}
		text = "func use" + strconv.Itoa(i) + text + "\n"
		starts[i] = line
		line += strings.Count(text, "\n")
		b.WriteString(text)
	}
	return b.String(), starts
}
