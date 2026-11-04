package bindc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Class is the class of a pointer position (a parameter or a result whose
// type is a data pointer or a callback).
type Class string

// The classes. Every pointer position gets exactly one.
const (
	// ClassHandle is `H *` or `const H *` for a handle type H.
	ClassHandle Class = "handle"
	// ClassValue is a const input value: `const S *` for a struct S
	// without pointers, or a parameter `const T name[N]`. Go passes a value.
	ClassValue Class = "value"
	// ClassOut is an out-parameter: Go returns the value.
	ClassOut Class = "out"
	// ClassInBuffer is a `const T *` with a length parameter: a Go slice.
	ClassInBuffer Class = "in_buffer"
	// ClassOutBuffer is the `T *` of an output triple
	// `T *buf, U capacity, U *count`: a caller slice.
	ClassOutBuffer Class = "out_buffer"
	// ClassOutCount is the `U *count` of an output triple: Go returns it.
	ClassOutCount Class = "out_count"
	// ClassRaw is a position no rule or exception classifies, or that two
	// rules classify. Its function is raw-only.
	ClassRaw Class = "raw"

	// ClassCallback is a function parameter of a bound callback typedef:
	// a Go func.
	ClassCallback Class = "callback"
	// ClassKey is a key: in a function, the `void *` that carries a bound
	// callback's token (the binding makes it; Go does not see it); in a
	// callback, a parameter that identifies the registration (the registry
	// consumes it; the Go func does not see it).
	ClassKey Class = "key"
	// ClassScalar is a scalar parameter of a callback: passed through.
	ClassScalar Class = "scalar"
	// ClassLength is the length parameter of a callback's in_buffer.
	ClassLength Class = "length"
	// ClassOutArray is a callback's `T *` that an exception gives a length
	// N: a `[N]T` result of the Go func that the trampoline writes back.
	ClassOutArray Class = "out_array"
)

// The rules. Each is a predicate on one pointer position and its
// neighbors; the rules do not consult each other. A position that exactly
// one rule matches gets the rule's class; zero or two matches make it raw.
const (
	ruleHandle    = "handle"     // `H *` or `const H *`, H a handle type
	ruleCreate    = "create"     // the `H **` out parameter a handle's create function names
	ruleOut       = "out"        // a lone non-const pointer to a value type, named with the out prefix
	ruleValue     = "value"      // a lone `const S *` (S a struct without pointers) or `const T name[N]`
	ruleInBuffer  = "in_buffer"  // `const T *p` and next an unsigned length named by a length_names template
	ruleOutBuffer = "out_buffer" // `T *p`, next an unsigned capacity name, then `U *` with a count name
	ruleOutCount  = "out_count"  // `U *` with a count name, after a capacity after a `T *`
	// ruleInBufferBytes is ruleInBuffer for a byte_length_names template.
	// The two rules give one class with different length units, so a
	// length that templates of both lists give makes two matches: the
	// position is raw, whatever the order of the lists.
	ruleInBufferBytes = "in_buffer_bytes"
	// ruleCallback: a parameter of a bound callback typedef (callback.go)
	// whose key has its source in the function and, when registered, whose
	// owner the function takes.
	ruleCallback = "callback"
	// ruleToken: a named `void *` that is the key source of a parameter of
	// the function whose callback typedef has a token key.
	ruleToken = "token"
)

// Position is the class of one pointer position of a function.
type Position struct {
	// Param is the parameter index, or -1 for the result.
	Param int
	Class Class
	// By is the rule that gave the class, or "exception".
	By string
	// Length is the length parameter of an in_buffer and the capacity
	// parameter of an out_buffer and an out_count, and the buffer of a
	// callback's length; else -1.
	Length int
	// Count is the count parameter of an out_buffer, and the buffer
	// parameter of an out_count; else -1.
	Count int
	// Bytes is true when the length of an in_buffer counts bytes.
	Bytes bool
	// Array is the element count of an out_array; else 0.
	Array int64
	// Reason says why a position is raw.
	Reason string
}

// FieldPosition is the class of one pointer field: a struct field whose
// type is a data pointer, a callback, or an array of those. A field that
// holds a struct by value is not a position itself; the fields of that
// struct are.
type FieldPosition struct {
	// Struct is the struct typedef, Field the field index, Name its name.
	Struct string
	Field  int
	Name   string
	Class  Class
	// Reason says why the field is raw.
	Reason string
}

// classifyFields gives every pointer field of every struct of the model
// its class, in model order, used by a function or not. No rule covers a
// field: the typed layer passes no struct that holds a pointer, so every
// pointer field is raw.
func (k *classifier) classifyFields() []FieldPosition {
	var out []FieldPosition
	for _, d := range k.m.Types {
		if d.Kind != DeclStruct {
			continue
		}
		for i, f := range d.Fields {
			what := k.fieldPointer(f.Type)
			if what == "" {
				continue
			}
			reason := what + " (a struct that holds a pointer reaches C only through the raw layer: Go memory that holds Go pointers must not reach C)"
			out = append(out, FieldPosition{Struct: d.Name, Field: i, Name: f.Name, Class: ClassRaw, Reason: reason})
		}
	}
	return out
}

// fieldPointer names the kind of pointer a field of type t is ("pointer
// field", "callback field", "array of pointers", "array of callbacks"), or
// "" when t is not a pointer position.
func (k *classifier) fieldPointer(t Type) string {
	array := false
	for t.Kind == TypeArray {
		array, t = true, *t.Elem
	}
	if !k.isPointerPos(t) {
		return ""
	}
	what := "pointer"
	if t.Kind == TypeNamed {
		what = "callback"
	}
	if array {
		return "array of " + what + "s"
	}
	return what + " field"
}

// classifier holds the facts of one model and contract that the rules use.
type classifier struct {
	m       *Model
	c       *Contract
	types   map[string]*TypeDecl
	handles map[string]*HandleRule
	creates map[string]*HandleRule
	exc     map[string]map[int]*Exception
	// entries are the callbacks entries by typedef, cbExc the exceptions
	// of callback positions, and cbs every callback typedef of the model
	// (callback.go).
	entries map[string]*CallbackRule
	cbExc   map[string]map[int]*Exception
	cbs     map[string]*BoundCallback
}

func newClassifier(m *Model, c *Contract) *classifier {
	k := &classifier{m: m, c: c, types: map[string]*TypeDecl{}, handles: map[string]*HandleRule{},
		creates: map[string]*HandleRule{}, exc: map[string]map[int]*Exception{},
		entries: map[string]*CallbackRule{}, cbExc: map[string]map[int]*Exception{}, cbs: map[string]*BoundCallback{}}
	for i := range m.Types {
		k.types[m.Types[i].Name] = &m.Types[i]
	}
	for i := range c.Handles {
		h := &c.Handles[i]
		k.handles[h.Type] = h
		k.creates[h.Create] = h
	}
	return k
}

// isPointerPos reports whether t is a pointer position: a data pointer or a
// callback type.
func (k *classifier) isPointerPos(t Type) bool {
	if t.Kind == TypePointer {
		return true
	}
	if t.Kind == TypeNamed {
		d := k.types[t.Name]
		return d != nil && d.Kind == DeclCallback
	}
	return false
}

// scalarOf resolves a scalar or a scalar typedef.
func (k *classifier) scalarOf(t Type) (Scalar, bool) {
	switch t.Kind {
	case TypeScalar:
		return t.Scalar, true
	case TypeNamed:
		if d := k.types[t.Name]; d != nil && d.Kind == DeclScalar {
			return d.Scalar, true
		}
	}
	return "", false
}

func isUnsignedScalar(s Scalar) bool {
	return s == Uint8 || s == Uint16 || s == Uint32 || s == Uint64
}

// unsigned reports whether t is an unsigned integer, maybe through a typedef.
func (k *classifier) unsigned(t Type) bool {
	s, ok := k.scalarOf(t)
	return ok && isUnsignedScalar(s)
}

// pointerFree reports whether a struct holds no data pointer and no
// callback, directly or in a nested struct or array.
func (k *classifier) pointerFree(name string) bool {
	d := k.types[name]
	if d == nil || d.Kind != DeclStruct {
		return false
	}
	for _, f := range d.Fields {
		if !k.storable(f.Type) {
			return false
		}
	}
	return true
}

// storable reports whether a value of t holds no pointer: a scalar, a
// scalar typedef, a struct without pointers, or an array of those.
func (k *classifier) storable(t Type) bool {
	switch t.Kind {
	case TypeScalar:
		return true
	case TypeArray:
		return k.storable(*t.Elem)
	case TypeNamed:
		d := k.types[t.Name]
		if d == nil {
			return false
		}
		switch d.Kind {
		case DeclScalar:
			return true
		case DeclStruct:
			return k.pointerFree(t.Name)
		}
	}
	return false
}

// element reports whether t is a type that a buffer can hold or an
// out-parameter can receive: a scalar, a scalar typedef or a struct
// without pointers. Arrays, void, handles and callbacks are not.
func (k *classifier) element(t Type) bool {
	return t.Kind != TypeArray && t.Kind != TypeVoid && k.storable(t)
}

// holdsPointer names the struct with a pointer that a pointer to t reaches,
// or "".
func (k *classifier) holdsPointer(t Type) string {
	if t.Kind == TypeNamed {
		if d := k.types[t.Name]; d != nil && d.Kind == DeclStruct && !k.pointerFree(t.Name) {
			return t.Name
		}
	}
	return ""
}

func (k *classifier) isHandleType(t Type) bool {
	return t.Kind == TypeNamed && k.handles[t.Name] != nil
}

// lengthUnits reports whether q, the parameter after the buffer p, is its
// length by a length_names template (elements) and by a byte_length_names
// template (bytes). Both can be true; the caller decides nothing by the
// order of the lists.
func (k *classifier) lengthUnits(p, q Param) (elements, bytes bool) {
	if !k.unsigned(q.Type) || q.Name == "" {
		return false, false
	}
	gives := func(templs []string) bool {
		for _, templ := range templs {
			if n, ok := expandTemplate(templ, p.Name); ok && n == q.Name {
				return true
			}
		}
		return false
	}
	return gives(k.c.Buffers.LengthNames), gives(k.c.Buffers.ByteLengthNames)
}

func (k *classifier) capacity(q Param) bool {
	return q.Name != "" && k.unsigned(q.Type) && slices.Contains(k.c.Buffers.CapacityNames, q.Name)
}

// lone reports whether parameter i is one value and not part of a buffer:
// the next parameter is not an unsigned integer with a capacity name or a
// name that some length template gives for some buffer name ({name} and
// {stem} match any name, so `const S *, uint32_t chunk_count` is not lone
// even unnamed), and the previous one has no capacity name.
func (k *classifier) lone(f *Function, i int) bool {
	if i+1 < len(f.Params) {
		q := f.Params[i+1]
		if k.capacity(q) || k.unsigned(q.Type) && k.lengthLike(q.Name) {
			return false
		}
	}
	return i == 0 || !k.capacity(f.Params[i-1])
}

// lengthLike reports whether name is what a length template gives for some
// buffer name.
func (k *classifier) lengthLike(name string) bool {
	if name == "" {
		return false
	}
	for _, l := range [][]string{k.c.Buffers.LengthNames, k.c.Buffers.ByteLengthNames} {
		for _, templ := range l {
			if templateMatches(templ, name) {
				return true
			}
		}
	}
	return false
}

// templateMatches reports whether name is templ with each placeholder
// replaced by a nonempty C name.
func templateMatches(templ, name string) bool {
	i := strings.IndexByte(templ, '{')
	if i < 0 {
		return templ == name
	}
	j := strings.IndexByte(templ[i:], '}') + i
	head, tail := templ[:i], templ[j+1:]
	if !strings.HasPrefix(name, head) {
		return false
	}
	rest := name[len(head):]
	for n := 1; n <= len(rest); n++ {
		if isCIdent("x"+rest[:n]) && templateMatches(tail, rest[n:]) {
			return true
		}
	}
	return false
}

func (k *classifier) nonConstPtr(t Type) bool { return t.Kind == TypePointer && !t.Elem.Const }
func (k *classifier) constPtr(t Type) bool    { return t.Kind == TypePointer && t.Elem.Const }

// countOf reports whether p is a `U *` count with a count name for the
// unsigned capacity c.
func (k *classifier) countOf(c, p Param) bool {
	return k.nonConstPtr(p.Type) && p.Type.LenExpr == "" && p.Name != "" &&
		slices.Contains(k.c.Buffers.CountNames, p.Name) && k.unsigned(*p.Type.Elem) &&
		sameScalar(k, *p.Type.Elem, c.Type)
}

func sameScalar(k *classifier, a, b Type) bool {
	x, ok1 := k.scalarOf(a)
	y, ok2 := k.scalarOf(b)
	return ok1 && ok2 && x == y
}

// rules returns the names of the rules that match parameter i of f.
func (k *classifier) rules(f *Function, i int) []string {
	p := f.Params[i]
	t := p.Type
	var out []string
	if t.Kind == TypeNamed && k.isPointerPos(t) {
		if k.callbackParam(f, i) == "" {
			out = append(out, ruleCallback)
		}
		return out
	}
	if t.Kind != TypePointer {
		return nil
	}
	if k.tokenOf(f, i) >= 0 {
		out = append(out, ruleToken)
	}
	elem := *t.Elem
	ptr := t.LenExpr == "" // not a `T name[N]` parameter
	if ptr && k.isHandleType(elem) {
		out = append(out, ruleHandle)
	}
	if h := k.creates[f.Name]; h != nil && ptr && p.Name == h.Out && !elem.Const && elem.Kind == TypePointer &&
		elem.Elem.Kind == TypeNamed && elem.Elem.Name == h.Type && !elem.Elem.Const {
		out = append(out, ruleCreate)
	}
	if ptr && !elem.Const && p.Name != "" && strings.HasPrefix(p.Name, k.c.Buffers.OutPrefix) &&
		k.element(elem) && k.lone(f, i) {
		out = append(out, ruleOut)
	}
	if elem.Const && k.lone(f, i) && (ptr && elem.Kind == TypeNamed && k.types[elem.Name] != nil &&
		k.types[elem.Name].Kind == DeclStruct && k.pointerFree(elem.Name) || !ptr && k.element(elem)) {
		out = append(out, ruleValue)
	}
	if ptr && elem.Const && k.element(elem) && i+1 < len(f.Params) {
		elements, bytes := k.lengthUnits(p, f.Params[i+1])
		if elements {
			out = append(out, ruleInBuffer)
		}
		if bytes {
			out = append(out, ruleInBufferBytes)
		}
	}
	if ptr && !elem.Const && k.element(elem) && i+2 < len(f.Params) && k.capacity(f.Params[i+1]) &&
		k.countOf(f.Params[i+1], f.Params[i+2]) {
		out = append(out, ruleOutBuffer)
	}
	if i >= 2 && k.countOf(f.Params[i-1], p) && k.capacity(f.Params[i-1]) {
		b := f.Params[i-2].Type
		if k.nonConstPtr(b) && b.LenExpr == "" && k.element(*b.Elem) {
			out = append(out, ruleOutCount)
		}
	}
	return out
}

// ruleClass maps a rule to its class.
var ruleClass = map[string]Class{
	ruleHandle: ClassHandle, ruleCreate: ClassOut, ruleOut: ClassOut, ruleValue: ClassValue,
	ruleInBuffer: ClassInBuffer, ruleInBufferBytes: ClassInBuffer, ruleOutBuffer: ClassOutBuffer,
	ruleOutCount: ClassOutCount, ruleCallback: ClassCallback, ruleToken: ClassKey,
	ruleCbKey: ClassKey, ruleCbScalar: ClassScalar, ruleCbLength: ClassLength,
}

// paramRef resolves a position reference: a parameter name or "#N".
func paramRef(f *Function, ref string) (int, bool) {
	if n, ok := strings.CutPrefix(ref, "#"); ok {
		i, err := strconv.Atoi(n)
		if err != nil || i < 0 || i >= len(f.Params) || strconv.Itoa(i) != n {
			return 0, false
		}
		return i, true
	}
	for i, p := range f.Params {
		if p.Name != "" && p.Name == ref {
			return i, true
		}
	}
	return 0, false
}

// classify gives every pointer position of f its class, and returns the
// reason f is raw-only, or "" when f is typed.
func (k *classifier) classify(f *Function) ([]Position, string) {
	var pos []Position
	byParam := map[int]int{} // param index -> index in pos
	exc := k.exc[f.Name]
	for i, p := range f.Params {
		if !k.isPointerPos(p.Type) {
			continue
		}
		byParam[i] = len(pos)
		if e := exc[i]; e != nil {
			pos = append(pos, k.fromException(f, i, e))
			continue
		}
		pos = append(pos, k.fromRules(f, i))
	}
	// An exception's out_buffer gives its count the class out_count.
	for _, q := range pos {
		if q.Class == ClassOutBuffer && q.By == "exception" {
			j := byParam[q.Count]
			pos[j] = Position{Param: q.Count, Class: ClassOutCount, By: "exception", Length: q.Length, Count: q.Param}
		}
	}
	if k.isPointerPos(f.Result) {
		reason := "pointer result"
		if f.Result.Kind == TypeNamed {
			reason = "callback result (the typed layer does not call a C function pointer that C returns)"
		}
		pos = append(pos, Position{Param: -1, Class: ClassRaw, Length: -1, Count: -1, Reason: reason})
	}
	var raw []string
	for _, q := range pos {
		if q.Class == ClassRaw {
			raw = append(raw, positionName(f, q.Param)+": "+q.Reason)
		}
	}
	if len(raw) > 0 {
		return pos, strings.Join(raw, "; ")
	}
	return pos, k.coherent(f, pos, byParam)
}

// fromRules classifies a pointer parameter by the rules.
func (k *classifier) fromRules(f *Function, i int) Position {
	q := ruledPosition(f, i, k.rules(f, i))
	if q.Class == ClassRaw && q.Reason == "" {
		q.Reason = k.noRuleReason(f, i)
	}
	return q
}

// ruledPosition gives parameter i of f the class of the one rule in rs.
// Zero rules make it raw with no reason (the caller gives it); two make
// it raw and name them.
func ruledPosition(f *Function, i int, rs []string) Position {
	q := Position{Param: i, Length: -1, Count: -1}
	switch len(rs) {
	case 0:
		q.Class = ClassRaw
		return q
	case 1:
	default:
		notes := make([]string, len(rs))
		for j, r := range rs {
			notes[j] = r
			switch r {
			case ruleInBuffer:
				notes[j] += " (" + f.Params[i+1].Name + " counts elements)"
			case ruleInBufferBytes:
				notes[j] += " (" + f.Params[i+1].Name + " counts bytes)"
			}
		}
		q.Class = ClassRaw
		q.Reason = "rules " + strings.Join(notes, " and ") + " both cover it"
		return q
	}
	q.Class, q.By = ruleClass[rs[0]], rs[0]
	switch rs[0] {
	case ruleInBuffer, ruleInBufferBytes:
		q.Length = i + 1
		q.Bytes = rs[0] == ruleInBufferBytes
	case ruleOutBuffer:
		q.Length, q.Count = i+1, i+2
	case ruleOutCount:
		q.Length, q.Count = i-1, i-2
	case ruleCbLength:
		q.Length = i - 1
	}
	return q
}

// noRuleReason says why no rule covers parameter i of f.
func (k *classifier) noRuleReason(f *Function, i int) string {
	t := f.Params[i].Type
	if t.Kind == TypeNamed {
		return "callback parameter: " + k.callbackParam(f, i)
	}
	if s := k.holdsPointer(*t.Elem); s != "" {
		return s + " holds a pointer (Go memory that holds Go pointers must not reach C)"
	}
	return "no rule covers it"
}

// fromException classifies a parameter by its exception. Bind has checked
// that the exception fits the parameter.
func (k *classifier) fromException(f *Function, i int, e *Exception) Position {
	q := Position{Param: i, Class: e.Class, By: "exception", Length: -1, Count: -1}
	switch e.Class {
	case ClassRaw:
		q.Reason = "exception: " + e.Why
	case ClassInBuffer:
		q.Length, _ = paramRef(f, e.Length)
		q.Bytes = e.Unit == "bytes"
	case ClassOutBuffer:
		q.Length, _ = paramRef(f, e.Length)
		q.Count, _ = paramRef(f, e.Count)
	}
	return q
}

// checkException reports why exception e does not fit parameter i of f, or "".
func (k *classifier) checkException(f *Function, i int, e *Exception) string {
	t := f.Params[i].Type
	if !k.isPointerPos(t) {
		return "the parameter is not a pointer"
	}
	isPtr := t.Kind == TypePointer
	ref := func(name, what string) (Param, int, string) {
		j, ok := paramRef(f, name)
		switch {
		case !ok:
			return Param{}, 0, fmt.Sprintf("%s %q is not a parameter", what, name)
		case j == i:
			return Param{}, 0, what + " is the buffer itself"
		case !k.unsigned(f.Params[j].Type):
			return Param{}, 0, fmt.Sprintf("%s %q is not an unsigned integer", what, name)
		}
		return f.Params[j], j, ""
	}
	switch e.Class {
	case ClassRaw:
		return ""
	case ClassValue:
		if isPtr && k.constPtr(t) && (t.LenExpr != "" && k.element(*t.Elem) ||
			t.LenExpr == "" && t.Elem.Kind == TypeNamed && k.types[t.Elem.Name] != nil &&
				k.types[t.Elem.Name].Kind == DeclStruct && k.pointerFree(t.Elem.Name)) {
			return ""
		}
		return "a value is a `const S *` (S a struct without pointers) or a `const T name[N]`"
	case ClassOut:
		if isPtr && k.nonConstPtr(t) && t.LenExpr == "" && k.element(*t.Elem) {
			return ""
		}
		return "an out-parameter is a `T *` to a scalar or a struct without pointers"
	case ClassInBuffer:
		if !(isPtr && k.constPtr(t) && t.LenExpr == "" && k.element(*t.Elem)) {
			return "an in_buffer is a `const T *` to a scalar or a struct without pointers"
		}
		_, _, msg := ref(e.Length, "length")
		return msg
	case ClassOutBuffer:
		if !(isPtr && k.nonConstPtr(t) && t.LenExpr == "" && k.element(*t.Elem)) {
			return "an out_buffer is a `T *` to a scalar or a struct without pointers"
		}
		c, _, msg := ref(e.Length, "capacity")
		if msg != "" {
			return msg
		}
		j, ok := paramRef(f, e.Count)
		if !ok || j == i || !k.nonConstPtr(f.Params[j].Type) || f.Params[j].Type.LenExpr != "" ||
			!k.unsigned(*f.Params[j].Type.Elem) || !sameScalar(k, *f.Params[j].Type.Elem, c.Type) {
			return fmt.Sprintf("count %q is not a pointer to the capacity's type", e.Count)
		}
		if k.exc[f.Name][j] != nil {
			return fmt.Sprintf("count %q has its own exception", e.Count)
		}
		return ""
	}
	return "unknown class"
}

// coherent checks that the buffers of a function fit together: each length
// or capacity parameter serves one buffer, each out_count belongs to the
// out_buffer that names it, and no by-value parameter or result holds a
// pointer. It returns the reason f is raw-only, or "".
func (k *classifier) coherent(f *Function, pos []Position, byParam map[int]int) string {
	used := map[int]int{}       // length or capacity param -> buffer param
	tokens := map[int]int{}     // token key param -> callback param
	handles := map[string]int{} // handle-keyed callback typedef -> callback param
	for _, q := range pos {
		if q.Class != ClassCallback {
			continue
		}
		if j := k.tokenSource(f, q.Param); j >= 0 {
			if c, ok := tokens[j]; ok {
				return fmt.Sprintf("%s: its key %s also serves %s", positionName(f, q.Param), positionName(f, j), positionName(f, c))
			}
			tokens[j] = q.Param
			continue
		}
		// A handle key: one typedef's registry holds one Go function per
		// key, so two positions of one typedef would share one entry.
		bc := k.cbs[f.Params[q.Param].Type.Name]
		if c, ok := handles[bc.Decl.Name]; ok {
			j, _ := k.keySource(bc.Decl, bc.Rule, f)
			return fmt.Sprintf("%s: its handle key %s also serves %s, so one registry entry would serve both",
				positionName(f, q.Param), positionName(f, j), positionName(f, c))
		}
		handles[bc.Decl.Name] = q.Param
	}
	for _, q := range pos {
		if _, ok := tokens[q.Param]; q.Class == ClassKey && !ok {
			return positionName(f, q.Param) + ": a key without its callback"
		}
	}
	for _, q := range pos {
		switch q.Class {
		case ClassInBuffer, ClassOutBuffer:
			if b, ok := used[q.Length]; ok {
				return fmt.Sprintf("%s: its length %s also serves %s", positionName(f, q.Param),
					positionName(f, q.Length), positionName(f, b))
			}
			used[q.Length] = q.Param
		}
		switch q.Class {
		case ClassOutBuffer:
			j, ok := byParam[q.Count]
			if !ok || pos[j].Class != ClassOutCount || pos[j].Count != q.Param {
				return positionName(f, q.Param) + ": its count is not its out_count"
			}
		case ClassOutCount:
			j, ok := byParam[q.Count]
			if !ok || pos[j].Class != ClassOutBuffer || pos[j].Count != q.Param {
				return positionName(f, q.Param) + ": an out_count without its out_buffer"
			}
		}
	}
	for i, p := range f.Params {
		if _, isPos := byParam[i]; isPos {
			continue
		}
		if _, isLen := used[i]; isLen {
			continue
		}
		if !k.storable(p.Type) {
			return positionName(f, i) + ": a struct by value that holds a pointer"
		}
	}
	if f.Result.Kind != TypeVoid && !k.isPointerPos(f.Result) && !k.storable(f.Result) {
		return "result: a struct by value that holds a pointer"
	}
	return ""
}

// positionName names a position for the report: "param 2 (node)",
// "param 0 (#0)", "result".
func positionName(f *Function, i int) string {
	if i < 0 {
		return "result"
	}
	n := f.Params[i].Name
	if n == "" {
		n = "#" + strconv.Itoa(i)
	}
	return fmt.Sprintf("param %d (%s)", i, n)
}
