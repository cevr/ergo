package bindc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/importer"
	gotoken "go/token"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The call properties generate a header, its C library, its contract and a
// Go driver of the typed layer; the C library computes what it receives,
// and the driver compares it with what the test computed from what it
// sent. callGenM1 checks by-value structs (M1 of wave 1c): every leaf of
// every argument and of a result crosses purego with its value, through
// the typed function and through a direct call of the raw function of
// every bound function, raw-only ones included (round 3). callGenM2
// checks autofill (M2 of wave 1c): C validates struct_size and abi_version
// of every struct it receives, directly, nested, in arrays, through a
// const pointer, a buffer, an output buffer and an out-parameter.

// pType is a C type of a generated call property.
type pType struct {
	scalar Scalar   // a scalar, or ""
	st     *pStruct // a struct, or nil
	elem   *pType   // an array of n elements
	n      int
}

type pStruct struct {
	c      string // C name, "g_s3"
	fields []pField
}

type pField struct {
	c string // C name
	t *pType
}

func (t *pType) spell() string {
	if t.st != nil {
		return t.st.c
	}
	return string(t.scalar)
}

// cDecl is the C declaration of a field or parameter of type t.
func (t *pType) cDecl(name string) string {
	if t.elem != nil {
		return t.elem.cDecl(name) + "[" + strconv.Itoa(t.n) + "]"
	}
	return t.spell() + " " + name
}

// leafVal is one scalar leaf of a value: its C access path, its type and
// its bits (the value as C converts it to uint64 for the hash: two's
// complement for an integer, 4 times the value for a float).
type leafVal struct {
	path string
	s    Scalar
	v    uint64
}

func isFloat(s Scalar) bool { return s == Float32 || s == Float64 }

// leafPaths lists the leaves of a value of type t at C path p, in order.
func (t *pType) leafPaths(p string, out *[]leafVal) {
	switch {
	case t.elem != nil:
		for i := 0; i < t.n; i++ {
			t.elem.leafPaths(p+"["+strconv.Itoa(i)+"]", out)
		}
	case t.st != nil:
		for _, f := range t.st.fields {
			f.t.leafPaths(p+"."+f.c, out)
		}
	default:
		*out = append(*out, leafVal{path: p, s: t.scalar})
	}
}

// goType is the Go type of t in the driver (package gen).
func (t *pType) goType(pkg string) string {
	switch {
	case t.elem != nil:
		return "[" + strconv.Itoa(t.n) + "]" + t.elem.goType(pkg)
	case t.st != nil:
		if pkg == "" {
			return goCamel(strings.TrimPrefix(t.st.c, "g_"))
		}
		return pkg + "." + goCamel(strings.TrimPrefix(t.st.c, "g_"))
	}
	return goScalar[t.scalar]
}

// goLit is the Go literal of a value of type t whose leaves, in order, are
// vals[*i:]; it advances *i.
func (t *pType) goLit(pkg string, vals []leafVal, i *int) string {
	switch {
	case t.elem != nil:
		var parts []string
		for k := 0; k < t.n; k++ {
			parts = append(parts, t.elem.goLit(pkg, vals, i))
		}
		return t.goType(pkg) + "{" + strings.Join(parts, ", ") + "}"
	case t.st != nil:
		var parts []string
		for _, f := range t.st.fields {
			parts = append(parts, goCamel(f.c)+": "+f.t.goLit(pkg, vals, i))
		}
		return t.goType(pkg) + "{" + strings.Join(parts, ", ") + "}"
	}
	v := vals[*i]
	*i++
	return scalarLit(v.s, v.v)
}

