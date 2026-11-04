package bindc

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A nameScope gives the names of one scope of a generated file: a
// package, the parameters and locals of one function, or the fields and
// methods of one struct. In a scope every name is unique, and no name is
// reserved or taken in a scope around it, so no generated name shadows or
// clashes with another. The emitters of the Go package and of the Ergo
// text both allocate their names here.
//
// A fixed name is one the binding's API derives from C and must keep (an
// exported type, function or field). claim takes it, or fails with an
// error that names both C sources: Bind refuses the binding. A free name is
// one the API does not expose by spelling (a parameter, an unexported
// field). alloc always gives one: the wanted name when it is free, else
// the name with escape applied while it is reserved, then with 2, 3, ...
// appended until it is free.
type nameScope struct {
	parent *nameScope
	// reserved reports a name the scope may not give (keywords, generated
	// helpers, generated methods).
	reserved func(string) bool
	// reservedAs names the owner of a reserved name in an error; ""
	// gives "a generated name".
	reservedAs string
	// escape turns a reserved name into another candidate; nil appends "_".
	escape func(string) string
	// label names the kind of name in an error: "Go name".
	label string
	// owner maps each name given in this scope to its source. claim is its
	// one writer: a name enters a scope only after claim has checked it
	// against the reserved names and the owners of this scope and the
	// scopes around it, so no name is seeded or overwritten.
	// TestNameScopeOneWriter fails when another function of the package
	// writes it.
	owner map[string]string
}

func newNameScope(parent *nameScope, label string, reserved func(string) bool) *nameScope {
	return &nameScope{parent: parent, label: label, reserved: reserved}
}

// taken reports whether n is reserved or given in s or a scope around it,
// and who has it.
func (s *nameScope) taken(n string) (string, bool) {
	for x := s; x != nil; x = x.parent {
		if o, ok := x.owner[n]; ok {
			return o, true
		}
		if x.reserved != nil && x.reserved(n) {
			if x.reservedAs != "" {
				return x.reservedAs, true
			}
			return "a generated name", true
		}
	}
	return "", false
}

// claim gives the fixed name n to source, a C identity such as
// "function fx_load". It fails when n is taken or reserved. It is the only
// function that adds a name to a scope.
func (s *nameScope) claim(n, source string) error {
	if o, ok := s.taken(n); ok {
		return fmt.Errorf("%s and %s have the %s %s", o, source, s.label, n)
	}
	if s.owner == nil {
		s.owner = map[string]string{}
	}
	s.owner[n] = source
	return nil
}

// mustClaim claims a name that the emitter itself declares; a refusal is
// a fault of the emitter, not of the header.
func (s *nameScope) mustClaim(n, source string) {
	if err := s.claim(n, source); err != nil {
		panic("bindc: " + err.Error())
	}
}

// alloc gives source a free name derived from want, which is not "".
func (s *nameScope) alloc(want, source string) string {
	escape := s.escape
	if escape == nil {
		escape = func(n string) string { return n + "_" }
	}
	n := want
	for s.isReserved(n) {
		n = escape(n)
	}
	base := n
	for k := 2; ; k++ {
		if _, ok := s.taken(n); !ok {
			break
		}
		n = base + strconv.Itoa(k)
		if s.isReserved(n) {
			// abiW plus a number is a call form: "abiW_2".
			n = base + "_" + strconv.Itoa(k)
		}
	}
	// n is free, so the claim cannot fail.
	s.mustClaim(n, source)
	return n
}

// isReserved reports whether n is reserved in s or a scope around it.
func (s *nameScope) isReserved(n string) bool {
	for x := s; x != nil; x = x.parent {
		if x.reserved != nil && x.reserved(n) {
			return true
		}
	}
	return false
}

