package bindc

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The generated shapes of TestLiveCallbackShapeProperty. Each shape is one
// admitted callback typedef with a C function that calls it with known
// values (g_call<n>) and one that calls it with a key the binding does not
// know (g_miss<n>). The expected Go signature and every value are written
// here, independently of the emitter.

// spParam is one parameter of a generated callback, or a pair (an
// in_buffer and its length).
type spParam struct {
	kind string // key, scalar, value, inbuf, out, outarr
	name string
	s    Scalar // scalar, out, outarr element; inbuf element (Uint8 or Int32)
	// scalar and out: the value (C and Go literals) and, for out, the
	// sentinel C writes first.
	cv, gv, sentinel string
	// inbuf: the elements (Go literals of s), and whether the length
	// counts bytes; value: the field values a, b, c; outarr: the elements
	// and the sentinels.
	elems, sentinels []string
	bytes            bool
	val, gval        [3]string
}

// spShape is one generated callback typedef.
type spShape struct {
	n      int
	handle bool // keyed by the fields of g_node (else a void * token)
	params []spParam
	result Scalar // "" for void, Uint64 stands in for void * when ptr is true
	ptr    bool
	rv     [2]string // the result: C and Go literals
	def    string    // the entry's default (JSON), C literal
}

var spInts = []Scalar{Int8, Int16, Int32, Int64, Uint8, Uint16, Uint32, Uint64}

// spScalar is a value of s, as C and Go literals: a boundary or a random
// value; floats are multiples of 1/4 (or 1/16), exact in both.
func spScalar(r *rand.Rand, s Scalar) (c, g string) {
	gt, ct := goScalar[s], cScalar(s)
	switch s {
	case Bool:
		if r.IntN(2) == 0 {
			return "false", "false"
		}
		return "true", "true"
	case Float32:
		v := strconv.FormatFloat(float64(r.IntN(8001)-4000)/4, 'f', -1, 64)
		return "(float)" + v, "float32(" + v + ")"
	case Float64:
		v := strconv.FormatFloat(float64(r.IntN(2000001)-1000000)/16, 'f', -1, 64)
		return "(double)" + v, "float64(" + v + ")"
	}
	bits := 8 * scalarSize[s]
	if isUnsignedScalar(s) {
		var v uint64
		switch r.IntN(4) {
		case 0:
			v = 1<<(bits-1)<<1 - 1 // the max
		case 1:
			v = 0
		default:
			v = r.Uint64() >> (64 - bits)
		}
		return fmt.Sprintf("(%s)%dULL", ct, v), fmt.Sprintf("%s(%d)", gt, v)
	}
	var v int64
	switch r.IntN(4) {
	case 0:
		v = -1 << (bits - 1) // the min
	case 1:
		v = 1<<(bits-1) - 1 // the max
	default:
		v = int64(r.Uint64()) >> (64 - bits)
	}
	cl := fmt.Sprintf("%dLL", v)
	if v == -1<<63 {
		cl = "(-9223372036854775807LL - 1)"
	}
	return fmt.Sprintf("(%s)%s", ct, cl), fmt.Sprintf("%s(%d)", gt, v)
}

// cScalar is the C spelling of a scalar.
func cScalar(s Scalar) string {
	switch s {
	case Bool:
		return "bool"
	case Float32:
		return "float"
	case Float64:
		return "double"
	}
	return string(s)
}

// spDistinct is a value of s other than the C literal not.
func spDistinct(r *rand.Rand, s Scalar, not string) (c, g string) {
	for {
		if c, g = spScalar(r, s); c != not {
			return c, g
		}
	}
}