// scalarLit is the Go literal of a leaf whose hash bits are v.
func scalarLit(s Scalar, v uint64) string {
	switch s {
	case Bool:
		return strconv.FormatBool(v != 0)
	case Float32, Float64:
		return strconv.FormatFloat(float64(int64(v))/4, 'f', -1, 64)
	case Int8, Int16, Int32, Int64:
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatUint(v, 10)
}

// truncate converts v to scalar s as C does, and returns the hash bits of
// the result: two's complement for an integer, 0 or 1 for a bool.
func truncate(s Scalar, v uint64) uint64 {
	switch s {
	case Int8:
		return uint64(int64(int8(v)))
	case Int16:
		return uint64(int64(int16(v)))
	case Int32:
		return uint64(int64(int32(v)))
	case Uint8:
		return uint64(uint8(v))
	case Uint16:
		return uint64(uint16(v))
	case Uint32:
		return uint64(uint32(v))
	case Bool:
		return v & 1
	case Float32, Float64:
		// A quarter in [-1000, 1000]: exact in float and double.
		return uint64(int64(v%8001) - 4000)
	}
	return v
}

// mix is the hash of the C library (callMixC).
func mix(h, v uint64) uint64 {
	h ^= v
	h *= 0x100000001b3
	h ^= h >> 29
	return h
}

const callMixC = `static uint64_t mix(uint64_t h, uint64_t v) {
    h ^= v;
    h *= UINT64_C(0x100000001b3);
    h ^= h >> 29;
    return h;
}
`

// cLeafU64 is the C expression of the hash bits of a leaf.
func cLeafU64(l leafVal) string {
	switch {
	case isFloat(l.s):
		return "(uint64_t)(int64_t)((double)(" + l.path + ") * 4.0)"
	case l.s == Int8 || l.s == Int16 || l.s == Int32 || l.s == Int64:
		return "(uint64_t)(int64_t)(" + l.path + ")"
	}
	return "(uint64_t)(" + l.path + ")"
}

// cLeafSet is the C statement that sets a leaf from the hash bits v (a C
// expression of uint64_t), as truncate does.
func cLeafSet(l leafVal, v string) string {
	if isFloat(l.s) {
		return fmt.Sprintf("%s = (%s)((double)((int64_t)((%s) %% 8001) - 4000) / 4.0);", l.path, l.s, v)
	}
	if l.s == Bool {
		return fmt.Sprintf("%s = (bool)((%s) & 1);", l.path, v)
	}
	return fmt.Sprintf("%s = (%s)(%s);", l.path, l.s, v)
}

// callCase is one generated call property case: the files of a header, its
// C library, its contract and a Go driver.
type callCase struct {
	header, csrc string
	c            *Contract
	contract     []byte
	// driverHead and the calls of the typed functions (driverSet) make the
	// driver.
	driverHead, driver string
	calls              []callSnippet
	// raws are the direct calls of raw functions (callGenM1); driverSet
	// makes rawSrc, a file of the generated package with the calls of the
	// bound ones, and the driver calls it.
	raws   []rawFn
	rawSrc string
	// typed and raw are the numbers of typed and raw calls the driver makes.
	typed, raw int
	stats      map[string]int
}

// callSnippet is the driver's call of one function.
type callSnippet struct{ name, code string }

// rawFn is a direct call of the raw function of a C function: its
// arguments, and the Go literal of the result C computes from them.
type rawFn struct {
	name string
	args []rawArg
	res  *pType // a struct result, or nil
	want string
}

// rawArg is one argument of a raw call: a value of type t whose Go literal
// in package gen is lit, or a nil pointer.
type rawArg struct {
	t   *pType
	lit string
	ptr bool
}

// rawCode is the call of fn in package gen: a struct crosses in its call
// form, as the typed layer passes it.
func rawCode(b *Binding, fn rawFn) string {
	var args []string
	for _, a := range fn.args {
		switch {
		case a.ptr:
			args = append(args, "nil")
		case a.t.st != nil:
			args = append(args, "toABI["+b.k.form(a.t.st.c).GoName()+"]("+a.lit+")")
		default:
			args = append(args, a.lit)
		}
	}
	call := rawName(fn.name) + "(" + strings.Join(args, ", ") + ")"
	if fn.res != nil {
		call = "fromABI[" + fn.res.goType("") + "](" + call + ")"
	}
	return fmt.Sprintf("\t{\n\t\tgot := %s\n\t\twant := %s\n\t\tcheck(%q, got == want, got, want, nil)\n\t}\n", call, fn.want, "raw "+fn.name)
}

const callDriverTail = "\tif bad > 0 {\n\t\tos.Exit(1)\n\t}\n}\n"

const callHeaderHead = `#ifndef GEN_H
#define GEN_H

#include <stdint.h>
#include <stdbool.h>

#define G_ABI UINT32_C(1)

typedef int32_t g_status;
#define G_OK INT32_C(0)
#define G_UNSUPPORTED_VERSION (-INT32_C(4))

uint32_t g_abi(void);
`

func callContract(autofill bool) *Contract {
	c := &Contract{
		Header:     "gen.h",
		Go:         GoTarget{Package: "gen", ImportPath: "ergo.local/bind/gen"},
		Prefixes:   Prefixes{Function: "g_", Macro: "G_", Type: "g_"},
		Status:     StatusRule{Type: "g_status", MacroPrefix: "G_", Success: "G_OK", Exclude: []Skip{}},
		ABI:        ABIRule{Function: "g_abi", Macro: "G_ABI"},
		Handles:    []HandleRule{},
		Autofill:   []AutofillRule{},
		Callbacks:  []CallbackRule{},
		Exceptions: []Exception{},
		Skips:      []Skip{},
	}
	c.Buffers.LengthNames = []string{"{name}_count"}
	c.Buffers.ByteLengthNames = []string{}
	c.Buffers.CapacityNames = []string{"capacity"}
	c.Buffers.CountNames = []string{"out_count"}
	c.Buffers.OutPrefix = "out_"
	if autofill {
		c.Autofill = []AutofillRule{{Field: "struct_size", Value: "size"}, {Field: "abi_version", Value: "abi"}}
	}
	return c
}

func (cc *callCase) finish(t *testing.T, c *Contract) {
	t.Helper()
	cc.c = c
	sum := sha256.Sum256([]byte(cc.header))
	c.HeaderSHA256 = hex.EncodeToString(sum[:])
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	cc.contract = data
}

// bind parses and binds the case. A function whose call plan purego cannot
// make as C does on every target makes Bind refuse the header, one line
// per function ("function NAME: reason; skip it in the contract"); the
// contract then skips it, as its author would.
func (cc *callCase) bind(t *testing.T) *Binding {
	t.Helper()
	m, err := Parse("gen.h", []byte(cc.header))
	if err != nil {
		t.Fatalf("the generated header does not parse: %v\n%s", err, cc.header)
	}
	b, err := Bind(m, []byte(cc.header), cc.contract)
	if refused := callPlanRefusals(err); len(refused) > 0 {
		for _, f := range m.Functions {
			name := f.Name
			why, ok := refused[name]
			if !ok {
				continue
			}
			cc.c.Skips = append(cc.c.Skips, Skip{Name: name, Why: "generated: purego cannot call it as C does"})
			switch {
			case strings.Contains(why, "purego v0.11.3 passes at most"):
				cc.stats["skipped for the stack"]++
			case strings.HasSuffix(why, stackStructRemedy):
				cc.stats["skipped for the registers"]++
			default:
				cc.stats["skipped for the call form"]++
			}
			cc.stats["skipped "+name]++
		}
		cc.finish(t, cc.c)
		b, err = Bind(m, []byte(cc.header), cc.contract)
	}
	if err != nil {
		t.Fatalf("Bind: %v\n%s", err, cc.header)
	}
	return b
}

var callScalars = []Scalar{Int8, Int16, Int32, Int64, Uint8, Uint16, Uint32, Uint64, Bool, Float32, Float64}

// callGenM1 generates by-value structs over the admitted scalars, arrays
// and nested structs (interior and trailing padding, HFAs, mixed float and
// integer eightbytes), and functions that take up to 8 arguments and
// return a hash of every leaf or a struct derived from it. It keeps the
// verifier's trigger: g_padded_value.
func callGenM1(t *testing.T, seed uint64, nFuncs int) *callCase {
	r := rand.New(rand.NewPCG(seed, 0xb1d))
	cc := &callCase{stats: map[string]int{}}
	var h, c, d strings.Builder
	h.WriteString(callHeaderHead)
	h.WriteString("\ntypedef struct { uint8_t tag; uint32_t value; } g_padded;\nuint32_t g_padded_value(g_padded value);\n\n")
	c.WriteString("#include \"gen.h\"\n\n" + callMixC + "\nuint32_t g_abi(void) { return G_ABI; }\n\nuint32_t g_padded_value(g_padded value) { return value.value; }\n\n")
	d.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"ergo.local/bind/gen\"\n)\n\nfunc main() {\n")
	d.WriteString("\tif err := gen.Load(os.Args[1]); err != nil {\n\t\tfmt.Println(\"load:\", err)\n\t\tos.Exit(1)\n\t}\n\tbad := 0\n")
	d.WriteString("\tcheck := func(name string, ok bool, got, want any, err error) {\n\t\tif ok && err == nil {\n\t\t\tfmt.Println(\"ok:\", name)\n\t\t\treturn\n\t\t}\n\t\tbad++\n\t\tfmt.Printf(\"bad: %s: got %#v, want %#v, error %v\\n\", name, got, want, err)\n\t}\n")
	cc.calls = append(cc.calls, callSnippet{"g_padded_value",
		"\t{\n\t\tgot, err := gen.PaddedValue(gen.Padded{Tag: 3, Value: 0x12345678})\n\t\tcheck(\"g_padded_value\", got == 0x12345678, got, uint32(0x12345678), err)\n\t}\n"})
	padded := &pType{st: &pStruct{c: "g_padded", fields: []pField{{c: "tag", t: &pType{scalar: Uint8}}, {c: "value", t: &pType{scalar: Uint32}}}}}
	cc.raws = append(cc.raws, rawFn{name: "g_padded_value", args: []rawArg{{t: padded, lit: "Padded{Tag: 3, Value: 0x12345678}"}}, want: "uint32(0x12345678)"})
	// The verifier's trigger of round 2 (M1, a repeat): a mixed float and
	// integer struct has no call form purego passes as C does on both
	// architectures, so Bind refuses the function, raw-only or not; a raw
	// function registered with the words form returned the bits of 1.5.
	h.WriteString("typedef struct { double x; uint64_t y; } g_mixed;\nuint64_t g_mixed_value(g_mixed value);\n\n")
	c.WriteString("uint64_t g_mixed_value(g_mixed value) { return value.y; }\n\n")
	mixed := &pType{st: &pStruct{c: "g_mixed", fields: []pField{{c: "x", t: &pType{scalar: Float64}}, {c: "y", t: &pType{scalar: Uint64}}}}}
	cc.calls = append(cc.calls, callSnippet{"g_mixed_value",
		"\t{\n\t\tgot, err := gen.MixedValue(gen.Mixed{X: 1.5, Y: 7})\n\t\tcheck(\"g_mixed_value\", got == 7, got, uint64(7), err)\n\t}\n"})
	cc.raws = append(cc.raws, rawFn{name: "g_mixed_value", args: []rawArg{{t: mixed, lit: "Mixed{X: 1.5, Y: 7}"}}, want: "uint64(7)"})

	// Structs.
	var structs []*pStruct
	scalar := func() *pType { return &pType{scalar: callScalars[r.IntN(len(callScalars))]} }
	for i := 0; i < 10; i++ {
		s := &pStruct{c: "g_s" + strconv.Itoa(i)}
		switch r.IntN(4) {
		case 0: // an HFA: one to four floats of one type, maybe in an array or a nested HFA
			fs := []Scalar{Float32, Float64}[r.IntN(2)]
			n := 1 + r.IntN(4)
			if r.IntN(2) == 0 && n > 1 {
				s.fields = []pField{{c: "f0", t: &pType{elem: &pType{scalar: fs}, n: n}}}
			} else {
				for k := 0; k < n; k++ {
					s.fields = append(s.fields, pField{c: "f" + strconv.Itoa(k), t: &pType{scalar: fs}})
				}
			}
		default:
			for k, n := 0, 1+r.IntN(5); k < n; k++ {
				var ft *pType
				switch x := r.IntN(10); {
				case x < 6 || len(structs) == 0:
					ft = scalar()
				case x < 8:
					ft = &pType{elem: scalar(), n: 1 + r.IntN(4)}
				case x < 9:
					ft = &pType{st: structs[r.IntN(len(structs))]}
				default:
					ft = &pType{elem: &pType{st: structs[r.IntN(len(structs))]}, n: 1 + r.IntN(2)}
				}
				s.fields = append(s.fields, pField{c: "f" + strconv.Itoa(k), t: ft})
			}
		}
		structs = append(structs, s)
		h.WriteString("typedef struct " + s.c + " {\n")
		for _, f := range s.fields {
			h.WriteString("    " + f.t.cDecl(f.c) + ";\n")
		}
		h.WriteString("} " + s.c + ";\n")
	}
	h.WriteString("\n")

	value := func(pt *pType) []leafVal {
		var ls []leafVal
		pt.leafPaths("", &ls)
		for i := range ls {
			ls[i].v = truncate(ls[i].s, r.Uint64())
		}
		return ls
	}
	for fi := 0; fi < nFuncs; fi++ {
		name := "g_f" + strconv.Itoa(fi)
		var params []string
		var args []string
		var cBody strings.Builder
		hash := uint64(14695981039346656037)
		cBody.WriteString("    uint64_t h = UINT64_C(14695981039346656037);\n")
		raw := rawFn{name: name}
		// A function with a void *ctx (no rule covers it) is raw-only: its
		// raw function is still registered and called.
		ctx := r.IntN(4) == 0
		if ctx {
			params = append(params, "void *ctx")
			raw.args = append(raw.args, rawArg{ptr: true})
			cBody.WriteString("    (void)ctx;\n")
		}
		// Up to 8 arguments: A6 refuses a call with a struct that does not
		// travel in registers (callregs_test.go covers that edge), so
		// longer calls would leave few bound struct arguments to check.
		for pi, np := 0, 1+r.IntN(8); pi < np; pi++ {
			pn := "p" + strconv.Itoa(pi)
			var pt *pType
			if r.IntN(2) == 0 {
				pt = &pType{st: structs[r.IntN(len(structs))]}
			} else {
				pt = scalar()
			}
			params = append(params, pt.cDecl(pn))
			ls := value(pt)
			k := 0
			args = append(args, pt.goLit("gen", ls, &k))
			k = 0
			raw.args = append(raw.args, rawArg{t: pt, lit: pt.goLit("", ls, &k)})
			for _, l := range ls {
				l.path = pn + l.path
				cBody.WriteString("    h = mix(h, " + cLeafU64(l) + ");\n")
				hash = mix(hash, l.v)
			}
		}
		var rt *pType
		if r.IntN(2) == 0 {
			rt = &pType{st: structs[r.IntN(len(structs))]}
		}
		res := "uint64_t"
		if rt != nil {
			res = rt.st.c
		}
		decl := res + " " + name + "(" + strings.Join(params, ", ") + ")"
		h.WriteString(decl + ";\n")
		c.WriteString(decl + " {\n" + cBody.String())
		var want string
		if rt == nil {
			c.WriteString("    return h;\n}\n\n")
			want = "uint64(" + strconv.FormatUint(hash, 10) + ")"
			raw.want = want
		} else {
			var ls []leafVal
			rt.leafPaths("", &ls)
			c.WriteString("    " + res + " r;\n    memset(&r, 0, sizeof r);\n")
			for i := range ls {
				v := mix(hash, uint64(i+1))
				ls[i].v = truncate(ls[i].s, v)
				l := ls[i]
				l.path = "r" + l.path
				c.WriteString("    " + cLeafSet(l, "mix(h, UINT64_C("+strconv.Itoa(i+1)+"))") + "\n")
			}
			c.WriteString("    return r;\n}\n\n")
			k := 0
			want = rt.goLit("gen", ls, &k)
			k = 0
			raw.res, raw.want = rt, rt.goLit("", ls, &k)
		}
		cc.raws = append(cc.raws, raw)
		if !ctx {
			cc.calls = append(cc.calls, callSnippet{name, fmt.Sprintf("\t{\n\t\tgot, err := gen.%s(%s)\n\t\twant := %s\n\t\tcheck(%q, got == want, got, want, err)\n\t}\n",
				goCamel(strings.TrimPrefix(name, "g_")), strings.Join(args, ", "), want, name)})
		}
	}
	h.WriteString("\n#endif\n")
	cc.header = h.String()
	cc.csrc = "#include <string.h>\n" + c.String()
	cc.driverHead = d.String()
	cc.finish(t, callContract(false))
	return cc
}

