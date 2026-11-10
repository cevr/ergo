package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// mentionsPart is the classification of one field of a kind of Type, or of
// a part a kind holds, for Mentions's walk (fu-ambient-ctor r5). Either the
// walk reads it, and sel is the selector that reads it, or it holds no
// type, and why says so.
type mentionsPart struct{ sel, why string }

// mentionsParts classifies every field of every kind of Type and of the
// parts a kind holds (Field, Method, Var, Row). A kind or a field missing
// here fails TestMentionsWalkNamesEveryKind: a new kind of type, or a new
// part of a signature, is red until the walk names it.
var mentionsParts = map[string]map[string]mentionsPart{
	"Basic": {
		"Kind": {why: "a basic type is made of no other type"},
		"Name": {why: "a basic type is made of no other type"},
	},
	"noValue": {},
	"Named": {
		"Obj":     {sel: "Obj"},
		"Orig":    {sel: "origin"},
		"TArgs":   {sel: "TArgs"},
		"under":   {sel: "underlying"},
		"TParams": {why: "an origin's parameters: an instance binds them in TArgs and its body, and an origin's body names them"},
		"def":     {why: "the defined-over type; under is its body"},
		"methods": {why: "196m functions are the package's Funcs, which the guard asks one by one"},
		"opFuncs": {why: "functions on an operation type (G-ex-e-2) are the package's Funcs, which the guard asks one by one"},
		"mlist":   {why: "196m functions are the package's Funcs, which the guard asks one by one"},
		"state":   {why: "resolution state"},
		"foreign": {why: "go.Foreign: a host value with no Ergo parts"},
		"ctx":     {why: "the checker that substitutes an instance's body"},
	},
	"Struct": {
		"Fields": {sel: "Fields"},
		"obj":    {sel: "obj"},
		"index":  {why: "a cache of Fields"},
	},
	"Field": {
		"Type": {sel: "Type"},
		"Name": {why: "a name: every field is walked, hidden or not"},
		"Pos":  {why: "a position"},
	},
	"Interface": {
		"Methods": {sel: "Methods"},
		"obj":     {sel: "obj"},
		"index":   {why: "a cache of Methods"},
		"byName":  {why: "Methods sorted"},
		"Set":     {why: "a predeclared constraint's type set: basic types only"},
	},
	"Method": {
		"Sig":  {sel: "Sig"},
		"Name": {why: "a name; implementers matches it"},
		"Pos":  {why: "a position"},
		"pkg":  {why: "sealedOwner reads it, and the Interface case walks the implementers"},
	},
	"Union": {
		"Variants": {sel: "Variants"},
		"Bad":      {why: "a refused variant, reported"},
	},
	"MachineType": {
		"State":  {sel: "State"},
		"Signal": {sel: "Signal"},
	},
	"Slice":   {"Elem": {sel: "Elem"}},
	"Array":   {"Elem": {sel: "Elem"}, "Len": {why: "a length is no type"}},
	"Map":     {"Key": {sel: "Key"}, "Elem": {sel: "Elem"}},
	"Pointer": {"Elem": {sel: "Elem"}},
	"Tuple":   {"Types": {sel: "Types"}},
	"Signature": {
		"TParams":     {sel: "TParams"},
		"Params":      {sel: "Params"},
		"Results":     {sel: "Results"},
		"Errors":      {sel: "Errors"},
		"Uses":        {sel: "Uses"},
		"NBracket":    {why: "a count of TParams"},
		"Fallible":    {why: "whether a row is written; Errors holds it"},
		"NonEscaping": {why: "a lifetime mark of a parameter's function type"},
	},
	"Var": {
		"Type":        {sel: "Type"},
		"Name":        {why: "a name"},
		"Pos":         {why: "a position"},
		"id":          {why: "the declaring identifier"},
		"used":        {why: "a use mark"},
		"param":       {why: "a kind mark"},
		"pkg":         {why: "a kind mark"},
		"group":       {why: "a switch binding's group"},
		"bindRow":     {why: "a kind mark"},
		"let":         {why: "a kind mark"},
		"mut":         {why: "a kind mark"},
		"reassigned":  {why: "a use mark"},
		"undiagnosed": {why: "a diagnostic mark"},
		"refused":     {why: "a diagnostic mark"},
		"byValue":     {why: "the syntax of a let mut value, for a message"},
	},
	"TypeParam": {
		"Constraint": {sel: "Constraint"},
		"Name":       {why: "a name"},
		"Kind":       {why: "a kind"},
		"Pos":        {why: "a position"},
		"conflict":   {why: "a diagnostic mark"},
		"id":         {why: "an identity"},
	},
	"RowArg": {"Row": {sel: "Row"}},
	"RowVar": {
		"sol":   {sel: "Solution"},
		"of":    {sel: "Solution"},
		"m":     {sel: "Solution"},
		"ctx":   {sel: "Solution"},
		"final": {why: "whether sol is final; read after the check"},
		"Fn":    {why: "the function whose row it is"},
		"Kind":  {why: "error or capability row: both are walked"},
	},
	"Row": {
		"elems": {sel: "Elems"},
		"ids":   {why: "the elements' interned ids"},
		"tab":   {why: "the intern table"},
		"rid":   {why: "the row's interned id"},
		"vars":  {why: "a mark: an element is a RowVar, walked as a kind"},
		"infer": {why: "an intern mark"},
		"gen":   {why: "an intern mark"},
	},
}

