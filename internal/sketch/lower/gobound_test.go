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
	v1 "ergo.local/prototype/runtime/v1"
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
// every use unseen; and a diagnostic of the generator's own text (own on a
// parameter that is no resource, or a retired form) fails, so a change of the
// binding format cannot starve the property unseen (goBoundOwnFault).
//
// Each use writes own before each parameter it keeps, read from the
// binding's own and the checker's conservative Go default, so it holds
// with and without inference (q137), and writes in its needs clause what
// the release of its result requires, which the checker names
// (goBoundReleaseNeed: base64.NewEncoder's io.WriteCloser); a Go pointer
// is passed as a go.Ref,
// and a method value's literal of a method that keeps its receiver takes
// the receiver as an own parameter instead of capturing it.
//
// A struct method that takes a Go pointer has a captured-receiver use: the
// call on a let mut that an escaping literal captured. A binding method's
// receiver binds no parameter of its signature, so the call writes no
// receiver, and a write-after-capture diagnostic fails the property
// (goBoundFalseWrite; ESC-idx r2). time.Time.In's must keep its full form.
//
// A method (an extern with a receiver, or an operation of a Go interface
// type) has a second use, its method value called: `let f = recv.M`. A Go
// method is a call-only extern (grammar.md, Extern), so that form is S108
// and steps down to the literal the diagnostic names, which must lower;
// os.File.ReadDir's must reach it.
func TestEveryGoBoundFunctionLowers(t *testing.T) {
	var programs []*lowered
	var refused []string
	kept, plain, dropped := 0, 0, 0
	sawReadDir, sawMethodValue, sawCapture := false, false, false
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
			if d > 0 {
				refused = append(refused, u.target)
			}
			if o == nil {
				continue
			}
			programs = append(programs, o)
			if path == "os" && strings.Contains(o.src, "\tlet r = if c { try os.ReadDir(") && !strings.Contains(o.src, "import go io/fs") {
				sawReadDir = true
			}
			if path == "time" && strings.Contains(o.src, "\tlet keep = Some{Value: func() time.Time { rm }}\n\tlet r = if c { rm.In(a0) }") {
				sawCapture = true
			}
			// []fs.DirEntry holds Go interfaces, so it keeps the file
			// and the literal uses it inside (keptShort; GB1).
			if path == "os" && strings.Contains(o.src, "\tlet f = func(p0 int) fails go.Error needs go.Foreign {\n\t\tlet r = try recv.ReadDir(p0)\n\t\tlet _ = r\n\t}\n\tif c { try f(") {
				sawMethodValue = true
			}
		}
	}
	t.Logf("Go-bound functions: %d in full form, %d plain, %d the checker or lowering refuses", kept, plain, dropped)
	if !slices.Equal(refused, goBoundRefused) {
		// A refusal outside the intended set is a checker or lowering
		// defect (the method-call argument index, ESC-idx), not a use the
		// language rules out; a missing one is a rule that went unseen.
		t.Errorf("the refused uses are %q, want exactly %q", refused, goBoundRefused)
	}
	if !sawReadDir {
		t.Errorf("the os program lost its full-form os.ReadDir use: the generator no longer reaches the case the property guards")
	}
	if !sawCapture {
		t.Errorf("the time program lost its full-form captured receiver of time.Time.In: the generator no longer reaches the receiver-borrow rule")
	}
	if !sawMethodValue {
		// A Go method is an extern: its method value is S108, and the
		// literal that diagnostic names lowers (grammar.md, Extern).
		t.Errorf("the method value recv.ReadDir of os.File did not step down to the literal the extern rule names")
	}
	typecheckGo(t, programs)
}

// goBoundRefused are the uses the property's generator cannot write, in
// generation order: a generic Go function whose type parameter no argument
// fixes (D6, "cannot infer T").
var goBoundRefused = []string{"reflect.TypeAssert", "reflect.TypeFor"}

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
	// crossing are the names of the use's own parameters whose type the
	// checker reports is not a resource (S110, mode crossing): the
	// program writes its clauses without them (goBoundProgram).
	crossing map[string]bool
	// capture marks a captured-receiver use: a resource receiver that the
	// literal holds cannot be handed off, so its full form may step down
	// on the generator's text, which goBoundOwnFault does not fail.
	capture bool
	// releaseNeeds are the capabilities the release of the use's result
	// requires (a Go io.WriteCloser's Close needs go.Foreign), which no
	// binding row states: the checker names them (S102, goBoundReleaseNeed)
	// and the program writes them in its needs clause (goBoundText).
	releaseNeeds []string
	// keptShort is a method value's plain form whose literal uses the
	// result inside itself instead of returning it: a result that holds a
	// handle keeps the receiver, so the literal cannot return it past the
	// captured receiver (interop.md, Resources 5, keeps; GB1). The
	// checker's keeps diagnostic on the plain form switches to it
	// (goBoundKept); not a step down.
	keptShort string
}