// driverSet makes the driver from the calls of the typed functions of the
// binding (a raw-only function has no typed function to call) and the
// direct calls of every bound function's raw function, and counts the
// shapes the calls cover. Every function of the header is bound or skipped
// by the contract for its call plan.
func (cc *callCase) driverSet(t *testing.T, b *Binding) {
	t.Helper()
	k := b.k
	typed, bound := map[string]bool{}, map[string]bool{}
	for _, f := range b.Funcs {
		bound[f.C.Name] = true
		if f.RawOnly != "" {
			// Its raw function is called below, whatever the reason.
			cc.stats["raw-only"]++
		} else {
			typed[f.C.Name] = true
		}
		ints, floats := 0, 0
		for _, p := range f.C.Params {
			if k.isByValueStruct(p.Type) {
				fm := k.form(p.Type.Name)
				cc.stats["struct argument "+map[abiKind]string{abiWords: "words", abiHFA: "hfa"}[fm.Kind]]++
				var ls []leaf
				k.leaves(p.Type, 0, func(l leaf) { ls = append(ls, l) })
				sum := int64(0)
				for _, l := range ls {
					sum += l.Size
				}
				if sum < fm.Size {
					cc.stats["padded struct argument"]++
				}
				switch {
				case fm.Size > 16:
					cc.stats["struct argument in memory"]++
				case fm.Kind == abiHFA:
					floats += int((fm.Size + 7) / 8)
				default:
					ints += fm.N
				}
				continue
			}
			if s, _ := k.scalarOf(p.Type); isFloat(s) {
				floats++
			} else {
				ints++
			}
		}
		if ints > amd64IntRegs || floats > amd64FloatRegs {
			cc.stats["registers run out"]++
		}
		if k.isByValueStruct(f.C.Result) {
			cc.stats["struct result "+map[abiKind]string{abiWords: "words", abiHFA: "hfa"}[k.form(f.C.Result.Name).Kind]]++
		}
	}
	skipped := map[string]bool{}
	for _, s := range b.Contract.Skips {
		skipped[s.Name] = true
	}
	for _, s := range cc.calls {
		if !bound[s.name] && !skipped[s.name] {
			t.Fatalf("%s is neither bound nor skipped", s.name)
		}
	}
	cc.driver, cc.typed, cc.raw = cc.driverHead, 0, 0
	for _, s := range cc.calls {
		if typed[s.name] {
			cc.driver += s.code
			cc.typed++
		}
	}
	cc.rawSrc = ""
	if len(cc.raws) > 0 {
		var w strings.Builder
		w.WriteString("package " + b.Contract.Go.Package + "\n\n// RawCalls calls the raw function of every bound function of the property.\n")
		w.WriteString("func RawCalls(check func(name string, ok bool, got, want any, err error)) {\n")
		for _, fn := range cc.raws {
			if !bound[fn.name] {
				continue
			}
			w.WriteString(rawCode(b, fn))
			cc.raw++
			if !typed[fn.name] {
				cc.stats["raw-only call"]++
			}
		}
		w.WriteString("}\n")
		cc.rawSrc = w.String()
		cc.driver += "\tgen.RawCalls(check)\n"
	}
	cc.driver += callDriverTail
}

