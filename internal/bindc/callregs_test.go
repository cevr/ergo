package bindc

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The register property (orchestrator amendment A6, after wave 1c round
// 4): a function with a by-value struct argument or result is bound only
// when, on each of the four targets of the build constraint, every
// argument and the result travel in registers. The cases come from a grid,
// not from Bind: 0 to 9 integer scalars and 0 to 9 double scalars before a
// struct, in three orders (integers first, doubles first, shuffled), then
// one of the grid structs (small integer structs, HFAs, large structs and
// mixed float and integer structs), or no struct; at times a scalar after
// it, and at times a struct result. The oracle below labels each case from
// a table of the four targets, written from the calling conventions and
// without the product's planning code.

// oracleTarget is one target of "(linux || darwin) && (amd64 || arm64)":
// its argument registers (general, floating-point) and how its calling
// convention passes a struct.
type oracleTarget struct {
	name     string
	gpr, fpr int
	sysV     bool // System V AMD64 classes; else AAPCS64
}

// oracleTargets: System V gives 6 general and 8 SSE argument registers;
// AAPCS64 gives 8 general (x0-x7) and 8 floating-point (v0-v7), and
// Apple's arm64 convention keeps them (it differs in stack packing only).
var oracleTargets = []oracleTarget{
	{name: "linux/amd64", gpr: 6, fpr: 8, sysV: true},
	{name: "linux/arm64", gpr: 8, fpr: 8},
	{name: "darwin/amd64", gpr: 6, fpr: 8, sysV: true},
	{name: "darwin/arm64", gpr: 8, fpr: 8},
}

// oracleSize is the size of the scalars of the grid structs.
var oracleSize = map[Scalar]int{Float32: 4, Float64: 8, Uint32: 4, Uint64: 8}

// oracleLayout is the size of a grid struct (scalar fields only, natural
// alignment) and the offset of each field.
func oracleLayout(st *pStruct) (size int, offs []int) {
	align := 1
	for _, f := range st.fields {
		s := oracleSize[f.t.scalar]
		size = (size + s - 1) / s * s
		offs = append(offs, size)
		size += s
		align = max(align, s)
	}
	return (size + align - 1) / align * align, offs
}

// oracleHFA reports whether st has one to four fields of one
// floating-point type.
func oracleHFA(st *pStruct) bool {
	if len(st.fields) == 0 || len(st.fields) > 4 || !isFloat(st.fields[0].t.scalar) {
		return false
	}
	for _, f := range st.fields {
		if f.t.scalar != st.fields[0].t.scalar {
			return false
		}
	}
	return true
}

// oracleEightbytes is the System V class of each eightbyte of st: INTEGER
// (true) when a field that is not floating-point overlaps it, else SSE.
func oracleEightbytes(st *pStruct) []bool {
	size, offs := oracleLayout(st)
	cls := make([]bool, (size+7)/8)
	for i, f := range st.fields {
		if !isFloat(f.t.scalar) {
			for b := offs[i]; b < offs[i]+oracleSize[f.t.scalar]; b++ {
				cls[b/8] = true
			}
		}
	}
	return cls
}

// oracleForm reports whether st has one call form that purego passes as C
// does on amd64 and arm64: an HFA, a struct above 16 bytes, or a struct
// whose every eightbyte is INTEGER (arm64 passes up to 16 bytes that are
// not an HFA in general registers, so an SSE eightbyte has no match).
func oracleForm(st *pStruct) bool {
	size, _ := oracleLayout(st)
	if oracleHFA(st) || size > 16 {
		return true
	}
	for _, integer := range oracleEightbytes(st) {
		if !integer {
			return false
		}
	}
	return true
}

// regs is the number of general and floating-point registers in which t
// passes st, or ok false when t does not pass it in registers.
func (t oracleTarget) regs(st *pStruct) (gpr, fpr int, ok bool) {
	size, _ := oracleLayout(st)
	if t.sysV {
		if size > 16 {
			return 0, 0, false // MEMORY
		}
		for _, integer := range oracleEightbytes(st) {
			if integer {
				gpr++
			} else {
				fpr++
			}
		}
		return gpr, fpr, true
	}
	switch {
	case oracleHFA(st):
		return 0, len(st.fields), true
	case size > 16:
		return 0, 0, false // a pointer to a copy
	}
	return (size + 7) / 8, 0, true
}

// returnsInRegs reports whether t returns st in registers: System V up
// to 16 bytes; AAPCS64 an HFA (v0-v3) or up to 16 bytes (x0, x1).
func (t oracleTarget) returnsInRegs(st *pStruct) bool {
	size, _ := oracleLayout(st)
	return size <= 16 || (!t.sysV && oracleHFA(st))
}

// firstOffRegs is the first position of a call that t does not pass in
// registers ("result", or "param N (pN)"), or "".
func (t oracleTarget) firstOffRegs(params []*pType, res *pType) string {
	if res != nil && res.st != nil && !t.returnsInRegs(res.st) {
		return "result"
	}
	gpr, fpr := 0, 0
	for i, p := range params {
		pos := fmt.Sprintf("param %d (p%d)", i, i)
		g, f := 1, 0
		switch {
		case p.st != nil:
			var ok bool
			if g, f, ok = t.regs(p.st); !ok {
				return pos
			}
		case isFloat(p.scalar):
			g, f = 0, 1
		}
		if gpr+g > t.gpr || fpr+f > t.fpr {
			return pos
		}
		gpr, fpr = gpr+g, fpr+f
	}
	return ""
}

// regCase is the oracle's label of one function of the grid: the call
// form refusal, or the first position not in registers on each target
// that has one (none: Bind must admit it).
type regCase struct {
	decl   string
	noForm bool
	off    map[string]string // target -> first position
}

// oracleLabel labels a call. Only a call with a by-value struct argument
// or result is subject to the rule; any other call is admitted.
func oracleLabel(decl string, params []*pType, res *pType) regCase {
	c := regCase{decl: decl, off: map[string]string{}}
	structs := []*pStruct{}
	if res != nil && res.st != nil {
		structs = append(structs, res.st)
	}
	for _, p := range params {
		if p.st != nil {
			structs = append(structs, p.st)
		}
	}
	if len(structs) == 0 {
		return c
	}
	for _, st := range structs {
		if !oracleForm(st) {
			c.noForm = true
			return c
		}
	}
	for _, t := range oracleTargets {
		if pos := t.firstOffRegs(params, res); pos != "" {
			c.off[t.name] = pos
		}
	}
	return c
}

// regStructs are the structs of the grid: HFAs of 1 to 4 floats or
// doubles (3 and 4 doubles are above 16 bytes), integer structs of 1 to 4
// uint32_t or uint64_t (3 and 4 uint64_t are above 16 bytes), and mixed
// ones: {float, uint32_t} (one INTEGER eightbyte), {double, uint64_t} and
// {uint64_t, double} (an SSE eightbyte: no call form), and {double,
// uint64_t, uint64_t} (24 bytes).
func regStructs() []*pStruct {
	var out []*pStruct
	for _, m := range []struct {
		tag string
		s   Scalar
	}{{"f", Float32}, {"d", Float64}, {"u", Uint32}, {"q", Uint64}} {
		for n := 1; n <= 4; n++ {
			st := &pStruct{c: "g_" + m.tag + strconv.Itoa(n)}
			for i := 0; i < n; i++ {
				st.fields = append(st.fields, pField{c: "m" + strconv.Itoa(i), t: &pType{scalar: m.s}})
			}
			out = append(out, st)
		}
	}
	for _, m := range []struct {
		tag string
		s   []Scalar
	}{{"xfu", []Scalar{Float32, Uint32}}, {"xdq", []Scalar{Float64, Uint64}}, {"xqd", []Scalar{Uint64, Float64}}, {"xdqq", []Scalar{Float64, Uint64, Uint64}}} {
		st := &pStruct{c: "g_" + m.tag}
		for i, s := range m.s {
			st.fields = append(st.fields, pField{c: "m" + strconv.Itoa(i), t: &pType{scalar: s}})
		}
		out = append(out, st)
	}
	return out
}

// callGenRegs generates the grid as a call case, with the C library, the
// typed and raw calls, and the oracle's label of every function.
func callGenRegs(t *testing.T, seed uint64) (*callCase, map[string]regCase) {
	r := rand.New(rand.NewPCG(seed, 0xa6))
	cc := &callCase{stats: map[string]int{}}
	labels := map[string]regCase{}
	var h, c, d strings.Builder
	h.WriteString(callHeaderHead + "\n")
	c.WriteString("#include <string.h>\n#include \"gen.h\"\n\n" + callMixC + "\nuint32_t g_abi(void) { return G_ABI; }\n\n")
	d.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"ergo.local/bind/gen\"\n)\n\nfunc main() {\n")
	d.WriteString("\tif err := gen.Load(os.Args[1]); err != nil {\n\t\tfmt.Println(\"load:\", err)\n\t\tos.Exit(1)\n\t}\n\tbad := 0\n")
	d.WriteString("\tcheck := func(name string, ok bool, got, want any, err error) {\n\t\tif ok && err == nil {\n\t\t\tfmt.Println(\"ok:\", name)\n\t\t\treturn\n\t\t}\n\t\tbad++\n\t\tfmt.Printf(\"bad: %s: got %#v, want %#v, error %v\\n\", name, got, want, err)\n\t}\n")
	structs := regStructs()
	for _, st := range structs {
		h.WriteString("typedef struct " + st.c + " {\n")
		for _, f := range st.fields {
			h.WriteString("    " + f.t.cDecl(f.c) + ";\n")
		}
		h.WriteString("} " + st.c + ";\n")
	}
	h.WriteString("\n")
	kinds := append([]*pStruct{nil}, structs...) // nil: no struct argument
	for ints := 0; ints <= 9; ints++ {
		for floats := 0; floats <= 9; floats++ {
			for _, st := range kinds {
				tag := "none"
				if st != nil {
					tag = strings.TrimPrefix(st.c, "g_")
				}
				name := fmt.Sprintf("g_i%df%d_%s", ints, floats, tag)
				var pts []*pType
				switch r.IntN(3) {
				case 0: // integers first
					for i := 0; i < ints+floats; i++ {
						pts = append(pts, &pType{scalar: map[bool]Scalar{true: Uint64, false: Float64}[i < ints]})
					}
				case 1: // doubles first
					for i := 0; i < ints+floats; i++ {
						pts = append(pts, &pType{scalar: map[bool]Scalar{true: Float64, false: Uint64}[i < floats]})
					}
				default:
					for i := 0; i < ints+floats; i++ {
						pts = append(pts, &pType{scalar: map[bool]Scalar{true: Uint64, false: Float64}[i < ints]})
					}
					r.Shuffle(len(pts), func(a, b int) { pts[a], pts[b] = pts[b], pts[a] })
				}
				if st != nil {
					pts = append(pts, &pType{st: st})
				}
				switch r.IntN(3) {
				case 0:
					pts = append(pts, &pType{scalar: Uint64})
				case 1:
					pts = append(pts, &pType{scalar: Float64})
				}
				var res *pType
				if r.IntN(4) == 0 {
					res = &pType{st: structs[r.IntN(len(structs))]}
				}
				if len(pts) == 0 {
					pts = append(pts, &pType{scalar: Uint64})
				}

				var params, args []string
				raw := rawFn{name: name}
				var body strings.Builder
				hash := uint64(14695981039346656037)
				body.WriteString("    uint64_t h = UINT64_C(14695981039346656037);\n")
				for pi, pt := range pts {
					pn := "p" + strconv.Itoa(pi)
					params = append(params, pt.cDecl(pn))
					var ls []leafVal
					pt.leafPaths("", &ls)
					for i := range ls {
						ls[i].v = truncate(ls[i].s, r.Uint64())
					}
					k := 0
					args = append(args, pt.goLit("gen", ls, &k))
					k = 0
					raw.args = append(raw.args, rawArg{t: pt, lit: pt.goLit("", ls, &k)})
					for _, l := range ls {
						l.path = pn + l.path
						body.WriteString("    h = mix(h, " + cLeafU64(l) + ");\n")
						hash = mix(hash, l.v)
					}
				}
				resC := "uint64_t"
				if res != nil {
					resC = res.st.c
				}
				decl := resC + " " + name + "(" + strings.Join(params, ", ") + ")"
				h.WriteString(decl + ";\n")
				c.WriteString(decl + " {\n" + body.String())
				var want string
				if res == nil {
					c.WriteString("    return h;\n}\n\n")
					want = "uint64(" + strconv.FormatUint(hash, 10) + ")"
					raw.want = want
				} else {
					var ls []leafVal
					res.leafPaths("", &ls)
					c.WriteString("    " + resC + " r;\n    memset(&r, 0, sizeof r);\n")
					for i := range ls {
						ls[i].v = truncate(ls[i].s, mix(hash, uint64(i+1)))
						l := ls[i]
						l.path = "r" + l.path
						c.WriteString("    " + cLeafSet(l, "mix(h, UINT64_C("+strconv.Itoa(i+1)+"))") + "\n")
					}
					c.WriteString("    return r;\n}\n\n")
					k := 0
					want = res.goLit("gen", ls, &k)
					k = 0
					raw.res, raw.want = res, res.goLit("", ls, &k)
				}
				cc.raws = append(cc.raws, raw)
				cc.calls = append(cc.calls, callSnippet{name, fmt.Sprintf("\t{\n\t\tgot, err := gen.%s(%s)\n\t\twant := %s\n\t\tcheck(%q, got == want, got, want, err)\n\t}\n",
					goCamel(strings.TrimPrefix(name, "g_")), strings.Join(args, ", "), want, name)})

				lab := oracleLabel(decl, pts, res)
				labels[name] = lab
				regStats(cc.stats, lab, pts, res)
			}
		}
	}
	h.WriteString("\n#endif\n")
	cc.header = h.String()
	cc.csrc = c.String()
	cc.driverHead = d.String()
	cc.finish(t, callContract(false))
	return cc, labels
}