// mentionsScope is, for each part that is not a kind, the case of
// mentioner.walk (or the mentioner method) whose body must read its
// walked fields: a field of a record in the Struct case, an operation in
// the Interface case, a parameter in the Signature case, a row's elements
// in mentioner.row. A kind's own fields are read in its own case.
var mentionsScope = map[string]string{
	"Field":  "Struct",
	"Method": "Interface",
	"Var":    "Signature",
	"Row":    "row",
}

// mentionsKinds are the Go types of the kinds and parts mentionsParts
// names, for reflection over their fields.
var mentionsKinds = map[string]reflect.Type{
	"Basic": reflect.TypeFor[Basic](), "noValue": reflect.TypeFor[noValue](), "Named": reflect.TypeFor[Named](),
	"Struct": reflect.TypeFor[Struct](), "Field": reflect.TypeFor[Field](), "Interface": reflect.TypeFor[Interface](),
	"Method": reflect.TypeFor[Method](), "Union": reflect.TypeFor[Union](), "MachineType": reflect.TypeFor[MachineType](),
	"Slice": reflect.TypeFor[Slice](), "Array": reflect.TypeFor[Array](), "Map": reflect.TypeFor[Map](), "Pointer": reflect.TypeFor[Pointer](),
	"Tuple": reflect.TypeFor[Tuple](), "Signature": reflect.TypeFor[Signature](), "Var": reflect.TypeFor[Var](),
	"TypeParam": reflect.TypeFor[TypeParam](), "RowArg": reflect.TypeFor[RowArg](), "RowVar": reflect.TypeFor[RowVar](),
	"Row": reflect.TypeFor[Row](),
}

// selectors are the selector names read in the nodes.
func selectors(nodes ...ast.Node) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		ast.Inspect(n, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				out[s.Sel.Name] = true
			}
			return true
		})
	}
	return out
}

