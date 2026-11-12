// Package derive expands `type T derive E` declarations (patterns.md):
// it evaluates each plan at check time, derives the bundle of declarations
// T owns, and compiles each pattern to a bounded regexpplan matcher. It
// runs before the checker, which then checks the bundle as written code.
package derive

import (
	"fmt"
	"path"
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/regexpplan"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Diag is a refused derive or pattern at a span of a file of the package.
type Diag struct {
	File     *syntax.File
	Pos, End syntax.Pos
	Msg      string
}

// Native is a matcher function of a bundle: the lowering emits its Go from
// Plan (lower/pattern.go), never its placeholder Ergo body.
type Native struct {
	Plan *regexpplan.Plan
	Find bool
}

// Derived records one derive for inspect (Derivation 7).
type Derived struct {
	Type  string
	Decl  *syntax.TypeDecl
	Plan  string   // the plan the derive evaluated
	Decls []string // the declarations it derived, by name
	// For a pattern: its text and the parse and find matchers.
	Pattern     string
	Parse, Find *regexpplan.Plan
	Steps       int // plan evaluation steps (Budgets 4)
}

// Package is the derive record of one package: its plan functions, which
// importers call, and what its derives produced.
type Package struct {
	Path    string
	funcs   map[string]*planFunc
	names   map[string]bool
	imports map[*syntax.File]map[string]string
	lookup  func(path string) *Package
	used    map[*syntax.File][]string // the import names a plan call used
	Natives map[*syntax.FuncDecl]*Native
	Derived []*Derived
	Diags   []Diag
}

func (p *Package) qualified(name string) string { return path.Base(p.Path) + "." + name }

// Empty reports a record with no plan function, derive, matcher or
// diagnostic: a nil record means the same to every reader (an importer's
// lookup, the lowering's matchers, inspect). The checked-std snapshot
// carries no derive record and refuses a std package whose record is not
// empty (check/export.go).
func (p *Package) Empty() bool {
	return p == nil || len(p.funcs) == 0 && len(p.Derived) == 0 && len(p.Natives) == 0 && len(p.Diags) == 0
}

func (p *Package) importPath(f *syntax.File, alias string) (string, bool) {
	path, ok := p.imports[f][alias]
	return path, ok
}

// UsesImport reports an import of the file f that a derive's plan called:
// the import is used, though no checked code names it.
func (p *Package) UsesImport(f *syntax.File, alias string) bool {
	if p == nil {
		return false
	}
	for _, a := range p.used[f] {
		if a == alias {
			return true
		}
	}
	return false
}

func (p *Package) imported(f *syntax.File, alias string) bool {
	_, ok := p.imports[f][alias]
	return ok
}

// isPlanFunc reports a plan function: a function whose one result is
// TypePlan or FieldPlan, with no type parameters, extern or receiver
// (Plan functions 1), where the package declares no type of that name.
func isPlanFunc(d *syntax.FuncDecl, types map[string]bool) bool {
	if d.Body == nil || d.Extern != nil || d.TypeParams != nil || d.Recv != nil || d.Type.Results == nil || len(d.Type.Results.List) != 1 {
		return false
	}
	id, ok := d.Type.Results.List[0].Type.(*syntax.Ident)
	return ok && (id.Name == "TypePlan" || id.Name == "FieldPlan") && !types[id.Name]
}

// Expand expands the derives of one package's files in place, removes its
// plan functions from them (they exist at check time only, Plan functions
// 3), and returns its record. lookup gives an imported package's record.
// A second Expand of the same files returns the first record.
func Expand(pkgPath string, files []*syntax.File, lookup func(path string) *Package) *Package {
	for _, f := range files {
		if p, ok := f.Expansion.(*Package); ok {
			return p
		}
	}
	p := &Package{Path: pkgPath, funcs: map[string]*planFunc{}, names: map[string]bool{}, imports: map[*syntax.File]map[string]string{}, lookup: lookup, used: map[*syntax.File][]string{}, Natives: map[*syntax.FuncDecl]*Native{}}
	types := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if t, ok := d.(*syntax.TypeDecl); ok {
				types[t.Name.Name] = true
			}
		}
	}
	for _, f := range files {
		p.imports[f] = map[string]string{}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.ImportDecl:
				for _, s := range d.Specs {
					if s.Kind == syntax.ImportGo {
						continue
					}
					alias := path.Base(s.Path.Value)
					if s.Name != nil {
						alias = s.Name.Name
					}
					p.imports[f][alias] = s.Path.Value
				}
			case *syntax.FuncDecl:
				p.names[d.Name.Name] = true
				if isPlanFunc(d, types) {
					p.funcs[d.Name.Name] = &planFunc{decl: d, file: f, pkg: p}
				}
			case *syntax.TypeDecl:
				p.names[d.Name.Name] = true
			case *syntax.VarDecl:
				for _, s := range d.Specs {
					for _, n := range s.Names {
						p.names[n.Name] = true
					}
				}
			}
		}
	}
	taken := map[string]bool{}
	for name := range p.names {
		if _, plan := p.funcs[name]; !plan {
			taken[name] = true
		}
	}
	for _, f := range files {
		var out []syntax.Decl
		for _, d := range f.Decls {
			if fd, ok := d.(*syntax.FuncDecl); ok && p.funcs[fd.Name.Name] != nil && p.funcs[fd.Name.Name].decl == fd {
				continue
			}
			out = append(out, d)
			if td, ok := d.(*syntax.TypeDecl); ok && td.Derive != nil {
				out = append(out, p.expand(f, td, taken)...)
			}
		}
		f.Decls = out
	}
	for _, f := range files {
		f.Expansion = p
	}
	return p
}