// The inventory of the names the Go emitter (emitgo.go) declares. Every
// scope of the Go package reserves its part, so a name from C never meets
// one of them. TestGoNameInventory checks that the emitted packages
// declare no unexported package-level name, local or method outside it.
var (
	// goPackageHelpers are the unexported package-level names and the
	// imported packages of the emitted file.
	goPackageHelpers = map[string]bool{
		"ready": true, "statusError": true, "sliceData": true, "toABI": true, "fromABI": true,
		"loadMu": true, "loaded": true, "symbols": true,
		// The callbacks (emitgo.go, callbacks): the registry, its lock and
		// helpers, the token counter and the trampolines' C pointers.
		"cbMu": true, "cbNext": true, "cbPtr": true, "cbFuncs": true, "cbEntry": true, "cbMake": true, "cbNew": true,
		"cbToken": true, "cbPut": true, "cbUndo": true, "cbDrop": true, "cbDropOwner": true, "cbOther": true, "cbDropOf": true,
		// The panic containment, the thread id and the Owner (emitthread.go).
		"cbPendMu": true, "cbPendN": true, "cbPendOn": true, "cbContain": true, "cbPending": true, "cbPendingHere": true,
		"cbTake": true, "cbTakeHere": true, "cbThread": true, "ownerJob": true, "ownerServe": true, "ownerRun": true, "ownerHere": true,
		"strconv": true, "atomic": true, "sync": true, "errors": true, "fmt": true, "runtime": true, "unsafe": true, "purego": true,
		"os": true, "debug": true, "syscall": true,
	}
	// goExported are the exported package-level names that come from no
	// declaration of the header.
	goExported = []string{"Status", "Load", "ABIMismatchError", "ErrNotLoaded", "ErrDestroyed", "ErrTooLong",
		"LiveCallbacks", "CallbackSlotsError", "CallbackPanic", "Owner", "NewOwner", "ErrOwnerClosed", "ErrOwnerThread",
		"ErrWrongThread", "ErrKeyInUse"}
	// goLocals are the locals of a typed function's body (_p, the pending
	// panic) and of a trampoline (_e, the registry entry; _ok, the Go
	// function returned).
	goLocals = map[string]bool{"err": true, "_r": true, "_e": true, "_p": true, "_ok": true, "_t": true}
	// goMethods are the methods the emitter gives a struct of the header.
	goMethods = map[string]bool{"fill": true}
	// goPredeclared are Go's predeclared identifiers.
	goPredeclared = map[string]bool{}
)

func init() {
	for _, n := range strings.Fields(`bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64
		rune string uint uint8 uint16 uint32 uint64 uintptr any comparable true false iota nil append cap clear close
		complex copy delete imag len make max min new panic print println real recover`) {
		goPredeclared[n] = true
	}
}

// hasNumber reports whether n is prefix followed by a decimal number.
func hasNumber(n, prefix string) bool {
	rest, ok := strings.CutPrefix(n, prefix)
	return ok && rest != "" && strings.Trim(rest, "0123456789") == ""
}

// isGoPackageHelper reports whether n is an unexported package-level name
// of the emitted file other than a raw function: goPackageHelpers, or a
// call form abiW<n>, abiF32x<n> or abiF64x<n> (abi.go). Bind claims the
// name c_<C name> of every raw function (rawName) in the package scope.
// An escape must reach a name that is not reserved: every reserved name
// with "_" appended is free.
func isGoPackageHelper(n string) bool {
	return goPackageHelpers[n] || hasNumber(n, "abiW") || hasNumber(n, "abiF32x") || hasNumber(n, "abiF64x") ||
		hasNumber(n, "cbMap") || hasNumber(n, "cbTramp") || hasNumber(n, "cbKey")
}

// isGoLocal reports whether n is a local of a typed body or a trampoline:
// goLocals, an out variable _o<i>, a loop variable _i<depth>, the locals
// of the callback parameter i of a typed function (_c<i> its C pointer,
// _k<i> its token, _s<i> and _h<i> the entry it replaced), or an argument
// _a<i> and a Go result _v<i> of a trampoline.
func isGoLocal(n string) bool {
	if goLocals[n] {
		return true
	}
	for _, p := range []string{"_o", "_i", "_c", "_k", "_s", "_h", "_a", "_v"} {
		if hasNumber(n, p) {
			return true
		}
	}
	return false
}

// isGoWord reports whether n is a Go keyword or a predeclared identifier.
func isGoWord(n string) bool { return goKeywords[n] || goPredeclared[n] || n == "_" }

// goPackageScope is the scope of the package-level names of the Go package.
func goPackageScope() *nameScope {
	s := newNameScope(nil, "Go name", func(n string) bool { return isGoWord(n) || isGoPackageHelper(n) })
	for _, n := range goExported {
		s.mustClaim(n, "the generated package")
	}
	return s
}

// goFuncScope is the scope of the parameters and locals of one typed
// function.
func goFuncScope(pkg *nameScope) *nameScope {
	return newNameScope(pkg, "Go name", func(n string) bool { return isGoWord(n) || isGoLocal(n) })
}

// goStructScope is the scope of the fields and methods of one struct.
func goStructScope() *nameScope {
	return newNameScope(nil, "Go name", func(n string) bool { return isGoWord(n) || goMethods[n] })
}

// The Ergo text has two scopes that can collide: the package (the import,
// the unions and their variants, the records and the externs) and the
// parameters of one extern. A field of a record has its struct's Go name:
// the Go struct scope makes it unique, and Ergo reserves no capitalized
// word. An Ergo name avoids what Ergo reserves: its keywords, its reserved
// words (every Go keyword is one or the other) and its predeclared names.
// It does not avoid Go's predeclared names: the checker binds an Ergo name
// by Ergo's rules, and lowering gives every Go name from the binding's
// identity (internal/sketch/lower, names.go), so an extern's parameter
// names never reach Go.

