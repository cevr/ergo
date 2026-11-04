package bindc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Binding is a model bound by its contract: the class of every pointer
// position, the typed or raw-only state of every function, and the Go name
// of every declaration. The emitters read only the binding.
type Binding struct {
	Model    *Model
	Contract *Contract
	// HeaderSHA256 and ContractSHA256 are the sha256 of the two inputs.
	HeaderSHA256, ContractSHA256 string
	// Funcs are the functions that are not skipped, in model order.
	Funcs []*BoundFunc
	// Fields are the pointer fields of every struct, used or not, in
	// model order. With the positions of Funcs they are every pointer
	// position of the binding.
	Fields []FieldPosition
	// Callbacks are the callback typedefs of the header, in model order,
	// bound or not (callback.go).
	Callbacks []*BoundCallback
	// Codes are the status codes other than success, in model order.
	Codes []*Constant
	// Success is the success code.
	Success *Constant
	// ABIWant is the value of the ABI macro.
	ABIWant uint64
	// Constants are the constants that are not skipped, in model order.
	Constants []*Constant
	// GoTypes maps every typedef name to its Go name.
	GoTypes map[string]string
	// Autofill maps a field name to "size" or "abi".
	Autofill map[string]string
	// FieldNames maps a struct to the Go names of its fields, in order:
	// CamelCase, or for an autofill field an unexported name off the
	// generated methods (names.go).
	FieldNames map[string][]string

	k   *classifier
	pkg *nameScope // the package-level Go names (names.go)
	// records are the C structs a typed signature reaches by value: the
	// Ergo text declares a record for each.
	records map[string]bool
}

// BoundFunc is one function of the binding.
type BoundFunc struct {
	C *Function
	// GoName is the exported name of its typed function, and the base of
	// the name of its raw function.
	GoName string
	// Positions are its pointer positions in parameter order, then the
	// result.
	Positions []Position
	// RawOnly is the reason the function has no typed form, or "".
	RawOnly string
	// Sig is the Go signature of the typed form (nil when raw-only).
	Sig *GoSig
	// ErgoParams are the Ergo names of Sig.Params, in order (names.go).
	ErgoParams []string
	// Destroys is the handle the function destroys, or nil.
	Destroys *HandleRule
}

// callbacks names the callback typedefs of the callback positions of a
// typed function, in parameter order.
func (f *BoundFunc) callbacks() []string {
	var out []string
	for _, q := range f.Positions {
		if q.Class == ClassCallback {
			out = append(out, f.C.Params[q.Param].Type.Name)
		}
	}
	return out
}

// goTyped reports whether GoPackage emits the typed form of f: a typed
// function without a callback position. The trampolines and the registry
// that a callback position needs are slice 2b of bind-c-2.
func (f *BoundFunc) goTyped() bool { return f.Sig != nil && len(f.callbacks()) == 0 }

// ErgoUnbound says why the Ergo text has no extern for f, or "". A
// raw-only function has its reason; a typed function that takes a callback
// is not bound because callbacks into Ergo are bind-c-7.
func (f *BoundFunc) ErgoUnbound() string {
	if f.Sig == nil {
		return f.RawOnly
	}
	if cbs := f.callbacks(); len(cbs) > 0 {
		return "it takes a callback (" + strings.Join(cbs, ", ") + "): callbacks into Ergo are bind-c-7"
	}
	return ""
}

