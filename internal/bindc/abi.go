package bindc

import (
	"fmt"
	"strconv"
	"strings"
)

// A struct passed or returned by value crosses purego in its call form, a
// Go struct of the plainest field types that purego v0.11.3 marshals the
// same way the C calling convention passes the struct on every tier-1
// target (System V on amd64, AAPCS64 on arm64, Apple's variant on darwin
// arm64). The typed layer copies the Go struct into its call form byte for
// byte (toABI) and the call form of a result back (fromABI).
//
// purego marshals a struct field by field, does not add padding, and
// guesses register classes from the first fields (func.go: "it does not
// support aligning fields properly"; struct_amd64.go tryPlaceRegister and
// getStruct, struct_arm64.go placeRegistersArm64, isHFA and isHVA). Passing
// the Go struct of a C struct is therefore wrong for padded structs, for
// float fields that arm64 passes in general registers, for arrays and
// nested structs of floats, and for returns without a field at byte 8. The
// call forms below use only shapes for which those paths are exact:
//
//   - an HFA (one to four fields of one floating-point type after nesting
//     and arrays are flattened): N flat float32 or float64 fields. amd64
//     passes it in SSE registers (or memory above 16 bytes); arm64 passes
//     one member per floating-point register.
//   - words (every other struct above 16 bytes, and a struct of up to 16
//     bytes whose every eightbyte holds an integer, bool or pointer): the
//     bytes as uint64 words. amd64 passes an eightbyte with an integer in a
//     general register (the INTEGER class wins over SSE) and memory above
//     16 bytes; arm64 passes up to 16 bytes in general registers and a copy
//     by reference above 16 bytes.
//
// A struct of up to 16 bytes with an eightbyte of only floating-point
// fields that is not an HFA ({float a, b; uint32_t c}, {double d;
// uint64_t x}, {float f; double d}) has no single form: amd64 passes that
// eightbyte in an SSE register, arm64 in a general one. Its functions are
// raw-only; its raw layer uses the words form (right on arm64 only), so
// that purego counts its arguments as for any other struct.
//
// purego's RegisterFunc panics ("too many stack arguments") when a call
// needs more stack than its argument block holds. Load registers every
// function, so Bind refuses a header with such a function (puregoStack);
// the contract skips it.
//
// purego also splits a struct between the last free registers and the
// stack, which no calling convention does, and its darwin arm64 stack
// packing is defined for scalars. So a by-value struct argument must fit
// in the registers left at its place on both architectures, and on arm64
// no argument before it may be on the stack; else the function is
// raw-only. Scalars after the last struct may spill to the stack.

// abiKind is the kind of a call form.
type abiKind int

const (
	abiWords abiKind = iota
	abiHFA
)

// abiForm is the call form of a C struct.
type abiForm struct {
	Kind abiKind
	// N is the number of uint64 words (abiWords) or of float members
	// (abiHFA).
	N int
	// Float is the member type of an HFA.
	Float Scalar
	// Size is the size of the C struct.
	Size int64
	// Why says why the form is not right on every target, or "".
	Why string
}

// GoSize is the size of the Go type of the form.
func (f abiForm) GoSize() int64 {
	if f.Kind == abiWords {
		return 8 * int64(f.N)
	}
	return f.Size
}

// GoName is the name of the Go type of the form.
func (f abiForm) GoName() string {
	switch f.Kind {
	case abiWords:
		return "abiW" + strconv.Itoa(f.N)
	case abiHFA:
		return "abiF" + strconv.FormatInt(8*scalarSize[f.Float], 10) + "x" + strconv.Itoa(f.N)
	}
	return ""
}

// GoDecl is the Go declaration of the type of the form.
func (f abiForm) GoDecl() string {
	switch f.Kind {
	case abiWords:
		if f.N <= 2 {
			return "type " + f.GoName() + " struct{ " + fieldList("w", f.N) + " uint64 }"
		}
		return "type " + f.GoName() + " struct{ w [" + strconv.Itoa(f.N) + "]uint64 }"
	case abiHFA:
		return "type " + f.GoName() + " struct{ " + fieldList("f", f.N) + " " + goScalar[f.Float] + " }"
	}
	return ""
}

func fieldList(prefix string, n int) string {
	var s []string
	for i := 0; i < n; i++ {
		s = append(s, prefix+strconv.Itoa(i))
	}
	return strings.Join(s, ", ")
}

// leaf is one scalar of a struct after nesting and arrays are flattened.
type leaf struct {
	Off, Size int64
	Float     bool
	Scalar    Scalar // "" for a pointer or a callback
}