// spGen builds shape n from fixed parameter kinds, or random ones when
// kinds is nil, within the admission bounds (6 integer-class and 8
// float-class parameters).
func spGen(r *rand.Rand, n int, handle bool, kinds []string, result string) spShape {
	sh := spShape{n: n, handle: handle}
	ints, floats := 1, 0
	if handle {
		ints = 3
	}
	if kinds == nil {
		for range r.IntN(11) {
			switch k := r.IntN(20); {
			case k < 4:
				kinds = append(kinds, "float")
			case k < 11:
				kinds = append(kinds, "int")
			case k < 13:
				kinds = append(kinds, "value")
			case k < 16:
				kinds = append(kinds, "inbuf")
			case k < 18:
				kinds = append(kinds, "out")
			default:
				kinds = append(kinds, "outarr")
			}
		}
	}
	for i, k := range kinds {
		p := spParam{kind: k, name: "p" + strconv.Itoa(i)}
		need := map[string][2]int{"float": {0, 1}, "int": {1, 0}, "value": {1, 0}, "inbuf": {2, 0}, "out": {1, 0}, "outarr": {1, 0}}[k]
		if k == "float" {
			p.kind = "scalar"
		}
		if ints+need[0] > callbackIntRegs || floats+need[1] > callbackFloatRegs {
			continue
		}
		ints, floats = ints+need[0], floats+need[1]
		switch k {
		case "float":
			p.s = []Scalar{Float32, Float64}[r.IntN(2)]
			p.cv, p.gv = spScalar(r, p.s)
		case "int":
			p.kind = "scalar"
			p.s = append(spInts, Bool)[r.IntN(len(spInts)+1)]
			p.cv, p.gv = spScalar(r, p.s)
		case "value":
			for j, s := range []Scalar{Int32, Float64, Uint8} {
				p.val[j], p.gval[j] = spScalar(r, s)
			}
		case "inbuf":
			p.s, p.bytes = []Scalar{Uint8, Int32}[r.IntN(2)], r.IntN(2) == 0
			if p.s == Uint8 {
				p.bytes = true
			}
			for range r.IntN(5) {
				_, g := spScalar(r, p.s)
				p.elems = append(p.elems, g)
			}
		case "out":
			p.name = "out_p" + strconv.Itoa(i)
			p.s = append(spInts, Bool, Float32, Float64)[r.IntN(len(spInts)+3)]
			p.cv, p.gv = spScalar(r, p.s)
			p.sentinel, _ = spDistinct(r, p.s, p.cv)
		case "outarr":
			p.s = []Scalar{Float32, Int32}[r.IntN(2)]
			for range 2 + r.IntN(2) {
				c, g := spScalar(r, p.s)
				sc, _ := spDistinct(r, p.s, c)
				p.elems, p.sentinels = append(p.elems, c+"|"+g), append(p.sentinels, sc)
			}
		}
		sh.params = append(sh.params, p)
	}
	// The key parameters, each at a random position.
	keys := []spParam{{kind: "key", name: "user_data"}}
	if handle {
		keys = []spParam{{kind: "key", name: "context_id", s: Uint64}, {kind: "key", name: "slot", s: Uint32}, {kind: "key", name: "generation", s: Uint32}}
	}
	for _, k := range keys {
		at := r.IntN(len(sh.params) + 1)
		sh.params = append(sh.params[:at], append([]spParam{k}, sh.params[at:]...)...)
	}
	if result == "" {
		result = []string{"void", "int", "bool", "ptr"}[r.IntN(4)]
	}
	switch result {
	case "void":
	case "ptr":
		sh.ptr, sh.result = true, Uint64
		v := 0x10000 + 16*r.IntN(1<<20)
		sh.rv = [2]string{fmt.Sprintf("(void *)(uintptr_t)%dULL", v), fmt.Sprintf("uintptr(%d)", v)}
		sh.def = "0"
	case "bool":
		sh.result = Bool
		sh.rv[0], sh.rv[1] = spScalar(r, Bool)
		sh.def = map[bool]string{true: "false", false: "true"}[sh.rv[0] == "true"]
	default:
		sh.result = spInts[r.IntN(len(spInts))]
		if strings.HasPrefix(result, "int:") {
			sh.result = Scalar(strings.TrimPrefix(result, "int:"))
		}
		sh.rv[0], sh.rv[1] = spScalar(r, sh.result)
		sh.def = "7"
		if !isUnsignedScalar(sh.result) && r.IntN(2) == 0 {
			sh.def = "-1"
		}
	}
	return sh
}

// spFixed are the boundary cases of every run: 6 integer-class
// parameters, 8 float-class, both, the OpenTUI measure and writer shapes,
// and each result kind.
func spFixed(r *rand.Rand) []spShape {
	var out []spShape
	add := func(handle bool, kinds []string, result string) {
		out = append(out, spGen(r, len(out), handle, kinds, result))
	}
	add(false, []string{"int", "int", "int", "int", "int"}, "void")
	add(false, []string{"float", "float", "float", "float", "float", "float", "float", "float"}, "int")
	add(false, []string{"int", "float", "int", "float", "int", "float", "int", "float", "int", "float", "float", "float", "float"}, "bool")
	add(true, []string{"float", "int", "float", "int", "outarr"}, "void")
	add(false, []string{"inbuf"}, "int:int64_t")
	add(true, []string{"value", "out"}, "bool")
	add(false, []string{"value", "inbuf", "out", "outarr"}, "ptr")
	for _, s := range spInts {
		add(r.IntN(2) == 0, nil, "int:"+string(s))
	}
	return out
}