// goBoundKept is the keeps rule's escape diagnostic (check/lend.go,
// keptEscapeText).
var goBoundKept = regexp.MustCompile(`this value comes from \S+ and keeps \S+: a Go method's result`)

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
	// (recv, its receiver type's text) its method value. recvOwn says the
	// binding keeps the receiver (own before a struct method's receiver
	// type, or before an interface operation's name, op).
	add := func(target, fn, recv string, recvOwn, op bool, tps *syntax.FieldList, ft *syntax.FuncType, end syntax.Pos) {
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
		var litArgs, args, types []string
		var callback, taken []bool
		pointerArg := false
		callee := name + "." + fn
		recvType := goBoundOwned(qualify(recv))
		if recv != "" {
			callee = "recv." + fn
		}
		// What the call keeps: the binding's own (before a parameter's
		// type, or for the receiver before the receiver's type or the
		// operation's name), and the conservative Go default the checker
		// adds to it: a parameter Go may keep unless runtime/v1's
		// reviewed table lends it (interop.md Resources 5,
		// check/hostclauses.go); a receiver only by the binding's own.
		// A callback keeps its own mode.
		takesRecv := recv != "" && recvOwn
		if ft.Params != nil {
			for _, fl := range ft.Params.List {
				n := max(1, len(fl.Names))
				for range n {
					a := fmt.Sprintf("a%d", len(args))
					p := fmt.Sprintf("p%d", len(args))
					args, litArgs = append(args, a), append(litArgs, p)
					pt := goBoundOwned(qualify(text(fl.Type)))
					pointerArg = pointerArg || strings.HasPrefix(text(fl.Type), "*")
					_, isFunc := fl.Type.(*syntax.FuncType)
					if isFunc && (fl.Own != nil || v1.GoSyncCallbacks()[target] == "") {
						// Go may keep a callback no review lists
						// (check/hostclauses.go), so the use's own
						// callback is owned (types.md, Function values 2).
						pt = "own " + pt
					}
					types = append(types, pt)
					callback = append(callback, isFunc)
					taken = append(taken, !isFunc && fl.Own != nil)
				}
			}
		}
		// A receiver is lent unless the binding writes own (the release,
		// or a result that may hold the receiver; GB1).
		off := 0
		if recv != "" && !op {
			off = 1
		}
		for i := range taken {
			if !callback[i] && !v1.GoArgLent(target, i+off) {
				taken[i] = true
			}
		}
		// mode is the mode word a kept parameter's type takes.
		mode := func(keep bool) string {
			if keep {
				return "own "
			}
			return ""
		}
		params := []string{"c bool"}
		if recv != "" {
			params = append(params, "recv "+mode(takesRecv)+recvType)
		}
		var litParams []string
		for i, pt := range types {
			params = append(params, args[i]+" "+mode(taken[i])+pt)
			litParams = append(litParams, litArgs[i]+" "+mode(taken[i])+pt)
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
		sig := strings.TrimSpace(qualify(tpText) + "(" + strings.Join(params, ", ") + ") " + rows)
		try := ""
		if strings.Contains(rows, "fails") {
			try = "try "
		}
		forms := func(callee string, args []string) (full, short string) {
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
		full, short := forms(callee, args)
		out = append(out, goBoundUse{
			target:  target,
			full:    sig + " {\n\t" + full + "\n}\n",
			short:   sig + " {\n\t" + short + "\n}\n",
			imports: paths,
		})
		if recv == "" {
			return
		}
		if !op && pointerArg {
			// The captured receiver: a method that takes a Go pointer,
			// called on a let mut that an escaping literal captured. A
			// binding method's receiver binds no parameter of its
			// signature, so the call borrows no receiver and writes none
			// (check.argParam, recvBorrow): the capture holds (types.md,
			// Function values 3; ESC-idx). Its plain form is the call's.
			fullC, _ := forms("rm."+fn, args)
			// let mut rm = recv moves recv, so the use owns it (a Go
			// receiver is lent, GB1); own on a crossing type narrows.
			// rm = sameRecv(rm) makes rm a reassigned let mut before the
			// capture; rm = rm would be S110 (a self-assignment, VET-CHECK
			// rank 1), so the program declares the own identity sameRecv.
			sigC := sig
			if !takesRecv {
				sigC = strings.Replace(sig, "recv "+recvType, "recv own "+recvType, 1)
			}
			out = append(out, goBoundUse{
				target:  target + " (captured receiver)",
				full:    sigC + " {\n\tlet mut rm = recv\n\trm = sameRecv(rm)\n\tlet keep = Some{Value: func() " + recvType + " { rm }}\n\t" + fullC + "\n\tlet _ = keep\n}\n",
				short:   sig + " {\n\t" + short + "\n}\n",
				imports: paths,
				capture: true,
			})
		}
		// The method value: in full form recv.M itself, called; in plain
		// form the literal a program passes where the method value is
		// refused (grammar.md, Extern), called. A literal captures recv
		// only when the method lends it: a captured resource cannot be
		// handed off (resources R10, R17), so a literal for a method that
		// takes its receiver takes the receiver as its parameter rv.
		// The literal writes the result type, so it imports what that
		// names.
		litRows := goBoundRows(qualify(src[ft.Params.End():end]))
		lit := strings.TrimSpace("func("+strings.Join(litParams, ", ")+") "+litRows) + " { " + try + callee + "(" + strings.Join(litArgs, ", ") + ") }"
		fullV, _ := forms("f", args)
		plainV := fullV
		if takesRecv {
			lit = strings.TrimSpace("func("+strings.Join(append([]string{"rv own " + recvType}, litParams...), ", ")+") "+litRows) + " { " + try + "rv." + fn + "(" + strings.Join(litArgs, ", ") + ") }"
			plainV, _ = forms("f", append([]string{"recv"}, args...))
		}
		litPaths := slices.Clone(paths)
		for _, m := range goBoundQualifier.FindAllStringSubmatch(lit, -1) {
			if p, ok := imports[m[1]]; ok && !slices.Contains(litPaths, p) {
				litPaths = append(litPaths, p)
			}
		}
		sort.Strings(litPaths)
		// The kept literal: no result, the result bound and used inside.
		keptShort := ""
		if results > 0 && !takesRecv {
			use := "let r = " + try + callee + "(" + strings.Join(litArgs, ", ") + ")\n\t\tlet _ = r"
			if results > 1 {
				names := make([]string, results)
				for i := range names {
					names[i] = fmt.Sprintf("r%d", i)
				}
				use = "let (" + strings.Join(names, ", ") + ") = " + try + callee + "(" + strings.Join(litArgs, ", ") + ")"
				for _, n := range names {
					use += "\n\t\tlet _ = " + n
				}
			}
			keptRows := goBoundRows(qualify(src[resEnd:end]))
			keptLit := strings.TrimSpace("func("+strings.Join(litParams, ", ")+") "+keptRows) + " {\n\t\t" + use + "\n\t}"
			call := try + "f(" + strings.Join(args, ", ") + ")"
			keptShort = sig + " {\n\tlet f = " + keptLit + "\n\tif c { " + call + " } else { " + call + " }\n}\n"
		}
		out = append(out, goBoundUse{
			target:       target + " (method value)",
			full:         sig + " {\n\tlet f = " + callee + "\n\t" + fullV + "\n}\n",
			short:        sig + " {\n\tlet f = " + lit + "\n\t" + plainV + "\n}\n",
			imports:      paths,
			plainImports: litPaths,
			keptShort:    keptShort,
		})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Extern == nil {
				continue
			}
			recv, recvOwn := "", false
			if d.Recv != nil {
				recv = text(d.Recv.List[0].Type)
				recvOwn = d.Recv.List[0].Own != nil
			}
			add(d.Extern.Target.Value, d.Name.Name, recv, recvOwn, false, d.TypeParams, d.Type, d.End())
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
					add(d.Extern.Target.Value+"."+n.Name, n.Name, d.Name.Name, m.Own != nil, true, nil, ft, m.Type.End())
				}
			}
		}
	}
	for i := range out {
		out[i].crossing = map[string]bool{}
	}
	return out
}

