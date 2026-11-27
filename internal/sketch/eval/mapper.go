package eval

import (
	"fmt"
	"go/constant"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// SourceByteOrigin locates the source file and UTF-8 byte range that produced
// a single decoded byte of an inline content-mapper payload.
type SourceByteOrigin struct {
	File  *syntax.File
	Start syntax.Pos
	End   syntax.Pos
}

// MapperOriginKind selects between a file-asset import and an inline derive site.
type MapperOriginKind int

const (
	OriginFile MapperOriginKind = iota
	OriginInline
)

// MapperOrigin carries the call-site origin metadata passed to ContentInput.Origin.
type MapperOrigin struct {
	Kind    MapperOriginKind
	Path    string
	Package string
	File    string
	Target  string
	Line    uint32
	Col     uint32
}

// MapperCompanion is one explicitly declared companion asset passed to a mapper.
type MapperCompanion struct {
	Path  string
	Bytes []byte
}

// MapperInput holds the closed check-time inputs for one content mapper run.
type MapperInput struct {
	Target     string
	Origin     MapperOrigin
	MediaKind  string
	Bytes      []byte
	Companions []MapperCompanion
}

// MapperSpan is a half-open byte range [Start, End).
type MapperSpan struct {
	Start uint32
	End   uint32
}

// MapperSpanMapping maps a generated Ergo byte interval back to its asset or companion source range.
type MapperSpanMapping struct {
	Generated    MapperSpan
	HasCompanion bool
	Companion    uint32
	Source       MapperSpan
	Label        string
}

// MapperDiag is a structured asset-level diagnostic returned by a mapper.
type MapperDiag struct {
	Warning      bool
	HasCompanion bool
	Companion    uint32
	Span         MapperSpan
	Code         string
	Message      string
	Hint         string
}

// MapperOutput is the verified output decoded from a MappedErgo Ergo value.
type MapperOutput struct {
	Source    string
	Spans     []MapperSpanMapping
	Diags     []MapperDiag
	Digest    []byte
	Steps     int64
	BytesUsed int64
}

// MapperBudget bounds compile-time content-mapper execution.
type MapperBudget struct {
	Steps     int64
	Depth     int
	Bytes     int64
	MaxInput  int
	MaxOutput int
	MaxSpans  int
	MaxDiags  int
}

const (
	DefaultMapperSteps     int64 = 1_000_000
	DefaultMapperDepth     int   = 128
	DefaultMapperBytes     int64 = 64 << 20
	DefaultMapperMaxInput  int   = 2 << 20
	DefaultMapperMaxOutput int   = 2 << 20
	DefaultMapperMaxSpans  int   = 65_536
	DefaultMapperMaxDiags  int   = 4_096
)

// DefaultMapperBudget returns the specification's finite compile-time mapper bounds.
func DefaultMapperBudget() MapperBudget {
	return MapperBudget{
		Steps:     DefaultMapperSteps,
		Depth:     DefaultMapperDepth,
		Bytes:     DefaultMapperBytes,
		MaxInput:  DefaultMapperMaxInput,
		MaxOutput: DefaultMapperMaxOutput,
		MaxSpans:  DefaultMapperMaxSpans,
		MaxDiags:  DefaultMapperMaxDiags,
	}
}

func (b MapperBudget) withDefaults() MapperBudget {
	def := DefaultMapperBudget()
	if b.Steps <= 0 {
		b.Steps = def.Steps
	}
	if b.Depth <= 0 {
		b.Depth = def.Depth
	}
	if b.Bytes <= 0 {
		b.Bytes = def.Bytes
	}
	if b.MaxInput <= 0 {
		b.MaxInput = def.MaxInput
	}
	if b.MaxOutput <= 0 {
		b.MaxOutput = def.MaxOutput
	}
	if b.MaxSpans <= 0 {
		b.MaxSpans = def.MaxSpans
	}
	if b.MaxDiags <= 0 {
		b.MaxDiags = def.MaxDiags
	}
	return b
}

// MapperEvalError describes a purity, FFI, budget, defect, or contract failure
// during check-time mapper evaluation.
type MapperEvalError struct {
	Kind      string // "purity", "ffi", "budget", "defect", "contract", "fail"
	Code      string
	Message   string
	Limit     string
	Frame     string
	At        syntax.Node
	File      *syntax.File
	CallStack []string
}

func (e *MapperEvalError) Error() string { return e.Message }

// DeriveEvalKind identifies what an inline `type T derive E` expression evaluated to.
type DeriveEvalKind int

const (
	DerivePlan DeriveEvalKind = iota
	DeriveFunc
	DeriveDirectOutput
)

// DeriveEvalResult captures the evaluated content-mapper descriptor for inline `derive E`.
type DeriveEvalResult struct {
	Kind        DeriveEvalKind
	MediaKind   string
	Bytes       []byte
	ByteOrigins []SourceByteOrigin
	Companions  []MapperCompanion
	MapperPkg   *check.Package
	MapperFn    *check.Func // non-nil when Mapper is a package function
	Callable    Value       // opaque *funcRef or *closure passed to InvokeMapper
	Direct      *MapperOutput
	Steps       int64
	BytesUsed   int64
}

type byteTracker struct {
	files    map[syntax.Decl]*syntax.File
	last     []SourceByteOrigin
	slots    map[*slot][]SourceByteOrigin
	slices   map[*backing][]SourceByteOrigin
	argOrigs [][]SourceByteOrigin
}

func newByteTracker(pkg *check.Package) *byteTracker {
	bt := &byteTracker{
		files:  map[syntax.Decl]*syntax.File{},
		slots:  map[*slot][]SourceByteOrigin{},
		slices: map[*backing][]SourceByteOrigin{},
	}
	if pkg != nil {
		for _, f := range pkg.Files() {
			for _, d := range f.Decls {
				bt.files[d] = f
			}
		}
	}
	return bt
}

func (bt *byteTracker) fileFor(fr *frame) *syntax.File {
	if fr == nil || fr.decl == nil {
		return nil
	}
	if f, ok := bt.files[fr.decl]; ok {
		return f
	}
	if fr.pkg != nil {
		for _, f := range fr.pkg.Files() {
			for _, d := range f.Decls {
				bt.files[d] = f
			}
		}
		if f, ok := bt.files[fr.decl]; ok {
			return f
		}
	}
	return nil
}

func (bt *byteTracker) take() []SourceByteOrigin {
	if bt == nil {
		return nil
	}
	out := bt.last
	bt.last = nil
	return out
}

func (bt *byteTracker) argMark() int {
	if bt == nil {
		return 0
	}
	return len(bt.argOrigs)
}

func (bt *byteTracker) truncateArgs(mark int) {
	if bt != nil && mark <= len(bt.argOrigs) {
		bt.argOrigs = bt.argOrigs[:mark]
	}
}

func (bt *byteTracker) pushArg() {
	if bt == nil {
		return
	}
	bt.argOrigs = append(bt.argOrigs, bt.take())
}

func (bt *byteTracker) popArgs(n int) [][]SourceByteOrigin {
	if bt == nil || n == 0 || len(bt.argOrigs) < n {
		return nil
	}
	start := len(bt.argOrigs) - n
	out := append([][]SourceByteOrigin(nil), bt.argOrigs[start:]...)
	bt.argOrigs = bt.argOrigs[:start]
	return out
}

func (bt *byteTracker) bindParam(fr *frame, v *check.Var, orig []SourceByteOrigin) {
	if bt == nil || v == nil || len(orig) == 0 {
		return
	}
	if s, ok := fr.vars.get(v); ok {
		bt.slots[s] = orig
	}
}

func (bt *byteTracker) onSlot(s *slot) {
	if bt == nil {
		return
	}
	bt.last = bt.slots[s]
}

func (bt *byteTracker) onConcat(a, b []SourceByteOrigin) {
	if bt == nil {
		return
	}
	if len(a) == 0 && len(b) == 0 {
		bt.last = nil
		return
	}
	out := make([]SourceByteOrigin, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	bt.last = out
}

func (bt *byteTracker) litOrigins(fr *frame, lit *syntax.BasicLit) []SourceByteOrigin {
	if bt == nil || lit == nil {
		return nil
	}
	decoded, spans, ok := syntax.DecodeStringWithOrigins(lit)
	if !ok || len(spans) != len(decoded) {
		return nil
	}
	f := bt.fileFor(fr)
	out := make([]SourceByteOrigin, len(spans))
	for i, sp := range spans {
		out[i] = SourceByteOrigin{File: f, Start: sp.Start, End: sp.End}
	}
	return out
}

func (bt *byteTracker) onConst(fr *frame, x syntax.Expr, v Value) {
	if bt == nil {
		return
	}
	s, ok := v.(string)
	if !ok {
		bt.last = nil
		return
	}
	orig := bt.constExprOrigins(fr, x)
	if len(orig) == len(s) {
		bt.last = orig
		return
	}
	bt.last = make([]SourceByteOrigin, len(s))
}

func (bt *byteTracker) constExprOrigins(fr *frame, x syntax.Expr) []SourceByteOrigin {
	if x == nil {
		return nil
	}
	switch x := unparen(x).(type) {
	case *syntax.BasicLit:
		return bt.litOrigins(fr, x)
	case *syntax.BinaryExpr:
		if x.Op == syntax.ADD {
			a := bt.constExprOrigins(fr, x.X)
			b := bt.constExprOrigins(fr, x.Y)
			if len(a) > 0 || len(b) > 0 {
				out := make([]SourceByteOrigin, 0, len(a)+len(b))
				out = append(out, a...)
				out = append(out, b...)
				return out
			}
		}
	case *syntax.Ident:
		if fr != nil && fr.info != nil {
			if cv, ok := fr.info.Values[x]; ok && cv != nil && cv.Kind() == constant.String {
				return make([]SourceByteOrigin, len(constant.StringVal(cv)))
			}
		}
	}
	return nil
}

func (bt *byteTracker) afterNative(fn *check.Func, args []Value, v Value, argOrigs [][]SourceByteOrigin) {
	if bt == nil || fn == nil {
		return
	}
	rt := check.BodyOf(fn).Runtime
	switch {
	case rt == "StringsBytes":
		if sv, ok := v.(sliceV); ok && sv.b != nil && len(argOrigs) == 1 && len(argOrigs[0]) == sv.n {
			bt.slices[sv.b] = append([]SourceByteOrigin(nil), argOrigs[0]...)
		}
	case rt == "StringsSlice":
		if str, ok := v.(string); ok && len(argOrigs) == 3 && len(argOrigs[0]) > 0 {
			lo, _ := args[1].(int64)
			hi, _ := args[2].(int64)
			if lo >= 0 && hi >= lo && int(hi) <= len(argOrigs[0]) && int(hi-lo) == len(str) {
				bt.last = append([]SourceByteOrigin(nil), argOrigs[0][lo:hi]...)
			}
		}
	case rt == "StringsBuilderText":
		if str, ok := v.(string); ok && len(args) == 1 {
			if sv, ok := args[0].(sliceV); ok && sv.b != nil {
				if orig, exists := bt.slices[sv.b]; exists && sv.off+sv.n <= len(orig) && sv.n == len(str) {
					bt.last = append([]SourceByteOrigin(nil), orig[sv.off:sv.off+sv.n]...)
				}
			}
		}
	}
}

func (bt *byteTracker) onAppendString(before, after sliceV, text string, orig []SourceByteOrigin) {
	if bt == nil || after.b == nil {
		return
	}
	var base []SourceByteOrigin
	if before.b != nil {
		if prev, ok := bt.slices[before.b]; ok && before.off+before.n <= len(prev) {
			base = prev[before.off : before.off+before.n]
		}
	}
	if len(base) == 0 && before.n > 0 {
		base = make([]SourceByteOrigin, before.n)
	}
	add := orig
	if len(add) != len(text) {
		add = make([]SourceByteOrigin, len(text))
	}
	combined := make([]SourceByteOrigin, 0, len(base)+len(add))
	combined = append(combined, base...)
	combined = append(combined, add...)
	bt.slices[after.b] = combined
}

func (bt *byteTracker) sliceOrigins(sv sliceV) []SourceByteOrigin {
	if bt == nil || sv.b == nil {
		return nil
	}
	orig, ok := bt.slices[sv.b]
	if !ok || sv.off+sv.n > len(orig) {
		return nil
	}
	return append([]SourceByteOrigin(nil), orig[sv.off:sv.off+sv.n]...)
}

func appendStringOrigin(dst []SourceByteOrigin, text string, exprOrig []SourceByteOrigin) []SourceByteOrigin {
	if len(exprOrig) == len(text) {
		return append(dst, exprOrig...)
	}
	for range len(text) {
		dst = append(dst, SourceByteOrigin{})
	}
	return dst
}

// CheckMapperFunc checks structurally that fn implements the pure
// ContentInput -> MappedErgo contract with empty fails/needs/tasks rows.
func CheckMapperFunc(fn *check.Func) error {
	if fn == nil || fn.Sig == nil {
		return fmt.Errorf("missing mapper function signature")
	}
	return checkMapperSig(fn.Name, fn.Sig)
}

func checkMapperSig(name string, sig *check.Signature) error {
	if sig == nil {
		return fmt.Errorf("mapper %s has no signature", name)
	}
	if len(sig.TParams) > 0 {
		return fmt.Errorf("content mapper %s cannot declare type parameters", name)
	}
	if sig.Errors != nil && !sig.Errors.Empty() {
		return fmt.Errorf("content mapper %s cannot declare a fails row (%s); return diagnostics in MappedErgo.Diags", name, sig.Errors)
	}
	if sig.Uses != nil && !sig.Uses.Empty() {
		return fmt.Errorf("content mapper %s must be pure with empty needs row (found needs %s)", name, sig.Uses)
	}
	if sig.Defects {
		return fmt.Errorf("content mapper %s cannot declare defects", name)
	}
	if len(sig.Params) != 1 || len(sig.Results) != 1 {
		return fmt.Errorf("content mapper %s must have signature func(ContentInput) MappedErgo", name)
	}
	if err := checkContentInputType(sig.Params[0].Type); err != nil {
		return fmt.Errorf("content mapper %s parameter: %w", name, err)
	}
	if err := checkMappedErgoType(sig.Results[0]); err != nil {
		return fmt.Errorf("content mapper %s result: %w", name, err)
	}
	return nil
}

func sigOfType(t check.Type) (*check.Signature, bool) {
	if s, ok := t.(*check.Signature); ok {
		return s, true
	}
	if n, ok := t.(*check.Named); ok {
		if s, ok := n.Underlying().(*check.Signature); ok {
			return s, true
		}
	}
	return nil, false
}

func checkContentInputType(t check.Type) error {
	s := structOf(t)
	if s == nil {
		return fmt.Errorf("expected ContentInput record type, found %s", t)
	}
	if !hasBasicField(s, "Target", check.String) ||
		!hasBasicField(s, "MediaKind", check.String) ||
		!hasByteSliceField(s, "Bytes") ||
		fieldType(s, "Origin") == nil ||
		fieldType(s, "Companions") == nil {
		return fmt.Errorf("type %s does not match ContentInput contract fields", t)
	}
	if err := checkOriginUnionType(fieldType(s, "Origin")); err != nil {
		return err
	}
	if err := checkCompanionsSliceType(fieldType(s, "Companions")); err != nil {
		return err
	}
	return nil
}

func checkOriginUnionType(t check.Type) error {
	n, ok := t.(*check.Named)
	if !ok {
		return fmt.Errorf("ContentInput.Origin must be AssetOrigin union, found %s", t)
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) != 2 {
		return fmt.Errorf("ContentInput.Origin must be two-variant AssetOrigin union, found %s", t)
	}
	_, _, ok = originVariants(u)
	if !ok {
		return fmt.Errorf("ContentInput.Origin variants do not match FileAsset and InlineDerive")
	}
	return nil
}

func originVariants(u *check.Union) (fileVar, inlineVar *check.Named, ok bool) {
	for _, vt := range u.Variants {
		n, isNamed := vt.(*check.Named)
		s := structOf(vt)
		if !isNamed || s == nil {
			return nil, nil, false
		}
		if hasBasicField(s, "Path", check.String) && hasBasicField(s, "Package", check.String) && len(s.Fields) == 2 {
			fileVar = n
		} else if hasBasicField(s, "Package", check.String) && hasBasicField(s, "File", check.String) &&
			hasBasicField(s, "Target", check.String) && hasBasicField(s, "Line", check.Uint32) &&
			hasBasicField(s, "Col", check.Uint32) && len(s.Fields) == 5 {
			inlineVar = n
		}
	}
	return fileVar, inlineVar, fileVar != nil && inlineVar != nil
}

func checkCompanionsSliceType(t check.Type) error {
	sl, ok := underlyingSlice(t)
	if !ok {
		return fmt.Errorf("ContentInput.Companions must be a slice, found %s", t)
	}
	s := structOf(sl.Elem)
	if s == nil || !hasBasicField(s, "Path", check.String) || !hasByteSliceField(s, "Bytes") {
		return fmt.Errorf("ContentInput.Companions element must match CompanionAsset { Path string, Bytes []byte }, found %s", sl.Elem)
	}
	return nil
}

func checkMappedErgoType(t check.Type) error {
	s := structOf(t)
	if s == nil {
		return fmt.Errorf("expected MappedErgo record type, found %s", t)
	}
	if !hasBasicField(s, "Source", check.String) ||
		!hasByteSliceField(s, "Digest") ||
		fieldType(s, "Spans") == nil ||
		fieldType(s, "Diags") == nil {
		return fmt.Errorf("type %s does not match MappedErgo contract fields", t)
	}
	return nil
}

func checkPlanType(t check.Type) (mapperSig *check.Signature, ok bool) {
	s := structOf(t)
	if s == nil {
		return nil, false
	}
	mType := fieldType(s, "Mapper")
	if mType == nil {
		return nil, false
	}
	sig, isSig := sigOfType(mType)
	if !isSig || checkMapperSig("Plan.Mapper", sig) != nil {
		return nil, false
	}
	if !hasBasicField(s, "MediaKind", check.String) || !hasByteSliceField(s, "Bytes") {
		return nil, false
	}
	if comp := fieldType(s, "Companions"); comp != nil {
		if checkCompanionsSliceType(comp) != nil {
			return nil, false
		}
	}
	return sig, true
}

func underlyingSlice(t check.Type) (*check.Slice, bool) {
	if sl, ok := t.(*check.Slice); ok {
		return sl, true
	}
	if n, ok := t.(*check.Named); ok {
		if sl, ok := n.Underlying().(*check.Slice); ok {
			return sl, true
		}
	}
	return nil, false
}

func fieldType(s *check.Struct, name string) check.Type {
	for _, f := range s.Fields {
		if f.Name == name {
			return f.Type
		}
	}
	return nil
}

func hasBasicField(s *check.Struct, name string, kind check.BasicKind) bool {
	ft := fieldType(s, name)
	if ft == nil {
		return false
	}
	b := basicOf(ft)
	return b != nil && b.Kind == kind
}

func hasByteSliceField(s *check.Struct, name string) bool {
	ft := fieldType(s, name)
	if ft == nil {
		return false
	}
	sl, ok := underlyingSlice(ft)
	if !ok {
		return false
	}
	b := basicOf(sl.Elem)
	return b != nil && b.Kind == check.Uint8
}

// ValidateMapperPurity statically verifies that fn and every reachable function
// and package initializer in its call graph have empty capability and task
// footprints and declare no direct foreign Go escape hatches.
func ValidateMapperPurity(pkg *check.Package, fn *check.Func) *MapperEvalError {
	if pkg == nil || fn == nil {
		return &MapperEvalError{Kind: "contract", Message: "missing mapper package or function"}
	}
	return validatePurity(pkg, fn.Name, fn, nil)
}

// ValidateExprPurity statically verifies that expr and every reachable function
// and package initializer in its call graph have empty capability and task
// footprints and declare no foreign Go imports or extern declarations.
func ValidateExprPurity(pkg *check.Package, label string, expr syntax.Expr) *MapperEvalError {
	if pkg == nil || expr == nil {
		return &MapperEvalError{Kind: "contract", Message: "missing derive expression"}
	}
	return validatePurity(pkg, label, nil, expr)
}

func validatePurity(pkg *check.Package, label string, rootFn *check.Func, rootExpr syntax.Expr) *MapperEvalError {
	goPkgsByPkg := map[*check.Package]map[*check.Package]bool{}
	goPkgsFor := func(p *check.Package) map[*check.Package]bool {
		if m, ok := goPkgsByPkg[p]; ok {
			return m
		}
		m := collectGoImports(p)
		goPkgsByPkg[p] = m
		return m
	}
	seenFuncs := map[*check.Func]bool{}
	seenPkgs := map[*check.Package]bool{}
	var walkPkgInits func(p *check.Package) *MapperEvalError
	var walkFunc func(g *check.Func) *MapperEvalError

	walkPkgInits = func(p *check.Package) *MapperEvalError {
		if p == nil || p.Info == nil || seenPkgs[p] {
			return nil
		}
		seenPkgs[p] = true
		for _, dep := range initImports(p) {
			if dep.Host() {
				continue
			}
			if err := walkPkgInits(dep); err != nil {
				return err
			}
		}
		specs := map[syntax.Expr]*syntax.VarSpec{}
		decls := map[*syntax.VarSpec]*syntax.VarDecl{}
		for _, f := range p.Files() {
			for _, d := range f.Decls {
				vd, ok := d.(*syntax.VarDecl)
				if !ok {
					continue
				}
				for _, sp := range vd.Specs {
					decls[sp] = vd
					if len(sp.Values) > 0 {
						specs[sp.Values[0]] = sp
					}
				}
			}
		}
		goPkgs := goPkgsFor(p)
		for _, inItem := range p.Info.InitOrder {
			if sp := specs[inItem.Value]; sp != nil {
				if vd := decls[sp]; vd != nil {
					if fp, ok := p.Info.Footprints[vd]; ok {
						if len(fp.Caps) > 0 {
							return &MapperEvalError{
								Kind:    "purity",
								Message: fmt.Sprintf("content mapper %s initializer requires capability %v; check-time mappers must be pure", label, fp.Caps[0]),
								At:      vd,
							}
						}
						if fp.Tasks {
							return &MapperEvalError{
								Kind:    "purity",
								Message: fmt.Sprintf("content mapper %s initializer starts tasks; check-time mappers must be pure", label),
								At:      vd,
							}
						}
					}
				}
			}
			if err := walkBody(inItem.Value, p, goPkgs, walkFunc); err != nil {
				return err
			}
		}
		return nil
	}

	walkFunc = func(g *check.Func) *MapperEvalError {
		if g == nil || seenFuncs[g] {
			return nil
		}
		seenFuncs[g] = true
		if g.Pkg != nil && g.Pkg.Host() {
			if stdPureGoNativeOf(g) == nil {
				return &MapperEvalError{
					Kind:    "ffi",
					Message: fmt.Sprintf("content mapper %s cannot call import go function %s at check time", label, g.Name),
					At:      g.Decl,
				}
			}
			return nil
		}
		if g.Decl != nil && g.Decl.Extern != nil && (g.Pkg == nil || !g.Pkg.Std()) {
			return &MapperEvalError{
				Kind:    "ffi",
				Message: fmt.Sprintf("content mapper %s cannot call extern Go declaration %s at check time", label, g.Name),
				At:      g.Decl,
			}
		}
		if g.Sig != nil {
			if g.Sig.Uses != nil && !g.Sig.Uses.Empty() {
				return &MapperEvalError{
					Kind:    "purity",
					Message: fmt.Sprintf("content mapper %s calls %s which requires capability %s; mappers must be pure", label, g.Name, g.Sig.Uses),
					At:      g.Decl,
				}
			}
		}
		if g.Pkg != nil && g.Pkg.Info != nil && g.Decl != nil {
			if fp, ok := g.Pkg.Info.Footprints[g.Decl]; ok {
				if len(fp.Caps) > 0 {
					return &MapperEvalError{
						Kind:    "purity",
						Message: fmt.Sprintf("content mapper %s calls %s which requires capability %s; mappers must be pure", label, g.Name, fp.Caps[0]),
						At:      g.Decl,
					}
				}
				if fp.Tasks {
					return &MapperEvalError{
						Kind:    "purity",
						Message: fmt.Sprintf("content mapper %s calls %s which starts tasks; mappers must be pure", label, g.Name),
						At:      g.Decl,
					}
				}
			}
			if g.Decl.Body != nil {
				if err := walkBody(g.Decl.Body, g.Pkg, goPkgsFor(g.Pkg), walkFunc); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walkPkgInits(pkg); err != nil {
		return err
	}
	if rootExpr != nil {
		if fp, ok := pkg.Info.Footprints[rootExpr]; ok {
			if len(fp.Caps) > 0 {
				return &MapperEvalError{
					Kind:    "purity",
					Message: fmt.Sprintf("derive expression %s requires capability %v; check-time evaluation must be pure", label, fp.Caps[0]),
					At:      rootExpr,
				}
			}
			if fp.Tasks {
				return &MapperEvalError{
					Kind:    "purity",
					Message: fmt.Sprintf("derive expression %s starts tasks; check-time evaluation must be pure", label),
					At:      rootExpr,
				}
			}
		}
		if err := walkBody(rootExpr, pkg, goPkgsFor(pkg), walkFunc); err != nil {
			return err
		}
	}
	if rootFn != nil {
		return walkFunc(rootFn)
	}
	return nil
}

func walkBody(node syntax.Node, p *check.Package, goPkgs map[*check.Package]bool, walkFunc func(*check.Func) *MapperEvalError) *MapperEvalError {
	if node == nil || p == nil || p.Info == nil {
		return nil
	}
	var out *MapperEvalError
	syntax.Inspect(node, func(n syntax.Node) bool {
		if out != nil {
			return false
		}
		switch x := n.(type) {
		case *syntax.CallExpr:
			if c := p.Info.Calls[x]; c != nil && c.Func != nil {
				if err := walkFunc(c.Func); err != nil {
					out = err
					return false
				}
			}
		case *syntax.Ident:
			if use := p.Info.Uses[x]; use != nil {
				switch o := use.(type) {
				case *check.Func:
					if err := walkFunc(o); err != nil {
						out = err
						return false
					}
				case *check.PkgName:
					if o.Pkg != nil && goPkgs[o.Pkg] && !p.Std() {
						out = &MapperEvalError{
							Kind:    "ffi",
							Message: fmt.Sprintf("content mapper cannot call import go package %s at check time", o.Pkg.Path),
							At:      x,
						}
						return false
					}
				}
			}
		}
		return true
	})
	return out
}

func collectGoImports(pkg *check.Package) map[*check.Package]bool {
	out := map[*check.Package]bool{}
	if pkg == nil || pkg.Info == nil {
		return out
	}
	for _, f := range pkg.Files() {
		for _, d := range f.Decls {
			id, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, sp := range id.Specs {
				if sp.Kind != syntax.ImportGo {
					continue
				}
				for _, pn := range pkg.Info.Uses {
					if p, ok := pn.(*check.PkgName); ok && p.Pkg != nil && p.Pos == sp.Pos() {
						out[p.Pkg] = true
					}
				}
			}
		}
	}
	return out
}

func initImports(p *check.Package) []*check.Package {
	if p == nil || p.Info == nil {
		return nil
	}
	seen := map[*check.Package]bool{}
	var out []*check.Package
	for _, o := range p.Info.Uses {
		if pn, ok := o.(*check.PkgName); ok && pn.Pkg != nil && pn.Pkg.Info != nil && !seen[pn.Pkg] {
			seen[pn.Pkg] = true
			out = append(out, pn.Pkg)
		}
	}
	return out
}

// EvalDeriveExpr evaluates the closed expression function generated for `type T derive E`
// under bounded pure check-time rules, tracking literal byte origins for inline diagnostics.
func EvalDeriveExpr(pkg *check.Package, evalFn *check.Func, b MapperBudget) (*DeriveEvalResult, *MapperEvalError) {
	b = b.withDefaults()
	if err := ValidateMapperPurity(pkg, evalFn); err != nil {
		return nil, err
	}
	bt := newByteTracker(pkg)
	in := &interp{
		budget:       Budget{Steps: b.Steps, Depth: b.Depth, Bytes: b.Bytes},
		root:         pkg,
		files:        map[syntax.Decl]*syntax.File{},
		initials:     map[*check.TypeName]Value{},
		mapOrder:     mapAscending,
		walk:         true,
		strictMapper: true,
		track:        bt,
	}
	var retErr *MapperEvalError
	var res *DeriveEvalResult
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(internalError); ok {
				retErr = &MapperEvalError{Kind: "defect", Message: ie.Error()}
				res = nil
				return
			}
			panic(r)
		}
	}()
	if initFail := in.initialize(pkg); initFail != nil {
		return nil, initFailureToMapperError(initFail)
	}
	in.begin()
	rootFr := in.newFrame(pkg, evalFn.Decl)
	val, ab := in.callResolved(rootFr, nil, evalFn, nil, nil)
	if ab != nil {
		return nil, abruptToMapperError(in, evalFn.Name, evalFn.Decl, b, ab)
	}
	outOrig := bt.take()
	res, err := decodeDeriveValue(bt, val, outOrig, evalFn.Sig.Results[0], in.steps-in.stepsAt, in.bytes-in.bytesAt)
	if err != nil {
		return nil, err
	}
	return res, retErr
}

// EvalDeriveVar evaluates a derive expression checked as a package variable
// initializer rhs (`let __ergo_derive_val = rhs`) while skipping its
// eager execution during package initialization so budget and byte origin
// tracking apply strictly to rhs.
func EvalDeriveVar(pkg *check.Package, decl *syntax.VarDecl, rhs syntax.Expr, typ check.Type, b MapperBudget) (*DeriveEvalResult, *MapperEvalError) {
	b = b.withDefaults()
	if pkg == nil || rhs == nil {
		return nil, &MapperEvalError{Kind: "contract", Message: "missing derive expression"}
	}
	if typ == nil && pkg.Info != nil {
		typ = pkg.Info.Types[rhs]
	}
	if typ == nil {
		return nil, &MapperEvalError{Kind: "contract", Message: "derive expression has no checked type", At: rhs}
	}
	if err := ValidateExprPurity(pkg, "derive", rhs); err != nil {
		return nil, err
	}
	bt := newByteTracker(pkg)
	in := &interp{
		budget:       Budget{Steps: b.Steps, Depth: b.Depth, Bytes: b.Bytes},
		root:         pkg,
		files:        map[syntax.Decl]*syntax.File{},
		initials:     map[*check.TypeName]Value{},
		mapOrder:     mapAscending,
		walk:         true,
		strictMapper: true,
		track:        bt,
		skipInitExpr: rhs,
	}
	var retErr *MapperEvalError
	var res *DeriveEvalResult
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(internalError); ok {
				retErr = &MapperEvalError{Kind: "defect", Message: ie.Error()}
				res = nil
				return
			}
			panic(r)
		}
	}()
	if initFail := in.initialize(pkg); initFail != nil {
		return nil, initFailureToMapperError(initFail)
	}
	in.begin()
	rootFr := in.newFrame(pkg, decl)
	val, ab := in.expr(rootFr, rhs)
	if ab != nil {
		return nil, abruptToMapperError(in, "derive", decl, b, ab)
	}
	outOrig := bt.take()
	res, err := decodeDeriveValue(bt, val, outOrig, typ, in.steps-in.stepsAt, in.bytes-in.bytesAt)
	if err != nil {
		return nil, err
	}
	return res, retErr
}

func initFailureToMapperError(initFail *initFailure) *MapperEvalError {
	kind := "purity"
	switch initFail.status {
	case Incomplete:
		kind = "budget"
	case Defect:
		kind = "defect"
	}
	return &MapperEvalError{
		Kind:    kind,
		Message: fmt.Sprintf("package initializer failed during derive evaluation: %s", initFail.reason),
	}
}

func decodeDeriveValue(bt *byteTracker, val Value, valOrig []SourceByteOrigin, typ check.Type, steps, bytesUsed int64) (*DeriveEvalResult, *MapperEvalError) {
	if mapperSig, ok := checkPlanType(typ); ok {
		rec, isRec := val.(*record)
		if !isRec {
			return nil, &MapperEvalError{Kind: "contract", Message: "expected a content Plan record"}
		}
		fnVal := rec.fields[fieldIndex(rec.t, "Mapper")]
		mk, _ := rec.fields[fieldIndex(rec.t, "MediaKind")].(string)
		bsVal, ok := rec.fields[fieldIndex(rec.t, "Bytes")].(sliceV)
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "Plan.Bytes must be []byte"}
		}
		rawBytes := decodeByteSlice(bsVal)
		var origs []SourceByteOrigin
		if bt != nil {
			origs = bt.sliceOrigins(bsVal)
		}
		if len(origs) != len(rawBytes) && len(valOrig) == len(rawBytes) {
			origs = valOrig
		}
		var comps []MapperCompanion
		if compType := fieldType(structOf(typ), "Companions"); compType != nil {
			if compSlice, ok := rec.fields[fieldIndex(rec.t, "Companions")].(sliceV); ok {
				comps = decodeCompanions(compSlice)
			}
		}
		_ = mapperSig
		var mapperFn *check.Func
		var mapperPkg *check.Package
		switch fn := fnVal.(type) {
		case *funcRef:
			mapperFn = fn.fn
			if fn.fn != nil {
				mapperPkg = fn.fn.Pkg
			}
		case *closure:
			if fn.home != nil {
				mapperPkg = fn.home.pkg
			}
		default:
			return nil, &MapperEvalError{Kind: "contract", Message: "Plan.Mapper must be a function value"}
		}
		return &DeriveEvalResult{
			Kind:        DerivePlan,
			MediaKind:   mk,
			Bytes:       rawBytes,
			ByteOrigins: origs,
			Companions:  comps,
			MapperPkg:   mapperPkg,
			MapperFn:    mapperFn,
			Callable:    fnVal,
			Steps:       steps,
			BytesUsed:   bytesUsed,
		}, nil
	}
	if sig, ok := sigOfType(typ); ok && checkMapperSig("derive", sig) == nil {
		var mapperFn *check.Func
		var mapperPkg *check.Package
		switch fn := val.(type) {
		case *funcRef:
			mapperFn = fn.fn
			if fn.fn != nil {
				mapperPkg = fn.fn.Pkg
			}
		case *closure:
			if fn.home != nil {
				mapperPkg = fn.home.pkg
			}
		default:
			return nil, &MapperEvalError{Kind: "contract", Message: "expected a content mapper function"}
		}
		return &DeriveEvalResult{
			Kind:       DeriveFunc,
			MapperPkg:  mapperPkg,
			MapperFn:   mapperFn,
			Callable:   val,
			Steps:      steps,
			BytesUsed:  bytesUsed,
		}, nil
	}
	if checkMappedErgoType(typ) == nil {
		out, err := decodeMappedErgo(val, steps, bytesUsed)
		if err != nil {
			return nil, err
		}
		return &DeriveEvalResult{
			Kind:       DeriveDirectOutput,
			Direct:     out,
			Steps:      steps,
			BytesUsed:  bytesUsed,
		}, nil
	}
	return nil, &MapperEvalError{
		Kind:    "contract",
		Message: fmt.Sprintf("derive expression has type %s; expected a content Plan, mapper func(ContentInput) MappedErgo, or MappedErgo", typ),
	}
}