// callGenM2 generates structs with autofill fields, directly, nested and
// in arrays, and one function per form that passes each: by value, by
// const pointer, as a const array, an input buffer, an output buffer and
// an out-parameter. C returns G_OK only when every autofill field of every
// struct it receives is set. It keeps the verifier's trigger:
// g_options_take.
func callGenM2(t *testing.T, seed uint64) *callCase {
	r := rand.New(rand.NewPCG(seed, 0xa070))
	cc := &callCase{stats: map[string]int{}}
	var h, c, d strings.Builder
	h.WriteString(callHeaderHead)
	h.WriteString(`
typedef struct g_options {
    uint32_t struct_size;
    uint32_t abi_version;
    uint32_t node_capacity;
    bool strict;
} g_options;
g_status g_options_take(g_options options);

`)
	c.WriteString("#include \"gen.h\"\n\nuint32_t g_abi(void) { return G_ABI; }\n\n")
	c.WriteString("g_status g_options_take(g_options options) {\n    return options.struct_size == sizeof(g_options) && options.abi_version == G_ABI ? G_OK : G_UNSUPPORTED_VERSION;\n}\n\n")
	d.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"ergo.local/bind/gen\"\n)\n\nfunc main() {\n")
	d.WriteString("\tif err := gen.Load(os.Args[1]); err != nil {\n\t\tfmt.Println(\"load:\", err)\n\t\tos.Exit(1)\n\t}\n\tbad := 0\n")
	d.WriteString("\tcheck := func(name string, err error) {\n\t\tif err == nil {\n\t\t\tfmt.Println(\"ok:\", name)\n\t\t\treturn\n\t\t}\n\t\tbad++\n\t\tfmt.Printf(\"bad: %s: %v\\n\", name, err)\n\t}\n")
	cc.calls = append(cc.calls, callSnippet{"g_options_take", "\tcheck(\"g_options_take\", gen.OptionsTake(gen.Options{NodeCapacity: 4}))\n"})

	ints := []Scalar{Uint8, Uint16, Uint32, Uint64, Int32, Bool}
	var structs []*pStruct
	for i := 0; i < 6; i++ {
		s := &pStruct{c: "g_a" + strconv.Itoa(i)}
		var fs []pField
		if r.IntN(4) != 0 {
			fs = append(fs, pField{c: "struct_size", t: &pType{scalar: Uint32}})
		}
		if r.IntN(4) != 0 || len(fs) == 0 {
			fs = append(fs, pField{c: "abi_version", t: &pType{scalar: Uint32}})
		}
		for k, n := 0, r.IntN(3); k < n; k++ {
			fs = append(fs, pField{c: "x" + strconv.Itoa(k), t: &pType{scalar: ints[r.IntN(len(ints))]}})
		}
		if len(structs) > 0 && r.IntN(2) == 0 {
			fs = append(fs, pField{c: "inner", t: &pType{st: structs[r.IntN(len(structs))]}})
		}
		if len(structs) > 0 && r.IntN(2) == 0 {
			fs = append(fs, pField{c: "list", t: &pType{elem: &pType{st: structs[r.IntN(len(structs))]}, n: 1 + r.IntN(3)}})
		}
		r.Shuffle(len(fs), func(a, b int) { fs[a], fs[b] = fs[b], fs[a] })
		s.fields = fs
		structs = append(structs, s)
		h.WriteString("typedef struct " + s.c + " {\n")
		for _, f := range s.fields {
			h.WriteString("    " + f.t.cDecl(f.c) + ";\n")
		}
		h.WriteString("} " + s.c + ";\n")
		// The C check of every autofill field, nested and in arrays.
		c.WriteString("static bool check_" + s.c + "(const " + s.c + " *v) {\n")
		for _, f := range s.fields {
			switch {
			case f.c == "struct_size":
				c.WriteString("    if (v->struct_size != sizeof(" + s.c + ")) return false;\n")
			case f.c == "abi_version":
				c.WriteString("    if (v->abi_version != G_ABI) return false;\n")
			case f.t.st != nil:
				c.WriteString("    if (!check_" + f.t.st.c + "(&v->" + f.c + ")) return false;\n")
			case f.t.elem != nil && f.t.elem.st != nil:
				c.WriteString(fmt.Sprintf("    for (int i = 0; i < %d; i++) if (!check_%s(&v->%s[i])) return false;\n", f.t.n, f.t.elem.st.c, f.c))
			}
		}
		c.WriteString("    return true;\n}\n\n")
	}
	h.WriteString("\n")
	for i, s := range structs {
		gt := "gen." + goCamel(strings.TrimPrefix(s.c, "g_"))
		n := strconv.Itoa(i)
		forms := []struct{ decl, body, call string }{
			{"g_status g_value" + n + "(" + s.c + " v)", "return check_" + s.c + "(&v) ? G_OK : G_UNSUPPORTED_VERSION;",
				"gen.Value" + n + "(" + gt + "{})"},
			{"g_status g_const" + n + "(const " + s.c + " *v)", "return check_" + s.c + "(v) ? G_OK : G_UNSUPPORTED_VERSION;",
				"gen.Const" + n + "(" + gt + "{})"},
			{"g_status g_array" + n + "(const " + s.c + " v[2])", "return check_" + s.c + "(&v[0]) && check_" + s.c + "(&v[1]) ? G_OK : G_UNSUPPORTED_VERSION;",
				"gen.Array" + n + "([2]" + gt + "{})"},
			{"g_status g_items" + n + "(const " + s.c + " *items, uint32_t items_count)",
				"for (uint32_t i = 0; i < items_count; i++) if (!check_" + s.c + "(&items[i])) return G_UNSUPPORTED_VERSION;\n    return items_count == 3 ? G_OK : G_UNSUPPORTED_VERSION;",
				"gen.Items" + n + "(make([]" + gt + ", 3))"},
			{"g_status g_fill" + n + "(" + s.c + " *out, uint32_t capacity, uint32_t *out_count)",
				"for (uint32_t i = 0; i < capacity; i++) if (!check_" + s.c + "(&out[i])) return G_UNSUPPORTED_VERSION;\n    *out_count = capacity;\n    return capacity == 2 ? G_OK : G_UNSUPPORTED_VERSION;",
				"func() error { _, err := gen.Fill" + n + "(make([]" + gt + ", 2)); return err }()"},
			{"g_status g_out" + n + "(" + s.c + " *out_value)", "return check_" + s.c + "(out_value) ? G_OK : G_UNSUPPORTED_VERSION;",
				"func() error { _, err := gen.Out" + n + "(); return err }()"},
		}
		for _, f := range forms {
			h.WriteString(f.decl + ";\n")
			c.WriteString(f.decl + " {\n    " + f.body + "\n}\n\n")
			fname := strings.Fields(strings.SplitN(f.decl, "(", 2)[0])[1]
			cc.calls = append(cc.calls, callSnippet{fname, fmt.Sprintf("\tcheck(%q, %s)\n", fname, f.call)})
		}
	}
	h.WriteString("\n#endif\n")
	cc.header = h.String()
	cc.csrc = c.String()
	cc.driverHead = d.String()
	cc.finish(t, callContract(true))
	return cc
}