// leaves calls visit for every scalar of a value of type t at offset off.
func (k *classifier) leaves(t Type, off int64, visit func(leaf)) {
	switch t.Kind {
	case TypePointer:
		visit(leaf{Off: off, Size: pointerSize})
		return
	case TypeArray:
		es := k.sizeOf(*t.Elem)
		for i := int64(0); i < t.Len; i++ {
			k.leaves(*t.Elem, off+i*es, visit)
		}
		return
	}
	if s, ok := k.scalarOf(t); ok {
		visit(leaf{Off: off, Size: scalarSize[s], Float: s == Float32 || s == Float64, Scalar: s})
		return
	}
	if t.Kind == TypeNamed {
		d := k.types[t.Name]
		switch {
		case d == nil:
		case d.Kind == DeclCallback:
			visit(leaf{Off: off, Size: pointerSize})
		case d.Kind == DeclStruct:
			for _, f := range d.Fields {
				k.leaves(f.Type, off+f.Offset, visit)
			}
		}
	}
}

// sizeOf is the size of a value of type t.
func (k *classifier) sizeOf(t Type) int64 {
	switch t.Kind {
	case TypePointer:
		return pointerSize
	case TypeArray:
		return t.Len * k.sizeOf(*t.Elem)
	}
	if s, ok := k.scalarOf(t); ok {
		return scalarSize[s]
	}
	if t.Kind == TypeNamed {
		if d := k.types[t.Name]; d != nil {
			if d.Kind == DeclStruct {
				return d.Size
			}
			return pointerSize
		}
	}
	return 0
}

// isByValueStruct reports whether t is a struct passed or returned by value.
func (k *classifier) isByValueStruct(t Type) bool {
	if t.Kind != TypeNamed {
		return false
	}
	d := k.types[t.Name]
	return d != nil && d.Kind == DeclStruct
}

// form is the call form of the struct named name.
func (k *classifier) form(name string) abiForm {
	d := k.types[name]
	f := abiForm{Size: d.Size}
	var ls []leaf
	k.leaves(Type{Kind: TypeNamed, Name: name}, 0, func(l leaf) { ls = append(ls, l) })
	hfa := len(ls) >= 1 && len(ls) <= 4
	for _, l := range ls {
		if !l.Float || l.Scalar != ls[0].Scalar {
			hfa = false
		}
	}
	words := int((d.Size + 7) / 8)
	switch {
	case hfa:
		f.Kind, f.N, f.Float = abiHFA, len(ls), ls[0].Scalar
	case d.Size > 16:
		f.Kind, f.N = abiWords, words
	default:
		f.Kind, f.N = abiWords, words
		for e := int64(0); e < int64(words); e++ {
			integer := false
			for _, l := range ls {
				if !l.Float && l.Off < 8*e+8 && l.Off+l.Size > 8*e {
					integer = true
				}
			}
			if !integer {
				f.Why = fmt.Sprintf("by-value struct %s: bytes %d to %d hold only floating-point fields and it is not an HFA, so amd64 passes them in an SSE register and arm64 in a general one; purego v0.11.3 has no single call form for both", name, 8*e, 8*e+7)
				return f
			}
		}
	}
	return f
}

// The registers of the tier-1 targets for arguments.
const (
	amd64IntRegs, amd64FloatRegs = 6, 8
	arm64IntRegs, arm64FloatRegs = 8, 8
)

// callFormReason is the reason a function with a by-value struct argument
// or result has no typed form, or "".
func (b *Binding) callFormReason(f *Function) string {
	k := b.k
	has := k.isByValueStruct(f.Result)
	for _, p := range f.Params {
		has = has || k.isByValueStruct(p.Type)
	}
	if !has {
		return ""
	}
	if k.isByValueStruct(f.Result) {
		if r := k.form(f.Result.Name); r.Why != "" {
			return "result: " + r.Why
		}
	}
	// amd64: a result above 16 bytes is written through a hidden pointer
	// in the first general register.
	var aInt, aFloat, rInt, rFloat int
	rStack := false
	if k.isByValueStruct(f.Result) && k.types[f.Result.Name].Size > 16 {
		aInt = 1
	}
	for i, p := range f.Params {
		name := positionName(f, i)
		if !k.isByValueStruct(p.Type) {
			float := false
			if s, ok := k.scalarOf(p.Type); ok {
				float = s == Float32 || s == Float64
			}
			if float {
				aFloat++
				if rFloat < arm64FloatRegs {
					rFloat++
				} else {
					rStack = true
				}
			} else {
				aInt++
				if rInt < arm64IntRegs {
					rInt++
				} else {
					rStack = true
				}
			}
			continue
		}
		fm := k.form(p.Type.Name)
		if fm.Why != "" {
			return name + ": " + fm.Why
		}
		// amd64.
		if fm.Size <= 16 {
			ni, nf := 0, 0
			if fm.Kind == abiHFA {
				nf = int((fm.Size + 7) / 8)
			} else {
				ni = fm.N
			}
			if aInt+ni > amd64IntRegs || aFloat+nf > amd64FloatRegs {
				return fmt.Sprintf("%s: by-value struct %s does not fit the registers left on amd64, and purego v0.11.3 would split it between registers and the stack", name, p.Type.Name)
			}
			aInt, aFloat = aInt+ni, aFloat+nf
		}
		// arm64.
		if rStack {
			return fmt.Sprintf("%s: by-value struct %s comes after an argument on the stack on arm64, where purego v0.11.3 packs only scalars", name, p.Type.Name)
		}
		switch {
		case fm.Kind == abiHFA:
			if rFloat+fm.N > arm64FloatRegs {
				return fmt.Sprintf("%s: by-value struct %s does not fit the registers left on arm64", name, p.Type.Name)
			}
			rFloat += fm.N
		case fm.Size <= 16:
			if rInt+fm.N > arm64IntRegs {
				return fmt.Sprintf("%s: by-value struct %s does not fit the registers left on arm64", name, p.Type.Name)
			}
			rInt += fm.N
		default: // a copy, passed by reference
			if rInt >= arm64IntRegs {
				return fmt.Sprintf("%s: by-value struct %s does not fit the registers left on arm64", name, p.Type.Name)
			}
			rInt++
		}
	}
	return ""
}