// spBatch is one generated package: at most 50 shapes, one header, one C
// library and one Go package gen<b>.
type spBatch struct {
	b      int
	shapes []spShape
}

func (sb spBatch) pkg() string { return "gen" + strconv.Itoa(sb.b) }

// cParams is the C parameter list of a shape's typedef.
func (sh spShape) cParams() []string {
	var ps []string
	for _, p := range sh.params {
		switch p.kind {
		case "key":
			if p.name == "user_data" {
				ps = append(ps, "void *user_data")
			} else {
				ps = append(ps, cScalar(p.s)+" "+p.name)
			}
		case "scalar", "out":
			star := ""
			if p.kind == "out" {
				star = "*"
			}
			ps = append(ps, cScalar(p.s)+" "+star+p.name)
		case "value":
			ps = append(ps, "const g_val *"+p.name)
		case "inbuf":
			unit := "_count"
			if p.bytes {
				unit = "_bytes"
			}
			ps = append(ps, "const "+cScalar(p.s)+" *"+p.name, "uint32_t "+p.name+unit)
		case "outarr":
			ps = append(ps, cScalar(p.s)+" *"+p.name)
		}
	}
	return ps
}

func (sh spShape) cResult() string {
	switch {
	case sh.ptr:
		return "void *"
	case sh.result == "":
		return "void "
	}
	return cScalar(sh.result) + " "
}

// header is the batch's header, contract and C source.
func (sb spBatch) files() (header string, c *Contract, csrc string) {
	var h, s strings.Builder
	h.WriteString(cbPrelude + "#define G_BAD INT32_C(1)\ntypedef struct g_val { int32_t a; double b; uint8_t c; } g_val;\n")
	s.WriteString("#include \"cb.h\"\n#include <stddef.h>\n\nuint32_t g_abi(void) { return 1; }\n")
	s.WriteString("struct g_h { int x; };\nstatic struct g_h the_h;\n")
	s.WriteString("g_status g_create(g_h **out_h) { *out_h = &the_h; return G_OK; }\ng_status g_destroy(g_h *h) { (void)h; return G_OK; }\n")
	var entries []CallbackRule
	var excs []Exception
	for _, sh := range sb.shapes {
		name := fmt.Sprintf("g_cb%d", sh.n)
		fmt.Fprintf(&h, "typedef %s(*%s)(%s);\n", sh.cResult(), name, strings.Join(sh.cParams(), ", "))
		src := "void *user_data"
		if sh.handle {
			// A call-scoped handle key binds only with an owner-thread
			// handle (g_h): calls on one key then nest, never overlap.
			src = "g_h *h, const g_node *node"
		}
		for _, fn := range []string{"call", "miss"} {
			fmt.Fprintf(&h, "g_status g_%s%d(%s, %s cb);\n", fn, sh.n, src, name)
			s.WriteString(sh.cFunc(fn, src, name))
		}
		e := CallbackRule{Typedef: name, Scope: ScopeCall, Key: KeyRule{Params: []string{"user_data"}, Source: "user_data"}, Why: "generated"}
		if sh.handle {
			e.Key = KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}
		}
		if sh.def != "" {
			e.Default = []byte(sh.def)
		}
		entries = append(entries, e)
		for _, p := range sh.params {
			if p.kind == "outarr" {
				excs = append(excs, Exception{Callback: name, Position: p.name, Class: ClassOutArray, ArrayLength: int64(len(p.elems)), Why: "generated"})
			}
		}
	}
	header = h.String()
	c = cbContract(header)
	c.Go = GoTarget{Package: sb.pkg(), ImportPath: "ergo.local/bind/" + sb.pkg()}
	c.Callbacks, c.Exceptions = entries, excs
	return header, c, s.String()
}

