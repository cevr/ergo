package bindc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// This file holds TestCallbackAdmissionProperty and its oracle: a table of
// the four tier-1 targets' argument registers and of what purego v0.11.3's
// callbacks marshal, written from the ABIs and the purego source, not from
// the product.

// cbTarget is one tier-1 target: how many integer-class and float-class
// arguments its C calling convention passes in registers.
type cbTarget struct {
	name              string
	intRegs, floatReg int
}

// cbTargets: System V amd64 passes 6 integer and 8 SSE arguments in
// registers (rdi, rsi, rdx, rcx, r8, r9; xmm0-7); AAPCS64 passes 8 and 8
// (x0-x7; v0-v7). darwin uses the same counts on each architecture.
var cbTargets = []cbTarget{
	{"linux/amd64", 6, 8}, {"darwin/amd64", 6, 8}, {"linux/arm64", 8, 8}, {"darwin/arm64", 8, 8},
}

// cbPuregoArg and cbPuregoResult are the argument and result kinds that
// purego v0.11.3's callbackWrap and compileCallback take (syscall_unix.go):
// integers, bool and pointers as arguments and results, float and double
// as arguments only ("unsupported return type" for a float result); a
// struct by value is not admitted (no proof here).
var (
	cbPuregoArg    = map[string]bool{"int": true, "float": true, "struct": false}
	cbPuregoResult = map[string]bool{"void": true, "int": true, "float": false, "struct": false}
)

// cbOracle says whether a callback with these argument kinds and this
// result kind is admitted, and the phrases its reason must hold when it is
// not.
func cbOracle(kinds []string, result string) (bool, []string) {
	var whys []string
	ints, floats := 0, 0
	for _, k := range kinds {
		switch k {
		case "int":
			ints++
		case "float":
			floats++
		}
		if !cbPuregoArg[k] {
			whys = append(whys, "a struct by value")
		}
	}
	for _, tg := range cbTargets {
		if ints > tg.intRegs {
			whys = append(whys, fmt.Sprintf("%d integer-class parameters", ints))
		}
		if floats > tg.floatReg {
			whys = append(whys, fmt.Sprintf("%d float-class parameters", floats))
		}
	}
	if !cbPuregoResult[result] {
		if result == "float" {
			whys = append(whys, "callbacks cannot return float or double")
		} else {
			whys = append(whys, "a struct result by value")
		}
	}
	return len(whys) == 0, whys
}

// cbPrelude is the fixed part of every generated callback header.
const cbPrelude = `#include <stdint.h>
#include <stdbool.h>

#define G_OK INT32_C(0)
#define G_ABI UINT32_C(1)

typedef int32_t g_status;
typedef struct g_h g_h;
typedef struct g_node { uint64_t context_id; uint32_t slot; uint32_t generation; } g_node;
typedef struct g_pt { float x; float y; } g_pt;

uint32_t g_abi(void);
g_status g_create(g_h **out_h);
g_status g_destroy(g_h *h);
`

// cbContract is the contract of a generated callback header.
func cbContract(src string) *Contract {
	sum := sha256.Sum256([]byte(src))
	return &Contract{
		Header: "cb.h", HeaderSHA256: hex.EncodeToString(sum[:]),
		Go:       GoTarget{Package: "cb", ImportPath: "ergo.local/bind/cb"},
		Status:   StatusRule{Type: "g_status", MacroPrefix: "G_", Success: "G_OK", Exclude: []Skip{}},
		ABI:      ABIRule{Function: "g_abi", Macro: "G_ABI"},
		Handles:  []HandleRule{{Type: "g_h", Create: "g_create", Out: "out_h", Destroy: "g_destroy", Thread: "owner"}},
		Autofill: []AutofillRule{},
		Buffers: BufferRules{LengthNames: []string{"{name}_count"}, ByteLengthNames: []string{"byte_count", "{name}_bytes"},
			CapacityNames: []string{"capacity"}, CountNames: []string{"out_count"}, OutPrefix: "out_"},
		Callbacks: []CallbackRule{}, Exceptions: []Exception{}, Skips: []Skip{},
	}
}