// InvokeMapper executes a pure checked content mapper (either a *check.Func or a
// Callable value returned inside Plan) on the closed MapperInput under strict budgets.
func InvokeMapper(pkg *check.Package, callable any, input MapperInput, b MapperBudget) (*MapperOutput, *MapperEvalError) {
	b = b.withDefaults()
	totalInput := len(input.Bytes)
	for _, c := range input.Companions {
		totalInput += len(c.Bytes)
	}
	if totalInput > b.MaxInput {
		return nil, &MapperEvalError{
			Kind:    "budget",
			Limit:   "input bytes",
			Message: fmt.Sprintf("content mapper input size %d bytes exceeds limit of %d bytes", totalInput, b.MaxInput),
		}
	}

	var sig *check.Signature
	var fnObj *check.Func
	var val Value
	switch target := callable.(type) {
	case *check.Func:
		if err := CheckMapperFunc(target); err != nil {
			return nil, &MapperEvalError{Kind: "contract", Message: err.Error(), At: target.Decl}
		}
		if err := ValidateMapperPurity(pkg, target); err != nil {
			return nil, err
		}
		fnObj = target
		sig = target.Sig
		if target.Pkg != nil {
			pkg = target.Pkg
		}
	case *funcRef:
		if target.fn == nil {
			return nil, &MapperEvalError{Kind: "contract", Message: "missing mapper function reference"}
		}
		if err := CheckMapperFunc(target.fn); err != nil {
			return nil, &MapperEvalError{Kind: "contract", Message: err.Error(), At: target.fn.Decl}
		}
		if err := ValidateMapperPurity(pkg, target.fn); err != nil {
			return nil, err
		}
		fnObj = target.fn
		sig = target.fn.Sig
		val = target
		if target.fn.Pkg != nil {
			pkg = target.fn.Pkg
		}
	case *closure:
		if target.home != nil && target.home.decl != nil && target.home.pkg != nil {
			if fp, ok := target.home.pkg.Info.Footprints[target.key]; ok {
				if len(fp.Caps) > 0 || fp.Tasks {
					return nil, &MapperEvalError{Kind: "purity", Message: "closure content mapper cannot use capabilities or tasks"}
				}
			}
			pkg = target.home.pkg
		}
		if target.lit != nil && pkg != nil {
			if err := ValidateExprPurity(pkg, "closure", target.lit); err != nil {
				return nil, err
			}
		}
		if target.lit != nil && target.home != nil && target.home.info != nil {
			if s, ok := sigOfType(target.home.info.Types[target.lit]); ok {
				sig = s
			}
		}
		if sig == nil {
			return nil, &MapperEvalError{Kind: "contract", Message: "cannot resolve closure mapper signature"}
		}
		if err := checkMapperSig("closure", sig); err != nil {
			return nil, &MapperEvalError{Kind: "contract", Message: err.Error()}
		}
		val = target
	default:
		return nil, &MapperEvalError{Kind: "contract", Message: fmt.Sprintf("invalid content mapper target %T", callable)}
	}

	in := &interp{
		budget:       Budget{Steps: b.Steps, Depth: b.Depth, Bytes: b.Bytes},
		root:         pkg,
		files:        map[syntax.Decl]*syntax.File{},
		initials:     map[*check.TypeName]Value{},
		mapOrder:     mapAscending,
		strictMapper: true,
	}
	var retErr *MapperEvalError
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(internalError); ok {
				retErr = &MapperEvalError{Kind: "defect", Message: ie.Error()}
				return
			}
			panic(r)
		}
	}()
	if initFail := in.initialize(pkg); initFail != nil {
		return nil, initFailureToMapperError(initFail)
	}
	in.begin()
	inputVal, err := buildContentInputValue(sig.Params[0].Type, input)
	if err != nil {
		return nil, &MapperEvalError{Kind: "contract", Message: err.Error()}
	}
	var decl syntax.Decl
	name := "mapper"
	if fnObj != nil {
		decl = fnObj.Decl
		name = fnObj.Name
	}
	rootFr := in.newFrame(pkg, decl)
	var raw Value
	var ab *abrupt
	if fnObj != nil {
		raw, ab = in.callResolved(rootFr, nil, fnObj, nil, []Value{inputVal})
	} else {
		raw, ab = in.callValue(rootFr, nil, val, []Value{inputVal})
	}
	if ab != nil {
		return nil, abruptToMapperError(in, name, decl, b, ab)
	}
	if retErr != nil {
		return nil, retErr
	}
	out, derr := decodeMappedErgo(raw, in.steps-in.stepsAt, in.bytes-in.bytesAt)
	if derr != nil {
		return nil, derr
	}
	if len(out.Source) > b.MaxOutput {
		return nil, &MapperEvalError{
			Kind:    "budget",
			Limit:   "output bytes",
			Frame:   name,
			Message: fmt.Sprintf("content mapper %s output size %d bytes exceeds limit of %d bytes", name, len(out.Source), b.MaxOutput),
		}
	}
	if len(out.Spans) > b.MaxSpans {
		return nil, &MapperEvalError{
			Kind:    "budget",
			Limit:   "span count",
			Frame:   name,
			Message: fmt.Sprintf("content mapper %s emitted %d span entries, exceeding limit of %d", name, len(out.Spans), b.MaxSpans),
		}
	}
	if len(out.Diags) > b.MaxDiags {
		return nil, &MapperEvalError{
			Kind:    "budget",
			Limit:   "diagnostic count",
			Frame:   name,
			Message: fmt.Sprintf("content mapper %s emitted %d diagnostics, exceeding limit of %d", name, len(out.Diags), b.MaxDiags),
		}
	}
	return out, nil
}

