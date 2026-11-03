package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// infoCorpus is every program TestInfoComplete reads: the fixtures, the
// stage fixtures copied into testdata/v1, the prototype's translated
// fixtures and the syntax corpus. A program with a diagnostic is not
// lowered, so only the clean ones count.
func infoCorpus(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/v1/*.ergo",
		"../proto/testdata/*.ergo",
		"../proto/testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
	} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out[f] = string(data)
		}
	}
	return out
}

// infoGaps lists what Info misses in a clean package (invariant 1) and
// the types it holds that are not final (invariant 2): every expression in
// a body or a package let has a Types entry, every identifier a Defs or
// Uses entry, every call a Calls entry, every call of a generic function
// and every instantiation used as a value an Instances entry, every pattern node of a match,
// let or for a Patterns entry of its form (a binding holding the Var its
// Defs entry holds), every catch arm a Patterns
// entry, every match and catch a Matches entry, every for-in a Loops entry
// and every with a Withs entry. Declarations count too: every declared
// name (function, type, context, type parameter, parameter, record field,
// operation and operation parameter) has a Defs entry, and every named
// context's providers are expressions with an Entries entry and, for one
// capability, an Implicit conversion. Names that denote no object are not
// identifiers of the contract: a field or operation name after a dot or
// before a colon in a record literal, a package name before a dot, and the
// blank `_`. An inline context's entries are stage E's (196x), so a with's
// argument is exempt. Invariant 2 holds for every Types entry, but the
// name in an explicit instantiation Fn[int] keeps its generic type, as in
// go/types.
func infoGaps(file *syntax.File, src string, info *Info) []string {
	var gaps []string
	at := func(n syntax.Node, what string) {
		line := 1 + strings.Count(src[:min(int(n.Pos()), len(src))], "\n")
		gaps = append(gaps, fmt.Sprintf("%d: %s", line, what))
	}
	notObject := map[*syntax.Ident]bool{}
	notValue := map[syntax.Expr]bool{}
	instName := map[syntax.Expr]bool{}          // the name in an explicit instantiation
	calledIndex := map[*syntax.IndexExpr]bool{} // Fn[int] called: its call holds the instance
	var own map[*TypeParam]bool                 // the enclosing declaration's type parameters
	// skip marks a subtree that holds no value expression: a written type,
	// a pattern (Patterns holds it) or a with's context (Withs holds it; an
	// inline context's entries are stage E's).
	skip := func(n syntax.Node, objects bool) {
		syntax.Inspect(n, func(m syntax.Node) bool {
			if x, ok := m.(syntax.Expr); ok {
				notValue[x] = true
			}
			if id, ok := m.(*syntax.Ident); ok && objects {
				notObject[id] = true
			}
			return true
		})
	}
	// pattern checks a pattern tree (stage B): every node has a Patterns
	// entry of the node's form, a record or tuple pattern one sub-pattern
	// per element, and a binding the Var its Defs entry holds. The subtree
	// holds no value expression.
	var pattern func(p syntax.Expr)
	pattern = func(p syntax.Expr) {
		pat := info.Patterns[p]
		if pat == nil {
			at(p, fmt.Sprintf("a pattern %T with no Patterns entry", p))
			skip(p, false)
			return
		}
		notValue[p] = true
		bound := func(id *syntax.Ident) {
			if id.Name != "_" && (pat.Var == nil || info.Defs[id] != pat.Var) {
				at(id, "the pattern binding "+id.Name+" whose Var is not its Defs entry")
			}
		}
		switch x := p.(type) {
		case *syntax.ParenExpr:
			pattern(x.X)
		case *syntax.Ident:
			switch pat.Kind {
			case PatBind:
				bound(x)
			case PatTypeTest:
				if pat.Type == nil || info.Uses[x] == nil {
					at(x, "the type test "+x.Name+" with no Type or no Uses entry")
				}
			case PatWildcard, PatConst:
			default:
				at(x, fmt.Sprintf("the pattern name %s of kind %d", x.Name, pat.Kind))
			}
		case *syntax.CompositeLit:
			if pat.Kind != PatRecord || pat.Type == nil || len(pat.Fields) != len(x.Elts) {
				at(x, "a record pattern whose Pattern is not a record of its fields")
			}
			skip(x.Type, false)
			for _, e := range x.Elts {
				if kv, ok := e.(*syntax.KeyValueExpr); ok {
					notValue[kv] = true
					if id, ok := kv.Key.(*syntax.Ident); ok {
						notValue[id], notObject[id] = true, true
					}
					pattern(kv.Value)
					continue
				}
				pattern(e)
			}
		case *syntax.TupleExpr:
			if pat.Kind != PatTuple || len(pat.Elems) != len(x.Elts) {
				at(x, "a tuple pattern whose Pattern is not a tuple of its elements")
			}
			for _, e := range x.Elts {
				pattern(e)
			}
		case *syntax.AsPattern:
			if pat.Kind != PatAs || len(pat.Elems) != 1 {
				at(x, "an as pattern whose Pattern is not an as")
			}
			bound(x.Name)
			notValue[x.Name] = true
			pattern(x.X)
		default:
			// A constant (a literal, a signed literal, a qualified
			// constant) or a qualified type test.
			if pat.Kind != PatConst && pat.Kind != PatTypeTest {
				at(x, fmt.Sprintf("a %T pattern of kind %d", x, pat.Kind))
			}
			skip(x, false)
		}
	}
	var visit func(n syntax.Node) bool
	visit = func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.DeclStmt:
			for _, spec := range n.Decl.Specs {
				if spec.Type != nil {
					skip(spec.Type, false)
				}
				if spec.Pattern != nil {
					pattern(spec.Pattern)
				} else if len(spec.Names) == 1 && spec.Names[0].Name != "_" && info.Defs[spec.Names[0]] == nil {
					// The frozen tree stores a single bare name in Names.
					// A known type or constant tests instead of declaring a
					// binder, and must have the same facts as any pattern.
					pattern(spec.Names[0])
				}
			}
		case *syntax.InterpolatedString:
			for _, text := range n.Texts {
				notValue[text] = true
			}
		case *syntax.TypeGuard:
			notValue[n] = true
		case *syntax.UnaryExpr:
			// A signed literal is one literal: its unsigned part, with its
			// parentheses, has no Types or Values entry (info.go, Types).
			if n.Op == syntax.SUB || n.Op == syntax.ADD {
				if _, ok := unparen(n.X).(*syntax.BasicLit); ok {
					for y := n.X; ; {
						notValue[y] = true
						if _, typed := info.Types[y]; typed {
							at(y, "the unsigned part of a signed literal with a Types entry")
						}
						if _, valued := info.Values[y]; valued {
							at(y, "the unsigned part of a signed literal with a Values entry")
						}
						p, ok := y.(*syntax.ParenExpr)
						if !ok {
							break
						}
						y = p.X
					}
				}
			}
		case *syntax.SelectorExpr:
			notValue[n.Sel] = true
			notObject[n.Sel] = true
			if id, ok := n.X.(*syntax.Ident); ok {
				if _, isPkg := info.Uses[id].(*PkgName); isPkg {
					notValue[id] = true // a package name is no value
				}
			}
		case *syntax.KeyValueExpr:
			if id, ok := n.Key.(*syntax.Ident); ok {
				notObject[id] = true
				notValue[id] = true
			}
			notValue[n] = true
		case *syntax.CompositeLit:
			if n.Type != nil {
				skip(n.Type, false)
			}
		case *syntax.IndexExpr:
			// Fn[T]: the brackets hold type arguments, which Calls holds.
			if _, generic := info.Types[n.X].(*Signature); generic {
				for _, ix := range n.Indices {
					skip(ix, false)
				}
				instName[n.X] = true
				// An instantiation used as a value, Max[int], has its
				// Instances entry; a called one is its call's (stage E1, C9).
				if inst := info.Instances[n]; !calledIndex[n] && (inst == nil || len(inst.TypeArgs) != len(n.Indices)) {
					at(n, "an instantiation used as a value with no Instances entry of its type arguments")
				}
			}
		case *syntax.TypeSwitchStmt:
			if info.Matches[n] == nil {
				at(n, "a match with no Matches entry")
			}
			for _, cl := range n.Clauses {
				for _, item := range cl.List {
					pattern(item)
				}
			}
		case *syntax.SwitchStmt:
			if info.Matches[n] == nil {
				at(n, "a value match with no Matches entry")
			}
			for _, cl := range n.Clauses {
				for _, item := range cl.List {
					pattern(item)
				}
			}
		case *syntax.CatchExpr:
			if info.Matches[n] == nil {
				at(n, "a catch with no Matches entry")
			}
			for _, arm := range n.Arms {
				for _, te := range arm.Types {
					if info.Patterns[te] == nil {
						at(te, "a catch arm with no Patterns entry")
					}
					skip(te, false)
				}
			}
		case *syntax.RangeStmt:
			if info.Loops[n] == nil {
				at(n, "a for-in with no Loops entry")
			}
			for _, b := range []syntax.Expr{n.Key, n.Value} {
				id, plain := b.(*syntax.Ident)
				// A single name that names a type or a constant tests
				// instead of binding (the one binder rule, stage D2), and
				// has the facts of any pattern.
				if b != nil && (!plain || (id.Name != "_" && info.Defs[id] == nil)) {
					pattern(b)
				}
			}
		case *syntax.CallExpr:
			if info.Calls[n] == nil {
				at(n, "a call with no Calls entry")
			} else if k := info.Calls[n].Kind; k == CallPrelude || k == CallLen {
				notValue[n.Fun] = true // a built-in callee is no value
			} else if _, inst := n.Fun.(*syntax.IndexExpr); inst && k == CallConversion {
				// Seq[int](f): the callee is a type, which Calls holds.
				skip(n.Fun, false)
			}
			// A call of a generic function has its Instances entry, with the
			// type arguments its Calls entry holds (stage E1, C9).
			if call := info.Calls[n]; call != nil && call.Kind == CallFunc && call.Func != nil && call.Func.Sig != nil && len(call.Func.Sig.TParams) > 0 {
				inst := info.Instances[n]
				switch {
				case inst == nil:
					at(n, "a call of the generic function "+call.Func.Name+" with no Instances entry")
				case inst.Func != call.Func || fmt.Sprint(inst.TypeArgs) != fmt.Sprint(call.TypeArgs):
					at(n, fmt.Sprintf("a call whose Instances entry %s%v is not its Calls entry %s%v", inst.Func.Name, inst.TypeArgs, call.Func.Name, call.TypeArgs))
				}
				if ix, ok := unparen(n.Fun).(*syntax.IndexExpr); ok {
					calledIndex[ix] = true
				}
			}
			if id, ok := n.Fun.(*syntax.Ident); ok && id.Name == "with" {
				notObject[id] = true
				notValue[id] = true
				if info.Withs[n] == nil {
					at(n, "a with with no Withs entry")
				}
				for _, a := range n.Args {
					skip(a, true)
				}
			}
		case *syntax.FuncLit:
			skip(n.Type, false)
		}
		if _, isStmt := n.(syntax.Stmt); isStmt {
			return true // an if or match statement: Tails and Matches hold it
		}
		switch n := n.(type) {
		case *syntax.Ident:
			if n.Name != "_" && !notObject[n] && info.Defs[n] == nil && info.Uses[n] == nil {
				at(n, "the identifier "+n.Name+" with no Defs or Uses entry")
			}
			if !notValue[n] && info.Defs[n] == nil {
				if _, ok := info.Types[n]; !ok && n.Name != "_" {
					at(n, "the expression "+n.Name+" with no Types entry")
				}
			}
		case syntax.Expr:
			if _, ok := info.Types[n]; !ok && !notValue[n] {
				at(n, fmt.Sprintf("a %T with no Types entry", n))
			}
		}
		// Invariant 2, for every expression with a Types entry. The one
		// exception: the name in an explicit instantiation Fn[int] keeps
		// its generic type, as in go/types (round-4 M3).
		if x, ok := n.(syntax.Expr); ok {
			if t, ok := info.Types[x]; ok {
				if why := notFinal(t, own); why != "" {
					at(n, fmt.Sprintf("a %T whose type %s %s", n, t, why))
				}
				if sig, ok := t.(*Signature); ok && len(sig.TParams) > 0 && !instName[x] {
					at(n, fmt.Sprintf("a %T whose type %s is generic outside an explicit instantiation", n, t))
				}
			}
		}
		return true
	}
	// declared lists a declaring identifier with no Defs entry.
	declared := func(id *syntax.Ident, what string) {
		if id != nil && id.Name != "_" && info.Defs[id] == nil {
			at(id, what+" "+id.Name+" with no Defs entry")
		}
	}
	fields := func(l *syntax.FieldList, what string) {
		if l == nil {
			return
		}
		for _, f := range l.List {
			for _, id := range f.Names {
				declared(id, what)
			}
		}
	}
	for _, d := range file.Decls {
		own = nil
		switch d := d.(type) {
		case *syntax.FuncDecl:
			declared(d.Name, "the function")
			fields(d.TypeParams, "the type parameter")
			own = map[*TypeParam]bool{}
			if d.TypeParams != nil {
				for _, f := range d.TypeParams.List {
					for _, id := range f.Names {
						if tp, ok := info.Defs[id].(*TypeParam); ok {
							own[tp] = true
						}
					}
				}
			}
			if d.Type != nil {
				fields(d.Type.Params, "the parameter")
			}
			if d.Body != nil {
				syntax.Inspect(d.Body, visit)
			}
		case *syntax.TypeDecl:
			declared(d.Name, "the type")
			fields(d.TypeParams, "the type parameter")
			// Record fields, operations and their parameters (m1).
			syntax.Inspect(d.Spec, func(m syntax.Node) bool {
				switch m := m.(type) {
				case *syntax.StructType:
					fields(m.Fields, "the field")
				case *syntax.InterfaceType:
					for _, op := range m.Methods {
						for _, id := range op.Names {
							declared(id, "the operation")
						}
						if ft, ok := op.Type.(*syntax.FuncType); ok {
							fields(ft.Params, "the operation parameter")
						}
					}
				}
				return true
			})
		case *syntax.VarDecl:
			own = map[*TypeParam]bool{}
			for _, spec := range d.Specs {
				for _, v := range spec.Values {
					syntax.Inspect(v, visit)
				}
				if spec.Pattern != nil {
					pattern(spec.Pattern)
					// Its names are package values: each has its Defs entry.
					syntax.Inspect(spec.Pattern, visit)
				}
			}
		case *syntax.ContextDecl:
			// A named context (spec D-A4; review M4): each provider is an
			// expression of the contract, its entry is in the context's
			// Entries with a conversion per capability, and a provider of
			// one capability has its Implicit conversion when its type is
			// not the capability. An inline context's entries are stage E's
			// (196x): TestInfoComplete exempts them (the with case above).
			own = map[*TypeParam]bool{}
			declared(d.Name, "the wire")
			context, _ := info.Defs[d.Name].(*Context)
			if context == nil {
				at(d, "a named wire with no Wire object")
				continue
			}
			entries := map[syntax.Expr]*ContextEntry{}
			embeds := 0
			for _, e := range context.Entries {
				if e.Provider != nil {
					entries[e.Provider] = e
				} else if e.Embedded != nil {
					embeds++
				}
			}
			wantEmbeds := 0
			for _, e := range d.Body.Entries {
				if e.Kind == syntax.ContextEmbed {
					wantEmbeds++
					continue
				}
				syntax.Inspect(e.Value, visit)
				entry := entries[e.Value]
				switch {
				case entry == nil:
					at(e.Value, "a wire provider with no Entries entry")
				case len(entry.Conversions) != len(entry.Caps) || len(entry.Caps) != 1+len(e.Keys):
					at(e.Value, fmt.Sprintf("a wire entry with %d capabilities and %d conversions", len(entry.Caps), len(entry.Conversions)))
				case len(entry.Caps) == 1 && entry.Conversions[0].Kind != 0:
					if _, ok := info.Implicit[e.Value]; !ok {
						at(e.Value, "a wire provider with no Implicit conversion")
					}
				}
			}
			if embeds != wantEmbeds {
				at(d, fmt.Sprintf("a named wire with %d embeddings and %d embedded Entries", wantEmbeds, embeds))
			}
			// Each capability the context binds has a provider entry that
			// lists it (Context.Provider), and each override names two
			// provider entries of one capability, the later its own.
			context.EachProvider(func(cap Type, e *ContextEntry) bool {
				if e == nil || e.Provider == nil || !listsCap(e, cap) {
					at(d, "a named wire whose entry for "+typeString(cap)+" does not provide it")
				}
				return true
			})
			for _, o := range context.Overrides {
				if o.Entry == nil || o.By == nil || o.Entry.Provider == nil || o.By.Provider == nil || !listsCap(o.Entry, o.Cap) || !listsCap(o.By, o.Cap) || entries[o.By.Provider] != o.By {
					at(d, "a named wire with an override whose entries do not both list its capability")
				}
			}
		}
	}
	// Each with of a named context holds one provider entry per discharged
	// capability: the entry its context's Provider returns, which lists it.
	for x, w := range info.Withs {
		if len(w.Contexts) == 0 {
			continue // an inline context value: stage E (196x)
		}
		if len(w.Entries) != len(w.Discharged) {
			at(x, fmt.Sprintf("a with of %d capabilities with %d Entries", len(w.Discharged), len(w.Entries)))
			continue
		}
		for i, d := range w.Discharged {
			e := w.Entries[i]
			if e == nil || e.Provider == nil || !listsCap(e, d) || w.Contexts[0].Provider(d) != e {
				at(x, "a with whose entry for "+typeString(d)+" does not provide it")
			}
		}
	}
	// Stage E2 (C13): no recorded type argument holds a Task.
	gaps = append(gaps, taskArgGaps(info)...)
	// Stage E2 (C18): no frame need outside a function body.
	gaps = append(gaps, frameGaps(file, info)...)
	sort.Strings(gaps)
	return gaps
}

// listsCap reports whether a provider entry lists cap among its keys.
func listsCap(e *ContextEntry, cap Type) bool {
	k, ok := keyOf(cap)
	if !ok {
		return false
	}
	for _, c := range e.Caps {
		if ck, ok := keyOf(c); ok && ck == k {
			return true
		}
	}
	return false
}

// cleanInfoGaps checks src as one package and returns infoGaps when the
// package is clean, and nil when it has a diagnostic (it is not lowered).
func cleanInfoGaps(src string) []string {
	file, err := syntax.Parse(src)
	if err != nil {
		return nil
	}
	pkg, diags := Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		return nil
	}
	return infoGaps(file, src, pkg.Info)
}

// InfoGaps is cleanInfoGaps for the external tests (FuzzCheck).
var InfoGaps = cleanInfoGaps

// notFinal says why a type in Info is not final (invariant 2), or "": it
// is invalid or holds an invalid part, it holds a row variable, or it
// holds a type parameter that own (the enclosing generic declaration's
// parameters) does not list. A nil own skips the parameter check.
func notFinal(t Type, own map[*TypeParam]bool) string {
	switch {
	case isInvalid(t):
		return "is invalid"
	case containsInvalid(t):
		return "holds an invalid part"
	}
	why := ""
	seen := map[Type]bool{}
	var walk func(t Type)
	row := func(r *Row) {
		for _, e := range r.Elems() {
			if _, isVar := e.(*RowVar); isVar && why == "" {
				why = "holds a row variable"
			}
			walk(e)
		}
	}
	walk = func(t Type) {
		if t == nil || why != "" || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *TypeParam:
			if own != nil && !own[t] {
				why = "holds the type parameter " + t.Name + " outside its declaration"
			}
		case *Named:
			for _, a := range t.TArgs {
				walk(a)
			}
		case *Pointer:
			walk(t.Elem)
		case *Slice:
			walk(t.Elem)
		case *Map:
			walk(t.Key)
			walk(t.Elem)
		case *Tuple:
			for _, e := range t.Types {
				walk(e)
			}
		case *Struct:
			for _, f := range t.Fields {
				walk(f.Type)
			}
		case *Union:
			for _, v := range t.Variants {
				walk(v)
			}
		case *Signature:
			if len(t.TParams) > 0 {
				// A generic function's own type: its parameters are bound
				// here (an explicit instantiation's name keeps it).
				return
			}
			for _, p := range t.Params {
				walk(p.Type)
			}
			for _, r := range t.Results {
				walk(r)
			}
			row(t.Errors)
			row(t.Uses)
		}
	}
	walk(t)
	return why
}

// TestInfoComplete (spec D-A4): over every clean program of the corpus,
// Info is complete and final (invariants 1 and 2), and checking twice
// gives the same Calls, Instances, Loops and Matches (invariant 4).
func TestInfoComplete(t *testing.T) {
	clean := 0
	for name, src := range infoCorpus(t) {
		file, err := syntax.Parse(src)
		if err != nil {
			continue
		}
		pkg, diags := Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			continue
		}
		clean++
		if gaps := infoGaps(file, src, pkg.Info); len(gaps) > 0 {
			t.Errorf("%s:\n\t%s", name, strings.Join(gaps, "\n\t"))
		}
		again, _ := Check("main", []*syntax.File{file}, nil)
		if a, b := infoDigest(pkg.Info), infoDigest(again.Info); a != b {
			t.Errorf("%s: two checks give different Info:\n%s\n----\n%s", name, a, b)
		}
	}
	if clean < 20 {
		t.Fatalf("only %d clean programs in the corpus", clean)
	}
}

// infoDigest writes Info's call, instance, loop and match entries in
// source order.
func infoDigest(info *Info) string {
	var lines []string
	for x, call := range info.Calls {
		fn := ""
		if call.Func != nil {
			fn = call.Func.Name
		}
		lines = append(lines, fmt.Sprintf("%d call %d %s %v %s %s", x.Pos(), call.Kind, fn, call.TypeArgs, rowText(call.Fails), rowText(call.Needs)))
	}
	for x, inst := range info.Instances {
		ws := ""
		for _, w := range inst.Witnesses {
			ws += fmt.Sprintf(" %s=%s{", w.Param.Name, w.Arg)
			for _, op := range w.Ops {
				inst := ""
				if op.Inst != nil {
					inst = fmt.Sprintf("%v", op.Inst.TypeArgs)
				}
				ws += fmt.Sprintf("%s:%v:%v%s ", op.Name, op.Func != nil, op.Sig, inst)
			}
			ws += "}"
		}
		lines = append(lines, fmt.Sprintf("%d instance %s %v%s", x.Pos(), inst.Func.Name, inst.TypeArgs, ws))
	}
	for s, l := range info.Loops {
		lines = append(lines, fmt.Sprintf("%d loop %d %s %s %d", s.Pos(), l.Domain, l.Elem, l.Pos, len(l.Bindings)))
	}
	for n, m := range info.Matches {
		lines = append(lines, fmt.Sprintf("%d match %v %d %v %v", n.Pos(), m.Subjects, m.Arms, m.Result, m.Partial))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestInfoContract reads the entries of one program the lowering needs:
// the call kinds and what each calls, a dot call's receiver, inferred type
// arguments, constant values, the implicit conversion of a value into an
// operation type with its table, loop domains and the defs of parameters.
func TestInfoContract(t *testing.T) {
	const src = `type Store { Get() string }
type Mem { V string }

func Get(m Mem) string { return m.V }

func Id[T](x T) T {
	return x
}

func NewStore(v string) Store { return Mem{V: v} }

func use(s Store, m Mem, xs []int8) int {
	let n = Id(xs)
	let mut total = len(n) + 2 * 3
	for i, x in xs {
		let y = int(x) + i
		s.Get()
		m.Get()
		total += y
	}
	for k in 0..total {
		total += k
	}
	return total
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	info := pkg.Info
	calls := map[string]*Call{}
	for x, call := range info.Calls {
		calls[src[x.Pos():x.End()]] = call
	}
	want := func(text string, kind CallKind) *Call {
		t.Helper()
		call := calls[text]
		if call == nil || call.Kind != kind {
			t.Fatalf("%s: want call kind %d, got %+v", text, kind, call)
		}
		return call
	}
	if c := want("Id(xs)", CallFunc); c.Func.Name != "Id" || len(c.TypeArgs) != 1 || c.TypeArgs[0].String() != "[]int8" {
		t.Errorf("Id(xs): %+v", c)
	}
	want("len(n)", CallLen)
	if c := want("int(x)", CallConversion); c.Type.String() != "int" {
		t.Errorf("int(x): %+v", c)
	}
	if c := want("s.Get()", CallOp); c.Op == nil || c.Op.Name != "Get" || src[c.Recv.Pos():c.Recv.End()] != "s" {
		t.Errorf("s.Get(): %+v", c)
	}
	if c := want("m.Get()", CallFunc); c.Func.Name != "Get" || src[c.Recv.Pos():c.Recv.End()] != "m" {
		t.Errorf("m.Get(): %+v", c)
	}
	conv := false
	for x, v := range info.Values {
		if src[x.Pos():x.End()] == "2 * 3" && v.String() == "6" {
			conv = true
		}
	}
	if !conv {
		t.Errorf("Values has no 6 for 2 * 3")
	}
	found := false
	for x, c := range info.Implicit {
		if src[x.Pos():x.End()] == "Mem{V: v}" && c.Kind == ConvOperations && c.Table["Get"] != nil && c.Table["Get"].Name == "Get" {
			found = true
		}
	}
	if !found {
		t.Errorf("Implicit has no operations conversion of Mem{V: v} to Store: %v", info.Implicit)
	}
	domains := map[LoopDomain]int{}
	for _, l := range info.Loops {
		domains[l.Domain] = len(l.Bindings)
	}
	if domains[LoopSlice] != 2 || domains[LoopRange] != 1 {
		t.Errorf("Loops: %v", domains)
	}
	params := 0
	for id, o := range info.Defs {
		if v, ok := o.(*Var); ok && v.param && (id.Name == "s" || id.Name == "xs") {
			params++
		}
	}
	if params < 2 {
		t.Errorf("Defs misses the parameters s and xs")
	}
}
