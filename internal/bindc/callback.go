package bindc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// This file holds the callbacks of a binding: the admission rule for
// callback shapes, the classes of a callback's parameters, the class of a
// function parameter whose type is a callback, and Bind's checks of the
// contract's callbacks entries.

// The register bounds of callback admission. A callback gets a typed
// trampoline only when, on each tier-1 target, every argument travels in a
// register: amd64 System V passes 6 integer-class and 8 float-class
// arguments in registers, AAPCS64 (arm64) 8 and 8. The bound is the
// smaller of each.
const (
	callbackIntRegs   = 6
	callbackFloatRegs = 8
)

// MaxBoundCallbacks is the most bound callback typedefs one binding may
// have. Each bound typedef takes one purego callback slot for its
// trampoline, once per process; purego v0.11.3 has 2000 slots per process
// and never frees one, so 64 per binding leaves room for other bindings
// and other purego users. Bind refuses a binding above it.
const MaxBoundCallbacks = 64

// BoundCallback is one callback typedef of the header and what Bind makes
// of it.
type BoundCallback struct {
	Decl *TypeDecl
	// GoName is the Go name of its typedef (the Go func type).
	GoName string
	// Rule is its callbacks entry, or nil.
	Rule *CallbackRule
	// Shape says why the typedef is not an admitted shape, or "".
	Shape string
	// Token is true when the key is one `void *` token.
	Token bool
	// Positions are the classes of its parameters, in order, when it has
	// an entry and an admitted shape.
	Positions []Position
	// RawOnly says why no function takes it typed: a shape that is not
	// admitted, no entry, or a raw position. It is "" when it is bound.
	RawOnly string
	// Index is its number among the bound typedefs of the binding, in
	// model order (its trampoline cbTramp<Index> and its registry
	// cbMap<Index> in the Go package), or -1 when it is raw.
	Index int
	// Sig is the Go func type of a bound typedef, or nil.
	Sig *GoCallbackSig
}

// Bound reports whether the typedef has a typed form.
func (c *BoundCallback) Bound() bool { return c.RawOnly == "" }

// callbackFunc views a callback typedef as a function, so that the rules
// and positionName read its parameters.
func callbackFunc(d *TypeDecl) *Function {
	f := &Function{Name: d.Name, Params: d.Params}
	if d.Result != nil {
		f.Result = *d.Result
	}
	return f
}

// callbackShape says why the callback typedef d gets no typed trampoline
// on every tier-1 target, or "". The admitted shapes: parameters that are
// fixed-width integers, bool, float, double and data pointers (void * as
// well), at most callbackIntRegs of integer class (integers, bool and
// pointers) and callbackFloatRegs of float class; a result that is void,
// a fixed-width integer, bool or a data pointer. purego v0.11.3's
// compileCallback refuses a float or double result, and no struct by value
// is admitted, as a parameter or a result. The reasons come in parameter
// order, then the counts, then the result.
func (k *classifier) callbackShape(d *TypeDecl) string {
	f := callbackFunc(d)
	var why []string
	ints, floats := 0, 0
	for i, p := range d.Params {
		t := p.Type
		switch {
		case t.Kind == TypePointer:
			ints++
		case t.Kind == TypeNamed && k.types[t.Name] != nil && k.types[t.Name].Kind == DeclCallback:
			why = append(why, positionName(f, i)+": a callback parameter (a function pointer)")
		case t.Kind == TypeNamed && k.types[t.Name] != nil && k.types[t.Name].Kind == DeclStruct:
			why = append(why, positionName(f, i)+": a struct by value")
		default:
			s, ok := k.scalarOf(t)
			switch {
			case !ok:
				why = append(why, positionName(f, i)+": not a scalar or a pointer")
			case s == Float32 || s == Float64:
				floats++
			default:
				ints++
			}
		}
	}
	if ints > callbackIntRegs {
		why = append(why, fmt.Sprintf("%d integer-class parameters (at most %d travel in registers on amd64)", ints, callbackIntRegs))
	}
	if floats > callbackFloatRegs {
		why = append(why, fmt.Sprintf("%d float-class parameters (at most %d travel in registers)", floats, callbackFloatRegs))
	}
	r := f.Result
	switch {
	case r.Kind == TypeVoid, r.Kind == TypePointer:
	case r.Kind == TypeNamed && k.types[r.Name] != nil && k.types[r.Name].Kind == DeclCallback:
		why = append(why, "a callback result (a function pointer)")
	case r.Kind == TypeNamed && k.types[r.Name] != nil && k.types[r.Name].Kind == DeclStruct:
		why = append(why, "a struct result by value")
	default:
		s, ok := k.scalarOf(r)
		switch {
		case !ok:
			why = append(why, "a result that is not a scalar or a pointer")
		case s == Float32 || s == Float64:
			why = append(why, "a "+string(s)+" result (purego v0.11.3 callbacks cannot return float or double)")
		}
	}
	return strings.Join(why, "; ")
}