// Bind checks a contract against its header and model, and classifies
// every pointer position. header is the header's bytes (for its sha256),
// contract the contract's bytes.
func Bind(m *Model, header, contract []byte) (*Binding, error) {
	c, err := DecodeContract(contract)
	if err != nil {
		return nil, err
	}
	hs, cs := sha256.Sum256(header), sha256.Sum256(contract)
	b := &Binding{Model: m, Contract: c, HeaderSHA256: hex.EncodeToString(hs[:]),
		ContractSHA256: hex.EncodeToString(cs[:]), GoTypes: map[string]string{}, Autofill: map[string]string{}}
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if c.Header != m.Header {
		bad("header is %q, but the model is of %q", c.Header, m.Header)
	}
	if c.HeaderSHA256 != b.HeaderSHA256 {
		bad("header_sha256 is %s, but the header's sha256 is %s", c.HeaderSHA256, b.HeaderSHA256)
	}
	b.k = newClassifier(m, c)
	k := b.k
	funcs := map[string]*Function{}
	for i := range m.Functions {
		funcs[m.Functions[i].Name] = &m.Functions[i]
	}
	consts := map[string]*Constant{}
	for i := range m.Constants {
		consts[m.Constants[i].Name] = &m.Constants[i]
	}

	// Skips: functions and constants, each once.
	skipped := map[string]bool{}
	for _, s := range c.Skips {
		switch {
		case skipped[s.Name]:
			bad("skips: %s appears twice", s.Name)
		case funcs[s.Name] == nil && consts[s.Name] == nil:
			bad("skips: %s is not a function or a constant of the header", s.Name)
		}
		skipped[s.Name] = true
	}
	need := func(what, name string, ok bool) {
		if !ok {
			bad("%s %s is not in the header", what, name)
		} else if skipped[name] {
			bad("%s %s is skipped", what, name)
		}
	}

	// Status: the typedef, the codes, the success code.
	st := k.types[c.Status.Type]
	if st == nil || st.Kind != DeclScalar || !slices.Contains([]Scalar{Int8, Int16, Int32, Int64, Uint8, Uint16, Uint32, Uint64}, st.Scalar) {
		bad("status.type %s is not an integer typedef of the header", c.Status.Type)
	} else {
		excluded := map[string]bool{}
		for _, e := range c.Status.Exclude {
			x := consts[e.Name]
			switch {
			case excluded[e.Name]:
				bad("status.exclude: %s appears twice", e.Name)
			case x == nil || !strings.HasPrefix(e.Name, c.Status.MacroPrefix) || x.Type != statusConstType(st.Scalar):
				bad("status.exclude: %s is not a constant with the status prefix and type", e.Name)
			}
			excluded[e.Name] = true
		}
		values := map[uint64]string{}
		for i := range m.Constants {
			x := &m.Constants[i]
			if !strings.HasPrefix(x.Name, c.Status.MacroPrefix) || x.Type != statusConstType(st.Scalar) || excluded[x.Name] || skipped[x.Name] {
				continue
			}
			if !fitsScalar(x, st.Scalar) {
				bad("status code %s = %s does not fit %s", x.Name, x.Value(), st.Scalar)
				continue
			}
			if prev, ok := values[x.Bits]; ok {
				bad("status codes %s and %s have the value %s; exclude one", prev, x.Name, x.Value())
				continue
			}
			values[x.Bits] = x.Name
			if x.Name == c.Status.Success {
				b.Success = x
			} else {
				b.Codes = append(b.Codes, x)
			}
		}
		if b.Success == nil {
			bad("status.success %s is not a status code of the header", c.Status.Success)
		}
	}

	// ABI: `uintN_t f(void)` and a constant that fits its result.
	if f := funcs[c.ABI.Function]; f == nil || skipped[f.Name] {
		need("abi.function", c.ABI.Function, f != nil)
	} else if s, ok := k.scalarOf(f.Result); len(f.Params) != 0 || !ok || !isUnsignedScalar(s) {
		bad("abi.function %s is not an unsigned integer function of no parameters", f.Name)
	} else if x := consts[c.ABI.Macro]; x == nil || skipped[x.Name] {
		need("abi.macro", c.ABI.Macro, x != nil)
	} else if x.Signed() && x.Int64() < 0 || !fitsScalar(x, s) {
		bad("abi.macro %s = %s does not fit the result of %s", x.Name, x.Value(), f.Name)
	} else {
		b.ABIWant = x.Bits
	}

	// Handles.
	seenHandle := map[string]bool{}
	for _, h := range c.Handles {
		if seenHandle[h.Type] {
			bad("handles: %s appears twice", h.Type)
		}
		seenHandle[h.Type] = true
		if d := k.types[h.Type]; d == nil || d.Kind != DeclOpaque {
			bad("handles: %s is not an opaque type of the header", h.Type)
			continue
		}
		cr := funcs[h.Create]
		need("handles: create", h.Create, cr != nil)
		if cr != nil {
			j, ok := paramRef(cr, h.Out)
			if !ok || cr.Params[j].Name != h.Out {
				bad("handles: %s has no parameter %s", h.Create, h.Out)
			} else if t := cr.Params[j].Type; t.Kind != TypePointer || t.LenExpr != "" || t.Elem.Const || t.Elem.Kind != TypePointer ||
				t.Elem.Elem.Kind != TypeNamed || t.Elem.Elem.Name != h.Type || t.Elem.Elem.Const {
				bad("handles: %s %s is not a `%s **`", h.Create, h.Out, h.Type)
			}
		}
		de := funcs[h.Destroy]
		need("handles: destroy", h.Destroy, de != nil)
		if de != nil {
			if len(de.Params) != 1 || !k.isHandleType(*elemOr(de.Params[0].Type)) || de.Params[0].Type.Elem.Name != h.Type || de.Params[0].Type.Elem.Const {
				bad("handles: %s does not take one `%s *`", h.Destroy, h.Type)
			}
		}
		if cr != nil && de != nil && cr == de {
			bad("handles: %s creates and destroys", h.Create)
		}
	}

	// Autofill: each field exists, as an unsigned integer that holds the value.
	for _, a := range c.Autofill {
		if b.Autofill[a.Field] != "" {
			bad("autofill: %s appears twice", a.Field)
		}
		b.Autofill[a.Field] = a.Value
		found := false
		for _, d := range m.Types {
			for _, f := range d.Fields {
				if f.Name != a.Field {
					continue
				}
				found = true
				s, ok := k.scalarOf(f.Type)
				if !ok || !isUnsignedScalar(s) || a.Value == "size" && scalarSize[s] < 4 ||
					a.Value == "abi" && b.ABIWant>>(8*scalarSize[s]-1)>>1 != 0 {
					bad("autofill: %s.%s is not an unsigned integer that holds the %s", d.Name, f.Name, a.Value)
				}
			}
		}
		if !found {
			bad("autofill: no struct has a field %s", a.Field)
		}
	}

	// Callbacks: each entry fits its typedef (callback.go).
	k.checkCallbacks(funcs, skipped, bad)

	// Exceptions: each fits its position, one per position.
	k.checkCallbackExceptions(bad)
	for i := range c.Exceptions {
		e := &c.Exceptions[i]
		if e.Callback != "" {
			continue
		}
		f := funcs[e.Function]
		if f == nil || skipped[e.Function] {
			need("exceptions: function", e.Function, f != nil)
			continue
		}
		j, ok := paramRef(f, e.Position)
		if !ok {
			bad("exceptions: %s has no parameter %s", e.Function, e.Position)
			continue
		}
		if f.Params[j].Name != "" && strings.HasPrefix(e.Position, "#") {
			bad("exceptions: %s %s has the name %s; use it", e.Function, e.Position, f.Params[j].Name)
			continue
		}
		if k.exc[f.Name] == nil {
			k.exc[f.Name] = map[int]*Exception{}
		}
		if k.exc[f.Name][j] != nil {
			bad("exceptions: %s %s has two exceptions", e.Function, e.Position)
			continue
		}
		k.exc[f.Name][j] = e
	}
	for i := range c.Exceptions {
		e := &c.Exceptions[i]
		if e.Callback != "" {
			continue
		}
		f := funcs[e.Function]
		j, ok := 0, false
		if f != nil {
			j, ok = paramRef(f, e.Position)
		}
		if !ok || k.exc[f.Name][j] != e {
			continue
		}
		if msg := k.checkException(f, j, e); msg != "" {
			bad("exceptions: %s %s: %s", e.Function, e.Position, msg)
			continue
		}
		if r := k.fromRules(f, j); r.Class == e.Class && r.Class != ClassRaw && r.Length == k.fromException(f, j, e).Length &&
			r.Count == k.fromException(f, j, e).Count && r.Bytes == k.fromException(f, j, e).Bytes {
			bad("exceptions: %s %s repeats the rule %s; remove it", e.Function, e.Position, r.By)
		}
	}
	if len(errs) > 0 {
		return nil, contractError(errs)
	}

	// Classes and Go names.
	for i := range m.Constants {
		if !skipped[m.Constants[i].Name] {
			b.Constants = append(b.Constants, &m.Constants[i])
		}
	}
	for _, d := range m.Types {
		if d.Name == c.Status.Type {
			b.GoTypes[d.Name] = "Status"
		} else {
			b.GoTypes[d.Name] = goCamel(strings.TrimPrefix(d.Name, c.Prefixes.Type))
		}
	}
	k.buildCallbacks(b)
	var planErrs []string // calls purego cannot make as C does (abi.go)
	destroys := map[string]*HandleRule{}
	for i := range c.Handles {
		destroys[c.Handles[i].Destroy] = &c.Handles[i]
	}
	for i := range m.Functions {
		f := &m.Functions[i]
		if skipped[f.Name] {
			continue
		}
		bf := &BoundFunc{C: f, GoName: goCamel(strings.TrimPrefix(f.Name, c.Prefixes.Function)), Destroys: destroys[f.Name]}
		bf.Positions, bf.RawOnly = k.classify(f)
		// Every bound function has a raw function that Load registers, so
		// its call plan is checked whether or not it is typed.
		if r := b.callPlan(f); r != "" {
			planErrs = append(planErrs, fmt.Sprintf("function %s: %s", f.Name, r))
		}
		b.Funcs = append(b.Funcs, bf)
	}
	if len(planErrs) > 0 {
		return nil, contractError(planErrs)
	}
	b.Fields = k.classifyFields()
	if err := b.checkNames(); err != nil {
		return nil, err
	}
	for _, bf := range b.Funcs {
		if bf.RawOnly == "" {
			bf.Sig = b.goSig(bf)
		}
	}
	if err := b.ergoNames(); err != nil {
		return nil, err
	}
	return b, nil
}

