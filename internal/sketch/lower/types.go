package lower

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// typeCtx spells checked types in Go inside one declaration, whose type
// parameters it names.
type typeCtx struct {
	l      *lowerer
	params map[*check.TypeParam]string
	site   syntax.Node // where a refusal of a type is located
}

// goType is t's Go type. Basic types keep their Go names (duration is
// time.Duration); a declared type is its lowered declaration with its
// type-kinded arguments; a function type is the uniform function value
// (funcType).
func (tc *typeCtx) goType(t check.Type) string {
	if t == check.NoValue {
		// A type parameter bound by a block with no value stands for no
		// value, lowered to struct{} (types.md, Inference): task.Go's T in
		// task.Go() { work() }, whose Wait returns struct{}{}, discarded.
		return "struct{}"
	}
	switch t := t.(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Invalid:
			ice(tc.site, "an invalid type reached lowering")
		case check.Duration:
			return tc.l.timePkg() + ".Duration"
		}
		return t.Name
	case *check.Named:
		// A std type is the runtime's Go type (stage D3b; std.go).
		name := tc.l.runtimeType(t, tc.site)
		if name == "" {
			// A type argument that is no value is struct{} (above); a
			// field func() T of the instance is then the one Go shape of
			// a function value with no result (funcType, C11).
			name = tc.l.useType(tc.l.typeDecl(t.Obj, tc.site)).name
		}
		origin := t
		if t.Orig != nil {
			origin = t.Orig
		}
		var args []string
		for i, a := range t.TArgs {
			if i < len(origin.TParams) && origin.TParams[i].Kind == check.KindType {
				args = append(args, tc.goType(a))
			}
		}
		if len(args) == 0 {
			return name
		}
		return name + "[" + strings.Join(args, ", ") + "]"
	case *check.Slice:
		return "[]" + tc.goType(t.Elem)
	case *check.Map:
		return "map[" + tc.goType(t.Key) + "]" + tc.goType(t.Elem)
	case *check.Pointer:
		return "*" + tc.goType(t.Elem)
	case *check.Signature:
		return tc.funcType(t)
	case *check.TypeParam:
		if name, ok := tc.params[t]; ok {
			return name
		}
		ice(tc.site, "the type parameter %s appears outside its declaration", t.Name)
	case *check.Tuple:
		// A tuple is structural (196t): an unnamed Go struct, identical
		// wherever its element types are, and comparable when they are.
		fields := make([]string, len(t.Types))
		for i, e := range t.Types {
			fields[i] = tupleField(i) + " " + tc.goType(e)
		}
		return "struct{ " + strings.Join(fields, "; ") + " }"
	case *check.Union:
		// The row value of `case err:` (D-C3) is a Go error holding one of
		// its error types.
		if tc.l.isErrorValue(t, tc.l.buildView()) {
			return "error"
		}
		ice(tc.site, "the anonymous union %s is not a row value; the checker builds no other", t)
	case *check.Interface:
		// An unnamed interface is a predeclared constraint, which only
		// constrains a type parameter (typeParams): Ergo has no any (D-E2),
		// so no value has one as its type.
		ice(tc.site, "the constraint %s used as a type, which the checker refuses", t)
	}
	// A Struct and a MachineType are only a Named type's underlying type,
	// and a RowArg only a row parameter's argument, which Go erases; no
	// value has another type (stage D3b round 1 review M2: this names no
	// owner, so a checked program must never reach it).
	ice(tc.site, "the type %s (%T) has no Go form", t, t)
	return ""
}

// funcType is the Go type of an Ergo function value. Every function value
// has one shape whatever its rows: it takes the environment first and
// returns an error last, so a row parameter never changes a Go type and a
// generic function over rows needs no specialised copy (spec D5). A
// function value with no result returns struct{} before the error
// (valueResults), so func() and the instance func() T whose T a block with
// no value binds (struct{}, types.md, Inference) are one Go type, wherever
// it is nested: a field, a slice element, a parameter's parameter (C11). A
// call of a declared function by name uses the declaration's own signature,
// which takes the environment too (stage D3b, Rule 1) and returns an error
// only when its error row is not empty.
func (tc *typeCtx) funcType(s *check.Signature) string {
	parts := []string{"*" + tc.l.env()}
	if s.ResultIsTask() {
		parts = append(parts, "*"+tc.l.rt()+".Scope") // the destination of the returned Task (stage D3b, Rule 2)
	}
	for _, p := range s.Params {
		parts = append(parts, tc.goType(p.Type))
	}
	var result check.Type
	if len(s.Results) > 1 {
		unreachable(tc.site, ruleOneResult, "a function type with %d results", len(s.Results))
	}
	if len(s.Results) == 1 {
		result = s.Results[0]
	}
	return "func(" + strings.Join(parts, ", ") + ")" + tc.valueResults(result)
}