// The rules of callback parameters, beside ruleOut, ruleValue,
// ruleInBuffer and ruleInBufferBytes, which read a callback's parameters
// as they read a function's.
const (
	ruleCbKey    = "key"    // a parameter the entry's key names
	ruleCbScalar = "scalar" // a scalar that the key does not name and that is not the length of the parameter before it
	ruleCbLength = "length" // an unsigned length that a length template gives for the `const T *` before it
)

// callbackRuleSet are the function rules that also apply to callbacks.
var callbackRuleSet = map[string]bool{ruleOut: true, ruleValue: true, ruleInBuffer: true, ruleInBufferBytes: true}

// cbRules returns the names of the rules that match parameter i of the
// callback cf with entry r.
func (k *classifier) cbRules(cf *Function, r *CallbackRule, i int) []string {
	p := cf.Params[i]
	var out []string
	keyed := p.Name != "" && slices.Contains(r.Key.Params, p.Name)
	if keyed {
		out = append(out, ruleCbKey)
	}
	isLen := k.lengthOfPrev(cf, i)
	if _, ok := k.scalarOf(p.Type); ok && !keyed && !isLen {
		out = append(out, ruleCbScalar)
	}
	if isLen {
		out = append(out, ruleCbLength)
	}
	for _, rr := range k.rules(cf, i) {
		if callbackRuleSet[rr] {
			out = append(out, rr)
		}
	}
	return out
}

// lengthOfPrev reports whether parameter i of f is the length of a
// `const T *` buffer just before it, by a template of either list.
func (k *classifier) lengthOfPrev(f *Function, i int) bool {
	if i == 0 {
		return false
	}
	b := f.Params[i-1].Type
	if b.Kind != TypePointer || b.LenExpr != "" || !b.Elem.Const || !k.element(*b.Elem) {
		return false
	}
	elements, bytes := k.lengthUnits(f.Params[i-1], f.Params[i])
	return elements || bytes
}

// classifyCallback gives every parameter of an admitted callback typedef
// with entry r its class, and returns the reason the typedef is raw, or "".
func (k *classifier) classifyCallback(d *TypeDecl, r *CallbackRule) ([]Position, string) {
	cf := callbackFunc(d)
	var pos []Position
	for i := range cf.Params {
		if e := k.cbExc[d.Name][i]; e != nil {
			q := k.fromException(cf, i, e)
			q.Array = e.ArrayLength
			pos = append(pos, q)
			continue
		}
		q := ruledPosition(cf, i, k.cbRules(cf, r, i))
		if q.Class == ClassRaw && q.Reason == "" {
			q.Reason = "no callback rule covers it"
			if t := cf.Params[i].Type; t.Kind == TypePointer {
				if s := k.holdsPointer(*t.Elem); s != "" {
					q.Reason = s + " holds a pointer (Go memory that holds Go pointers must not reach C)"
				}
			}
		}
		pos = append(pos, q)
	}
	// An in_buffer exception gives its length the class length.
	for _, q := range slices.Clone(pos) {
		if q.Class == ClassInBuffer && q.By == "exception" {
			pos[q.Length] = Position{Param: q.Length, Class: ClassLength, By: "exception", Length: q.Param, Count: -1}
		}
	}
	var raw []string
	for _, q := range pos {
		if q.Class == ClassRaw {
			raw = append(raw, positionName(cf, q.Param)+": "+q.Reason)
		}
	}
	if len(raw) > 0 {
		return pos, strings.Join(raw, "; ")
	}
	// Each length serves its one buffer.
	for _, q := range pos {
		switch q.Class {
		case ClassInBuffer:
			if l := pos[q.Length]; l.Class != ClassLength || l.Length != q.Param {
				return pos, positionName(cf, q.Param) + ": its length " + positionName(cf, q.Length) + " is not its length position"
			}
		case ClassLength:
			if b := pos[q.Length]; b.Class != ClassInBuffer || b.Length != q.Param {
				return pos, positionName(cf, q.Param) + ": a length without its in_buffer"
			}
		}
	}
	return pos, ""
}