// cFunc is g_call<n> or g_miss<n>: it calls cb once with the known values
// and checks the result and every out position. g_call passes the key it
// got and wants the Go values; g_miss passes an unknown key and wants the
// default and every out position untouched. A mismatch is G_BAD.
func (sh spShape) cFunc(fn, src, name string) string {
	var w strings.Builder
	fmt.Fprintf(&w, "g_status g_%s%d(%s, %s cb) {\n", fn, sh.n, src, name)
	miss := fn == "miss"
	var args, checks []string
	for _, p := range sh.params {
		switch p.kind {
		case "key":
			switch {
			case p.name == "user_data" && miss:
				args = append(args, "(void *)((uintptr_t)user_data + ((uintptr_t)1 << 40))")
			case p.name == "user_data":
				args = append(args, "user_data")
			case p.name == "generation" && miss:
				args = append(args, "node->generation + 1")
			default:
				args = append(args, "node->"+p.name)
			}
		case "scalar":
			args = append(args, p.cv)
		case "value":
			fmt.Fprintf(&w, "    g_val %s = {%s, %s, %s};\n", p.name, p.val[0], p.val[1], p.val[2])
			args = append(args, "&"+p.name)
		case "inbuf":
			n := len(p.elems)
			if n == 0 {
				args = append(args, "NULL", "0")
				break
			}
			var cs []string
			for _, e := range p.elems {
				cs = append(cs, strings.TrimSuffix(strings.TrimPrefix(e, goScalar[p.s]+"("), ")"))
			}
			fmt.Fprintf(&w, "    static const %s %s[] = {%s};\n", cScalar(p.s), p.name, strings.Join(cs, ", "))
			length := strconv.Itoa(n)
			if p.bytes {
				length = fmt.Sprintf("%d * sizeof(%s)", n, cScalar(p.s))
			}
			args = append(args, p.name, length)
		case "out":
			fmt.Fprintf(&w, "    %s %s = %s;\n", cScalar(p.s), p.name, p.sentinel)
			args = append(args, "&"+p.name)
			want := p.cv
			if miss {
				want = p.sentinel
			}
			checks = append(checks, fmt.Sprintf("%s == %s", p.name, want))
		case "outarr":
			fmt.Fprintf(&w, "    %s %s[%d] = {%s};\n", cScalar(p.s), p.name, len(p.elems), strings.Join(p.sentinels, ", "))
			args = append(args, p.name)
			for i, e := range p.elems {
				want := strings.SplitN(e, "|", 2)[0]
				if miss {
					want = p.sentinels[i]
				}
				checks = append(checks, fmt.Sprintf("%s[%d] == %s", p.name, i, want))
			}
		}
	}
	call := "cb(" + strings.Join(args, ", ") + ")"
	if sh.result == "" {
		fmt.Fprintf(&w, "    %s;\n", call)
	} else {
		fmt.Fprintf(&w, "    %sr = %s;\n", sh.cResult(), call)
		want := sh.rv[0]
		if miss {
			want = sh.def
			if sh.ptr {
				want = "NULL"
			}
		}
		checks = append(checks, "r == "+want)
	}
	if len(checks) == 0 {
		checks = []string{"1"}
	}
	fmt.Fprintf(&w, "    return (%s) ? G_OK : G_BAD;\n}\n", strings.Join(checks, " && "))
	return w.String()
}

// goSig is the Go func type the shape must have: scalars, values and
// in_buffers in C order; the result, then outs and out_arrays in C order.
func (sh spShape) goSig(pkg string) (params []string, results []string) {
	if sh.ptr {
		results = append(results, "uintptr")
	} else if sh.result != "" {
		results = append(results, goScalar[sh.result])
	}
	for _, p := range sh.params {
		switch p.kind {
		case "scalar":
			params = append(params, p.name+" "+goScalar[p.s])
		case "value":
			params = append(params, p.name+" "+pkg+".GVal")
		case "inbuf":
			et := goScalar[p.s]
			if et == "uint8" {
				et = "byte"
			}
			params = append(params, p.name+" []"+et)
		case "out":
			results = append(results, goScalar[p.s])
		case "outarr":
			results = append(results, fmt.Sprintf("[%d]%s", len(p.elems), goScalar[p.s]))
		}
	}
	return params, results
}

