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
// The v1 admission rule for by-value structs (orchestrator amendment A6,
// 2026-11-04): a function with a by-value struct argument or result is
// bound only when, on every target the build constraint selects
// (abiTargets), every argument and the result travel in registers (no
// argument of the call goes on the stack, and each struct is passed in
// registers under that target's rules: System V classes on amd64, an HFA
// or up to 16 bytes on arm64). Else Bind refuses it with the targets and
// the first position that is not in registers on each (structPlan). purego
// v0.11.3 marshals a struct on the stack differently per target, and on
// darwin arm64 not as C does (it copies the bytes of a struct above 16
// bytes inline after a stack argument instead of a pointer to a copy);
// earlier rules that modelled the conventions' stack cases one by one
// missed that. Calls of scalars and pointers only are not affected:
// purego passes them on the stack as C does on every target.
//
// purego's RegisterFunc panics ("too many stack arguments") when a call
// needs more stack than its argument block holds (puregoStack).
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

// The ends of the error of a function whose call plan is invalid: every
// such error ends with callPlanRemedy, and the error of a by-value struct
// in a call that does not travel in registers on every target ends with
// stackStructRemedy (A6).
const (
	callPlanRemedy    = "skip it in the contract"
	stackStructRemedy = "a by-value struct in a call that uses the stack is not supported in v1; " + callPlanRemedy + " or pass a pointer"
)

// abiTarget is one target of the package's build constraint, with the
// number of argument registers of its calling convention in each bank:
// System V on amd64 (6 general, 8 SSE), AAPCS64 on arm64 (8 general, 8
// floating-point), which Apple's arm64 convention keeps for registers.
type abiTarget struct {
	goos, goarch string
	regs         [2]int
}

func (t abiTarget) String() string { return t.goos + "/" + t.goarch }

// abiTargets are the targets of "(linux || darwin) && (amd64 || arm64)",
// the build constraint of the Go package (emitgo.go).
var abiTargets = []abiTarget{
	{"linux", "amd64", [2]int{amd64IntRegs, amd64FloatRegs}},
	{"linux", "arm64", [2]int{arm64IntRegs, arm64FloatRegs}},
	{"darwin", "amd64", [2]int{amd64IntRegs, amd64FloatRegs}},
	{"darwin", "arm64", [2]int{arm64IntRegs, arm64FloatRegs}},
}

// The register banks of an argument list.
const (
	bankInt = iota
	bankFloat
)

var bankName = [2]string{"general", "floating-point"}

// callPlan is the error text of a function whose call purego v0.11.3
// cannot make as C does on every target of the package's build constraint
// (linux and darwin, amd64 and arm64), without "function NAME: ", or "".
// Bind refuses a header with such a function.
func (b *Binding) callPlan(f *Function) string {
	k := b.k
	has := k.isByValueStruct(f.Result)
	for _, p := range f.Params {
		has = has || k.isByValueStruct(p.Type)
	}
	if has {
		return b.structPlan(f)
	}
	if r := b.puregoStack(f); r != "" {
		return r + "; " + callPlanRemedy
	}
	return ""
}

// structPlan is the error text of a function f with a by-value struct
// argument or result when a struct has no call form, or when an argument
// or the result does not travel in registers on a target, or "".
func (b *Binding) structPlan(f *Function) string {
	k := b.k
	if k.isByValueStruct(f.Result) {
		if r := k.form(f.Result.Name); r.Why != "" {
			return "result: " + r.Why + "; " + callPlanRemedy
		}
	}
	for i, p := range f.Params {
		if k.isByValueStruct(p.Type) {
			if fm := k.form(p.Type.Name); fm.Why != "" {
				return positionName(f, i) + ": " + fm.Why + "; " + callPlanRemedy
			}
		}
	}
	// The targets on which a position is not in registers, grouped by the
	// position and its reason, in the order of abiTargets.
	var whys []string
	on := map[string][]string{}
	for _, t := range abiTargets {
		if r := k.registers(t, f); r != "" {
			if on[r] == nil {
				whys = append(whys, r)
			}
			on[r] = append(on[r], t.String())
		}
	}
	if len(whys) == 0 {
		return ""
	}
	var s []string
	for _, r := range whys {
		s = append(s, "on "+strings.Join(on[r], ", ")+": "+r)
	}
	return strings.Join(s, "; ") + "; " + stackStructRemedy
}