// buildCallbacks gives every callback typedef of the model its
// BoundCallback, after Bind has checked the entries and the exceptions.
func (k *classifier) buildCallbacks(b *Binding) {
	for i := range k.m.Types {
		d := &k.m.Types[i]
		if d.Kind != DeclCallback {
			continue
		}
		bc := &BoundCallback{Decl: d, GoName: b.GoTypes[d.Name], Rule: k.entries[d.Name], Shape: k.callbackShape(d), Index: -1}
		switch {
		case bc.Shape != "":
			bc.RawOnly = d.Name + " is not an admitted callback shape: " + bc.Shape
		case bc.Rule == nil:
			bc.RawOnly = d.Name + " has no callbacks entry in the contract"
		default:
			bc.Token = k.tokenKey(d, bc.Rule)
			var why string
			bc.Positions, why = k.classifyCallback(d, bc.Rule)
			if why != "" {
				bc.RawOnly = d.Name + " has a raw position: " + why
			}
		}
		if bc.Bound() {
			bc.Index = b.boundCallbacks()
		}
		k.cbs[d.Name] = bc
		b.Callbacks = append(b.Callbacks, bc)
	}
}

// boundCallbacks is the number of bound callback typedefs of b so far.
func (b *Binding) boundCallbacks() int {
	n := 0
	for _, c := range b.Callbacks {
		if c.Bound() {
			n++
		}
	}
	return n
}

// checkSlotBound refuses a binding with more than MaxBoundCallbacks bound
// callback typedefs, with the count and the bound.
func (b *Binding) checkSlotBound() error {
	if n := b.boundCallbacks(); n > MaxBoundCallbacks {
		return contractError([]string{fmt.Sprintf("callbacks: %d bound callback typedefs, above the bound of %d per binding "+
			"(each takes one of purego's 2000 callback slots per process, and purego never frees one)", n, MaxBoundCallbacks)})
	}
	return nil
}

// tokenKey reports whether the key of entry r is one `void *` parameter of
// d (Bind has checked the key).
func (k *classifier) tokenKey(d *TypeDecl, r *CallbackRule) bool {
	if len(r.Key.Params) != 1 {
		return false
	}
	j, ok := namedParam(d.Params, r.Key.Params[0])
	return ok && isVoidPtr(d.Params[j].Type)
}

// isVoidPtr reports whether t is `void *`.
func isVoidPtr(t Type) bool {
	return t.Kind == TypePointer && t.LenExpr == "" && t.Elem.Kind == TypeVoid && !t.Elem.Const
}

// namedParam finds the parameter named n (never an unnamed one).
func namedParam(ps []Param, n string) (int, bool) {
	for i, p := range ps {
		if p.Name != "" && p.Name == n {
			return i, true
		}
	}
	return 0, false
}

