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

// exposesParts classifies every field of every kind of Type, and of the
// parts a kind holds (Field, Method, Var, Row), for Exposes's walk
// (fu-ambient-ctor r4). A field is either walked, and sel is the selector
// the walk reads it by (exposer.walk, call or row must name it), or it
// holds no value a caller may get or supply, and why says so. A kind or a
// field missing here fails TestExposesWalkNamesEveryKind: a new kind of
// type, or a new part of a signature, is a new channel until the walk
// names it.
var exposesParts = map[string]map[string]struct{ sel, why string }{
	"Basic": {
		"Kind": {why: "a basic value holds no record"},
		"Name": {why: "a basic value holds no record"},
	},
	"noValue": {},
	"Named": {
		"under":   {sel: "underlying"},
		"Obj":     {why: "the declaration; its body is under"},
		"Orig":    {why: "an instance's origin; underlying substitutes its body"},
		"TArgs":   {why: "substituted into under; a type argument the body does not hold gives no value"},
		"TParams": {why: "the origin's parameters, bound in an instance's under"},
		"def":     {why: "the defined-over type; under is its body"},
		"methods": {why: "196m functions are the package's Funcs, asked one by one"},
		"mlist":   {why: "196m functions are the package's Funcs, asked one by one"},
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
		"Name": {sel: "Name"},
		"Pos":  {why: "a position"},
	},
	"Interface": {
		"Methods": {sel: "Methods"},
		"index":   {why: "a cache of Methods"},
		"byName":  {why: "Methods sorted"},
		"Set":     {why: "a predeclared constraint's type set: no operation, no value"},
		"obj":     {why: "the declaration; sealing is Buildable's"},
	},
	"Method": {
		"Sig":  {sel: "Sig"},
		"Name": {why: "a name"},
		"Pos":  {why: "a position"},
		"pkg":  {why: "an unexported operation's package: who implements it, not what a call gives"},
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
	"Map":     {"Key": {sel: "Key"}, "Elem": {sel: "Elem"}},
	"Pointer": {"Elem": {sel: "Elem"}},
	"Tuple":   {"Types": {sel: "Types"}},
	"Signature": {
		"Params":      {sel: "Params"},
		"Results":     {sel: "Results"},
		"Errors":      {sel: "Errors"},
		"Uses":        {sel: "Uses"},
		"TParams":     {why: "bound by the call: a value of one comes through Params or Results"},
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
		"Kind":  {why: "error or capability row: the signature's place gives the polarity"},
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

// exposesKinds are the Go types of the kinds and parts exposesParts
// names, for reflection over their fields.
var exposesKinds = map[string]reflect.Type{
	"Basic": reflect.TypeFor[Basic](), "noValue": reflect.TypeFor[noValue](), "Named": reflect.TypeFor[Named](),
	"Struct": reflect.TypeFor[Struct](), "Field": reflect.TypeFor[Field](), "Interface": reflect.TypeFor[Interface](),
	"Method": reflect.TypeFor[Method](), "Union": reflect.TypeFor[Union](), "MachineType": reflect.TypeFor[MachineType](),
	"Slice": reflect.TypeFor[Slice](), "Map": reflect.TypeFor[Map](), "Pointer": reflect.TypeFor[Pointer](),
	"Tuple": reflect.TypeFor[Tuple](), "Signature": reflect.TypeFor[Signature](), "Var": reflect.TypeFor[Var](),
	"TypeParam": reflect.TypeFor[TypeParam](), "RowArg": reflect.TypeFor[RowArg](), "RowVar": reflect.TypeFor[RowVar](),
	"Row": reflect.TypeFor[Row](),
}

// TestExposesWalkNamesEveryKind is the guard of Exposes's walk
// (fu-ambient-ctor r4: three rounds each found one more channel). It
// reads the package's own source: every type with an aType method is a
// kind of Type, and exposer.walk's type switch has one case per kind and
// no default. Every field of each kind and part is classified in
// exposesParts, and a walked field's selector is read in walk, call or
// row. So a new kind, or a new field of a signature, record or operation,
// is red here until the walk handles it.
func TestExposesWalkNamesEveryKind(t *testing.T) {
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
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			recv, ok := star.X.(*ast.Ident)
			if !ok {
				continue
			}
			if fn.Name.Name == "aType" {
				kinds = append(kinds, recv.Name)
			}
			if recv.Name == "exposer" {
				walkers[fn.Name.Name] = fn
			}
		}
	}
	slices.Sort(kinds)
	if len(kinds) < 15 {
		t.Fatalf("found %d kinds of Type (%v): the aType scan is broken", len(kinds), kinds)
	}
	walk := walkers["walk"]
	if walk == nil || walkers["call"] == nil || walkers["row"] == nil {
		t.Fatalf("exposer methods %v: want walk, call and row", slices.Sorted(maps.Keys(walkers)))
	}
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
				t.Errorf("exposer.walk's type switch has a default case: every kind is named, so a new one is red")
			}
			for _, x := range cc.List {
				var id *ast.Ident
				if star, ok := x.(*ast.StarExpr); ok {
					id, _ = star.X.(*ast.Ident)
				}
				if id == nil {
					t.Errorf("exposer.walk's case at %s is not *Kind", fset.Position(x.Pos()))
					continue
				}
				cases = append(cases, id.Name)
			}
		}
		return false
	})
	if switches != 1 {
		t.Fatalf("exposer.walk has %d type switches, want 1", switches)
	}
	slices.Sort(cases)
	if !slices.Equal(cases, kinds) {
		t.Errorf("exposer.walk's cases are %v, the kinds of Type are %v: name each kind once", cases, kinds)
	}
	// The selectors walk, call and row read.
	sels := map[string]bool{}
	for _, fn := range walkers {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				sels[s.Sel.Name] = true
			}
			return true
		})
	}
	for _, k := range kinds {
		if _, ok := exposesKinds[k]; !ok {
			t.Errorf("kind %s has no entry in exposesKinds and exposesParts: classify its fields", k)
		}
	}
	for name, rt := range exposesKinds {
		parts, ok := exposesParts[name]
		if !ok {
			t.Errorf("%s has no entry in exposesParts", name)
			continue
		}
		var fields []string
		for i := range rt.NumField() {
			f := rt.Field(i).Name
			fields = append(fields, f)
			p, ok := parts[f]
			switch {
			case !ok:
				t.Errorf("%s.%s is not classified in exposesParts: the walk reads it (sel) or it holds no value (why)", name, f)
			case p.sel == "" && p.why == "":
				t.Errorf("%s.%s: neither walked nor explained", name, f)
			case p.sel != "" && !sels[p.sel]:
				t.Errorf("%s.%s is walked by selector %s, which exposer's walk, call and row do not read", name, f, p.sel)
			}
		}
		for f := range parts {
			if !slices.Contains(fields, f) {
				t.Errorf("exposesParts names %s.%s, which is not a field", name, f)
			}
		}
	}
}

