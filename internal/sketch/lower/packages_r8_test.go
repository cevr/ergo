package lower

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Round 8 (196b): check.Check meets a package's files in canonical order
// (check/canon.go), so all it derives is a function of the sources. The
// property TestCheckIsCanonical holds at check's boundary: for every order
// of the files of a program, the whole check result (a dump of every
// exported value, with the text of every type) and its diagnostics, both
// builds' bytes and the refusals are the same.

// checkerDump renders a check result as text, deterministic for a given
// result: every exported field reached from the package, maps sorted by
// their rendered entries, lists in their order, a type by its String()
// and its parts, an object by a reference and, once in a section of its
// own, by its fields, a node by the path of its file and its span.
type checkerDump struct {
	loc     map[syntax.Node]string
	stack   map[uintptr]bool
	objects map[uintptr]reflect.Value // every object met, rendered in full once
	named   map[uintptr]*check.Named  // every named type met, its body rendered once
}

var (
	objectType = reflect.TypeOf((*check.Object)(nil)).Elem()
	typeType   = reflect.TypeOf((*check.Type)(nil)).Elem()
	syntaxPath = reflect.TypeOf(syntax.File{}).PkgPath()
)

// dumpUnit renders u's check result and the diagnostics ds of the files
// u lists; each diagnostic names its file by path.
func dumpUnit(u Unit, ds []check.Diagnostic) string {
	d := &checkerDump{loc: map[syntax.Node]string{}, stack: map[uintptr]bool{}, objects: map[uintptr]reflect.Value{}, named: map[uintptr]*check.Named{}}
	for i, f := range u.Files {
		path := u.Sources[i].Path
		syntax.Inspect(f, func(n syntax.Node) bool {
			if n != nil {
				d.loc[n] = fmt.Sprintf("%s@%d-%d", path, n.Pos(), n.End())
			}
			return true
		})
	}
	var b strings.Builder
	var diags []string
	for _, x := range ds {
		diags = append(diags, fmt.Sprintf("%s %d %d:%d+%d %s %s", u.Sources[x.File].Path, x.Offset, x.Line, x.Column, x.Length, x.Code, x.Message))
	}
	sort.Strings(diags)
	b.WriteString("diagnostics:\n" + strings.Join(diags, "\n") + "\n")
	if u.Package == nil {
		return b.String()
	}
	b.WriteString("package: " + d.full(reflect.ValueOf(u.Package).Elem()) + "\n")
	var exported []string
	for _, o := range u.Package.Exported() {
		exported = append(exported, d.ref(reflect.ValueOf(o)))
	}
	sort.Strings(exported)
	b.WriteString("exported: " + strings.Join(exported, ", ") + "\n")
	// The sections of objects and named types grow while they render.
	done := map[uintptr]bool{}
	var sections []string
	for {
		var next []uintptr
		for p := range d.objects {
			if !done[p] {
				next = append(next, p)
			}
		}
		for p := range d.named {
			if !done[p] {
				next = append(next, p)
			}
		}
		if len(next) == 0 {
			break
		}
		for _, p := range next {
			done[p] = true
			if o, ok := d.objects[p]; ok {
				sections = append(sections, "object "+d.ref(o)+" = "+d.full(o.Elem()))
				continue
			}
			n := d.named[p]
			sections = append(sections, "named "+d.val(reflect.ValueOf(n))+" = "+d.val(reflect.ValueOf(n.Underlying())))
		}
	}
	sort.Strings(sections)
	b.WriteString(strings.Join(sections, "\n"))
	return b.String()
}

// ref is a short name of an object: its Go type, name, package, offset
// and the text of its type, if it has them. It also queues the object.
func (d *checkerDump) ref(v reflect.Value) string {
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Sprintf("%v", v)
	}
	d.objects[v.Pointer()] = v
	e := v.Elem()
	s := e.Type().Name()
	if f := e.FieldByName("Name"); f.IsValid() {
		s += " " + f.String()
	}
	if f := e.FieldByName("Pkg"); f.IsValid() && f.Kind() == reflect.Pointer && !f.IsNil() {
		s += " " + f.Interface().(*check.Package).Path
	}
	if f := e.FieldByName("Pos"); f.IsValid() {
		s += fmt.Sprintf("@%d", f.Int())
	}
	if f := e.FieldByName("Decl"); f.IsValid() {
		s += " " + d.val(f)
	}
	if f := e.FieldByName("Type"); f.IsValid() && f.Type() == typeType && !f.IsNil() {
		s += " : " + f.Interface().(check.Type).String()
	}
	return "<" + s + ">"
}