// expand evaluates one derive and returns the declarations it derives.
func (p *Package) expand(f *syntax.File, d *syntax.TypeDecl, taken map[string]bool) (decls []syntax.Decl) {
	name := d.Name.Name
	ev := &evaluator{limits: DefaultLimits(), typeName: name}
	refuse := func(e *evalError) []syntax.Decl {
		p.diag(f, d, e)
		// One mistake, one diagnostic: the type stays declared, an empty
		// record, with no derived functions (Diagnostics 3).
		empty, _ := syntax.Parse("type " + name + " {}\n")
		reposition(empty.Decls[0], d.DerivePos)
		d.Spec = empty.Decls[0].(*syntax.TypeDecl).Spec
		return nil
	}
	var value any
	err := func() (err *evalError) {
		defer func() {
			if r := recover(); r != nil {
				e, ok := r.(*evalError)
				if !ok {
					panic(r)
				}
				err = e
			}
		}()
		value = ev.eval(&frame{vars: map[string]any{}, file: f, pkg: p}, d.Derive)
		return nil
	}()
	if err != nil {
		return refuse(err)
	}
	pl, ok := value.(*plan)
	at := origin{f, d.Derive.Pos()}
	if !ok || pl.kind == "Field" {
		return refuse(&evalError{at, d.Derive.End(), fmt.Sprintf("the derive of %s needs a TypePlan; this expression gives %s", name, describe(value))})
	}
	rec := &Derived{Type: name, Decl: d, Plan: planText(pl), Steps: ev.steps}
	var text string
	switch pl.kind {
	case "Pattern":
		pt, perr := parsePattern(pl.text.s)
		var shape *Shape
		if perr == nil {
			shape, perr = shapeOf(name, pt)
		}
		var parse, find *regexpplan.Plan
		if perr == nil {
			var cerr error
			parse, cerr = regexpplan.Compile(pt.root, regexpplan.Parse, regexpplan.DefaultLimits())
			if cerr == nil {
				find, cerr = regexpplan.Compile(pt.root, regexpplan.Find, regexpplan.DefaultLimits())
			}
			if cerr != nil {
				perr = &PatternError{0, cerr.Error()}
				if e, ok := cerr.(*regexpplan.Error); ok {
					perr = &PatternError{e.Offset, compileReason(e.Reason)}
				}
			}
		}
		if perr != nil {
			return refuse(patternError(pl.text, perr, at))
		}
		tags := map[string]int{}
		for i, n := range parse.Names() {
			tags[n] = i + 1
		}
		assignTags(shape, tags)
		text = patternBundle(name, shape, parse.Slots())
		rec.Pattern, rec.Parse, rec.Find = pl.text.s, parse, find
	case "Refine":
		base, length, e := refineBase(pl.base, f)
		if e != nil {
			return refuse(e)
		}
		// An empty span (lo > hi) is the where form's own S108.
		text = refineBundle(name, base, length, pl.lo, pl.hi)
	default:
		var extra strings.Builder
		typ, e := typeText(pl, name, "", f, &extra, true)
		if e != nil {
			return refuse(e)
		}
		text = typ + extra.String()
	}
	parsed, perr := syntax.Parse(text)
	if perr != nil {
		panic(fmt.Sprintf("derive: internal compiler error: the bundle of %s does not parse: %v\n%s", name, perr, text))
	}
	var head *syntax.TypeDecl
	var rest []syntax.Decl
	for _, x := range parsed.Decls {
		reposition(x, d.DerivePos)
		if t, ok := x.(*syntax.TypeDecl); ok && t.Name.Name == name && head == nil {
			head = t
			continue
		}
		rest = append(rest, x)
	}
	d.Spec, d.Where, d.WherePos = head.Spec, head.Where, d.DerivePos
	if head.Where == nil {
		d.WherePos = syntax.NoPos
	}
	for _, x := range rest {
		var n string
		switch x := x.(type) {
		case *syntax.FuncDecl:
			x.Derived, n = d, x.Name.Name
		case *syntax.LawDecl:
			x.Derived, n = d, x.Name.Name
		case *syntax.TypeDecl:
			x.Origin, n = d, x.Name.Name
		}
		if taken[n] {
			return refuse(&evalError{origin{f, d.DerivePos}, d.DerivePos + syntax.Pos(len("derive")), fmt.Sprintf("%s is already declared in this package: the derive of %s generates it", n, name)})
		}
		decls = append(decls, x)
		rec.Decls = append(rec.Decls, n)
	}
	for _, x := range decls {
		taken[declName(x)] = true
		if fd, ok := x.(*syntax.FuncDecl); ok && rec.Parse != nil {
			switch fd.Name.Name {
			case matchName(name):
				p.Natives[fd] = &Native{Plan: rec.Parse}
			case scanName(name):
				p.Natives[fd] = &Native{Plan: rec.Find, Find: true}
			}
		}
	}
	p.Derived = append(p.Derived, rec)
	return decls
}

