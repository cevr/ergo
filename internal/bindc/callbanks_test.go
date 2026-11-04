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

// The bank property (wave 1c round 4): a by-value struct needs registers of
// one bank only (general for an integer struct, floating-point for an
// HFA), so scalars that fill the other bank must not make Bind refuse it,
// and a struct whose own bank has too few registers left must be refused.
// The cases come from a grid, not from Bind: 0 to 8 integer scalars and 0
// to 9 double scalars before the struct, in three orders (integers first,
// doubles first, shuffled), then an HFA of 1 to 4 float or double members
// or an integer struct of 1 to 4 uint32_t or uint64_t members, and at
// times a scalar after it. bankValid, written from the two conventions,
// labels each case.

// bankStructs are the structs of the bank property.
func bankStructs() []*pStruct {
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
	return out
}

// bankValid is whether C and purego v0.11.3 agree on a call that passes st
// after ints integer and floats floating-point scalars, on amd64 (System
// V) and arm64 (AAPCS64), or the bank that has too few registers left.
//
// amd64: a struct of up to 16 bytes takes one register per eightbyte, all
// from the bank of its class (6 general, 8 SSE); a larger one goes in
// memory. arm64: an HFA takes one floating-point register per member (8);
// another struct of up to 16 bytes takes one general register per
// eightbyte (8); a larger one is a pointer to a copy, an integer. When the
// bank has too few left, C passes the struct on the stack, and purego does
// not do that as C does: the case must be refused.
func bankValid(st *pStruct, ints, floats int) (bool, string) {
	m, n := st.fields[0].t.scalar, len(st.fields)
	size := n * int(scalarSize[m])
	words := (size + 7) / 8
	switch {
	case size <= 16 && isFloat(m) && floats+words > 8:
		return false, "amd64 floating-point"
	case size <= 16 && !isFloat(m) && ints+words > 6:
		return false, "amd64 general"
	case isFloat(m) && floats+n > 8:
		return false, "arm64 floating-point"
	case !isFloat(m) && size <= 16 && ints+words > 8:
		return false, "arm64 general"
	}
	return true, ""
}

// callGenBanks generates the bank grid as a call case, with the C
// library, the typed and raw calls, and the label of every function. It
// keeps the verifier's case of round 3: g_banks, seven integers and a
// float HFA, returns 37.
func callGenBanks(t *testing.T, seed uint64) (*callCase, map[string]string) {
	r := rand.New(rand.NewPCG(seed, 0xba4c))
	cc := &callCase{stats: map[string]int{}}
	invalid := map[string]string{}
	var h, c, d strings.Builder
	h.WriteString(callHeaderHead + "\n")
	c.WriteString("#include \"gen.h\"\n\n" + callMixC + "\nuint32_t g_abi(void) { return G_ABI; }\n\n")
	d.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"ergo.local/bind/gen\"\n)\n\nfunc main() {\n")
	d.WriteString("\tif err := gen.Load(os.Args[1]); err != nil {\n\t\tfmt.Println(\"load:\", err)\n\t\tos.Exit(1)\n\t}\n\tbad := 0\n")
	d.WriteString("\tcheck := func(name string, ok bool, got, want any, err error) {\n\t\tif ok && err == nil {\n\t\t\tfmt.Println(\"ok:\", name)\n\t\t\treturn\n\t\t}\n\t\tbad++\n\t\tfmt.Printf(\"bad: %s: got %#v, want %#v, error %v\\n\", name, got, want, err)\n\t}\n")
	structs := bankStructs()
	for _, st := range structs {
		h.WriteString("typedef struct " + st.c + " {\n")
		for _, f := range st.fields {
			h.WriteString("    " + f.t.cDecl(f.c) + ";\n")
		}
		h.WriteString("} " + st.c + ";\n")
	}
	h.WriteString("\n")

	// The verifier's case.
	h.WriteString("typedef struct { float x; float y; } g_hfa;\nuint64_t g_banks(uint64_t a, uint64_t b, uint64_t c, uint64_t d, uint64_t e, uint64_t f, uint64_t g, g_hfa v);\n")
	c.WriteString("uint64_t g_banks(uint64_t a, uint64_t b, uint64_t c, uint64_t d, uint64_t e, uint64_t f, uint64_t g, g_hfa v) {\n    return a + b + c + d + e + f + g + (uint64_t)v.x + (uint64_t)v.y;\n}\n\n")
	cc.calls = append(cc.calls, callSnippet{"g_banks",
		"\t{\n\t\tgot, err := gen.Banks(1, 2, 3, 4, 5, 6, 7, gen.Hfa{X: 4, Y: 5})\n\t\tcheck(\"g_banks\", got == 37, got, uint64(37), err)\n\t}\n"})
	hfa := &pType{st: &pStruct{c: "g_hfa", fields: []pField{{c: "x", t: &pType{scalar: Float32}}, {c: "y", t: &pType{scalar: Float32}}}}}
	var bargs []rawArg
	for i := 1; i <= 7; i++ {
		bargs = append(bargs, rawArg{t: &pType{scalar: Uint64}, lit: strconv.Itoa(i)})
	}
	cc.raws = append(cc.raws, rawFn{name: "g_banks", args: append(bargs, rawArg{t: hfa, lit: "Hfa{X: 4, Y: 5}"}), want: "uint64(37)"})

	for ints := 0; ints <= 8; ints++ {
		for floats := 0; floats <= 9; floats++ {
			for _, st := range structs {
				order := r.IntN(3)
				name := fmt.Sprintf("g_b%dx%d_%s", ints, floats, strings.TrimPrefix(st.c, "g_"))
				var pts []*pType
				for i := 0; i < ints; i++ {
					pts = append(pts, &pType{scalar: Uint64})
				}
				for i := 0; i < floats; i++ {
					pts = append(pts, &pType{scalar: Float64})
				}
				switch order {
				case 1:
					for i := range pts {
						pts[i] = &pType{scalar: Float64}
						if i >= floats {
							pts[i] = &pType{scalar: Uint64}
						}
					}
				case 2:
					r.Shuffle(len(pts), func(a, b int) { pts[a], pts[b] = pts[b], pts[a] })
				}
				pts = append(pts, &pType{st: st})
				switch r.IntN(3) {
				case 0:
					pts = append(pts, &pType{scalar: Uint64})
				case 1:
					pts = append(pts, &pType{scalar: Float64})
				}
				if ok, why := bankValid(st, ints, floats); !ok {
					invalid[name] = why
					cc.stats["refused: "+why]++
				} else {
					cc.stats["valid"]++
					if st.fields[0].t.scalar == Float32 || st.fields[0].t.scalar == Float64 {
						if ints >= 6 {
							cc.stats["valid: float struct, general bank full on amd64"]++
						}
					} else if floats >= 8 {
						cc.stats["valid: integer struct, floating-point bank full"]++
					}
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
				decl := "uint64_t " + name + "(" + strings.Join(params, ", ") + ")"
				h.WriteString(decl + ";\n")
				c.WriteString(decl + " {\n" + body.String() + "    return h;\n}\n\n")
				want := "uint64(" + strconv.FormatUint(hash, 10) + ")"
				raw.want = want
				cc.raws = append(cc.raws, raw)
				cc.calls = append(cc.calls, callSnippet{name, fmt.Sprintf("\t{\n\t\tgot, err := gen.%s(%s)\n\t\twant := %s\n\t\tcheck(%q, got == want, got, want, err)\n\t}\n",
					goCamel(strings.TrimPrefix(name, "g_")), strings.Join(args, ", "), want, name)})
			}
		}
	}
	h.WriteString("\n#endif\n")
	cc.header = h.String()
	cc.csrc = c.String()
	cc.driverHead = d.String()
	cc.finish(t, callContract(false))
	return cc, invalid
}