// full renders the exported fields of the struct v.
func (d *checkerDump) full(v reflect.Value) string {
	var parts []string
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		parts = append(parts, f.Name+": "+d.val(v.Field(i)))
	}
	return v.Type().Name() + "{" + strings.Join(parts, ", ") + "}"
}

func (d *checkerDump) val(v reflect.Value) string {
	if !v.IsValid() {
		return "nil"
	}
	t := v.Type()
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		return d.val(v.Elem())
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return ""
	case reflect.Map:
		var entries []string
		it := v.MapRange()
		for it.Next() {
			entries = append(entries, d.val(it.Key())+" => "+d.val(it.Value()))
		}
		sort.Strings(entries)
		return "map{" + strings.Join(entries, "; ") + "}"
	case reflect.Slice, reflect.Array:
		var elems []string
		for i := 0; i < v.Len(); i++ {
			elems = append(elems, d.val(v.Index(i)))
		}
		return "[" + strings.Join(elems, ", ") + "]"
	case reflect.Struct:
		return d.full(v)
	case reflect.Pointer:
		if v.IsNil() {
			return "nil"
		}
	default:
		if s, ok := v.Interface().(fmt.Stringer); ok && t.PkgPath() != "" {
			return s.String()
		}
		return fmt.Sprintf("%v", v)
	}
	// A pointer.
	if t.Elem().PkgPath() == syntaxPath {
		n := v.Interface().(syntax.Node)
		if l, ok := d.loc[n]; ok {
			return "node(" + l + ")"
		}
		return fmt.Sprintf("synth(%s@%d-%d)", t.Elem().Name(), n.Pos(), n.End())
	}
	switch x := v.Interface().(type) {
	case *check.Package:
		return "pkg(" + x.Path + ")"
	case *check.Row:
		var elems []string
		for _, e := range x.Elems() {
			elems = append(elems, d.val(reflect.ValueOf(e)))
		}
		return "row{" + strings.Join(elems, " , ") + "}"
	case *check.RowVar:
		return "rowvar(" + x.String() + " fn=" + d.ref(reflect.ValueOf(x.Fn)) + fmt.Sprintf(" kind=%v", x.Kind) + " sol=" + d.val(reflect.ValueOf(x.Solution())) + ")"
	case *check.Named:
		d.named[v.Pointer()] = x
		// Every exported field of Named: Obj, Orig, TArgs, TParams. Orig is
		// rendered as this shallow form too, so a recursive type ends.
		orig := "nil"
		if x.Orig != nil && x.Orig != x {
			orig = d.val(reflect.ValueOf(x.Orig))
		}
		return "named(" + x.String() + " obj=" + d.ref(reflect.ValueOf(x.Obj)) + " orig=" + orig + " targs=" + d.val(reflect.ValueOf(x.TArgs)) + " tparams=" + d.val(reflect.ValueOf(x.TParams)) + ")"
	}
	if t.Implements(objectType) {
		return d.ref(v)
	}
	p := v.Pointer()
	if d.stack[p] {
		return "<cycle " + t.Elem().Name() + ">"
	}
	d.stack[p] = true
	defer delete(d.stack, p)
	s := d.val(v.Elem())
	if t.Implements(typeType) {
		s = "type(" + v.Interface().(check.Type).String() + ")" + s
	}
	return s
}

// checkListed checks the files of package path as listed and returns its
// unit and diagnostics; the unit has a package even if there are some.
func checkListed(t *testing.T, path string, srcs []pkgSource, imp pkgImporter) (Unit, []check.Diagnostic) {
	t.Helper()
	u := Unit{}
	for _, s := range srcs {
		f, err := syntax.Parse(s.text)
		if err != nil {
			t.Fatalf("%s: %v\n%s", s.path, err, s.text)
		}
		f.Name = s.path
		u.Files = append(u.Files, f)
		u.Sources = append(u.Sources, Source{Path: s.path, Text: s.text})
	}
	var imps check.Importer
	if imp != nil {
		imps = imp
	}
	pkg, ds := check.Check(path, u.Files, imps)
	u.Package = pkg
	return u, ds
}