// keySource finds the parameter of f that gives the key of the callback
// typedef d with entry r, or says why f has none.
func (k *classifier) keySource(d *TypeDecl, r *CallbackRule, f *Function) (int, string) {
	j, ok := namedParam(f.Params, r.Key.Source)
	if !ok {
		return -1, fmt.Sprintf("the key (%s) of %s has no source: no parameter %s", strings.Join(r.Key.Params, ", "), d.Name, r.Key.Source)
	}
	t := f.Params[j].Type
	if k.tokenKey(d, r) {
		if !isVoidPtr(t) {
			return -1, fmt.Sprintf("the key source %s of %s is not a `void *`", r.Key.Source, d.Name)
		}
		return j, ""
	}
	if t.Kind != TypePointer || t.LenExpr != "" || !t.Elem.Const || t.Elem.Kind != TypeNamed || !k.keyStruct(d, r, t.Elem.Name) {
		return -1, fmt.Sprintf("the key source %s of %s is not a `const S *` whose struct S has exactly the fields %s",
			r.Key.Source, d.Name, strings.Join(r.Key.Params, ", "))
	}
	return j, ""
}

// keyStruct reports whether struct s has exactly the key's fields: the
// key's parameter names in order, each with the scalar type of the
// callback's parameter.
func (k *classifier) keyStruct(d *TypeDecl, r *CallbackRule, s string) bool {
	sd := k.types[s]
	if sd == nil || sd.Kind != DeclStruct || len(sd.Fields) != len(r.Key.Params) {
		return false
	}
	for i, fl := range sd.Fields {
		j, ok := namedParam(d.Params, r.Key.Params[i])
		if fl.Name != r.Key.Params[i] || !ok || !sameScalar(k, fl.Type, d.Params[j].Type) {
			return false
		}
	}
	return true
}

// callbackParam says why parameter i of f, whose type is a callback
// typedef, is not typed, or "".
func (k *classifier) callbackParam(f *Function, i int) string {
	bc := k.cbs[f.Params[i].Type.Name]
	if bc == nil {
		return "not a callback typedef"
	}
	if bc.RawOnly != "" {
		return bc.RawOnly
	}
	if _, why := k.keySource(bc.Decl, bc.Rule, f); why != "" {
		return why
	}
	if bc.Rule.Scope == ScopeRegistered && !k.takesHandle(f, bc.Rule.Owner) {
		return fmt.Sprintf("registered %s needs its owner, a `%s *`, as a parameter", bc.Decl.Name, bc.Rule.Owner)
	}
	return ""
}

// takesHandle reports whether f has a `H *` parameter.
func (k *classifier) takesHandle(f *Function, h string) bool {
	for _, p := range f.Params {
		if t := p.Type; t.Kind == TypePointer && t.LenExpr == "" && t.Elem.Kind == TypeNamed && t.Elem.Name == h {
			return true
		}
	}
	return false
}

// tokenOf returns the parameter of f whose callback's token key parameter
// i of f is (a named `void *` that a token key names as its source), or -1.
func (k *classifier) tokenOf(f *Function, i int) int {
	p := f.Params[i]
	if p.Name == "" || !isVoidPtr(p.Type) {
		return -1
	}
	for j, q := range f.Params {
		if q.Type.Kind != TypeNamed {
			continue
		}
		if bc := k.cbs[q.Type.Name]; bc != nil && bc.Rule != nil && bc.Token && bc.Rule.Key.Source == p.Name {
			return j
		}
	}
	return -1
}

// tokenSource returns the token parameter of the callback parameter j of
// f, or -1 when its key is not a token or has no source.
func (k *classifier) tokenSource(f *Function, j int) int {
	bc := k.cbs[f.Params[j].Type.Name]
	if bc == nil || bc.Rule == nil || !bc.Token {
		return -1
	}
	s, why := k.keySource(bc.Decl, bc.Rule, f)
	if why != "" {
		return -1
	}
	return s
}