// ergoNames gives the names of the Ergo text (names.go). It claims the
// package-level names, the status variants, the records and the externs,
// in one scope with the import and the generated unions, and refuses a
// name that is taken there or that Ergo reserves. It allocates the
// parameters of each extern in a scope of its own.
func (b *Binding) ergoNames() error {
	pkg, errs := ergoPackageScope(b.Contract.Go.ImportPath)
	claim := func(n, source string) {
		if !isGoIdent(n) || !isExported(n) {
			errs = append(errs, fmt.Sprintf("%s: Ergo name %q is not an exported identifier", source, n))
			return
		}
		if err := pkg.claim(n, source); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, x := range b.Codes {
		claim(strings.TrimPrefix(b.CodeGoName(x), "Status"), "status code "+x.Name)
	}
	b.records = b.recordStructs()
	for _, d := range b.Model.Types {
		if b.records[d.Name] {
			claim(b.GoTypes[d.Name], "struct "+d.Name)
		}
	}
	for _, f := range b.Funcs {
		if f.ErgoUnbound() == "" {
			claim(f.GoName, "function "+f.C.Name)
		}
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return contractError(errs)
	}
	for _, f := range b.Funcs {
		if f.ErgoUnbound() != "" {
			continue
		}
		sc := ergoFuncScope(pkg)
		for _, p := range f.Sig.Params {
			f.ErgoParams = append(f.ErgoParams, sc.alloc(paramWant(f.C.Params[p.C], p.C, b), paramSource(f.C, p.C)))
		}
	}
	return nil
}

// recordStructs are the C structs a typed signature of the Ergo text
// reaches by value, directly, through an array or a pointer, or as a
// field of one.
func (b *Binding) recordStructs() map[string]bool {
	k := b.k
	out := map[string]bool{}
	var mark func(t Type)
	mark = func(t Type) {
		for t.Kind == TypeArray || t.Kind == TypePointer {
			t = *t.Elem
		}
		if d := k.types[t.Name]; t.Kind == TypeNamed && d != nil && d.Kind == DeclStruct && !out[t.Name] {
			out[t.Name] = true
			for _, fl := range d.Fields {
				mark(fl.Type)
			}
		}
	}
	for _, f := range b.Funcs {
		if f.ErgoUnbound() != "" {
			continue
		}
		for _, p := range f.Sig.Params {
			mark(f.C.Params[p.C].Type)
		}
		for _, r := range f.Sig.Results {
			if r.C < 0 {
				mark(f.C.Result)
			} else {
				mark(f.C.Params[r.C].Type)
			}
		}
	}
	return out
}

func elemOr(t Type) *Type {
	if t.Kind == TypePointer {
		return t.Elem
	}
	return &Type{}
}

// statusConstType is the C type of a constant that holds a status code of
// scalar s: int for the narrow types, as INTn_C gives (C11 7.20.4.1).
func statusConstType(s Scalar) Scalar {
	switch s {
	case Int8, Int16, Int32:
		return Int32
	case Uint8, Uint16:
		return Int32
	case Uint32:
		return Uint32
	case Int64:
		return Int64
	}
	return Uint64
}

// fitsScalar reports whether the value of x fits scalar s.
func fitsScalar(x *Constant, s Scalar) bool {
	bits := uint(8 * scalarSize[s])
	if x.Signed() {
		v := x.Int64()
		if s == Int8 || s == Int16 || s == Int32 || s == Int64 {
			lo := int64(-1) << (bits - 1)
			return v >= lo && v <= -(lo+1)
		}
		return v >= 0 && (bits == 64 || uint64(v)>>bits == 0)
	}
	v := x.Uint64()
	if s == Int8 || s == Int16 || s == Int32 || s == Int64 {
		return v>>(bits-1) == 0
	}
	return bits == 64 || v>>bits == 0
}

// checkNames gives the Go names of the package scope and of every struct's
// fields (names.go). It refuses two declarations with one Go name, a Go
// name that is not an exported identifier, and two fields of one struct
// with one Go name; it renames an unexported (autofill) field off the
// generated methods and Go's keywords.
func (b *Binding) checkNames() error {
	pkg := goPackageScope()
	var errs []string
	add := func(goName, cName string) {
		if !isGoIdent(goName) || goName[0] < 'A' || goName[0] > 'Z' {
			errs = append(errs, fmt.Sprintf("%s: Go name %q is not an exported identifier", cName, goName))
			return
		}
		if err := pkg.claim(goName, cName); err != nil {
			errs = append(errs, err.Error())
		}
	}
	b.FieldNames = map[string][]string{}
	for _, d := range b.Model.Types {
		if d.Kind != DeclStruct {
			continue
		}
		sc := goStructScope()
		for _, f := range d.Fields {
			if b.Autofill[f.Name] != "" {
				n := lowerCamel(f.Name)
				if n == "" {
					errs = append(errs, fmt.Sprintf("%s: field %s has no Go name", d.Name, f.Name))
					continue
				}
				b.FieldNames[d.Name] = append(b.FieldNames[d.Name], sc.alloc(n, f.Name))
				continue
			}
			n := goCamel(f.Name)
			b.FieldNames[d.Name] = append(b.FieldNames[d.Name], n)
			if !isGoIdent(n) || n[0] < 'A' || n[0] > 'Z' {
				errs = append(errs, fmt.Sprintf("%s: field %s: Go name %q is not an exported identifier", d.Name, f.Name, n))
			} else if prev, ok := sc.taken(n); ok {
				errs = append(errs, fmt.Sprintf("%s: fields %s and %s have the Go name %s", d.Name, prev, f.Name, n))
			} else {
				sc.mustClaim(n, f.Name)
			}
		}
	}
	for _, f := range b.Funcs {
		if err := pkg.claim(rawName(f.C.Name), "the raw function of "+f.C.Name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, d := range b.Model.Types {
		if d.Name == b.Contract.Status.Type {
			continue
		}
		add(b.GoTypes[d.Name], d.Name)
	}
	for _, f := range b.Funcs {
		add(f.GoName, f.C.Name)
	}
	for _, x := range b.Codes {
		add(b.CodeGoName(x), x.Name)
	}
	for _, x := range b.Constants {
		add(x.Name, x.Name)
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return contractError(errs)
	}
	b.pkg = pkg
	return nil
}

// goNameOwner is the source that has the package-level Go name n, or "".
func (b *Binding) goNameOwner(n string) string { return b.pkg.owner[n] }

// CodeGoName is the Go name of a status code: "Status" and the code's name
// without the status prefix, in CamelCase.
func (b *Binding) CodeGoName(x *Constant) string {
	return "Status" + goCamel(strings.TrimPrefix(x.Name, b.Contract.Status.MacroPrefix))
}

// goCamel turns snake_case into CamelCase: "scene_set_text" ->
// "SceneSetText".
func goCamel(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(strings.ToLower(part[1:]))
	}
	return b.String()
}

// lowerCamel turns snake_case into lowerCamelCase: "byte_count" -> "byteCount".
func lowerCamel(s string) string {
	c := goCamel(s)
	if c == "" {
		return ""
	}
	return strings.ToLower(c[:1]) + c[1:]
}

func isGoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

var goKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var") {
		goKeywords[k] = true
	}
}