// TestCallPropertyTypechecks: the Go packages of the generated call
// properties, with their direct raw calls, type-check on amd64 and arm64
// (with the purego stub), and Bind refuses the verifier's g_mixed_value.
// The gate runs it; TestLiveByValueProperty and TestLiveAutofillProperty
// run the same packages against C.
func TestCallPropertyTypechecks(t *testing.T) {
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	n := max(1, *propCases/250)
	for i := 0; i < n; i++ {
		seed := *propSeed + uint64(i)
		for gi, cc := range []*callCase{callGenM1(t, seed, 30), callGenM2(t, seed)} {
			b := cc.bind(t)
			cc.driverSet(t, b)
			if gi == 0 && cc.stats["skipped g_mixed_value"] != 1 {
				t.Fatalf("seed %d: Bind did not refuse g_mixed_value (a mixed float and integer struct by value)", seed)
			}
			name, src, err := GoPackage(b)
			if err != nil {
				t.Fatalf("seed %d: GoPackage: %v", seed, err)
			}
			files := map[string][]byte{name: src}
			if cc.rawSrc != "" {
				files["rawcalls.go"] = []byte(cc.rawSrc)
			}
			for _, arch := range []string{"amd64", "arm64"} {
				if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, arch, files); err != nil {
					t.Fatalf("seed %d: the generated package does not type-check on %s:\n%v\n%s", seed, arch, err, cc.header)
				}
			}
			checkGoInventory(t, b, src)
		}
	}
}