// goBoundRows is the rows text of a binding's signature, trimmed.
func goBoundRows(s string) string {
	return strings.TrimSpace(s)
}

// goBoundOwned is the type a use declares for a parameter of the binding's
// type t: a Go pointer `*S` is the owned handle go.Ref[S], since Go keeps
// its arguments by default and a borrow cannot be passed to a kept Go
// argument (interop.md Resources 5; a go.Ref is accepted where Go takes
// `*S`, q38). Any other type is t.
func goBoundOwned(t string) string {
	if s, ok := strings.CutPrefix(t, "*"); ok {
		return "go.Ref[" + goBoundBorrows.ReplaceAllString(s, "${1}mut ") + "]"
	}
	// A function type's borrow parameter (func(*Flag)) is the borrow,
	// written mut in Ergo source (MODES-2).
	return goBoundBorrows.ReplaceAllString(t, "${1}mut ")
}

// goBoundBorrows finds a borrow *T written as a function type's parameter.
var goBoundBorrows = regexp.MustCompile(`([(,] ?|[(,] ?[a-z_][A-Za-z0-9_]* )\*`)

// goBoundOwnFault marks a diagnostic that the generator's own text causes:
// own on a name whose type is no resource after narrowing, a retired
// form, or an own the use leaves out (a resource the call takes, or a
// capture it hands off). Such a use
// steps down on the generator, not on the binding, so it fails the
// property: a change of the binding format must not starve the property
// unseen (batch 17 M2).
var goBoundOwnFault = regexp.MustCompile(`takes \S+ has no effect|is gone: write own|is lent to this function literal, so it cannot hand|cannot hand \S+ off`)

