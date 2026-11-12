package v1

import "unsafe"

// Owned forms (Body.Owned): bodies that take their first argument over.
// The lowering calls one only where that argument is consumed, so no Ergo
// binding reads its storage after the call (docs/spec/types.md, Bindings
// 2, consumed arguments). Each body that uses unsafe is listed, with its
// invariant and review, in internal/residue/unsafe.txt (owner q112:
// reviewed unsafe bodies in runtime/v1 only).

// StringsBuilderTextOwned is StringsBuilderText with no copy: the text
// shares buf's bytes, as Go's strings.Builder.String shares its buffer.
// Invariant: no code writes buf[0:len(buf)] after the call. The caller
// passes a consumed buffer, and no Ergo operation writes a slice's bytes
// below its length except through a binding that holds it (a Builder's
// writes append past the length, or copy on growth).
func StringsBuilderTextOwned(buf []byte) string {
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}