// TestLiveByValueProperty (ERGO_BINDC_LIVE=1; cc and the network): for
// generated by-value structs and functions (callGenM1), every typed call
// and every direct call of a bound function's raw function (raw-only ones
// included) through the real purego v0.11.3 gives the value C computes
// from the fields it received, and every struct result has the fields C
// set. A function Bind refuses for its call plan is skipped and not called.
func TestLiveByValueProperty(t *testing.T) {
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to call generated C libraries through the real purego")
	}
	stats := map[string]int{}
	for i := 0; i < max(1, *propCases/500); i++ {
		seed := *propSeed + uint64(i)
		cc := callGenM1(t, seed, 40)
		cc.driverSet(t, cc.bind(t))
		runCallCase(t, cc)
		for k, v := range cc.stats {
			if !strings.HasPrefix(k, "skipped g_") {
				stats[k] += v
			}
		}
	}
	t.Logf("by-value property: %v", stats)
	// A6: no struct is passed in memory and no call with a struct runs out
	// of registers; such calls are skipped for the registers, and the
	// register grid (TestLiveRegisterProperty) calls scalars on the stack.
	for _, k := range []string{"struct argument words", "struct argument hfa", "padded struct argument",
		"struct result words", "struct result hfa", "raw-only", "raw-only call", "skipped for the call form", "skipped for the registers"} {
		if stats[k] == 0 {
			t.Errorf("the generator made no case of %q", k)
		}
	}
}