// goBoundFalseWrite is the diagnostic of a write after an escaping capture
// (check/escaping.go, writeAfterText and captureWriteText). A use writes
// its captured receiver only before the capture, so the call itself must
// write nothing: a binding method borrows no receiver (ESC-idx).
var goBoundFalseWrite = regexp.MustCompile(`is written here after a function literal or block captured it|is a let mut of an enclosing frame, and this function literal`)

// goBoundCrossing is S110 on a name of a use's clause: the name's type
// crosses (mode crossing, resources R1), a fact of the type the checker
// decides and the binding's text does not state (a binding's own on a
// crossing value is inert). The generator writes the name without own and
// checks the program again; this is not a step down.
var goBoundCrossing = regexp.MustCompile(`takes (\w+) has no effect`)

// goBoundReleaseNeed is S102 on a capability that the release of a value
// the use owns requires (resources, Context ownership 6): the row is
// written, not inferred (EX2), so the generator adds the capability to the
// use's needs clause and checks the program again; not a step down.
var goBoundReleaseNeed = regexp.MustCompile(`, run by the release of .* requires (\S+), which is not in the capability row of function use\d+`)

// goBoundOwnText is a kept parameter the generator writes: its name and
// own before a type that is not a function's (a callback's own is its
// mode, not a kept resource).
var goBoundOwnText = regexp.MustCompile(`\b(\w+) own (?:func\b)?`)

// goBoundNames are the names u's kept parameters write in form f.
func goBoundNames(u goBoundUse, f goBoundForm) map[string]bool {
	out := map[string]bool{}
	for _, m := range goBoundOwnText.FindAllStringSubmatch(goBoundText(u, f), -1) {
		if !strings.HasSuffix(m[0], "func") {
			out[m[1]] = true
		}
	}
	return out
}

