package bindc

import (
	"strconv"
	"strings"
)

// Model is the binding model of one C header: every fact the header states,
// in declaration order, with the layout of every struct. It holds no
// positions and no comments, so a model printed and parsed again is equal to
// itself (see Print).
type Model struct {
	// Header is the base name of the header file.
	Header string `json:"header"`
	// Guard is the include guard macro, or "" when the header has none.
	Guard string `json:"guard,omitempty"`
	// CPlusPlus is true when the declarations sit in the block
	// `#ifdef __cplusplus` / `extern "C" {` / `#endif` and its close.
	CPlusPlus bool `json:"cplusplus,omitempty"`
	// Includes are the standard headers the header includes, in order:
	// "stdint.h", "stdbool.h" or "stddef.h".
	Includes  []string   `json:"includes"`
	Constants []Constant `json:"constants"`
	Types     []TypeDecl `json:"types"`
	Functions []Function `json:"functions"`
}

// Scalar is a C scalar type of the subset, named by its C spelling.
type Scalar string

// The scalar types of the subset.
const (
	Int8    Scalar = "int8_t"
	Int16   Scalar = "int16_t"
	Int32   Scalar = "int32_t"
	Int64   Scalar = "int64_t"
	Uint8   Scalar = "uint8_t"
	Uint16  Scalar = "uint16_t"
	Uint32  Scalar = "uint32_t"
	Uint64  Scalar = "uint64_t"
	Bool    Scalar = "bool"
	Float32 Scalar = "float"
	Float64 Scalar = "double"
)

// scalarSize holds the size of each scalar on every tier-1 target (linux and
// darwin on amd64 and arm64); the alignment of a scalar equals its size.
var scalarSize = map[Scalar]int64{
	Int8: 1, Int16: 2, Int32: 4, Int64: 8,
	Uint8: 1, Uint16: 2, Uint32: 4, Uint64: 8,
	Bool: 1, Float32: 4, Float64: 8,
}

// scalarHeader names the standard header that declares a scalar type.
var scalarHeader = map[Scalar]string{
	Int8: "stdint.h", Int16: "stdint.h", Int32: "stdint.h", Int64: "stdint.h",
	Uint8: "stdint.h", Uint16: "stdint.h", Uint32: "stdint.h", Uint64: "stdint.h",
	Bool: "stdbool.h",
}

// pointerSize is the size and alignment of a data or function pointer on
// every tier-1 target.
const pointerSize = 8

// Constant is an object-like macro whose body is an integer constant
// expression of the subset. Type is the C type of the expression after the
// usual conversions (int32_t for int, uint32_t for unsigned int, int64_t
// for long and long long, uint64_t for their unsigned forms; the tier-1
// targets are LP64). Bits holds the value in two's complement, truncated
// to the width of Type. Expr is the body in canonical spelling.
type Constant struct {
	Name string
	Type Scalar
	Bits uint64
	Expr string
}

// Int64 is the value of a signed constant.
func (c Constant) Int64() int64 {
	if c.Type == Int32 {
		return int64(int32(uint32(c.Bits)))
	}
	return int64(c.Bits)
}

// Uint64 is the value of an unsigned constant.
func (c Constant) Uint64() uint64 { return c.Bits }

// Signed reports whether the constant's type is signed.
func (c Constant) Signed() bool { return c.Type == Int32 || c.Type == Int64 }

// Value is the decimal value of the constant.
func (c Constant) Value() string {
	if c.Signed() {
		return strconv.FormatInt(c.Int64(), 10)
	}
	return strconv.FormatUint(c.Bits, 10)
}

// DeclKind is the kind of a type declaration.
type DeclKind string

// The type declarations of the subset.
const (
	// DeclScalar is `typedef <scalar> name;`.
	DeclScalar DeclKind = "scalar"
	// DeclOpaque is `typedef struct tag name;` with no definition.
	DeclOpaque DeclKind = "opaque"
	// DeclStruct is `typedef struct [tag] { fields } name;`.
	DeclStruct DeclKind = "struct"
	// DeclCallback is `typedef R (*name)(params);`.
	DeclCallback DeclKind = "callback"
)