// valueResults is the result list of a function value of the uniform shape
// (funcType): its value, or struct{} when it has none, then the error.
// struct{} has no size, so the shape costs no register and no allocation.
func (tc *typeCtx) valueResults(result check.Type) string {
	if result == nil {
		return " (struct{}, error)"
	}
	return tc.resultList(result, true)
}

// resultList is a Go signature's results: the value, then the error when
// fallible.
func (tc *typeCtx) resultList(result check.Type, fallible bool) string {
	switch {
	case result != nil && fallible:
		return " (" + tc.goType(result) + ", error)"
	case result != nil:
		return " " + tc.goType(result)
	case fallible:
		return " error"
	}
	return ""
}

// methodSig is an operation's Go method signature, after its name: the
// caller's task first (rows.md, Rows 7: capabilities become Go interfaces
// whose methods take the task handle first), then, for an operation that
// returns a Task, the destination its return moves the Task to (stage D3b,
// Rule 2, as a function's; stage E2, C15), then its parameters.
func (tc *typeCtx) methodSig(s *check.Signature) string {
	params := []string{"*" + tc.l.taskPkg() + ".Task"}
	if s.ResultIsTask() {
		params = append(params, "*"+tc.l.rt()+".Scope")
	}
	for _, p := range s.Params {
		params = append(params, tc.goType(p.Type))
	}
	if len(s.Results) > 1 {
		unreachable(tc.site, ruleOneResult, "an operation with %d results", len(s.Results))
	}
	var result check.Type
	if len(s.Results) == 1 {
		result = s.Results[0]
	}
	return "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, len(rowElems(s.Errors)) > 0)
}

// zero is the zero value of t in Go, for the value slot of a failure
// return.
func (tc *typeCtx) zero(t check.Type) string {
	if b, ok := t.(*check.Basic); ok {
		switch {
		case b.Kind == check.Bool:
			return "false"
		case b.Kind == check.String:
			return `""`
		case b.Kind == check.Duration:
			return "0"
		case b.Kind >= check.Int && b.Kind <= check.Complex128:
			return "0"
		}
	}
	return "*new(" + tc.goType(t) + ")"
}

// Defaults (types.md, Zero values; Loops and len rule 5). The default of a
// type, for a let without a value and for a record field a literal omits,
// is Go's zero value, except that a Map is an empty, writable Map, never
// Go's nil map, which panics on a write. A Map inside a record or a tuple,
// named or not, a generic instance included, is one too, at any depth.
//
// One owner builds every default, in work linear in the type graph, output
// included (spec D16, Speed 8):
//
//   - holdsMap decides whether a type's default holds a Map, memoized per
//     lowering by type identity: a defined type or instance is visited once,
//     a tuple is decided from its elements' results.
//   - A defined type or instance whose default holds a Map has one generated
//     Go function, with no parameters, that returns its default
//     (defaultFunc). It is emitted once, at first use; its name is given by
//     the naming pass (reserveDefaults). A default of the type is a call of
//     it, so a type that two fields share is built by one function, never
//     by a copy of its literal: a diamond of records costs one function per
//     record, not a tree of literals.
//   - A Map is `map[K]V{}` inline; an unnamed tuple is an inline keyed
//     literal over its elements' defaults.
//
// Every call builds new Maps, so two defaults never share a Map. The memo
// holds decisions and names, never runtime values.
//
// Prior art: Rust's #[derive(Default)] emits one default() per type, which
// calls each field's Default::default(), so a nested default is a call, not
// an inlined tree; Go zero-fills by type layout. Gleam, ReScript and Elixir
// have no implicit default: a value is always built, as here the empty Map
// is built by the generated function.

// holdState is holdsMap's memo of a defined type or instance.
type holdState int8

const (
	holdsUnknown holdState = iota
	holdsVisiting
	holdsNo
	holdsYes
)