// goDriver is the Go code of one shape in the driver: it calls g_call<n>
// with a func that checks every argument and returns the known results,
// then g_miss<n> with a func that must not run.
func (sh spShape) goDriver(pkg string) string {
	var w strings.Builder
	params, results := sh.goSig(pkg)
	res := strings.Join(results, ", ")
	if len(results) > 1 {
		res = "(" + res + ")"
	}
	keyArg := ""
	if sh.handle {
		keyArg = fmt.Sprintf("h, %s.GNode{ContextId: %d, Slot: %d, Generation: %d}, ", pkg, 1000+sh.n, sh.n%7, 1+sh.n%5)
	}
	id := fmt.Sprintf("%s.%d", pkg, sh.n)
	var checks, rets, zeros []string
	for _, p := range sh.params {
		switch p.kind {
		case "scalar":
			checks = append(checks, fmt.Sprintf("%s != %s", p.name, p.gv))
		case "value":
			checks = append(checks, fmt.Sprintf("%s != (%s.GVal{A: %s, B: %s, C: %s})", p.name, pkg, p.gval[0], p.gval[1], p.gval[2]))
		case "inbuf":
			et := goScalar[p.s]
			if et == "uint8" {
				et = "byte"
			}
			checks = append(checks, fmt.Sprintf("!slices.Equal(%s, []%s{%s})", p.name, et, strings.Join(p.elems, ", ")))
		}
	}
	if sh.ptr {
		rets, zeros = append(rets, sh.rv[1]), append(zeros, "0")
	} else if sh.result != "" {
		rets, zeros = append(rets, sh.rv[1]), append(zeros, "*new("+goScalar[sh.result]+")")
	}
	for _, p := range sh.params {
		switch p.kind {
		case "out":
			rets, zeros = append(rets, p.gv), append(zeros, "*new("+goScalar[p.s]+")")
		case "outarr":
			var gs []string
			for _, e := range p.elems {
				gs = append(gs, strings.SplitN(e, "|", 2)[1])
			}
			t := fmt.Sprintf("[%d]%s", len(p.elems), goScalar[p.s])
			rets, zeros = append(rets, t+"{"+strings.Join(gs, ", ")+"}"), append(zeros, t+"{}")
		}
	}
	ret := func(vs []string) string {
		if len(vs) == 0 {
			return ""
		}
		return "\t\treturn " + strings.Join(vs, ", ") + "\n"
	}
	fmt.Fprintf(&w, "\tcalls = 0\n\tif err := %s.GCall%d(%sfunc(%s) %s {\n\t\tcalls++\n", pkg, sh.n, keyArg, strings.Join(params, ", "), res)
	if len(checks) > 0 {
		fmt.Fprintf(&w, "\t\tif %s {\n\t\t\tfail(\"%s: the Go side got other values: %%v\", []any{%s})\n\t\t}\n", strings.Join(checks, " || "), id, sh.goArgNames())
	}
	fmt.Fprintf(&w, "%s\t}); err != nil || calls != 1 {\n\t\tfail(\"%s call: %%v after %%d calls (C saw other results)\", err, calls)\n\t}\n\tok(\"%s call\")\n", ret(rets), id, id)
	fmt.Fprintf(&w, "\tif err := %s.GMiss%d(%sfunc(%s) %s {\n\t\tfail(\"%s miss: Go code ran for an unknown key\")\n%s\t}); err != nil {\n\t\tfail(\"%s miss: %%v (C did not get the default, or an out was written)\", err)\n\t}\n\tok(\"%s miss\")\n",
		pkg, sh.n, keyArg, strings.Join(params, ", "), res, id, ret(zeros), id, id)
	return w.String()
}

// goArgNames are the Go parameter names of the shape, comma-separated.
func (sh spShape) goArgNames() string {
	var ns []string
	for _, p := range sh.params {
		switch p.kind {
		case "scalar", "value", "inbuf":
			ns = append(ns, p.name)
		}
	}
	return strings.Join(ns, ", ")
}