// registers is "" when every argument and the result of f travel in
// registers on target t, else the first position that does not, and why.
func (k *classifier) registers(t abiTarget, f *Function) string {
	if k.isByValueStruct(f.Result) {
		if _, _, why := k.structRegs(t, f.Result.Name, true); why != "" {
			return "result: " + why
		}
	}
	var used [2]int
	for i, p := range f.Params {
		bank, n := bankInt, 1
		what := "it"
		if k.isByValueStruct(p.Type) {
			var why string
			if bank, n, why = k.structRegs(t, p.Type.Name, false); why != "" {
				return positionName(f, i) + ": " + why
			}
			what = "by-value struct " + p.Type.Name
		} else if s, ok := k.scalarOf(p.Type); ok && (s == Float32 || s == Float64) {
			bank = bankFloat
		}
		if left := t.regs[bank] - used[bank]; n > left {
			return fmt.Sprintf("%s: %s needs %d %s registers and %d are left, so it goes on the stack",
				positionName(f, i), what, n, bankName[bank], left)
		}
		used[bank] += n
	}
	return ""
}

// structRegs is the bank and the number of registers in which target t
// passes (or, for a result, returns) the struct named name, or why it does
// not pass it in registers. The struct has a call form (form's Why is "").
func (k *classifier) structRegs(t abiTarget, name string, result bool) (bank, n int, why string) {
	fm := k.form(name)
	switch t.goarch {
	case "amd64":
		// System V: above 16 bytes the class is MEMORY; else one register
		// per eightbyte, SSE for an HFA, general for words (the INTEGER
		// class wins over SSE in an eightbyte).
		if fm.Size > 16 {
			verb := "passes"
			if result {
				verb = "returns"
			}
			return 0, 0, fmt.Sprintf("by-value struct %s is %d bytes, so amd64 %s it in memory", name, fm.Size, verb)
		}
		if fm.Kind == abiHFA {
			return bankFloat, int((fm.Size + 7) / 8), ""
		}
		return bankInt, fm.N, ""
	case "arm64":
		// AAPCS64: an HFA in one floating-point register per member; any
		// other struct of up to 16 bytes in one general register per
		// eightbyte; above 16 bytes a pointer to a copy (an argument) or
		// memory at the address in x8 (a result).
		if fm.Kind == abiHFA {
			return bankFloat, fm.N, ""
		}
		if fm.Size > 16 {
			if result {
				return 0, 0, fmt.Sprintf("by-value struct %s is %d bytes and not an HFA, so arm64 returns it in memory", name, fm.Size)
			}
			return 0, 0, fmt.Sprintf("by-value struct %s is %d bytes and not an HFA, so arm64 passes a pointer to a copy", name, fm.Size)
		}
		return bankInt, fm.N, ""
	}
	return 0, 0, "no calling convention is known for " + t.String()
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
// function of f, which has no by-value struct, or "". It counts as
// RegisterFunc does on each target: the words of the scalars and pointers
// past the registers, and on darwin arm64 their bytes.
func (b *Binding) puregoStack(f *Function) string {
	k := b.k
	type arg struct {
		float  bool
		goSize int64
	}
	var args []arg
	for _, p := range f.Params {
		a := arg{goSize: pointerSize}
		if s, ok := k.scalarOf(p.Type); ok {
			a.float, a.goSize = s == Float32 || s == Float64, scalarSize[s]
		}
		args = append(args, a)
	}
	// amd64.
	ints, floats, stack := 0, 0, 0
	for _, a := range args {
		switch {
		case a.float && floats < amd64FloatRegs:
			floats++
		case !a.float && ints < amd64IntRegs:
			ints++
		default:
			stack++
		}
	}
	if stack > amd64StackWords {
		return fmt.Sprintf("purego v0.11.3 passes at most %d stack words on amd64, and this call needs %d", amd64StackWords, stack)
	}
	// arm64.
	ints, floats, stack = 0, 0, 0
	for _, a := range args {
		switch {
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
		default:
			bytes += a.goSize
		}
	}
	if bytes = (bytes + 7) &^ 7; bytes > darwinStackBytes {
		return fmt.Sprintf("purego v0.11.3 passes at most %d stack bytes on darwin arm64, and this call needs %d", darwinStackBytes, bytes)
	}
	return ""
}