// defaultValue is the default of t in Go (see Defaults). It is "" when Go's
// zero value is the default, so a declaration can omit it.
func (tc *typeCtx) defaultValue(t check.Type) string {
	if !tc.l.holdsMap(t) {
		return ""
	}
	switch u := t.(type) {
	case *check.Map:
		return tc.goType(t) + "{}"
	case *check.Tuple:
		return tc.goType(t) + "{" + strings.Join(tc.tupleDefaults(u), ", ") + "}"
	case *check.Named:
		name, free := tc.l.defaultFunc(u, tc.site)
		if len(free) == 0 {
			return name + "()"
		}
		args := make([]string, len(free))
		for i, tp := range free {
			args[i] = tc.goType(tp)
		}
		return name + "[" + strings.Join(args, ", ") + "]()"
	}
	ice(tc.site, "the type %s holds a Map but has no default", t)
	return ""
}

// tupleDefaults is the keyed Go elements that set each element of the tuple
// u whose default is not Go's zero value.
func (tc *typeCtx) tupleDefaults(u *check.Tuple) []string {
	var parts []string
	for i, e := range u.Types {
		if v := tc.defaultValue(e); v != "" {
			parts = append(parts, tupleField(i)+": "+v)
		}
	}
	return parts
}

// defaultFunc is the generated function that returns the default of n, a
// defined type or instance whose default holds a Map, and the type
// parameters of the enclosing declaration that n mentions, which the
// function takes as its own type parameters. The function is emitted once,
// at its first use; the defaults of n's fields and elements call their own
// functions.
func (l *lowerer) defaultFunc(n *check.Named, site syntax.Node) (string, []*check.TypeParam) {
	key := l.defaultKey(n)
	name := l.pkgName(key, defaultBase(n))
	free := freeTypeParams(n)
	if l.defaults[key] {
		return name, free
	}
	l.defaults[key] = true
	scope := newNamer(l.names)
	list, names := l.typeParams(free, scope, site)
	tc := &typeCtx{l: l, params: names, site: site}
	goType := tc.goType(n)
	var parts []string
	switch under := n.Underlying().(type) {
	case *check.Map:
	case *check.Tuple:
		// A defined tuple keeps its Go type: a struct of F0, F1, ...
		parts = tc.tupleDefaults(under)
	case *check.Struct:
		parts = tc.fieldDefaults(l.typeDecl(n.Obj, site), under, nil)
	default:
		ice(site, "the type %s holds a Map but has no default", n)
	}
	l.gen.WriteString("func " + name + list + "() " + goType + " {\n\treturn " + goType + "{" + strings.Join(parts, ", ") + "}\n}\n\n")
	return name, free
}

// defaultBase is the preferred Go name of n's default function.
func defaultBase(n *check.Named) string { return "ergoDefault" + n.Obj.Name }

// defaultKey is the package-scope key of n's default function: n's
// spelling (typeKey), one function per Go type.
func (l *lowerer) defaultKey(n *check.Named) pkgKey {
	return pkgKey{"default", l.typeKey(n)}
}

// typeKey spells t for a key: deterministic, from the source alone, and
// distinct for distinct Go types. A declared type is named by its package
// path, name and position, a type parameter by its name, position and
// source file (paramFile); a
// type argument that is not type-kinded is left out, as goType leaves it
// out. It is memoized per type object, so a key is built from its
// children's keys once.
func (l *lowerer) typeKey(t check.Type) string {
	if k, ok := l.typeKeys[t]; ok {
		return k
	}
	var k string
	switch u := t.(type) {
	case *check.Basic:
		k = u.Name
	case *check.Named:
		path := ""
		if u.Obj.Pkg != nil {
			path = u.Obj.Pkg.Path
		}
		k = path + "." + u.Obj.Name + "@" + strconv.Itoa(int(u.Obj.Pos))
		if args := typeArgs(u); len(args) > 0 {
			keys := make([]string, len(args))
			for i, a := range args {
				keys[i] = l.typeKey(a)
			}
			k += "[" + strings.Join(keys, ", ") + "]"
		}
	case *check.TypeParam:
		k = "$" + u.Name + "@" + strconv.Itoa(int(u.Pos)) + l.paramFile(u)
	case *check.Slice:
		k = "[]" + l.typeKey(u.Elem)
	case *check.Map:
		k = "map[" + l.typeKey(u.Key) + "]" + l.typeKey(u.Elem)
	case *check.Pointer:
		k = "*" + l.typeKey(u.Elem)
	case *check.Tuple:
		keys := make([]string, len(u.Types))
		for i, e := range u.Types {
			keys[i] = l.typeKey(e)
		}
		k = "(" + strings.Join(keys, ", ") + ")"
	case *check.Signature:
		keys := make([]string, len(u.Params))
		for i, p := range u.Params {
			keys[i] = l.typeKey(p.Type)
		}
		k = "func(" + strings.Join(keys, ", ") + ")"
		for _, r := range u.Results {
			k += " " + l.typeKey(r)
		}
	default:
		k = "?" + t.String()
	}
	l.typeKeys[t] = k
	return k
}