// lowerings is what both builds of u return: the emitted files, or the
// refusal text.
func lowerings(u Unit) map[string]string {
	out := map[string]string{}
	for _, tests := range []bool{false, true} {
		lower := Lower
		if tests {
			lower = LowerTests
		}
		fs, err := lower(u)
		if err != nil {
			out[fmt.Sprintf("tests=%v/error", tests)] = err.Error()
			continue
		}
		for _, f := range fs {
			out[fmt.Sprintf("tests=%v/%s", tests, f.Path)] = string(f.Data)
		}
	}
	return out
}

// r8Program generates a program of n files and main.ergo. File k declares
// the error type Ak with a function that fails it, a function whose
// written failure is E over a function type with a parameter of a mode
// the seed chooses (escaping or not), a function whose row is inferred
// from boxing fail[T] for a T the seed chooses (a delayed row
// substitution), and two more declaration kinds the seed chooses (a
// machine with inline variants, tests with a property and a law, a
// capability with a provider and a context, an overload with a let).
// main.ergo has the generic error types, the row of every Ak, a catch over
// it with a row value and its match, catches whose subjects are the
// escaping and the inferred rows, and, when refuse is set, a generic
// forwarding of the row value into an error type's field, whose derived
// text the lowering refuses.
func r8Program(seed int64, n int, refuse bool) []pkgSource {
	r := rand.New(rand.NewSource(seed))
	tys := []string{"int", "string", "bool", "float64", "[]int"}
	extras := []string{
		"type St%[1]d = On%[1]d{} | Off%[1]d{ N int }\ntype Sg%[1]d = Tick%[1]d{}\nfunc step%[1]d(s St%[1]d, e Sg%[1]d) Decision[St%[1]d, int] { Enter[St%[1]d]{Next: Off%[1]d{N: 1}} }\ntype M%[1]d machine[St%[1]d, Sg%[1]d] { initial: On%[1]d, step: step%[1]d }\n",
		"test T%[1]d { assert %[1]d == %[1]d }\ntest P%[1]d(n int) { assert n == n }\nlaw L%[1]d(n int) { n + 0 == n }\n",
		"type C%[1]d { Get() int }\ntype Pv%[1]d {}\nfunc Get(x Pv%[1]d) int { %[1]d }\nlet X%[1]d = context { C%[1]d: Pv%[1]d }\nfunc use%[1]d() int needs C%[1]d { C%[1]d.Get() }\n",
		"type Q%[1]d { N int }\nfunc Pick[T](q Q%[1]d, x T) T { x }\nlet V%[1]d int = %[1]d + 1\n",
	}
	var files []pkgSource
	var errs, tries, escs, infs, arms []string
	for k := 0; k < n; k++ {
		mode := ""
		if r.Intn(2) == 0 {
			mode = "escaping "
		}
		var text strings.Builder
		fmt.Fprintf(&text, "type A%[1]d { N int }\nfunc a%[1]d(n int) int fails A%[1]d {\n\tif n == %[1]d {\n\t\treturn A%[1]d{N: %[1]d}\n\t}\n\tn\n}\n", k)
		fmt.Fprintf(&text, "func esc%d() int fails E[func(g %sfunc())] { %d }\n", k, mode, k)
		fmt.Fprintf(&text, "func inf%d() int { try box(fail[%s]); %d }\n", k, tys[r.Intn(len(tys))], k)
		for _, x := range r.Perm(len(extras))[:2] {
			fmt.Fprintf(&text, extras[x], k)
		}
		files = append(files, pkgSource{fmt.Sprintf("/r8/f%d.ergo", k), text.String()})
		errs = append(errs, fmt.Sprintf("A%d", k))
		if k < n-1 {
			tries = append(tries, fmt.Sprintf("\tlet _ = try a%d(n)\n", k))
			escs = append(escs, fmt.Sprintf("\tif n == %d {\n\t\treturn try esc%d()\n\t}\n", k, k))
			infs = append(infs, fmt.Sprintf("\tif n == %d {\n\t\treturn try inf%d()\n\t}\n", k, k))
		}
		if k > 0 {
			arms = append(arms, fmt.Sprintf("\t\tcase A%d:\n\t\t\t%d\n", k, k))
		}
	}
	last := n - 1
	main := fmt.Sprintf(`type E[T] { V T }
type Err[T] {}
type Box[T] { V T }
type Wrap[T] { V T }
func boom[T]() fails Err[T] { Err[T] }
func fail[T]() { try boom[T]() }
func box[T](v T) fails Box[T] { Box[T]{V: v} }
func wrap[T](v T) int fails Wrap[T] { Wrap[T]{V: v} }
func each(n int) int fails %s {
%s	try a%d(n)
}
func escs(n int) int {
%s	try esc%d()
}
func infs(n int) int {
%s	try inf%d()
}
func name(n int) int {
	each(n) catch {
	case A0 as e:
		e.N
	case err:
		match err {
%s		}
	}
}
`, strings.Join(errs, " | "), strings.Join(tries, ""), last, strings.Join(escs, ""), last, strings.Join(infs, ""), last, strings.Join(arms, ""))
	body := "\tlet _ = escs(0) catch { case _: 0 }\n\tlet _ = infs(0) catch { case _: 0 }\n\tlet _ = name(0)\n"
	if refuse {
		main += "func outer(n int) int { each(n) catch { case err: try wrap(err) } }\n"
		body += "\tlet _ = outer(0) catch { case _: 0 }\n"
	}
	main += "func main() {\n" + body + "}\n"
	return append(files, pkgSource{"/r8/main.ergo", main})
}