// cbBind parses src and binds it with c.
func cbBind(t *testing.T, src string, c *Contract) (*Binding, error) {
	t.Helper()
	m, err := Parse("cb.h", []byte(src))
	if err != nil {
		t.Fatalf("the generated header does not parse:\n%v\n%s", err, src)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return Bind(m, []byte(src), data)
}

// cbShape is one generated callback typedef: its parameters (C
// declarations and kinds) and its result.
type cbShape struct {
	params, kinds []string
	result, rkind string
	token         bool   // one parameter is `void *user_data`
	handle        bool   // the measure shape: keyed by the fields of g_node
	outArray      string // the parameter an out_array exception names
}

var (
	cbIntDecls    = []string{"int8_t", "int16_t", "int32_t", "int64_t", "uint8_t", "uint16_t", "uint32_t", "uint64_t", "bool", "uint8_t *", "const uint32_t *", "const g_pt *", "void *"}
	cbFloatDecls  = []string{"float", "double"}
	cbResultDecls = []struct{ decl, kind string }{
		{"void", "void"}, {"int8_t", "int"}, {"int16_t", "int"}, {"int32_t", "int"}, {"int64_t", "int"},
		{"uint8_t", "int"}, {"uint16_t", "int"}, {"uint32_t", "int"}, {"uint64_t", "int"}, {"bool", "int"},
		{"uint8_t *", "int"}, {"void *", "int"}, {"float", "float"}, {"double", "float"}, {"g_pt", "struct"},
	}
)

// genShape makes a callback with nInt integer-class and nFloat float-class
// parameters in any order, structs by value at any position, and a result
// of any kind.
func genShape(r *rand.Rand, nInt, nFloat, nStruct int) cbShape {
	var kinds []string
	for i := 0; i < nInt; i++ {
		kinds = append(kinds, "int")
	}
	for i := 0; i < nFloat; i++ {
		kinds = append(kinds, "float")
	}
	for i := 0; i < nStruct; i++ {
		kinds = append(kinds, "struct")
	}
	r.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })
	s := cbShape{kinds: kinds}
	tokenAt := -1
	if nInt > 0 && r.IntN(4) != 0 {
		for i, k := range kinds {
			if k == "int" {
				tokenAt = i
				break
			}
		}
	}
	for i, k := range kinds {
		var decl string
		switch {
		case i == tokenAt:
			decl, s.token = "void *user_data", true
		case k == "int":
			decl = declare(cbIntDecls[r.IntN(len(cbIntDecls))], fmt.Sprintf("a%d", i))
		case k == "float":
			decl = declare(cbFloatDecls[r.IntN(len(cbFloatDecls))], fmt.Sprintf("a%d", i))
		default:
			decl = fmt.Sprintf("g_pt a%d", i)
		}
		s.params = append(s.params, decl)
	}
	res := cbResultDecls[r.IntN(len(cbResultDecls))]
	s.result, s.rkind = res.decl, res.kind
	return s
}

// declare is the C declaration of name with type typ.
func declare(typ, name string) string {
	if strings.HasSuffix(typ, "*") {
		return typ + name
	}
	return typ + " " + name
}

// header is the generated header of s: the typedef g_cb and a function
// that registers it.
func (s cbShape) header() string {
	params := "void"
	if len(s.params) > 0 {
		params = strings.Join(s.params, ", ")
	}
	sep := " "
	if strings.HasSuffix(s.result, "*") {
		sep = ""
	}
	reg := "g_status g_reg(g_h *h, void *user_data, g_cb cb);\n"
	if s.handle {
		reg = "g_status g_reg(g_h *h, const g_node *node, g_cb cb);\n"
	}
	return cbPrelude + fmt.Sprintf("typedef %s%s(*g_cb)(%s);\n", s.result, sep, params) + reg
}

// entry is the callbacks entry of s, and its exceptions.
func (s cbShape) entry() (CallbackRule, []Exception) {
	e := CallbackRule{Typedef: "g_cb", Scope: ScopeCall, Key: KeyRule{Params: []string{"user_data"}, Source: "user_data"}, Why: "generated"}
	if s.handle {
		e.Scope, e.Owner = ScopeRegistered, "g_h"
		e.Key = KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}
	}
	switch s.rkind {
	case "void":
	case "int":
		e.Default = json.RawMessage("0")
		if s.result == "bool" {
			e.Default = json.RawMessage("false")
		}
	default:
		e.Default = json.RawMessage("0")
	}
	var exc []Exception
	if s.outArray != "" {
		exc = append(exc, Exception{Callback: "g_cb", Position: s.outArray, Class: ClassOutArray, ArrayLength: 2, Why: "generated"})
	}
	return e, exc
}

// fixedShapes are the boundary cases: 6 and 7 integer-class parameters, 8
// and 9 float-class ones, the OpenTUI measure's shape, and each refused
// result kind.
func fixedShapes() []cbShape {
	ints := func(n int) []string {
		ps := []string{"void *user_data"}
		for i := 1; i < n; i++ {
			ps = append(ps, fmt.Sprintf("uint32_t a%d", i))
		}
		return ps
	}
	floats := func(n int) []string {
		ps := []string{"void *user_data"}
		for i := 0; i < n; i++ {
			ps = append(ps, fmt.Sprintf("float f%d", i))
		}
		return ps
	}
	kinds := func(ni, nf int) []string {
		var k []string
		for i := 0; i < ni; i++ {
			k = append(k, "int")
		}
		for i := 0; i < nf; i++ {
			k = append(k, "float")
		}
		return k
	}
	return []cbShape{
		{params: ints(6), kinds: kinds(6, 0), result: "void", rkind: "void", token: true},
		{params: ints(7), kinds: kinds(7, 0), result: "void", rkind: "void", token: true},
		{params: floats(8), kinds: kinds(1, 8), result: "int64_t", rkind: "int", token: true},
		{params: floats(9), kinds: kinds(1, 9), result: "int64_t", rkind: "int", token: true},
		{params: []string{"uint64_t context_id", "uint32_t slot", "uint32_t generation", "float width", "uint32_t width_mode",
			"float height", "uint32_t height_mode", "float *out_size"},
			kinds: []string{"int", "int", "int", "float", "int", "float", "int", "int"}, result: "void", rkind: "void",
			handle: true, outArray: "out_size"},
		{params: ints(1), kinds: kinds(1, 0), result: "float", rkind: "float", token: true},
		{params: ints(1), kinds: kinds(1, 0), result: "double", rkind: "float", token: true},
		{params: ints(1), kinds: kinds(1, 0), result: "g_pt", rkind: "struct", token: true},
		{params: []string{"void *user_data", "g_pt p"}, kinds: []string{"int", "struct"}, result: "void", rkind: "void", token: true},
	}
}