// TestLiveAutofillProperty (ERGO_BINDC_LIVE=1; cc and the network): for
// generated autofill structs (callGenM2), C finds every autofill field set
// in every form that passes a struct to it.
func TestLiveAutofillProperty(t *testing.T) {
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to call generated C libraries through the real purego")
	}
	for i := 0; i < max(1, *propCases/500); i++ {
		seed := *propSeed + uint64(i)
		cc := callGenM2(t, seed)
		b := cc.bind(t)
		cc.driverSet(t, b)
		skips := cc.stats["skipped for the stack"] + cc.stats["skipped for the call form"] + cc.stats["skipped for the registers"]
		if cc.typed+skips != len(cc.calls) {
			var raw []string
			for _, f := range b.Funcs {
				if f.RawOnly != "" {
					raw = append(raw, f.C.Name+": "+f.RawOnly)
				}
			}
			t.Fatalf("seed %d: %d of %d calls are typed, want every form typed but those skipped (%d); raw-only:\n%s\n%s",
				seed, cc.typed, len(cc.calls), skips, strings.Join(raw, "\n"), cc.header)
		}
		runCallCase(t, cc)
	}
}

// runCallCase builds the case's C library and its Go package with the real
// purego v0.11.3 (vet on linux and darwin, amd64 and arm64), runs the
// driver and requires one "ok" line per typed call and per raw call.
func runCallCase(t *testing.T, cc *callCase) {
	t.Helper()
	cc0 := requireCC(t)
	b := cc.bind(t)
	name, src, err := GoPackage(b)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mod/go.mod", []byte("module ergo.local/bind\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	write("mod/go.sum", readInput(t, "testdata/purego.sum"))
	write("mod/gen/"+name, src)
	if cc.rawSrc != "" {
		write("mod/gen/rawcalls.go", []byte(cc.rawSrc))
	}
	write("mod/driver/main.go", []byte(cc.driver))
	write("c/gen.h", []byte(cc.header))
	write("c/gen.c", []byte(cc.csrc))
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	env := append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=readonly -modcacherw", "GOMODCACHE="+filepath.Join(root, "modcache"), "GOWORK=off")
	run := func(dir string, extra []string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = filepath.Join(root, dir)
		cmd.Env = append(append([]string{}, env...), extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(out string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("%v\n%s\nheader:\n%s", err, out, cc.header)
		}
		return out
	}
	for _, goos := range []string{"linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			must(run("mod", []string{"GOOS=" + goos, "GOARCH=" + goarch}, goTool, "vet", "./gen"))
		}
	}
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	lib := filepath.Join(root, "libgen"+ext)
	must(run("c", nil, cc0, "-std=c11", "-shared", "-fPIC", "-o", lib, "gen.c"))
	must(run("mod", nil, goTool, "build", "-o", filepath.Join(root, "driver-bin"), "./driver"))
	out, err := run("", nil, filepath.Join(root, "driver-bin"), lib)
	oks := strings.Count(out, "ok: ")
	if err != nil || oks != cc.typed+cc.raw {
		t.Fatalf("the driver: %v; %d ok lines, want %d (typed %d, raw %d)\n%s\nheader:\n%s", err, oks, cc.typed+cc.raw, cc.typed, cc.raw, out, cc.header)
	}
}