// The round 7 verify's probes (R7 M1, M2), as programs.
var (
	r7NestedKeys = []pkgSource{
		{"/v7/a.ergo", "func a() int { try box(fail[int]); 1 }\n"},
		{"/v7/b.ergo", "func b() int { try box(fail[string]); 2 }\n"},
		{"/v7/main.ergo", "type Err[T] {}\ntype Box[T] { V T }\nfunc boom[T]() fails Err[T] { Err[T] }\nfunc fail[T]() { try boom[T]() }\nfunc box[T](v T) fails Box[T] { Box[T]{V:v} }\nfunc both(n int) int { if n == 0 { return try a() }; try b() }\nfunc main() { let _ = both(0) catch { case _: 0 } }\n"},
	}
	r7EscapingKeys = []pkgSource{
		{"/v7/a.ergo", "func a() int fails E[func(g func())] { 1 }\n"},
		{"/v7/b.ergo", "func b() int fails E[func(g escaping func())] { 2 }\n"},
		{"/v7/main.ergo", "type E[T] { V T }\nfunc both(n int) int { if n == 0 { return try a() }; try b() }\nfunc main() { let _ = both(0) catch { case _: 0 } }\n"},
	}
	r7RowValueRefusal = []pkgSource{
		{"/v7/a.ergo", "type A { N int }\nfunc a(n int) int fails A { if n == 0 { return A{N:0} }; n }\n"},
		{"/v7/b.ergo", "type B { N int }\nfunc b(n int) int fails B { if n == 1 { return B{N:1} }; n }\n"},
		{"/v7/main.ergo", "type Wrap[T] { V T }\nfunc wrap[T](v T) int fails Wrap[T] { Wrap[T]{V:v} }\nfunc each(n int) int fails A | B { let _ = try a(n); try b(n) }\nfunc outer(n int) int { each(n) catch { case err: try wrap(err) } }\nfunc main() { let _ = outer(0) catch { case _: 0 } }\n"},
	}
)

// canonCase is one program of the property and what it must show.
type canonCase struct {
	name   string
	files  []pkgSource
	diags  bool   // the checker reports diagnostics, so nothing is lowered
	refuse bool   // the lowering refuses
	stdout string // what the run build prints, if it is not refused
}