// goBoundText is u's text in form f, without own on u's crossing names.
func goBoundText(u goBoundUse, f goBoundForm) string {
	text := u.full
	if f == goBoundPlain {
		text = u.short
	}
	text = goBoundOwnText.ReplaceAllStringFunc(text, func(m string) string {
		sub := goBoundOwnText.FindStringSubmatch(m)
		if u.crossing[sub[1]] && !strings.HasSuffix(m, "func") {
			return sub[1] + " "
		}
		return m
	})
	if len(u.releaseNeeds) == 0 {
		return text
	}
	// The function's own signature is the text's first line.
	sig, body, _ := strings.Cut(text, " {\n")
	needs := strings.Join(u.releaseNeeds, ", ")
	switch {
	case strings.Contains(sig, " needs "):
		sig = strings.Replace(sig, " needs ", " needs "+needs+", ", 1)
	default:
		sig += " needs " + needs
	}
	return sig + " {\n" + body
}

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
		// First narrow each clause to the names whose type is a resource:
		// a clause name the checker reports crosses (S110) leaves the
		// clause, and the program is checked again before any step down.
		useAt := func(line int) int {
			i := -1
			for j, s := range starts {
				if s != 0 && s <= line {
					i = j
				}
			}
			return i
		}
		narrowed := false
		for _, l := range goBoundFaults(o) {
			m := goBoundCrossing.FindStringSubmatch(l.text)
			if i := useAt(l.line); m != nil && i >= 0 && !uses[i].crossing[m[1]] && goBoundNames(uses[i], forms[i])[m[1]] {
				uses[i].crossing[m[1]] = true
				narrowed = true
			}
			if i := useAt(l.line); i >= 0 && forms[i] == goBoundPlain && uses[i].keptShort != "" && goBoundKept.MatchString(l.text) {
				uses[i].short, uses[i].keptShort, uses[i].plainImports = uses[i].keptShort, "", nil
				narrowed = true
			}
			m = goBoundReleaseNeed.FindStringSubmatch(l.text)
			if i := useAt(l.line); m != nil && i >= 0 && !slices.Contains(uses[i].releaseNeeds, m[1]) {
				uses[i].releaseNeeds = append(uses[i].releaseNeeds, m[1])
				narrowed = true
			}
		}
		if narrowed {
			continue
		}
		stepped := false
		seen := map[int]bool{}
		for _, l := range goBoundFaults(o) {
			i := useAt(l.line)
			if goBoundOwnFault.MatchString(l.text) && (i < 0 || !uses[i].capture || forms[i] != goBoundFull) {
				t.Errorf("%s: the generator's own text refuses a use: %s\n%s", path, l.text, src)
			}
			if goBoundFalseWrite.MatchString(l.text) {
				// No use writes a captured local after its capture.
				t.Errorf("%s: the checker reports a write the use does not make: %s\n%s", path, l.text, src)
			}
			if i >= 0 && !l.internal && !seen[i] {
				seen[i] = true
				if goBoundDebug {
					t.Logf("%s: %s steps from form %d: %s", path, uses[i].target, forms[i], l.text)
				}
				forms[i]++
				goBoundDrop(t, path, uses[i], forms[i], l.text)
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
					goBoundDrop(t, path, uses[i], forms[i], faults[0].text)
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

// goBoundStated are the refusals of a plain use that the language states,
// each with its rule: a use the checker refuses in both forms for any
// other reason fails the property, so a checker defect cannot hide among
// the dropped uses (fu-gobound-recv: an off-by-one receiver in
// paramNonEscaping refused 16 Go methods that take a callback).
var goBoundStated = []struct {
	text *regexp.Regexp
	rule string
}{
	// reflect.TypeFor, reflect.TypeAssert: T is only in the result, so a
	// call must write its type arguments (D6).
	{regexp.MustCompile(`cannot infer \w+`), "D6"},
}

// goBoundDrop fails the property when u, at form f after a fault text,
// is dropped for a reason goBoundStated does not state.
func goBoundDrop(t *testing.T, path string, u goBoundUse, f goBoundForm, text string) {
	t.Helper()
	if f != goBoundDropped {
		return
	}
	for _, s := range goBoundStated {
		if s.text.MatchString(text) {
			return
		}
	}
	t.Errorf("%s: %s: the checker or the lowering refuses its plain use with no stated reason: %s", path, u.target, text)
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
		if forms[i] == goBoundDropped {
			continue
		}
		text := goBoundText(u, forms[i])
		text = "func use" + strconv.Itoa(i) + text + "\n"
		starts[i] = line
		line += strings.Count(text, "\n")
		b.WriteString(text)
	}
	for i, u := range uses {
		if u.capture && forms[i] == goBoundFull {
			b.WriteString("func sameRecv[T own](x own T) T { x }\n")
			break
		}
	}
	return b.String(), starts
}