// TestLiveCallbackShapeProperty (A5, ERGO_BINDC_LIVE=1): generated admitted
// callback shapes (max(200, bindc.cases/5), the boundary cases of
// spFixed included), in packages of at most 50 bound typedefs: each
// binds with every shape bound, its Go package builds and vets with the
// real purego for linux and darwin on amd64 and arm64, and a generated C
// file calls each callback with known values: the Go side receives exactly
// them, and C receives exactly the result and the out positions. A call
// with a key the binding does not know runs no Go code, and C gets the
// default with its out positions untouched.
func TestLiveCallbackShapeProperty(t *testing.T) {
	e := newLiveEnv(t)
	cc := requireCC(t)
	r := rand.New(rand.NewPCG(*propSeed, 0x5a4e))
	shapes := spFixed(r)
	for len(shapes) < max(200, *propCases/5) {
		shapes = append(shapes, spGen(r, len(shapes), r.IntN(2) == 0, nil, ""))
	}
	var batches []spBatch
	for i, sh := range shapes {
		if i%50 == 0 {
			batches = append(batches, spBatch{b: len(batches)})
		}
		sh.n = i % 50
		batches[len(batches)-1].shapes = append(batches[len(batches)-1].shapes, sh)
	}
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	var d strings.Builder
	d.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"runtime\"\n\t\"slices\"\n\n")
	for _, sb := range batches {
		fmt.Fprintf(&d, "\t%q\n", "ergo.local/bind/"+sb.pkg())
	}
	d.WriteString(")\n\nfunc fail(format string, args ...any) {\n\tfmt.Printf(\"FAIL: \"+format+\"\\n\", args...)\n\tos.Exit(1)\n}\n\nfunc ok(c string) { fmt.Println(\"ok: \" + c) }\n\n")
	// g_h has thread owner: every handle stays on the thread that created it.
	d.WriteString("var _ = slices.Equal[[]byte, byte]\n\nfunc main() {\n\truntime.LockOSThread()\n\tcalls := 0\n\t_ = calls\n")
	var libs []string
	for i, sb := range batches {
		header, c, csrc := sb.files()
		b, err := cbBind(t, header, c)
		if err != nil {
			t.Fatalf("batch %d: Bind: %v\n%s", i, err, header)
		}
		for _, cb := range b.Callbacks {
			if !cb.Bound() {
				t.Fatalf("batch %d: %s is not bound: %s", i, cb.Decl.Name, cb.RawOnly)
			}
		}
		for _, f := range b.Funcs {
			if strings.HasPrefix(f.C.Name, "g_call") || strings.HasPrefix(f.C.Name, "g_miss") {
				if f.RawOnly != "" {
					t.Fatalf("batch %d: %s is raw-only: %s", i, f.C.Name, f.RawOnly)
				}
			}
		}
		name, src, err := GoPackage(b)
		if err != nil {
			t.Fatalf("batch %d: GoPackage: %v", i, err)
		}
		e.write(filepath.Join(e.mod, sb.pkg(), name), src)
		cdir := filepath.Join(e.root, "c", sb.pkg())
		e.write(filepath.Join(cdir, "cb.h"), []byte(header))
		e.write(filepath.Join(cdir, "gen.c"), []byte(csrc))
		lib := filepath.Join(e.root, "lib"+sb.pkg()+ext)
		if out, err := e.try(cdir, "", nil, cc, "-std=c11", "-Wall", "-Werror", "-shared", "-fPIC", "-o", lib, "gen.c"); err != nil {
			t.Fatalf("batch %d: cc: %v\n%s\n%s", i, err, out, csrc)
		}
		libs = append(libs, lib)
		fmt.Fprintf(&d, "\tif err := %s.Load(os.Args[%d]); err != nil {\n\t\tfail(\"Load %s: %%v\", err)\n\t}\n", sb.pkg(), i+1, sb.pkg())
		fmt.Fprintf(&d, "\t{\n\th, err := %s.GCreate()\n\tif err != nil {\n\t\tfail(\"GCreate %s: %%v\", err)\n\t}\n\t_ = h\n", sb.pkg(), sb.pkg())
		for _, sh := range sb.shapes {
			d.WriteString(sh.goDriver(sb.pkg()))
		}
		d.WriteString("\t}\n")
	}
	d.WriteString("}\n")
	e.write(filepath.Join(e.mod, "driver", "main.go"), []byte(d.String()))
	for _, goos := range []string{"linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			e.run(e.mod, "", []string{"GOOS=" + goos, "GOARCH=" + goarch}, e.goTool, "vet", "./...")
		}
	}
	bin := filepath.Join(e.root, "driver-bin")
	e.run(e.mod, "", nil, e.goTool, "build", "-o", bin, "./driver")
	out, err := e.try(e.root, "", nil, bin, libs...)
	if oks := strings.Count(out, "ok: "); err != nil || oks != 2*len(shapes) {
		t.Fatalf("the driver: %v; %d ok lines, want %d\n%s", err, oks, 2*len(shapes), tail(out, 3000))
	}
	t.Logf("%d shapes in %d packages", len(shapes), len(batches))
}