// regStats counts the shapes of the grid the oracle labels.
func regStats(stats map[string]int, lab regCase, pts []*pType, res *pType) {
	hasStruct := res != nil
	for _, p := range pts {
		hasStruct = hasStruct || p.st != nil
	}
	switch {
	case !hasStruct:
		ints, floats := 0, 0
		for _, p := range pts {
			if isFloat(p.scalar) {
				floats++
			} else {
				ints++
			}
		}
		if ints > 8 || floats > 8 {
			stats["admitted: scalars on the stack on every target"]++
		} else {
			stats["admitted: no struct"]++
		}
	case lab.noForm:
		stats["refused: no call form"]++
	case len(lab.off) == 0:
		stats["admitted: struct in registers"]++
		if res != nil {
			stats["admitted: struct result"]++
		}
	default:
		var ts []string
		for _, t := range oracleTargets {
			if _, ok := lab.off[t.name]; ok {
				ts = append(ts, t.name)
			}
		}
		stats["refused on "+strings.Join(ts, ", ")]++
		for _, pos := range lab.off {
			switch {
			case pos == "result":
				stats["refused at the result"]++
			case pos == fmt.Sprintf("param %d (p%d)", len(pts)-1, len(pts)-1) && pts[len(pts)-1].st == nil:
				stats["refused at a trailing scalar"]++
			}
		}
	}
}