// checkBankAdmission requires Bind to refuse exactly the functions of the
// grid that bankValid labels invalid, each with the bank it lacks.
func checkBankAdmission(t *testing.T, cc *callCase, invalid map[string]string) {
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
	for name, why := range refused {
		want, bad := invalid[name]
		bank := map[string]string{"amd64 general": "general registers on amd64", "amd64 floating-point": "floating-point registers on amd64",
			"arm64 general": "general registers on arm64", "arm64 floating-point": "floating-point registers on arm64"}[want]
		switch {
		case !bad:
			wrong = append(wrong, "refused a valid call: "+name+": "+why)
		case !strings.Contains(why, bank):
			wrong = append(wrong, fmt.Sprintf("refused %s for %q, want the %s bank", name, why, want))
		}
	}
	for name := range invalid {
		if _, ok := refused[name]; !ok {
			wrong = append(wrong, "admitted "+name+", whose "+invalid[name]+" bank has too few registers left")
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Fatalf("%d of %d cases wrong:\n%s", len(wrong), len(cc.calls), strings.Join(wrong[:min(len(wrong), 40)], "\n"))
	}
}

// TestCallPlanBanks: over the grid, Bind admits every call valid by
// construction (the verifier's g_banks included) and refuses every call
// whose struct's own bank is exhausted. The gate runs it.
func TestCallPlanBanks(t *testing.T) {
	stats := map[string]int{}
	for i := 0; i < max(1, *propCases/250); i++ {
		cc, invalid := callGenBanks(t, *propSeed+uint64(i))
		checkBankAdmission(t, cc, invalid)
		for k, v := range cc.stats {
			stats[k] += v
		}
	}
	t.Logf("bank grid: %v", stats)
	for _, k := range []string{"valid", "valid: float struct, general bank full on amd64", "valid: integer struct, floating-point bank full",
		"refused: amd64 general", "refused: amd64 floating-point", "refused: arm64 floating-point"} {
		if stats[k] == 0 {
			t.Errorf("the grid made no case of %q", k)
		}
	}
}

// TestLiveBankProperty (ERGO_BINDC_LIVE=1; cc and the network): every call
// of the grid that Bind admits gives, typed and raw, the hash C computes
// from what it received; g_banks returns 37.
func TestLiveBankProperty(t *testing.T) {
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to call generated C libraries through the real purego")
	}
	for i := 0; i < max(1, *propCases/1000); i++ {
		cc, invalid := callGenBanks(t, *propSeed+uint64(i))
		checkBankAdmission(t, cc, invalid)
		cc.driverSet(t, cc.bind(t))
		if cc.typed != len(cc.calls)-len(invalid) || cc.raw != cc.typed {
			t.Fatalf("typed %d, raw %d, want %d each", cc.typed, cc.raw, len(cc.calls)-len(invalid))
		}
		runCallCase(t, cc)
	}
}