// typeArgs is n's type-kinded type arguments, the ones its Go type takes.
func typeArgs(n *check.Named) []check.Type {
	origin := n
	if n.Orig != nil {
		origin = n.Orig
	}
	var args []check.Type
	for i, a := range n.TArgs {
		if i < len(origin.TParams) && origin.TParams[i].Kind == check.KindType {
			args = append(args, a)
		}
	}
	return args
}

// freeTypeParams is the type-kinded type parameters that t's Go type
// mentions, in the order of their first mention.
func freeTypeParams(t check.Type) []*check.TypeParam {
	var out []*check.TypeParam
	seen := map[*check.TypeParam]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		switch u := t.(type) {
		case *check.TypeParam:
			if u.Kind == check.KindType && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		case *check.Named:
			for _, a := range typeArgs(u) {
				walk(a)
			}
		case *check.Slice:
			walk(u.Elem)
		case *check.Map:
			walk(u.Key)
			walk(u.Elem)
		case *check.Pointer:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Signature:
			for _, p := range u.Params {
				walk(p.Type)
			}
			for _, r := range u.Results {
				walk(r)
			}
		}
	}
	walk(t)
	return out
}

// reserveDefaults gives, in the naming pass, the key and preferred name of
// every default function the lowering can emit: one for each defined type
// or instance whose default holds a Map and that a let without a value, a
// record literal or a field or element of one reaches. It reads the
// checker's Defs and Types that this build reads (owners.go); each type
// object is walked once. Reserving a
// function the lowering does not emit only renames a local.
func (l *lowerer) reserveDefaults(reserve func(key pkgKey, base string)) {
	walked := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if t == nil || walked[t] {
			return
		}
		walked[t] = true
		switch u := t.(type) {
		case *check.Slice:
			walk(u.Elem)
		case *check.Map:
			walk(u.Key)
			walk(u.Elem)
		case *check.Pointer:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Named:
			if l.holdsMap(u) {
				reserve(l.defaultKey(u), defaultBase(u))
			}
			switch under := u.Underlying().(type) {
			case *check.Tuple:
				walk(under)
			case *check.Struct:
				for _, f := range under.Fields {
					walk(f.Type)
				}
			}
		}
	}
	for _, p := range l.sourcedPkgs() {
		for id, o := range p.Info.Defs {
			if v, ok := o.(*check.Var); ok && l.readsFact(p, id) {
				walk(v.Type)
			}
		}
		for x, t := range p.Info.Types {
			if l.readsFact(p, x) {
				walk(t)
			}
		}
	}
}

// fieldDefaults is the keyed Go elements that set each field of the record
// s, not in set, whose default is not Go's zero value.
func (tc *typeCtx) fieldDefaults(td *typeDecl, s *check.Struct, set map[string]bool) []string {
	var parts []string
	for _, f := range s.Fields {
		if set[f.Name] {
			continue
		}
		if v := tc.defaultValue(f.Type); v != "" {
			name, ok := td.fieldName[f.Name]
			if !ok {
				ice(tc.site, "%s has no field %s", td.name, f.Name)
			}
			parts = append(parts, name+": "+v)
		}
	}
	return parts
}

// holdsMap reports whether t's default holds a Map: t is a Map, or a
// record or tuple, named or not, with a field or element that holds one. A
// slice's default is empty, so its elements hold nothing. A type parameter
// has no default (the checker refuses it). A defined type or instance is
// decided once per lowering (see Defaults); a type that reaches itself
// through its own fields holds nothing more through that path.
func (l *lowerer) holdsMap(t check.Type) bool {
	l.holdsVisits++
	switch u := t.(type) {
	case *check.Map:
		return true
	case *check.Tuple:
		for _, e := range u.Types {
			if l.holdsMap(e) {
				return true
			}
		}
	case *check.Named:
		switch l.holds[u] {
		case holdsYes:
			return true
		case holdsNo, holdsVisiting:
			return false
		}
		l.holds[u] = holdsVisiting
		held := false
		switch under := u.Underlying().(type) {
		case *check.Map:
			held = true
		case *check.Tuple:
			held = l.holdsMap(under)
		case *check.Struct:
			for _, f := range under.Fields {
				if l.holdsMap(f.Type) {
					held = true
					break
				}
			}
		}
		l.holds[u] = holdsNo
		if held {
			l.holds[u] = holdsYes
		}
		return held
	}
	return false
}