func canonCases() []canonCase {
	missing := setProgram(3, false)
	missing[3].text += "func miss(n int) string { each(n) catch { case E2: \"2\" } }\n"
	cases := []canonCase{
		{"sets", setProgram(3, false), false, false, "6 e0:0 e1 e2 ok\n"},
		{"sets refused", setProgram(3, true), false, true, ""},
		{"sets diagnostics", missing, true, false, ""},
		{"r7 nested keys", r7NestedKeys, false, false, ""},
		{"r7 escaping keys", r7EscapingKeys, false, false, ""},
		{"r7 row value refusal", r7RowValueRefusal, false, true, ""},
		// Two files whose first declarations have one name (overloads of
		// Get): a key of that name alone would tie them.
		{"first names tie", []pkgSource{
			{"/tie/a.ergo", "func Get(q Q0) int { 0 }\ntype Q0 {}\ntype E0 { N int }\nfunc fail0() int fails E0 { E0{N: 1} }\n"},
			{"/tie/b.ergo", "func Get(q Q1) int { 1 }\ntype Q1 {}\ntype E1 { N int }\nfunc fail1() int fails E1 { E1{N: 1} }\n"},
			{"/tie/main.ergo", "func each() int fails E0 | E1 {\n\tlet _ = try fail1()\n\tlet _ = try fail0()\n\t0\n}\nfunc rest() int { each() catch { case err: keep(err) } }\nfunc keep[T](v T) int { 1 }\nfunc main() { let _ = rest() }\n"},
		}, false, false, ""},
		// Round 9: files of equal text under distinct names. Only the name
		// tells them apart; the duplicate is in the later name.
		{"equal text duplicate", []pkgSource{
			{"/equal/a.ergo", "type A {}\n"},
			{"/equal/b.ergo", "type A {}\n"},
			{"/equal/main.ergo", "func main() {}\n"},
		}, true, false, ""},
		{"equal text errors", []pkgSource{
			{"/equal/a.ergo", "func f() int { missing }\n"},
			{"/equal/b.ergo", "func f() int { missing }\n"},
			{"/equal/c.ergo", "func f() int { missing }\n"},
			{"/equal/main.ergo", "func main() {}\n"},
		}, true, false, ""},
		{"equal text valid", []pkgSource{
			{"/equal/a.ergo", "// shared\n"},
			{"/equal/b.ergo", "// shared\n"},
			{"/equal/main.ergo", "type A { N int }\nfunc main() needs Console { Console.Println(\"\\(A{N: 1}.N)\") }\n"},
		}, false, false, "1\n"},
	}
	for seed := int64(0); seed < 3; seed++ {
		cases = append(cases,
			canonCase{fmt.Sprintf("r8 seed %d", seed), r8Program(seed, 3, false), false, false, ""},
			canonCase{fmt.Sprintf("r8 seed %d refused", seed), r8Program(seed, 3, true), false, true, ""})
	}
	cases = append(cases, canonCase{"r8 seed 3 of 4 files", r8Program(3, 4, true), false, true, ""})
	return cases
}

// TestCheckIsCanonical: for every order of the files of each program, a
// fresh check gives the same result (the dump of the whole result and the
// diagnostics, each naming its file by path), and the lowering of both
// builds the same bytes or the same refusal text. Each program's first
// listing builds and runs, or is refused as the case says.
func TestCheckIsCanonical(t *testing.T) {
	needsBuild(t)
	for _, c := range canonCases() {
		t.Run(c.name, func(t *testing.T) {
			var wantDump string
			var want map[string]string
			for i, order := range permutations(len(c.files)) {
				var listed []pkgSource
				for _, k := range order {
					listed = append(listed, c.files[k])
				}
				u, ds := checkListed(t, "main", listed, nil)
				if c.diags != (len(ds) > 0) {
					t.Fatalf("order %v: diagnostics %v", order, ds)
				}
				dump := dumpUnit(u, ds)
				var got map[string]string
				if !c.diags {
					got = lowerings(u)
					if c.refuse != (got["tests=false/error"] != "") {
						t.Fatalf("order %v: refusal %q", order, got["tests=false/error"])
					}
				}
				if i == 0 {
					wantDump, want = dump, got
					if !c.diags && !c.refuse {
						buildAndRun(t, []*lowered{mustLowerUnit(t, "canon", u, false)}, map[string]string{"canon": c.stdout})
					}
					if testing.Verbose() {
						t.Logf("dump %d lines; lowering %d files; refusal %q", strings.Count(dump, "\n"), len(got), got["tests=false/error"])
					}
					continue
				}
				if dump != wantDump {
					wl, gl := strings.Split(wantDump, "\n"), strings.Split(dump, "\n")
					for j := range wl {
						if j >= len(gl) || wl[j] != gl[j] {
							other := ""
							if j < len(gl) {
								other = gl[j]
							}
							t.Fatalf("order %v: the check result differs at line %d:\n%.2000s\nversus\n%.2000s", order, j+1, wl[j], other)
						}
					}
					t.Fatalf("order %v: the check result differs past line %d", order, len(wl))
				}
				if !c.diags {
					sameEmission(t, fmt.Sprintf("order %v", order), want, got)
				}
			}
		})
	}
}