// TypeDecl is one typedef. The fields used depend on Kind.
type TypeDecl struct {
	Kind DeclKind `json:"kind"`
	Name string   `json:"name"`
	// Tag is the struct tag (opaque and struct; "" for an untagged struct).
	Tag string `json:"tag,omitempty"`
	// Scalar is the underlying type of a scalar typedef.
	Scalar Scalar `json:"scalar,omitempty"`
	// Fields, Size and Align describe a struct.
	Fields []Field `json:"fields,omitempty"`
	Size   int64   `json:"size,omitempty"`
	Align  int64   `json:"align,omitempty"`
	// Result and Params describe a callback.
	Result *Type   `json:"result,omitempty"`
	Params []Param `json:"params,omitempty"`
}

// Field is a struct field with its layout.
type Field struct {
	Name   string `json:"name"`
	Type   Type   `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	Align  int64  `json:"align"`
}

// Function is a function prototype. Params is nil for `(void)`.
type Function struct {
	Name   string  `json:"name"`
	Result Type    `json:"result"`
	Params []Param `json:"params"`
}

// Param is a parameter; Name is "" for an unnamed parameter.
type Param struct {
	Name string `json:"name,omitempty"`
	Type Type   `json:"type"`
}

// TypeKind is the kind of a type use.
type TypeKind string

// The type uses of the subset.
const (
	TypeVoid    TypeKind = "void"
	TypeScalar  TypeKind = "scalar"
	TypeNamed   TypeKind = "named"
	TypePointer TypeKind = "pointer"
	TypeArray   TypeKind = "array"
)

// Type is a use of a type in a field, a parameter or a result.
//
// Const qualifies a void, scalar or named type that is the target of a
// pointer (`const T *`); the subset has no other qualifier position.
// An array has an element type and a length; LenExpr is the spelling of
// the length (a literal or the name of an earlier constant). A parameter
// declared as an array, `const uint16_t color[4]`, is a pointer (C11
// 6.7.6.3p7) that keeps the declared Len and LenExpr.
type Type struct {
	Kind    TypeKind `json:"kind"`
	Scalar  Scalar   `json:"scalar,omitempty"`
	Name    string   `json:"name,omitempty"`
	Const   bool     `json:"const,omitempty"`
	Elem    *Type    `json:"elem,omitempty"`
	Len     int64    `json:"len,omitempty"`
	LenExpr string   `json:"len_expr,omitempty"`
}

// IsPointer reports whether t is a data pointer.
func (t Type) IsPointer() bool { return t.Kind == TypePointer }

// Spell is the C spelling of t as an abstract declarator, as in a cast:
// "uint32_t", "const ot_handle *", "ot_context **", "uint16_t[4]",
// "uint8_t *[2]".
func (t Type) Spell() string { return t.Declare("") }

// Declare is the C declaration of name with type t: "const ot_handle *node",
// "uint16_t foreground[4]". With name "" it is the abstract declarator.
func (t Type) Declare(name string) string {
	if t.Kind == TypePointer && t.LenExpr != "" {
		return t.Elem.Declare(name) + "[" + t.LenExpr + "]"
	}
	base, suffix := t.split()
	ptrs := ""
	for p := base; p.Kind == TypePointer; p = *p.Elem {
		ptrs += "*"
	}
	inner := base
	for inner.Kind == TypePointer {
		inner = *inner.Elem
	}
	var b strings.Builder
	if inner.Const {
		b.WriteString("const ")
	}
	b.WriteString(inner.baseName())
	if ptrs != "" {
		b.WriteString(" ")
		b.WriteString(ptrs)
		b.WriteString(name)
	} else if name != "" {
		b.WriteString(" ")
		b.WriteString(name)
	}
	b.WriteString(suffix)
	return b.String()
}

// split separates an array type into its element and the "[N]" suffix.
func (t Type) split() (Type, string) {
	if t.Kind != TypeArray {
		return t, ""
	}
	return *t.Elem, "[" + t.LenExpr + "]"
}

func (t Type) baseName() string {
	switch t.Kind {
	case TypeVoid:
		return "void"
	case TypeScalar:
		return string(t.Scalar)
	case TypeNamed:
		return t.Name
	}
	return "?"
}