// checkCallbacks checks the contract's callbacks entries against the
// model: each names a callback typedef once, with an admitted shape, a key
// of its own parameters that has a source in some function that takes it,
// a default that fits its result, and for a registered callback a handle
// owner and end functions that take the key.
func (k *classifier) checkCallbacks(funcs map[string]*Function, skipped map[string]bool, bad func(string, ...any)) {
	for i := range k.c.Callbacks {
		r := &k.c.Callbacks[i]
		at := "callbacks: " + r.Typedef + ": "
		d := k.types[r.Typedef]
		if d == nil || d.Kind != DeclCallback {
			bad("%sis not a callback typedef of the header", at)
			continue
		}
		if k.entries[r.Typedef] != nil {
			bad("%sappears twice", at)
			continue
		}
		k.entries[r.Typedef] = r
		if s := k.callbackShape(d); s != "" {
			bad("%sis not an admitted callback shape (%s); its functions stay raw-only, so remove the entry", at, s)
			continue
		}
		if msg := k.checkKey(d, r); msg != "" {
			bad("%s%s", at, msg)
			continue
		}
		sources := 0
		for fi := range k.m.Functions {
			f := &k.m.Functions[fi]
			if skipped[f.Name] || !takesType(f, d.Name) {
				continue
			}
			if _, why := k.keySource(d, r, f); why == "" {
				sources++
			}
		}
		if sources == 0 {
			bad("%sthe key has no source (a parameter %s) in any function that takes it", at, r.Key.Source)
		}
		if msg := k.checkDefault(d, r); msg != "" {
			bad("%s%s", at, msg)
		}
		switch r.Scope {
		case ScopeCall:
			if r.Owner != "" || len(r.Ends) > 0 {
				bad("%sa call-scoped callback has no owner and no end functions", at)
				continue
			}
		case ScopeRegistered:
			if r.Owner == "" {
				bad("%sa registered callback needs an owner", at)
			} else if k.handles[r.Owner] == nil {
				bad("%sthe owner %s is not a handle of the contract", at, r.Owner)
			}
		}
		seenEnd := map[string]bool{}
		for _, e := range r.Ends {
			f := funcs[e]
			switch {
			case seenEnd[e]:
				bad("%send function %s appears twice", at, e)
			case f == nil || skipped[e]:
				bad("%send function %s is not a function of the header, or is skipped", at, e)
			default:
				if _, why := k.keySource(d, r, f); why != "" {
					bad("%send function %s does not take the key: %s", at, e, why)
				}
			}
			seenEnd[e] = true
		}
	}
}

// takesType reports whether f has a parameter of the typedef name.
func takesType(f *Function, name string) bool {
	for _, p := range f.Params {
		if p.Type.Kind == TypeNamed && p.Type.Name == name {
			return true
		}
	}
	return false
}

// checkKey checks the key parameters of entry r: each names a parameter of
// d, and the key is one `void *` or scalar integers.
func (k *classifier) checkKey(d *TypeDecl, r *CallbackRule) string {
	for _, n := range r.Key.Params {
		j, ok := namedParam(d.Params, n)
		if !ok {
			return fmt.Sprintf("key parameter %s is not a parameter of %s", n, d.Name)
		}
		t := d.Params[j].Type
		if isVoidPtr(t) {
			if len(r.Key.Params) != 1 {
				return fmt.Sprintf("key parameter %s is a `void *`: a token key has one parameter", n)
			}
			continue
		}
		if s, ok := k.scalarOf(t); !ok || s == Bool || s == Float32 || s == Float64 {
			return fmt.Sprintf("key parameter %s is a %s: a key is one `void *` or the integer fields of a handle struct", n, t.Spell())
		}
	}
	return ""
}

// checkDefault checks the default of entry r against the result of d.
func (k *classifier) checkDefault(d *TypeDecl, r *CallbackRule) string {
	res := callbackFunc(d).Result
	v := string(r.Default)
	if res.Kind == TypeVoid {
		if v != "" {
			return "a default for a void result"
		}
		return ""
	}
	if v == "" {
		return fmt.Sprintf("the %s result needs a default (the value C gets when no Go code runs)", res.Spell())
	}
	if res.Kind == TypePointer {
		if v != "0" {
			return fmt.Sprintf("default %s does not fit %s (0, NULL, is the only pointer default)", v, res.Spell())
		}
		return ""
	}
	s, _ := k.scalarOf(res)
	ok := false
	switch {
	case s == Bool:
		ok = v == "true" || v == "false"
	case isUnsignedScalar(s):
		_, err := strconv.ParseUint(v, 10, int(8*scalarSize[s]))
		ok = err == nil
	default:
		_, err := strconv.ParseInt(v, 10, int(8*scalarSize[s]))
		ok = err == nil
	}
	if !ok {
		return fmt.Sprintf("default %s does not fit %s", v, res.Spell())
	}
	return ""
}