// TestMentionsWalkNamesEveryKind is the guard of Mentions's walk
// (fu-ambient-ctor r4, r5). It reads the package's own source: every type
// with an aType method (pointer or value receiver) is a kind of Type, and
// mentioner.walk's type switch has one case per kind and no default. Every
// field of each kind and part is classified in mentionsParts, and a walked
// field's selector is read in the case of its own kind (mentionsScope for
// a part). So a new kind, or a new field of a signature, record or
// operation, is red here until the walk reads it where it belongs.
func TestMentionsWalkNamesEveryKind(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var kinds []string
	walkers := map[string]*ast.FuncDecl{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			rt := fn.Recv.List[0].Type
			if star, ok := rt.(*ast.StarExpr); ok {
				rt = star.X
			}
			recv, ok := rt.(*ast.Ident)
			if !ok {
				continue
			}
			if fn.Name.Name == "aType" {
				kinds = append(kinds, recv.Name)
			}
			if recv.Name == "mentioner" {
				walkers[fn.Name.Name] = fn
			}
		}
	}
	slices.Sort(kinds)
	if len(kinds) < 15 {
		t.Fatalf("found %d kinds of Type (%v): the aType scan is broken", len(kinds), kinds)
	}
	walk := walkers["walk"]
	if walk == nil || walkers["row"] == nil {
		t.Fatalf("mentioner methods %v: want walk and row", slices.Sorted(maps.Keys(walkers)))
	}
	// scope: the selectors each case of the switch reads, and row's.
	scope := map[string]map[string]bool{"row": selectors(walkers["row"].Body)}
	var cases []string
	switches := 0
	ast.Inspect(walk.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.TypeSwitchStmt)
		if !ok {
			return true
		}
		switches++
		for _, s := range sw.Body.List {
			cc := s.(*ast.CaseClause)
			if cc.List == nil {
				t.Errorf("mentioner.walk's type switch has a default case: every kind is named, so a new one is red")
			}
			if len(cc.List) > 1 {
				t.Errorf("mentioner.walk's case at %s names several kinds: one case per kind", fset.Position(cc.Pos()))
			}
			for _, x := range cc.List {
				var id *ast.Ident
				if star, ok := x.(*ast.StarExpr); ok {
					id, _ = star.X.(*ast.Ident)
				}
				if id == nil {
					t.Errorf("mentioner.walk's case at %s is not *Kind", fset.Position(x.Pos()))
					continue
				}
				cases = append(cases, id.Name)
				var body []ast.Node
				for _, st := range cc.Body {
					body = append(body, st)
				}
				scope[id.Name] = selectors(body...)
			}
		}
		return false
	})
	if switches != 1 {
		t.Fatalf("mentioner.walk has %d type switches, want 1", switches)
	}
	slices.Sort(cases)
	if !slices.Equal(cases, kinds) {
		t.Errorf("mentioner.walk's cases are %v, the kinds of Type are %v: name each kind once", cases, kinds)
	}
	for _, k := range kinds {
		if _, ok := mentionsKinds[k]; !ok {
			t.Errorf("kind %s has no entry in mentionsKinds and mentionsParts: classify its fields", k)
		}
	}
	for name, rt := range mentionsKinds {
		parts, ok := mentionsParts[name]
		if !ok {
			t.Errorf("%s has no entry in mentionsParts", name)
			continue
		}
		where := name
		if s, ok := mentionsScope[name]; ok {
			where = s
		}
		var fields []string
		for i := range rt.NumField() {
			f := rt.Field(i).Name
			fields = append(fields, f)
			p, ok := parts[f]
			switch {
			case !ok:
				t.Errorf("%s.%s is not classified in mentionsParts: the walk reads it (sel) or it holds no type (why)", name, f)
			case p.sel == "" && p.why == "":
				t.Errorf("%s.%s: neither walked nor explained", name, f)
			case p.sel != "" && !scope[where][p.sel]:
				t.Errorf("%s.%s is walked by selector %s, which the %s case of mentioner.walk does not read", name, f, p.sel, where)
			}
		}
		for f := range parts {
			if !slices.Contains(fields, f) {
				t.Errorf("mentionsParts names %s.%s, which is not a field", name, f)
			}
		}
	}
}

