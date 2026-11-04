package bindc

import (
	"fmt"
	"strconv"
	"strings"
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
	// escape turns a reserved name into another candidate; nil appends "_".
	escape func(string) string
	// label names the kind of name in an error: "Go name".
	label string
	owner map[string]string
}

func newNameScope(parent *nameScope, label string, reserved func(string) bool) *nameScope {
	return &nameScope{parent: parent, label: label, reserved: reserved, owner: map[string]string{}}
}

// taken reports whether n is reserved or given in s or a scope around it,
// and who has it.
func (s *nameScope) taken(n string) (string, bool) {
	for x := s; x != nil; x = x.parent {
		if o, ok := x.owner[n]; ok {
			return o, true
		}
		if x.reserved != nil && x.reserved(n) {
			return "a generated name", true
		}
	}
	return "", false
}

// claim gives the fixed name n to source, a C identity such as
// "function fx_load". It fails when n is taken or reserved.
func (s *nameScope) claim(n, source string) error {
	if o, ok := s.taken(n); ok {
		return fmt.Errorf("%s and %s have the %s %s", o, source, s.label, n)
	}
	s.owner[n] = source
	return nil
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
	s.owner[n] = source
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
		"strconv": true, "atomic": true, "sync": true, "errors": true, "fmt": true, "runtime": true, "unsafe": true, "purego": true,
	}
	// goExported are the exported package-level names that come from no
	// declaration of the header.
	goExported = []string{"Status", "Load", "ABIMismatchError", "ErrNotLoaded", "ErrDestroyed", "ErrTooLong"}
	// goLocals are the locals of a typed function's body.
	goLocals = map[string]bool{"err": true, "_r": true}
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
	return goPackageHelpers[n] || hasNumber(n, "abiW") || hasNumber(n, "abiF32x") || hasNumber(n, "abiF64x")
}

// isGoLocal reports whether n is a local of a typed body: goLocals, an out
// variable _o<i> or a loop variable _i<depth>.
func isGoLocal(n string) bool {
	return goLocals[n] || hasNumber(n, "_o") || hasNumber(n, "_i")
}

// isGoWord reports whether n is a Go keyword or a predeclared identifier.
func isGoWord(n string) bool { return goKeywords[n] || goPredeclared[n] || n == "_" }

// goPackageScope is the scope of the package-level names of the Go package.
func goPackageScope() *nameScope {
	s := newNameScope(nil, "Go name", func(n string) bool { return isGoWord(n) || isGoPackageHelper(n) })
	for _, n := range goExported {
		s.owner[n] = "the generated package"
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