func abruptToMapperError(in *interp, rootName string, at syntax.Node, b MapperBudget, ab *abrupt) *MapperEvalError {
	frame := in.mapperLastFrame
	if frame == "" {
		frame = rootName
	}
	stack := append([]string(nil), in.mapperFrames...)
	switch ab.kind {
	case ctlExhausted:
		limit := fmt.Sprintf("step budget (%d)", b.Steps)
		if strings.Contains(ab.msg, "call depth") {
			limit = fmt.Sprintf("call depth (%d)", b.Depth)
		} else if strings.Contains(ab.msg, "bytes") || strings.Contains(ab.msg, "byte") {
			limit = fmt.Sprintf("byte budget (%d)", b.Bytes)
		}
		return &MapperEvalError{
			Kind:      "budget",
			Limit:     limit,
			Frame:     frame,
			At:        at,
			CallStack: stack,
			Message:   fmt.Sprintf("content mapper evaluation stopped at %s in %s: %s", limit, frame, ab.msg),
		}
	case ctlUnsupported:
		return &MapperEvalError{
			Kind:      "purity",
			Frame:     frame,
			At:        at,
			CallStack: stack,
			Message:   fmt.Sprintf("content mapper cannot run %s at check time", ab.msg),
		}
	case ctlDefect:
		return &MapperEvalError{
			Kind:      "defect",
			Frame:     frame,
			At:        at,
			CallStack: stack,
			Message:   fmt.Sprintf("content mapper defect in %s: %s", frame, ab.msg),
		}
	default:
		return &MapperEvalError{
			Kind:      "fail",
			Frame:     frame,
			At:        at,
			CallStack: stack,
			Message:   fmt.Sprintf("content mapper failed in %s: %s", frame, ab.msg),
		}
	}
}