// constant is a checked constant of type t in Go: the exact value, converted
// to t's Go type, so no Go untyped-constant rule picks another type (196u,
// spec D4). A float is written as an exact fraction, so float32 rounds once,
// from the exact value.
func (tc *typeCtx) constant(t check.Type, v constant.Value, site syntax.Node) string {
	goType := tc.goType(t)
	// A defined type over a basic type keeps its own name: Flag(true).
	basic, _ := t.(*check.Basic)
	nominal := false
	if n, ok := t.(*check.Named); ok {
		basic, _ = n.Underlying().(*check.Basic)
		nominal = true
	}
	switch v.Kind() {
	// An untyped Go string or bool constant becomes bool or string as a
	// bare literal; a defined type over one is written as a conversion, so
	// the constant keeps its nominal type.
	case constant.Bool:
		if basic == nil || basic.Kind != check.Bool {
			ice(site, "a bool constant of type %s", t)
		}
		if nominal {
			return goType + "(" + strconv.FormatBool(constant.BoolVal(v)) + ")"
		}
		return strconv.FormatBool(constant.BoolVal(v))
	case constant.String:
		if basic == nil || !isString(basic) {
			ice(site, "a string constant of type %s", t)
		}
		if nominal {
			return goType + "(" + strconv.Quote(constant.StringVal(v)) + ")"
		}
		return strconv.Quote(constant.StringVal(v))
	case constant.Int:
		return goType + "(" + v.ExactString() + ")"
	case constant.Float:
		if isInteger(t) {
			if i := constant.ToInt(v); i.Kind() == constant.Int {
				return goType + "(" + i.ExactString() + ")"
			}
		}
		return goType + "(" + exactFloat(v) + ")"
	case constant.Complex:
		return goType + "(complex(" + exactFloat(constant.Real(v)) + ", " + exactFloat(constant.Imag(v)) + "))"
	}
	ice(site, "the constant %s has no Go spelling", v)
	return ""
}

// exactFloat spells a float constant exactly as Go untyped constant
// arithmetic, num.0/den.
func exactFloat(v constant.Value) string {
	if v.Kind() == constant.Int {
		return v.ExactString() + ".0"
	}
	num, den := constant.Num(v), constant.Denom(v)
	if den.ExactString() == "1" {
		return "(" + num.ExactString() + ".0)"
	}
	return "(" + num.ExactString() + ".0 / " + den.ExactString() + ")"
}

// foreignOwner is the type of another, non-prelude package that declares
// the operations of t, or nil: t itself, or the type a defined type is
// defined over, at any depth, through a pointer too. So `type MyQ
// queue.Queue[int]` has queue.Queue's operations, and so has
// `*queue.Queue[int]`.
func (l *lowerer) foreignOwner(t check.Type) *check.Named {
	seen := map[*check.Named]bool{}
	for t != nil {
		if p, ok := t.(*check.Pointer); ok {
			t = p.Elem
		}
		n, ok := t.(*check.Named)
		if !ok || seen[n] {
			return nil
		}
		seen[n] = true
		if pkg := n.Obj.Pkg; pkg != nil && pkg != l.pkg && pkg.Path != "prelude" {
			return n
		}
		t = n.Defined()
	}
	return nil
}

func isInteger(t check.Type) bool {
	b, ok := t.(*check.Basic)
	return ok && b.Kind >= check.Int && b.Kind <= check.Uintptr
}

// isErrorValue reports a value of an error type in a body of owner o:
// returning it is a failure (types.md, Error types 2). The checker's error
// types of the type's package, and of this package, that o sees decide it
// (errorType).
func (l *lowerer) isErrorValue(t check.Type, o check.FactOwner) bool {
	if u, row := t.(*check.Union); row {
		// A row value (D-C3): every variant is an error type.
		for _, v := range u.Variants {
			if !l.isErrorValue(v, o) {
				return false
			}
		}
		return len(u.Variants) > 0
	}
	n, ok := t.(*check.Named)
	return ok && l.errorType(n, o)
}