func declName(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Name.Name
	case *syntax.LawDecl:
		return d.Name.Name
	case *syntax.TypeDecl:
		return d.Name.Name
	}
	return ""
}

func compileReason(r string) string {
	switch r {
	case "nullable repeated body":
		return "the body of this repeat can match no bytes; each repetition must consume a byte"
	case "expanded node budget":
		return "this pattern expands past 4096 nodes; a pattern is built in bounded size"
	case "NFA instruction budget":
		return "this pattern needs more than 65536 instructions; a pattern is built in bounded size"
	case "nil, cyclic or excessively deep pattern":
		return "this pattern nests deeper than 256"
	}
	return r
}

func assignTags(s *Shape, tags map[string]int) {
	var visit func(fs []*Field)
	visit = func(fs []*Field) {
		for _, f := range fs {
			f.Tag = tags[f.Name]
			if f.Record != nil {
				visit(f.Record.Fields)
			}
		}
	}
	if s.Union {
		visit(s.Variants)
	} else {
		visit(s.Record.Fields)
	}
}

// patternError maps a pattern byte back to the source byte that spelled
// it (Diagnostics 1 and 2).
func patternError(text str, e *PatternError, derive origin) *evalError {
	at := derive
	if e.Offset >= 0 && e.Offset < len(text.at) {
		at = text.at[e.Offset]
	} else if len(text.at) > 0 {
		at = text.at[len(text.at)-1]
	}
	return &evalError{at, at.pos + 1, e.Msg}
}

// diag records e. A position in another file of the package is reported
// there; one outside the package is reported at the derive, naming it.
func (p *Package) diag(f *syntax.File, d *syntax.TypeDecl, e *evalError) {
	if e.at.file != nil && p.imports[e.at.file] != nil {
		p.Diags = append(p.Diags, Diag{e.at.file, e.at.pos, max(e.end, e.at.pos), e.msg})
		return
	}
	msg := e.msg
	if e.at.file != nil && e.at.pos.IsValid() {
		line, col := e.at.file.Position(e.at.pos)
		where := e.at.file.Name
		if where == "" {
			where = "another package"
		}
		msg += fmt.Sprintf(" (at %s:%d:%d)", where, line, col)
	}
	p.Diags = append(p.Diags, Diag{f, d.DerivePos, d.DerivePos + syntax.Pos(len("derive")), msg})
}

func describe(v any) string {
	switch v.(type) {
	case str:
		return "a string"
	case int64:
		return "an integer"
	case bool:
		return "a boolean"
	case *plan:
		return "a FieldPlan"
	}
	return "a list"
}

// refineBase is the base type of a Refine plan as source text, and whether
// its span bounds a length (a string or a slice base).
func refineBase(base *plan, f *syntax.File) (string, bool, *evalError) {
	if base.kind != "Of" {
		return "", false, &evalError{base.at, base.at.pos, "Refine refines a named or basic type: write Refine(Of[B](), lo, hi)"}
	}
	text := base.file.Src[base.typ.Pos():base.typ.End()]
	if base.file != f && !basic[text] {
		return "", false, &evalError{base.at, base.at.pos, "Of in another package names that package's type: pass the plan of the type as a parameter"}
	}
	length := text == "string" || strings.HasPrefix(text, "[]")
	return text, length, nil
}