func buildContentInputValue(t check.Type, input MapperInput) (*record, error) {
	inNamed, ok := t.(*check.Named)
	inStruct := structOf(t)
	if !ok || inStruct == nil {
		return nil, fmt.Errorf("ContentInput must be a named record")
	}
	originType := fieldType(inStruct, "Origin")
	originNamed, ok := originType.(*check.Named)
	if !ok {
		return nil, fmt.Errorf("Origin must be a named union")
	}
	originUnion, ok := originNamed.Underlying().(*check.Union)
	if !ok {
		return nil, fmt.Errorf("Origin underlying type must be a union")
	}
	fileVar, inlineVar, ok := originVariants(originUnion)
	if !ok {
		return nil, fmt.Errorf("Origin variants do not match AssetOrigin")
	}
	var originVal *record
	if input.Origin.Kind == OriginFile {
		fs := structOf(fileVar)
		r := newRecord(fileVar, "", len(fs.Fields))
		r.fields[fieldIndex(fileVar, "Path")] = input.Origin.Path
		r.fields[fieldIndex(fileVar, "Package")] = input.Origin.Package
		originVal = r
	} else {
		is := structOf(inlineVar)
		r := newRecord(inlineVar, "", len(is.Fields))
		r.fields[fieldIndex(inlineVar, "Package")] = input.Origin.Package
		r.fields[fieldIndex(inlineVar, "File")] = input.Origin.File
		r.fields[fieldIndex(inlineVar, "Target")] = input.Origin.Target
		r.fields[fieldIndex(inlineVar, "Line")] = input.Origin.Line
		r.fields[fieldIndex(inlineVar, "Col")] = input.Origin.Col
		originVal = r
	}
	compType := fieldType(inStruct, "Companions")
	compSlice, ok := underlyingSlice(compType)
	if !ok {
		return nil, fmt.Errorf("Companions must be a slice")
	}
	compElemNamed, ok := compSlice.Elem.(*check.Named)
	compElemStruct := structOf(compSlice.Elem)
	if !ok || compElemStruct == nil {
		return nil, fmt.Errorf("CompanionAsset must be a named record")
	}
	compElems := make([]Value, len(input.Companions))
	for i, c := range input.Companions {
		cr := newRecord(compElemNamed, "", len(compElemStruct.Fields))
		cr.fields[fieldIndex(compElemNamed, "Path")] = c.Path
		cr.fields[fieldIndex(compElemNamed, "Bytes")] = encodeByteSlice(c.Bytes)
		compElems[i] = cr
	}
	rec := newRecord(inNamed, "", len(inStruct.Fields))
	rec.fields[fieldIndex(inNamed, "Target")] = input.Target
	rec.fields[fieldIndex(inNamed, "Origin")] = originVal
	rec.fields[fieldIndex(inNamed, "MediaKind")] = input.MediaKind
	rec.fields[fieldIndex(inNamed, "Bytes")] = encodeByteSlice(input.Bytes)
	rec.fields[fieldIndex(inNamed, "Companions")] = newSlice(compElems)
	return rec, nil
}