// regRefusal is a call-plan refusal of Bind for registers, parsed: the
// first position Bind names for each target.
func regRefusal(why string) (map[string]string, bool) {
	body, ok := strings.CutSuffix(why, "; "+stackStructRemedy)
	if !ok {
		return nil, false
	}
	off := map[string]string{}
	for _, seg := range strings.Split(body, "; ") {
		seg, ok := strings.CutPrefix(seg, "on ")
		if !ok {
			return nil, false
		}
		targets, rest, _ := strings.Cut(seg, ": ")
		pos, _, _ := strings.Cut(rest, ": ")
		for _, t := range strings.Split(targets, ", ") {
			off[t] = pos
		}
	}
	return off, true
}

// checkRegAdmission requires Bind to admit exactly the functions of the
// grid the oracle admits, and to refuse each other one with the oracle's
// reason: no call form, or each target on which a position is not in
// registers, with that first position.
func checkRegAdmission(t *testing.T, cc *callCase, labels map[string]regCase) {
	t.Helper()
	m, err := Parse("gen.h", []byte(cc.header))
	if err != nil {
		t.Fatalf("the generated header does not parse: %v", err)
	}
	_, err = Bind(m, []byte(cc.header), cc.contract)
	refused := callPlanRefusals(err)
	if err != nil && len(refused) == 0 {
		t.Fatalf("Bind: %v", err)
	}
	var wrong []string
	for name, lab := range labels {
		why, isRefused := refused[name]
		switch {
		case lab.noForm:
			if !isRefused || !strings.Contains(why, "hold only floating-point fields") || !strings.HasSuffix(why, "; "+callPlanRemedy) {
				wrong = append(wrong, fmt.Sprintf("%s: want a refusal for its call form, got %q", lab.decl, why))
			}
		case len(lab.off) == 0:
			if isRefused {
				wrong = append(wrong, fmt.Sprintf("%s: refused a call that travels in registers on every target: %s", lab.decl, why))
			}
		default:
			got, ok := regRefusal(why)
			if !isRefused || !ok {
				wrong = append(wrong, fmt.Sprintf("%s: admitted or refused without the registers rule, want %v: %q", lab.decl, lab.off, why))
				continue
			}
			if fmt.Sprint(got) != fmt.Sprint(lab.off) {
				wrong = append(wrong, fmt.Sprintf("%s: refused with first positions %v, want %v: %q", lab.decl, got, lab.off, why))
			}
		}
	}
	for name := range refused {
		if _, ok := labels[name]; !ok {
			wrong = append(wrong, "refused "+name+", which is not in the grid")
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Fatalf("%d of %d cases wrong:\n%s", len(wrong), len(labels), strings.Join(wrong[:min(len(wrong), 40)], "\n"))
	}
}

// TestCallPlanRegisters: over the grid, Bind admits exactly the calls
// that the four-target oracle puts in registers on every target (and the
// calls without a by-value struct), and refuses every other one with each
// target that fails and its first position. The gate runs it.
func TestCallPlanRegisters(t *testing.T) {
	stats := map[string]int{}
	for i := 0; i < max(1, *propCases/250); i++ {
		cc, labels := callGenRegs(t, *propSeed+uint64(i))
		checkRegAdmission(t, cc, labels)
		for k, v := range cc.stats {
			stats[k] += v
		}
	}
	t.Logf("register grid: %v", stats)
	for _, k := range []string{"admitted: struct in registers", "admitted: struct result", "admitted: no struct",
		"admitted: scalars on the stack on every target", "refused: no call form",
		"refused on linux/amd64, darwin/amd64", "refused on linux/arm64, darwin/arm64",
		"refused on linux/amd64, linux/arm64, darwin/amd64, darwin/arm64",
		"refused at the result", "refused at a trailing scalar"} {
		if stats[k] == 0 {
			t.Errorf("the grid made no case of %q", k)
		}
	}
}

// TestLiveRegisterProperty (ERGO_BINDC_LIVE=1; cc and the network): every
// call of the grid that Bind admits gives, typed and raw, the hash C
// computes from what it received, or the struct result C built from it.
func TestLiveRegisterProperty(t *testing.T) {
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to call generated C libraries through the real purego")
	}
	for i := 0; i < max(1, *propCases/1000); i++ {
		cc, labels := callGenRegs(t, *propSeed+uint64(i))
		checkRegAdmission(t, cc, labels)
		admitted := 0
		for _, lab := range labels {
			if !lab.noForm && len(lab.off) == 0 {
				admitted++
			}
		}
		cc.driverSet(t, cc.bind(t))
		if cc.typed != admitted || cc.raw != cc.typed {
			t.Fatalf("typed %d, raw %d, want %d each", cc.typed, cc.raw, admitted)
		}
		runCallCase(t, cc)
	}
}