// TestCallbackAdmissionProperty: over generated callback typedefs (0 to 10
// integer-class and 0 to 10 float-class parameters in any order, every
// scalar width, pointers, structs by value, every result kind), the
// admitted set equals the oracle's; a refused shape's functions are
// raw-only with the reason, and a contract entry for it is a Bind error;
// an admitted shape with an entry binds, and without one its functions are
// raw-only with that reason. The boundary cases run first, with fixed
// shapes.
func TestCallbackAdmissionProperty(t *testing.T) {
	stats := map[string]int{}
	defer func() { t.Logf("%d cases: %v", *propCases, stats) }()
	r := rand.New(rand.NewPCG(*propSeed, 0xcb))
	shapes := fixedShapes()
	for len(shapes) < len(fixedShapes())+*propCases {
		nStruct := 0
		if r.IntN(5) == 0 {
			nStruct = 1 + r.IntN(2)
		}
		shapes = append(shapes, genShape(r, r.IntN(11), r.IntN(11), nStruct))
	}
	for n, s := range shapes {
		src := s.header()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("case %d: %s\n%s", n, fmt.Sprintf(format, args...), src)
		}
		admitted, whys := cbOracle(s.kinds, s.rkind)
		if n < len(fixedShapes()) {
			stats["fixed"]++
		}

		// Without an entry: the shape is classified, and g_reg is raw-only.
		b, err := cbBind(t, src, cbContract(src))
		if err != nil {
			fail("Bind without an entry: %v", err)
		}
		bc := b.Callbacks[0]
		if (bc.Shape == "") != admitted {
			fail("admitted %v, the oracle says %v (%v); the shape reason is %q", bc.Shape == "", admitted, whys, bc.Shape)
		}
		for _, why := range whys {
			if !strings.Contains(bc.Shape, why) {
				fail("the shape reason %q does not say %q", bc.Shape, why)
			}
		}
		reg := b.Funcs[len(b.Funcs)-1]
		want := "g_cb has no callbacks entry in the contract"
		if !admitted {
			want = "g_cb is not an admitted callback shape: " + bc.Shape
			stats["refused"]++
		} else {
			stats["admitted"]++
		}
		if reg.RawOnly == "" || !strings.Contains(reg.RawOnly, want) {
			fail("g_reg: raw-only %q, want it to say %q", reg.RawOnly, want)
		}
		if !s.token && !s.handle {
			continue
		}

		// With an entry: a Bind error for a refused shape; else it binds.
		c := cbContract(src)
		e, exc := s.entry()
		c.Callbacks, c.Exceptions = []CallbackRule{e}, append(c.Exceptions, exc...)
		b, err = cbBind(t, src, c)
		if !admitted {
			if err == nil || !strings.Contains(err.Error(), "callbacks: g_cb: is not an admitted callback shape") {
				fail("an entry for a refused shape: Bind error %v", err)
			}
			stats["refused entry"]++
			continue
		}
		if err != nil {
			fail("an entry for an admitted shape: Bind: %v", err)
		}
		bc, reg = b.Callbacks[0], b.Funcs[len(b.Funcs)-1]
		if bc.Shape != "" || bc.Rule == nil {
			fail("an admitted shape with an entry: shape %q, rule %v", bc.Shape, bc.Rule)
		}
		if bc.Bound() != (reg.RawOnly == "") {
			fail("g_cb bound %v (%s), but g_reg raw-only %q", bc.Bound(), bc.RawOnly, reg.RawOnly)
		}
		if n < len(fixedShapes()) && !bc.Bound() {
			fail("the fixed shape is not bound: %s", bc.RawOnly)
		}
		if bc.Bound() {
			stats["bound"]++
		}
	}
	if !testingShort() {
		for _, k := range []string{"admitted", "refused", "refused entry", "bound"} {
			if stats[k] < *propCases/20 {
				t.Errorf("the generator makes %d cases of %q in %d; want at least %d", stats[k], k, *propCases, *propCases/20)
			}
		}
	}
}