// checkCallbackExceptions checks the exceptions on callback positions:
// each names a callback with an entry and one of its parameters, one per
// parameter, fits it, and does not repeat what the rules give.
func (k *classifier) checkCallbackExceptions(bad func(string, ...any)) {
	var mine []*Exception
	for i := range k.c.Exceptions {
		e := &k.c.Exceptions[i]
		if e.Callback == "" {
			continue
		}
		d := k.types[e.Callback]
		if d == nil || d.Kind != DeclCallback || k.entries[e.Callback] == nil {
			bad("exceptions: callback %s is not a callback typedef with a callbacks entry", e.Callback)
			continue
		}
		cf := callbackFunc(d)
		j, ok := paramRef(cf, e.Position)
		switch {
		case !ok:
			bad("exceptions: %s has no parameter %s", e.Callback, e.Position)
			continue
		case cf.Params[j].Name != "" && strings.HasPrefix(e.Position, "#"):
			bad("exceptions: %s %s has the name %s; use it", e.Callback, e.Position, cf.Params[j].Name)
			continue
		}
		if k.cbExc[d.Name] == nil {
			k.cbExc[d.Name] = map[int]*Exception{}
		}
		if k.cbExc[d.Name][j] != nil {
			bad("exceptions: %s %s has two exceptions", e.Callback, e.Position)
			continue
		}
		k.cbExc[d.Name][j] = e
		mine = append(mine, e)
	}
	for _, e := range mine {
		d := k.types[e.Callback]
		cf := callbackFunc(d)
		j, _ := paramRef(cf, e.Position)
		if msg := k.checkCbException(d, j, e); msg != "" {
			bad("exceptions: %s %s: %s", e.Callback, e.Position, msg)
			continue
		}
		r, x := ruledPosition(cf, j, k.cbRules(cf, k.entries[d.Name], j)), k.fromException(cf, j, e)
		if r.Class == e.Class && r.Class != ClassRaw && r.Length == x.Length && r.Bytes == x.Bytes {
			bad("exceptions: %s %s repeats the rule %s; remove it", e.Callback, e.Position, r.By)
		}
	}
}

// checkCbException reports why exception e does not fit parameter i of
// the callback typedef d, or "".
func (k *classifier) checkCbException(d *TypeDecl, i int, e *Exception) string {
	r := k.entries[d.Name]
	cf := callbackFunc(d)
	p := cf.Params[i]
	if p.Name != "" && slices.Contains(r.Key.Params, p.Name) {
		return "a key parameter takes no exception"
	}
	switch e.Class {
	case ClassRaw:
		return "" // any parameter of a callback, a scalar too
	case ClassOutArray:
		t := p.Type
		if t.Kind == TypePointer && t.LenExpr == "" && !t.Elem.Const && k.element(*t.Elem) {
			return ""
		}
		return "an out_array is a `T *` to a scalar or a struct without pointers"
	case ClassInBuffer:
		if msg := k.checkException(cf, i, e); msg != "" {
			return msg
		}
		j, _ := paramRef(cf, e.Length)
		if cf.Params[j].Name != "" && slices.Contains(r.Key.Params, cf.Params[j].Name) {
			return fmt.Sprintf("length %q is a key parameter", e.Length)
		}
		if k.cbExc[d.Name][j] != nil {
			return fmt.Sprintf("length %q has its own exception", e.Length)
		}
		return ""
	}
	return k.checkException(cf, i, e)
}
