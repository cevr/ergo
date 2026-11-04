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
// eightbyte in an SSE register, arm64 in a general one.
//
// purego's RegisterFunc panics ("too many stack arguments") when a call
// needs more stack than its argument block holds (puregoStack).
//
// Both conventions, and purego, count the general and the floating-point
// registers as two banks, and an argument takes registers only from its
// own banks; stack arguments take the next stack slots in order. A struct
// that the convention passes in registers needs registers of one bank
// (words: general; HFA: floating-point). When that bank has too few left,
// C passes the whole struct on the stack, but purego splits it between
// the last registers and the stack, or gives each float member of an HFA
// its own stack word. So such a struct must fit the bank it needs at its
// place on both architectures (structPlan); the other bank does not
// matter. A struct that amd64 passes in memory (above 16 bytes) and the
// pointer to the copy that arm64 passes for a non-HFA above 16 bytes take
// stack slots as scalars do, wherever they come.
//
// The raw layer registers every bound function at Load, so a call plan is
// valid on every target the package's build constraint selects, or Bind
// refuses the header (callPlan): no function, raw-only or typed, is
// emitted with a call purego would make wrongly or not at all. The
// contract skips such a function to bind the rest of the header.

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

// callPlanRemedy ends the error of a function whose call plan is invalid.
const callPlanRemedy = "skip it in the contract"

// callPlan is the reason purego v0.11.3 cannot call f as C does on every
// target of the package's build constraint (linux and darwin, amd64 and
// arm64), or "". Bind refuses a header with such a function.
func (b *Binding) callPlan(f *Function) string {
	if r := b.structPlan(f); r != "" {
		return r
	}
	return b.puregoStack(f)
}

// structPlan is the reason a by-value struct argument or result of f has
// no call form purego passes as C does on every target, or "".
func (b *Binding) structPlan(f *Function) string {
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
	// The registers used in each bank, per architecture. A scalar takes one
	// register of its bank, or a stack slot when the bank is full; either
	// way it never touches the other bank. On amd64 a result above 16 bytes
	// is written through a hidden pointer in the first general register.
	amd64 := regBanks{arch: "amd64", cap: [2]int{amd64IntRegs, amd64FloatRegs}}
	arm64 := regBanks{arch: "arm64", cap: [2]int{arm64IntRegs, arm64FloatRegs}}
	if k.isByValueStruct(f.Result) && k.types[f.Result.Name].Size > 16 {
		amd64.used[bankInt] = 1
	}
	for i, p := range f.Params {
		name := positionName(f, i)
		if !k.isByValueStruct(p.Type) {
			bank := bankInt
			if s, ok := k.scalarOf(p.Type); ok && (s == Float32 || s == Float64) {
				bank = bankFloat
			}
			amd64.used[bank]++
			arm64.used[bank]++
			continue
		}
		fm := k.form(p.Type.Name)
		if fm.Why != "" {
			return name + ": " + fm.Why
		}
		// amd64: up to 16 bytes in registers of one bank (an HFA takes one
		// SSE register per eightbyte), else in memory, on the stack.
		if fm.Size <= 16 {
			bank, n := bankInt, fm.N
			if fm.Kind == abiHFA {
				bank, n = bankFloat, int((fm.Size+7)/8)
			}
			if r := amd64.take(bank, n); r != "" {
				return fmt.Sprintf("%s: by-value struct %s %s", name, p.Type.Name, r)
			}
		}
		// arm64: an HFA in one floating-point register per member, up to
		// 16 other bytes in general registers, else a pointer to a copy,
		// which takes a general register or a stack slot as an integer.
		switch {
		case fm.Kind == abiHFA:
			if r := arm64.take(bankFloat, fm.N); r != "" {
				return fmt.Sprintf("%s: by-value struct %s %s", name, p.Type.Name, r)
			}
		case fm.Size <= 16:
			if r := arm64.take(bankInt, fm.N); r != "" {
				return fmt.Sprintf("%s: by-value struct %s %s", name, p.Type.Name, r)
			}
		default:
			arm64.used[bankInt]++
		}
	}
	return ""
}

// The register banks of an argument list.
const (
	bankInt = iota
	bankFloat
)

var bankName = [2]string{"general", "floating-point"}

// regBanks counts the registers an argument list has used in each bank
// of one architecture; a count past the bank's size means the bank is
// full and the rest went to the stack.
type regBanks struct {
	arch string
	cap  [2]int
	used [2]int
}

// take gives a struct n registers of bank, or says why it cannot: with
// fewer left, C passes the struct on the stack, which purego v0.11.3 does
// not do as C does.
func (r *regBanks) take(bank, n int) string {
	left := max(0, r.cap[bank]-r.used[bank])
	if n > left {
		return fmt.Sprintf("needs %d %s registers on %s and %d are left, so C passes it on the stack, which purego v0.11.3 does not do as C does (it splits a struct between registers and the stack)",
			n, bankName[bank], r.arch, left)
	}
	r.used[bank] += n
	return ""
}

// The argument block of purego v0.11.3 (syscall.go maxArgs on amd64 and
// arm64): RegisterFunc panics when a call needs more stack than it holds
// (func.go: maxArgs minus the integer registers, in words; on darwin
// arm64, estimateStackBytes against the same words as bytes).
const (
	puregoMaxArgs    = 32
	amd64StackWords  = puregoMaxArgs - amd64IntRegs
	arm64StackWords  = puregoMaxArgs - arm64IntRegs
	darwinStackBytes = 8 * arm64StackWords
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
		return fmt.Sprintf("purego v0.11.3 passes at most %d stack words on amd64, and this call needs %d", amd64StackWords, stack)
	}
	// arm64.
	ints, floats, stack = 0, 0, 0
	for _, a := range args {
		switch {
		case a.struc && a.form.Kind == abiHFA:
			floats += a.form.N
		case a.struc && a.goSize > 16 && ints < arm64IntRegs: // a pointer to a copy
			ints++
		case a.struc && a.goSize > 16:
			stack++
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
		return fmt.Sprintf("purego v0.11.3 passes at most %d stack words on arm64, and this call needs %d", arm64StackWords, stack)
	}
	// darwin arm64.
	ints, floats, bytes := 0, 0, int64(0)
	for _, a := range args {
		switch {
		case a.float && floats < arm64FloatRegs:
			floats++
		case !a.float && ints < arm64IntRegs:
			ints++
		case a.struc && a.form.Kind != abiHFA && a.goSize > 16: // a pointer to a copy
			bytes += pointerSize
		default:
			bytes += a.goSize
		}
	}
	if bytes = (bytes + 7) &^ 7; bytes > darwinStackBytes {
		return fmt.Sprintf("purego v0.11.3 passes at most %d stack bytes on darwin arm64, and this call needs %d", darwinStackBytes, bytes)
	}
	return ""
}