// pathDiags is each diagnostic of a fresh check of files as listed, by the
// path of the file it names, sorted; and the paths the checker imported,
// in order.
func pathDiags(t *testing.T, files []pkgSource, order []int) (diags, imports []string) {
	t.Helper()
	var listed []pkgSource
	for _, k := range order {
		listed = append(listed, files[k])
	}
	u, _ := checkListed(t, "main", listed, nil)
	imp := &tracingImporter{}
	_, ds := check.Check("main", u.Files, imp)
	for _, d := range ds {
		diags = append(diags, fmt.Sprintf("%s %s", u.Sources[d.File].Path, d))
	}
	sort.Strings(diags)
	return diags, imp.calls
}

// tracingImporter serves std and records each import path asked for.
type tracingImporter struct{ calls []string }

func (i *tracingImporter) Import(path string) (*check.Package, error) {
	i.calls = append(i.calls, path)
	return check.Std.Import(path)
}

// The round 8 verify's probes, as regressions: for every listing of files
// of equal text under distinct names, of files with errors in several of
// them, of files that tie on their import-stripped text and of files that
// differ only in comments, the diagnostics name the same files by path,
// and the checker imports the same paths in the same order.
func TestVerifyR8EqualTextIdentity(t *testing.T) {
	for k, files := range [][]pkgSource{
		{{"/equal/a.ergo", "type A {}\n"}, {"/equal/b.ergo", "type A {}\n"}, {"/equal/main.ergo", "func main() {}\n"}},
		{{"/cycle/a.ergo", "let A = B\n"}, {"/cycle/b.ergo", "let B = A\n"}, {"/cycle/main.ergo", "func main() {}\n"}},
		{{"/bad/a.ergo", "func a() int { missing }\n"}, {"/bad/b.ergo", "func b() string { 7 }\n"}, {"/bad/main.ergo", "func main() {}\n"}},
		{{"/ties/a.ergo", "import log\nimport trace\ntype A {}\n"}, {"/ties/b.ergo", "import trace\nimport log\ntype A {}\n"}, {"/ties/main.ergo", "func main() {}\n"}},
		{{"/comments/a.ergo", "// alpha\ntype A {}\n"}, {"/comments/b.ergo", "// beta\ntype A {}\n"}, {"/comments/main.ergo", "func main() {}\n"}},
	} {
		var want, wantCalls []string
		for i, order := range permutations(len(files)) {
			got, calls := pathDiags(t, files, order)
			if len(got) == 0 {
				t.Fatalf("case %d: expected diagnostics", k)
			}
			if i == 0 {
				want, wantCalls = got, calls
			} else if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(wantCalls, calls) {
				t.Fatalf("case %d, listing %v: %v imports %v; want %v imports %v", k, order, got, calls, want, wantCalls)
			}
		}
	}
	// The duplicate is in the later name, as in Go.
	got, _ := pathDiags(t, []pkgSource{{"/equal/a.ergo", "type A {}\n"}, {"/equal/b.ergo", "type A {}\n"}, {"/equal/main.ergo", "func main() {}\n"}}, []int{1, 0, 2})
	if len(got) != 1 || !strings.HasPrefix(got[0], "/equal/b.ergo ") {
		t.Fatalf("want the duplicate in b.ergo, got %v", got)
	}
}

// TestCheckRefusesTwoFilesOfOneName: two files with the same non-empty
// name are a caller error, an internal compiler error (check/canon.go).
func TestCheckRefusesTwoFilesOfOneName(t *testing.T) {
	var files []*syntax.File
	for _, src := range []string{"type A {}\n", "type B {}\n"} {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = "same.ergo"
		files = append(files, f)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), `two files of one package are named "same.ergo"`) {
			t.Fatalf("recovered %v", r)
		}
	}()
	check.Check("main", files, nil)
}