// TestExposesEachKind gives each case of exposer.walk, and each part of a
// signature, one row that only that case decides: a mutant that drops a
// case, or walks a part at the wrong polarity, changes its row. Types are
// built by hand, so kinds the source cannot write today (a type argument
// row, an unsolved row variable) have a row too.
func TestExposesEachKind(t *testing.T) {
	lib := &Package{Name: "lib", Path: "lib"}
	app := &Package{Name: "app", Path: "app"}
	record := func(name string, fields ...*Field) *Named {
		tn := &TypeName{Name: name, Pkg: lib}
		s := &Struct{Fields: fields, obj: tn}
		n := &Named{Obj: tn, under: s, state: 2}
		tn.Type = n
		return n
	}
	field := func(name string, t Type) *Field { return &Field{Name: name, Type: t} }
	ops := func(name string, ms ...*Method) *Named {
		tn := &TypeName{Name: name, Pkg: lib}
		it := &Interface{Methods: ms, obj: tn}
		n := &Named{Obj: tn, under: it, state: 2}
		tn.Type = n
		return n
	}
	op := func(name string, sig *Signature) *Method { return &Method{Name: name, Sig: sig} }
	row := func(ts ...Type) *Row { return &Row{elems: ts} }
	params := func(ts ...Type) []*Var {
		var out []*Var
		for _, t := range ts {
			out = append(out, &Var{Name: "v", Type: t})
		}
		return out
	}
	fn := func(ps []Type, rs []Type) *Signature { return &Signature{Params: params(ps...), Results: rs} }

	tok := record("Tok", field("v", Typ[Int]))                        // authority
	hidden := record("Hidden", field("x", tok), field("N", Typ[Int])) // authority; Tok unreadable
	shown := record("Shown", field("X", tok), field("n", Typ[Int]))   // authority; Tok readable
	open := record("Open", field("X", tok))
	sink := ops("Sink", op("Put", fn([]Type{tok}, nil)))
	src := ops("Src", op("Get", fn(nil, []Type{tok})))
	w := record("W", field("X", tok))
	pair := record("Pair", field("F", fn([]Type{w}, nil)), field("Y", w))
	signal := record("Sig", field("F", fn([]Type{tok}, nil)))

	for _, c := range []struct {
		name string
		t    Type
		want []string
	}{
		{"basic", Typ[Int], nil},
		{"no value", NoValue, nil},
		{"named record with authority", tok, []string{"Tok"}},
		{"record body without its name", tok.under, []string{"Tok"}},
		{"open record's field", open, []string{"Tok"}},
		{"record with authority hides its unexported field", hidden, []string{"Hidden"}},
		{"record with authority shows its exported field", shown, []string{"Shown", "Tok"}},
		{"tuple element", &Tuple{Types: []Type{Typ[Int], tok}}, []string{"Tok"}},
		{"union variant", &Union{Variants: []Type{open}}, []string{"Tok"}},
		{"borrow", &Pointer{Elem: tok}, []string{"Tok"}},
		{"slice element", &Slice{Elem: tok}, []string{"Tok"}},
		{"Map key", &Map{Key: tok, Elem: Typ[Int]}, []string{"Tok"}},
		{"Map element", &Map{Key: Typ[String], Elem: tok}, []string{"Tok"}},
		{"result", fn(nil, []Type{tok}), []string{"Tok"}},
		{"failure", &Signature{Errors: row(tok)}, []string{"Tok"}},
		{"parameter: the caller supplies it", fn([]Type{tok}, nil), nil},
		{"parameter's parameter", fn([]Type{fn([]Type{tok}, nil)}, nil), []string{"Tok"}},
		{"parameter's result: the caller's own", fn([]Type{fn(nil, []Type{tok})}, nil), nil},
		{"needs row: the caller's provider receives Put's argument", &Signature{Uses: row(sink)}, []string{"Tok"}},
		{"needs row: the caller's provider gives Get's result", &Signature{Uses: row(src)}, nil},
		{"operation result", src, []string{"Tok"}},
		{"operation parameter: the caller supplies it", sink, nil},
		{"machine state", &MachineType{State: tok, Signal: Typ[Int]}, []string{"Tok"}},
		{"machine signal: its sender supplies it", &MachineType{State: Typ[Int], Signal: tok}, nil},
		{"machine signal's function: the machine calls it", &MachineType{State: Typ[Int], Signal: signal}, []string{"Tok"}},
		{"type parameter with a constraint", &TypeParam{Name: "T", Constraint: src}, []string{"Tok"}},
		{"type parameter with none", &TypeParam{Name: "T"}, nil},
		{"row argument", &RowArg{Row: row(tok)}, []string{"Tok"}},
		{"row variable's solution", &Signature{Errors: row(&RowVar{sol: row(tok)})}, []string{"Tok"}},
		{"unsolved row variable", &RowVar{}, nil},
		{"met where supplied, then where received", pair, []string{"Tok"}},
	} {
		var got []string
		for _, e := range Exposes(c.t, app) {
			got = append(got, e.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: Exposes is %v, want %v", c.name, got, c.want)
		}
	}
}