// ergoPredeclared returns the names the Ergo checker predeclares for every
// package, which a declaration or a parameter may not take (D-D3, "a
// prelude name cannot be redeclared"): the universe names of D-D3's list,
// and the exported names of the prelude's scope.
var ergoPredeclared = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	// The universe names of internal/sketch/check (check.go universe and
	// bindings.go predeclaredNames, without notPreludeNames). The checker
	// does not export them; TestErgoPredeclaredMatchesChecker compares
	// this set with the checker's refusals.
	for _, n := range strings.Fields(`bool string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64
		float32 float64 duration byte rune comparable ordered number true false len Map`) {
		m[n] = true
	}
	// The prelude's names, read from its source as the checker reads it:
	// every exported top-level name, except a function whose first
	// parameter is a slice or a Map, which is an operation of that type
	// and not a name in scope (D-D8).
	for _, src := range std.Prelude() {
		f, err := syntax.Parse(src)
		if err != nil {
			panic("bindc: the prelude does not parse: " + err.Error())
		}
		add := func(id *syntax.Ident) {
			if id != nil && isExported(id.Name) {
				m[id.Name] = true
			}
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.TypeDecl:
				add(d.Name)
				if u, ok := d.Spec.(*syntax.InterfaceType); ok {
					for _, v := range u.Union {
						if v, ok := v.(*syntax.VariantSpec); ok {
							add(v.Name)
						}
					}
				}
			case *syntax.FuncDecl:
				if !isCollectionOp(d) {
					add(d.Name)
				}
			case *syntax.ContextDecl:
				add(d.Name)
			case *syntax.VarDecl:
				for _, s := range d.Specs {
					for _, id := range s.Names {
						add(id)
					}
				}
			}
		}
	}
	return m
})

// isCollectionOp reports whether the first parameter of f is a slice or a
// Map.
func isCollectionOp(f *syntax.FuncDecl) bool {
	if f.Type == nil || f.Type.Params == nil || len(f.Type.Params.List) == 0 {
		return false
	}
	switch t := f.Type.Params.List[0].Type.(type) {
	case *syntax.ArrayType:
		return t.Len == nil
	case *syntax.MapType:
		return true
	}
	return false
}

func isExported(n string) bool { return n != "" && n[0] >= 'A' && n[0] <= 'Z' }

// isErgoReserved reports whether n is an Ergo keyword, an Ergo reserved
// word (syntax.IsKeyword holds both) or a predeclared Ergo name.
func isErgoReserved(n string) bool {
	return syntax.IsKeyword(n) || ergoPredeclared()[n] || n == "_"
}

// ergoPackageScope is the scope of the package-level names of the Ergo
// text. It claims the import's name first, then the names the Ergo emitter
// declares for every binding, like every other name: an import whose name
// Ergo reserves, or that a generated name takes, is refused. It returns
// the refusals; the scope holds only the names it could claim.
func ergoPackageScope(importPath string) (*nameScope, []string) {
	s := newNameScope(nil, "Ergo name", isErgoReserved)
	s.reservedAs = "an Ergo keyword or predeclared name"
	var errs []string
	imp, source := importName(importPath), "the import of "+importPath
	if !isGoIdent(imp) {
		errs = append(errs, fmt.Sprintf("%s: Ergo name %q is not an identifier; choose another go.import_path", source, imp))
	} else if err := s.claim(imp, source); err != nil {
		errs = append(errs, err.Error()+"; choose another go.import_path")
	}
	for _, g := range ergoGenerated {
		if err := s.claim(g.name, g.source); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return s, errs
}

// importName is the name an import binds: the last element of its path
// (internal/sketch/check, lastElement).
func importName(importPath string) string {
	return importPath[strings.LastIndexByte(importPath, '/')+1:]
}

// ergoGenerated are the package-level names the Ergo emitter declares for
// every binding, with their sources, in the order the scope claims them.
var ergoGenerated = []struct{ name, source string }{
	{"Status", "the Status union"}, {"CallError", "the CallError union"},
	{"NotLoaded", "a variant of the CallError union"}, {"Destroyed", "a variant of the CallError union"},
	{"TooLong", "a variant of the CallError union"},
}

// ergoFuncScope is the scope of the parameters of one extern. A reserved
// name gets "Arg", a taken one a number.
func ergoFuncScope(pkg *nameScope) *nameScope {
	s := newNameScope(pkg, "Ergo name", isErgoReserved)
	s.escape = func(n string) string { return n + "Arg" }
	return s
}