func encodeByteSlice(b []byte) sliceV {
	vals := make([]Value, len(b))
	for i, x := range b {
		vals[i] = x
	}
	sv := newSlice(vals)
	sv.b.elem = 1
	return sv
}

func decodeByteSlice(sv sliceV) []byte {
	elems := sv.elems()
	out := make([]byte, len(elems))
	for i, v := range elems {
		out[i], _ = v.(byte)
	}
	return out
}

func decodeCompanions(sv sliceV) []MapperCompanion {
	elems := sv.elems()
	out := make([]MapperCompanion, 0, len(elems))
	for _, e := range elems {
		r, ok := e.(*record)
		if !ok {
			continue
		}
		path, _ := r.fields[fieldIndex(r.t, "Path")].(string)
		bs, _ := r.fields[fieldIndex(r.t, "Bytes")].(sliceV)
		out = append(out, MapperCompanion{Path: path, Bytes: decodeByteSlice(bs)})
	}
	return out
}

func decodeMappedErgo(val Value, steps, bytesUsed int64) (*MapperOutput, *MapperEvalError) {
	rec, ok := val.(*record)
	if !ok {
		return nil, &MapperEvalError{Kind: "contract", Message: fmt.Sprintf("expected MappedErgo record, found %T", val)}
	}
	src, ok := rec.fields[fieldIndex(rec.t, "Source")].(string)
	if !ok {
		return nil, &MapperEvalError{Kind: "contract", Message: "MappedErgo.Source must be string"}
	}
	digestSlice, _ := rec.fields[fieldIndex(rec.t, "Digest")].(sliceV)
	spansRec, ok := rec.fields[fieldIndex(rec.t, "Spans")].(*record)
	if !ok {
		return nil, &MapperEvalError{Kind: "contract", Message: "MappedErgo.Spans must be SourceSpanMap record"}
	}
	entriesSlice, ok := spansRec.fields[fieldIndex(spansRec.t, "Entries")].(sliceV)
	if !ok {
		return nil, &MapperEvalError{Kind: "contract", Message: "SourceSpanMap.Entries must be a slice"}
	}
	spans := make([]MapperSpanMapping, 0, len(entriesSlice.elems()))
	for _, e := range entriesSlice.elems() {
		sr, ok := e.(*record)
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "SpanMapping must be a record"}
		}
		genSpan, ok := decodeSpan(sr.fields[fieldIndex(sr.t, "Generated")])
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "SpanMapping.Generated must be SourceSpan"}
		}
		srcSpan, ok := decodeSpan(sr.fields[fieldIndex(sr.t, "Source")])
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "SpanMapping.Source must be SourceSpan"}
		}
		hasComp, compIdx, ok := decodeOptionUint32(sr.fields[fieldIndex(sr.t, "Companion")])
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "SpanMapping.Companion must be Option[uint32]"}
		}
		label, _ := sr.fields[fieldIndex(sr.t, "Label")].(string)
		spans = append(spans, MapperSpanMapping{
			Generated:    genSpan,
			HasCompanion: hasComp,
			Companion:    compIdx,
			Source:       srcSpan,
			Label:        label,
		})
	}
	diagsSlice, ok := rec.fields[fieldIndex(rec.t, "Diags")].(sliceV)
	if !ok {
		return nil, &MapperEvalError{Kind: "contract", Message: "MappedErgo.Diags must be a slice"}
	}
	var warnType *check.Named
	if diagsField := fieldType(structOf(rec.t), "Diags"); diagsField != nil {
		if sl, ok := underlyingSlice(diagsField); ok {
			if ds := structOf(sl.Elem); ds != nil {
				if sevType, ok := fieldType(ds, "Severity").(*check.Named); ok {
					if u, ok := sevType.Underlying().(*check.Union); ok && len(u.Variants) >= 2 {
						warnType, _ = u.Variants[1].(*check.Named)
					}
				}
			}
		}
	}
	diags := make([]MapperDiag, 0, len(diagsSlice.elems()))
	for _, d := range diagsSlice.elems() {
		dr, ok := d.(*record)
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "MapperDiag must be a record"}
		}
		sevRec, ok := dr.fields[fieldIndex(dr.t, "Severity")].(*record)
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "MapperDiag.Severity must be DiagSeverity"}
		}
		warning := warnType != nil && sevRec.t != nil && sevRec.t.Obj == warnType.Obj
		hasComp, compIdx, ok := decodeOptionUint32(dr.fields[fieldIndex(dr.t, "Companion")])
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "MapperDiag.Companion must be Option[uint32]"}
		}
		sp, ok := decodeSpan(dr.fields[fieldIndex(dr.t, "Span")])
		if !ok {
			return nil, &MapperEvalError{Kind: "contract", Message: "MapperDiag.Span must be SourceSpan"}
		}
		code, _ := dr.fields[fieldIndex(dr.t, "Code")].(string)
		msg, _ := dr.fields[fieldIndex(dr.t, "Message")].(string)
		hint, _ := dr.fields[fieldIndex(dr.t, "Hint")].(string)
		diags = append(diags, MapperDiag{
			Warning:      warning,
			HasCompanion: hasComp,
			Companion:    compIdx,
			Span:         sp,
			Code:         code,
			Message:      msg,
			Hint:         hint,
		})
	}
	return &MapperOutput{
		Source:    src,
		Spans:     spans,
		Diags:     diags,
		Digest:    decodeByteSlice(digestSlice),
		Steps:     steps,
		BytesUsed: bytesUsed,
	}, nil
}

func decodeSpan(v Value) (MapperSpan, bool) {
	r, ok := v.(*record)
	if !ok {
		return MapperSpan{}, false
	}
	st, ok1 := r.fields[fieldIndex(r.t, "Start")].(uint32)
	en, ok2 := r.fields[fieldIndex(r.t, "End")].(uint32)
	return MapperSpan{Start: st, End: en}, ok1 && ok2
}

func decodeOptionUint32(v Value) (has bool, idx uint32, ok bool) {
	r, isRec := v.(*record)
	if !isRec || r.t == nil {
		return false, 0, false
	}
	s := structOf(r.t)
	if s == nil {
		return false, 0, false
	}
	if len(s.Fields) == 0 {
		return false, 0, true
	}
	if len(s.Fields) == 1 {
		u, uok := r.fields[0].(uint32)
		return true, u, uok
	}
	return false, 0, false
}