// The argument block of purego v0.11.3 (syscall.go maxArgs on amd64 and
// arm64): RegisterFunc panics when a call needs more stack than it holds
// (func.go: maxArgs minus the integer registers, in words; on darwin
// arm64, estimateStackBytes against the same words as bytes).
const (
	puregoMaxArgs     = 32
	amd64StackWords   = puregoMaxArgs - amd64IntRegs
	arm64StackWords   = puregoMaxArgs - arm64IntRegs
	darwinStackBytes  = 8 * arm64StackWords
	puregoStackRemedy = "skip it in the contract"
)

// puregoStack is the reason purego's RegisterFunc would panic on the raw
// function of f, or "". It counts as RegisterFunc does on each target:
// scalars past the registers, an amd64 struct above 16 bytes in memory
// (and its hidden result pointer), and on darwin arm64 the bytes of every
// argument past the registers, a struct counting as one integer.
func (b *Binding) puregoStack(f *Function) string {
	k := b.k
	type arg struct {
		float  bool
		struc  bool
		form   abiForm
		goSize int64
	}
	var args []arg
	for _, p := range f.Params {
		a := arg{goSize: pointerSize}
		switch s, ok := k.scalarOf(p.Type); {
		case ok:
			a.float, a.goSize = s == Float32 || s == Float64, scalarSize[s]
		case k.isByValueStruct(p.Type):
			a.struc, a.form = true, k.form(p.Type.Name)
			a.goSize = a.form.GoSize()
		}
		args = append(args, a)
	}
	// amd64.
	ints, floats, stack := 0, 0, 0
	for _, a := range args {
		switch {
		case a.struc && a.goSize > 16:
			stack += int((a.goSize + 7) / 8)
		case a.struc && a.form.Kind == abiHFA:
			floats += int((a.goSize + 7) / 8)
		case a.struc:
			ints += a.form.N
		case a.float && floats < amd64FloatRegs:
			floats++
		case !a.float && ints < amd64IntRegs:
			ints++
		default:
			stack++
		}
	}
	if k.isByValueStruct(f.Result) && k.form(f.Result.Name).GoSize() > 16 && ints >= amd64IntRegs {
		stack++
	}
	if stack > amd64StackWords {
		return fmt.Sprintf("%s: purego v0.11.3 passes at most %d stack words on amd64, and this call needs %d; %s", f.Name, amd64StackWords, stack, puregoStackRemedy)
	}
	// arm64.
	ints, floats, stack = 0, 0, 0
	for _, a := range args {
		switch {
		case a.struc && a.form.Kind == abiHFA:
			floats += a.form.N
		case a.struc && a.goSize > 16:
			ints++
		case a.struc:
			ints += a.form.N
		case a.float && floats < arm64FloatRegs:
			floats++
		case !a.float && ints < arm64IntRegs:
			ints++
		default:
			stack++
		}
	}
	if stack > arm64StackWords {
		return fmt.Sprintf("%s: purego v0.11.3 passes at most %d stack words on arm64, and this call needs %d; %s", f.Name, arm64StackWords, stack, puregoStackRemedy)
	}
	// darwin arm64.
	ints, floats, bytes := 0, 0, int64(0)
	for _, a := range args {
		switch {
		case a.float && floats < arm64FloatRegs:
			floats++
		case !a.float && ints < arm64IntRegs:
			ints++
		default:
			bytes += a.goSize
		}
	}
	if bytes = (bytes + 7) &^ 7; bytes > darwinStackBytes {
		return fmt.Sprintf("%s: purego v0.11.3 passes at most %d stack bytes on darwin arm64, and this call needs %d; %s", f.Name, darwinStackBytes, bytes, puregoStackRemedy)
	}
	return ""
}