// TestMentionsEachKind gives each case of mentioner.walk, and each part of
// a signature, a record and an operation type, one row that only that case
// decides: a mutant that drops a case or a part changes its row. Types are
// built by hand, so kinds the source cannot write today (a type argument
// row, an unsolved row variable, a phantom type argument) have a row too.
func TestMentionsEachKind(t *testing.T) {
	lib := &Package{Name: "lib", Path: "lib"}
	app := &Package{Name: "app", Path: "app"}
	declare := func(name string, under Type) *Named {
		tn := &TypeName{Name: name, Pkg: lib}
		n := &Named{Obj: tn, under: under, state: 2}
		tn.Type = n
		lib.Types = append(lib.Types, tn)
		switch u := under.(type) {
		case *Struct:
			u.obj = tn
		case *Interface:
			u.obj = tn
		}
		return n
	}
	field := func(name string, t Type) *Field { return &Field{Name: name, Type: t} }
	record := func(name string, fields ...*Field) *Named { return declare(name, &Struct{Fields: fields}) }
	op := func(name string, sig *Signature) *Method { return &Method{Name: name, Sig: sig} }
	ops := func(name string, ms ...*Method) *Named { return declare(name, &Interface{Methods: ms}) }
	row := func(ts ...Type) *Row { return &Row{elems: ts} }
	params := func(ts ...Type) []*Var {
		var out []*Var
		for _, t := range ts {
			out = append(out, &Var{Name: "v", Type: t})
		}
		return out
	}
	fn := func(ps []Type, rs []Type) *Signature { return &Signature{Params: params(ps...), Results: rs} }

	tok := record("Tok", field("v", Typ[Int]))
	hidden := record("Hidden", field("x", tok), field("N", Typ[Int]))
	src := ops("Src", op("Get", fn(nil, []Type{tok})))
	// Sealed has an unexported operation of lib, and Live implements it
	// (a function peek of lib whose first parameter is a Live).
	sealed := ops("Sealed", &Method{Name: "peek", Sig: fn(nil, []Type{Typ[Int]}), pkg: lib})
	live := record("Live", field("c", tok))
	lib.Funcs = append(lib.Funcs, &Func{Name: "peek", Pkg: lib, Sig: fn([]Type{live}, []Type{Typ[Int]})})
	// Phantom[Tok]: an instance whose body does not hold its argument.
	phantomTN := &TypeName{Name: "Phantom", Pkg: lib}
	phantom := &Named{Obj: phantomTN, TArgs: []Type{tok}, under: &Struct{obj: phantomTN}, state: 2}

	for _, c := range []struct {
		name string
		t    Type
		from *Package
		want []string
	}{
		{"basic", Typ[Int], app, nil},
		{"no value", NoValue, app, nil},
		{"named record", tok, app, []string{"Tok"}},
		{"record body without its name", tok.under, app, []string{"Tok"}},
		{"hidden field of a record with authority", hidden, app, []string{"Hidden", "Tok"}},
		{"type argument the body does not hold", phantom, app, []string{"Phantom", "Tok"}},
		{"tuple element", &Tuple{Types: []Type{Typ[Int], tok}}, app, []string{"Tok"}},
		{"union variant", &Union{Variants: []Type{tok}}, app, []string{"Tok"}},
		{"borrow", &Pointer{Elem: tok}, app, []string{"Tok"}},
		{"slice element", &Slice{Elem: tok}, app, []string{"Tok"}},
		{"Map key", &Map{Key: tok, Elem: Typ[Int]}, app, []string{"Tok"}},
		{"Map element", &Map{Key: Typ[String], Elem: tok}, app, []string{"Tok"}},
		{"result", fn(nil, []Type{tok}), app, []string{"Tok"}},
		{"parameter", fn([]Type{tok}, nil), app, []string{"Tok"}},
		{"failure row", &Signature{Errors: row(tok)}, app, []string{"Tok"}},
		{"needs row", &Signature{Uses: row(tok)}, app, []string{"Tok"}},
		{"type parameter of a signature", &Signature{TParams: []*TypeParam{{Name: "T", Constraint: tok}}}, app, []string{"Tok"}},
		{"operation", src, app, []string{"Src", "Tok"}},
		{"operation type body without its name", src.under, app, []string{"Src", "Tok"}},
		{"sealed operation type's implementer", sealed, app, []string{"Live", "Sealed", "Tok"}},
		{"sealed operation type in its own package", sealed, lib, []string{"Sealed"}},
		{"machine state", &MachineType{State: tok, Signal: Typ[Int]}, app, []string{"Tok"}},
		{"machine signal", &MachineType{State: Typ[Int], Signal: tok}, app, []string{"Tok"}},
		{"type parameter's constraint", &TypeParam{Name: "T", Constraint: tok}, app, []string{"Tok"}},
		{"type parameter with none", &TypeParam{Name: "T"}, app, nil},
		{"row argument", &RowArg{Row: row(tok)}, app, []string{"Tok"}},
		{"row variable's solution", &RowVar{sol: row(tok)}, app, []string{"Tok"}},
		{"unsolved row variable", &RowVar{}, app, nil},
	} {
		var got []string
		for _, e := range Mentions(c.t, c.from) {
			got = append(got, e.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: Mentions is %v, want %v", c.name, got, c.want)
		}
	}
}
