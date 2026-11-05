// Package rawimports is the fixture of TestLoweringImportsNoReflection: the
// round 5 verify's reflection and unsafe escapes (R5 M1) read a raw list
// with no field identifier, so the guard refuses their imports.
package rawimports

import (
	"reflect"
	"unsafe"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// caught: reflection reaches Tests by name.
func escReflection(p *check.Package) []*check.Test {
	return reflect.ValueOf(p).Elem().FieldByName("Tests").Interface().([]*check.Test)
}

// caught: unsafe reads Files through a struct of the same layout.
func escUnsafe(u *lower.Unit) []*syntax.File {
	return (*struct {
		Package *check.Package
		Files   []*syntax.File
		Sources []lower.Source
		Imports []lower.Unit
	})(unsafe.Pointer(u)).Files
}