var basic = map[string]bool{"int": true, "int8": true, "int16": true, "int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "byte": true, "string": true, "bool": true, "float64": true, "float32": true, "rune": true}

// typeText is the type a structural plan gives a field of owner, writing
// the nominal types it needs into extra. top is the derived type itself.
func typeText(pl *plan, owner, field string, f *syntax.File, extra *strings.Builder, top bool) (string, *evalError) {
	nominal := owner + field
	switch pl.kind {
	case "Of":
		text := pl.file.Src[pl.typ.Pos():pl.typ.End()]
		if pl.file != f && !basic[text] {
			return "", &evalError{pl.at, pl.at.pos, "Of in another package names that package's type: pass the plan of the type as a parameter"}
		}
		if top {
			return fmt.Sprintf("type %s %s\n", nominal, text), nil
		}
		return text, nil
	case "Optional", "List":
		inner, err := typeText(pl.base, owner, field, f, extra, false)
		if err != nil {
			return "", err
		}
		t := "Option[" + inner + "]"
		if pl.kind == "List" {
			t = "[]" + inner
		}
		if top {
			return fmt.Sprintf("type %s %s\n", nominal, t), nil
		}
		return t, nil
	case "Record", "Union":
		var parts []string
		seen := map[string]bool{}
		for _, fp := range pl.fields {
			n := fp.name.s
			if !isIdent(n) || seen[n] {
				at := pl.at
				if len(fp.name.at) > 0 {
					at = fp.name.at[0]
				}
				return "", &evalError{at, at.pos + 1, fmt.Sprintf("the field %q is not an identifier, or is named twice", n)}
			}
			seen[n] = true
			if pl.kind == "Union" {
				if fp.base.kind != "Record" {
					return "", &evalError{fp.at, fp.at.pos, "a variant of a Union is a Record"}
				}
				body, err := recordFields(fp.base, n, f, extra)
				if err != nil {
					return "", err
				}
				parts = append(parts, n+" "+body)
				continue
			}
			t, err := typeText(fp.base, nominal, n, f, extra, false)
			if err != nil {
				return "", err
			}
			parts = append(parts, n+" "+t)
		}
		if pl.kind == "Union" {
			fmt.Fprintf(extra, "type %s = %s\n", nominal, strings.Join(parts, " | "))
		} else {
			fmt.Fprintf(extra, "type %s { %s }\n", nominal, strings.Join(parts, ", "))
		}
		if top {
			return "", nil // the type's declaration is in extra
		}
		return nominal, nil
	}
	return "", &evalError{pl.at, pl.at.pos, fmt.Sprintf("a %s inside a structural plan is not derived yet: derive it as its own type and use Of", pl.kind)}
}

func recordFields(pl *plan, owner string, f *syntax.File, extra *strings.Builder) (string, *evalError) {
	var parts []string
	for _, fp := range pl.fields {
		t, err := typeText(fp.base, owner, fp.name.s, f, extra, false)
		if err != nil {
			return "", err
		}
		parts = append(parts, fp.name.s+" "+t)
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

// planText prints a plan value for inspect.
func planText(pl *plan) string {
	switch pl.kind {
	case "Pattern":
		return fmt.Sprintf("Pattern(%q)", pl.text.s)
	case "Refine":
		return fmt.Sprintf("Refine(%s, %d, %d)", planText(pl.base), pl.lo, pl.hi)
	case "Of":
		return "Of[" + pl.file.Src[pl.typ.Pos():pl.typ.End()] + "]()"
	case "Optional", "List":
		return pl.kind + "(" + planText(pl.base) + ")"
	case "Field":
		return fmt.Sprintf("Field(%q, %s)", pl.name.s, planText(pl.base))
	}
	var parts []string
	for _, fp := range pl.fields {
		parts = append(parts, planText(fp))
	}
	return pl.kind + "([]FieldPlan{" + strings.Join(parts, ", ") + "})"
}

var posType = reflect.TypeOf(syntax.Pos(0))

// reposition sets every valid position in the generated declaration x to
// at, the derive: the bundle has no source of its own (as a refinement's
// derived declarations stand at its where).
func reposition(x any, at syntax.Pos) {
	var visit func(v reflect.Value)
	visit = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				visit(v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				fv := v.Field(i)
				if !fv.CanSet() {
					continue
				}
				if fv.Type() == posType {
					if syntax.Pos(fv.Int()).IsValid() {
						fv.SetInt(int64(at))
					}
					continue
				}
				if v.Type().Field(i).Name == "Derived" || v.Type().Field(i).Name == "Origin" {
					continue
				}
				visit(fv)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(x))
}
