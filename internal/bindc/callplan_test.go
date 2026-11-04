package bindc

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// TestCallPlanRefusals: Bind refuses every function whose call plan purego
// v0.11.3 cannot make as C does on all four targets, typed or raw-only,
// with the C function and the reason, one line each; with those functions
// skipped in the contract the rest of the header binds, and the Go package
// registers none of them. g_mixed_value is the verifier's trigger of round
// 2 (M1, a repeat): its raw function was registered with the words form
// and returned the bits of 1.5 instead of 7 on amd64. A call with a
// by-value struct that does not travel in registers on every target is
// refused with each target and its first position off the registers
// (amendment A6; g_large_after_stack is the verifier's case of round 4).
func TestCallPlanRefusals(t *testing.T) {
	u64s := func(prefix string, n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, "uint64_t "+prefix+string(rune('a'+i%26))+strings.Repeat("x", i/26))
		}
		return strings.Join(s, ", ")
	}
	f64s := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, "double d"+string(rune('a'+i)))
		}
		return strings.Join(s, ", ")
	}
	amd64s := "on linux/amd64, darwin/amd64: "
	arm64s := "on linux/arm64, darwin/arm64: "
	all := "on linux/amd64, linux/arm64, darwin/amd64, darwin/arm64: "
	stackStruct := "; " + stackStructRemedy
	refused := []struct{ decl, why string }{
		{"uint64_t g_mixed_value(g_mixed value)", "param 0 (value): by-value struct g_mixed: bytes 0 to 7 hold only floating-point fields and it is not an HFA"},
		// Raw-only (no rule covers ctx), and still refused: Load registers it.
		{"uint64_t g_mixed_ctx(void *ctx, g_mixed value)", "param 1 (value): by-value struct g_mixed: bytes 0 to 7"},
		{"g_mixed g_mixed_result(void)", "result: by-value struct g_mixed: bytes 0 to 7"},
		{"uint64_t g_float_pair(g_fpair value)", "by-value struct g_fpair: bytes 0 to 7 hold only floating-point fields"},
		{"uint64_t g_stack(" + u64s("s", 40) + ")", "purego v0.11.3 passes at most 26 stack words on amd64, and this call needs 34; " + callPlanRemedy},
		// A6: a call with a by-value struct must travel in registers on
		// every target; each target that fails is named with its first
		// position off the registers.
		{"uint64_t g_amd64_regs(" + u64s("n", 6) + ", g_pair p)", amd64s + "param 6 (p): by-value struct g_pair needs 2 general registers and 0 are left, so it goes on the stack" + stackStruct},
		{"uint64_t g_amd64_split(" + u64s("n", 5) + ", g_pair p)", amd64s + "param 5 (p): by-value struct g_pair needs 2 general registers and 1 are left, so it goes on the stack" + stackStruct},
		{"uint64_t g_amd64_floats(" + f64s(8) + ", g_hfa v)", amd64s + "param 8 (v): by-value struct g_hfa needs 1 floating-point registers and 0 are left, so it goes on the stack; " +
			arm64s + "param 8 (v): by-value struct g_hfa needs 2 floating-point registers and 0 are left, so it goes on the stack" + stackStruct},
		{"uint64_t g_arm64_regs(" + f64s(6) + ", g_quad q)", amd64s + "param 6 (q): by-value struct g_quad is 32 bytes, so amd64 passes it in memory; " +
			arm64s + "param 6 (q): by-value struct g_quad needs 4 floating-point registers and 2 are left, so it goes on the stack" + stackStruct},
		// The verifier's case of round 4 (M1): purego's darwin arm64
		// collector copied g_big's bytes inline after the ninth integer.
		{"uint64_t g_large_after_stack(uint64_t a, uint64_t b, uint64_t c, uint64_t d, uint64_t e, uint64_t f, uint64_t g, uint64_t h, uint64_t i, g_big v, uint64_t tail)",
			amd64s + "param 6 (g): it needs 1 general registers and 0 are left, so it goes on the stack; " +
				arm64s + "param 8 (i): it needs 1 general registers and 0 are left, so it goes on the stack" + stackStruct},
		// The verifier's second marshaller case of round 4: nine doubles
		// and a large struct.
		{"uint64_t g_nine_doubles(" + f64s(9) + ", g_big v)", all + "param 8 (di): it needs 1 floating-point registers and 0 are left, so it goes on the stack" + stackStruct},
		// The verifier's case of round 3, admitted in round 4: the seventh
		// integer goes on the stack on amd64.
		{"uint64_t g_banks(" + u64s("n", 7) + ", g_hfa v)", amd64s + "param 6 (ng): it needs 1 general registers and 0 are left, so it goes on the stack" + stackStruct},
		{"g_big g_big_result(uint64_t a)", amd64s + "result: by-value struct g_big is 24 bytes, so amd64 returns it in memory; " +
			arm64s + "result: by-value struct g_big is 24 bytes and not an HFA, so arm64 returns it in memory" + stackStruct},
		{"g_quad g_quad_result(uint64_t a)", amd64s + "result: by-value struct g_quad is 32 bytes, so amd64 returns it in memory" + stackStruct},
	}
	// Calls in registers on every target: a struct needs registers of its
	// own bank only, and a full other bank does not matter.
	admitted := []string{
		"uint64_t g_pair_sum(g_pair p)",
		"uint64_t g_six_then_hfa(" + u64s("n", 6) + ", g_hfa v)",
		"uint64_t g_pair_floats(" + f64s(8) + ", g_pair p)",
		"uint64_t g_hfa4_after_floats(" + f64s(4) + ", g_hfa4 v)",
		"g_pair g_pair_result(" + u64s("n", 4) + ")",
		"g_hfa g_hfa_result(" + f64s(6) + ")",
		"uint64_t g_spill_scalars(" + u64s("n", 12) + ", " + f64s(10) + ")",
	}
	header := callHeaderHead + `
typedef struct { double x; uint64_t y; } g_mixed;
typedef struct { float a; float b; uint32_t c; } g_fpair;
typedef struct { uint64_t a; uint64_t b; } g_pair;
typedef struct { uint64_t a; uint64_t b; uint64_t c; } g_big;
typedef struct { double a; double b; double c; double d; } g_quad;
typedef struct { float x; float y; } g_hfa;
typedef struct { float a; float b; float c; float d; } g_hfa4;
`
	for _, d := range admitted {
		header += d + ";\n"
	}
	for _, r := range refused {
		header += r.decl + ";\n"
	}
	header += "\n#endif\n"
	cc := &callCase{header: header, stats: map[string]int{}}
	cc.finish(t, callContract(false))
	m, err := Parse("gen.h", []byte(cc.header))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = Bind(m, []byte(cc.header), cc.contract)
	if err == nil {
		t.Fatalf("Bind accepted a header with %d functions purego cannot call as C does", len(refused))
	}
	lines := callPlanRefusals(err)
	var skips []Skip
	for _, r := range refused {
		name := strings.Fields(strings.SplitN(r.decl, "(", 2)[0])[1]
		why, ok := lines[name]
		switch {
		case !ok:
			t.Errorf("Bind did not refuse %s:\n%v", name, err)
		case !strings.Contains(why, r.why) || !strings.Contains(why, "; "+callPlanRemedy):
			t.Errorf("Bind refused %s with\n%q, want\n%q and %q", name, why, r.why, callPlanRemedy)
		}
		delete(lines, name)
		skips = append(skips, Skip{Name: name, Why: "purego cannot call it as C does"})
	}
	for name, why := range lines {
		t.Errorf("Bind refused %s, which has a valid call plan: %s", name, why)
	}
	c := callContract(false)
	c.Skips = skips
	cc.finish(t, c)
	b, err := Bind(m, []byte(cc.header), cc.contract)
	if err != nil {
		t.Fatalf("Bind with the refused functions skipped: %v", err)
	}
	_, src, err := GoPackage(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range admitted {
		name := strings.Fields(strings.SplitN(d, "(", 2)[0])[1]
		if !strings.Contains(string(src), rawName(name)+" ") {
			t.Errorf("the Go package has no raw function of %s", name)
		}
	}
	for _, s := range skips {
		if strings.Contains(string(src), rawName(s.Name)) {
			t.Errorf("the Go package registers %s, which the contract skips", s.Name)
		}
	}
}

// callPlanRefusals maps each function a Bind error refuses for its call
// plan ("function NAME: reason; skip it in the contract[ or pass a
// pointer]") to its reason.
func callPlanRefusals(err error) map[string]string {
	out := map[string]string{}
	if err == nil {
		return out
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "function ")
		if !ok || !strings.Contains(line, "; "+callPlanRemedy) {
			continue
		}
		name, why, _ := strings.Cut(rest, ": ")
		out[name] = why
	}
	return out
}

// skipCallPlans adds a contract skip for every function err refuses for
// its call plan, as the contract's author would, and returns the contract
// and the names; none when err refuses no call plan.
func skipCallPlans(t *testing.T, err error, contract []byte) ([]byte, []string) {
	t.Helper()
	refused := callPlanRefusals(err)
	if len(refused) == 0 {
		return contract, nil
	}
	var c Contract
	if err := json.Unmarshal(contract, &c); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range refused {
		c.Skips = append(c.Skips, Skip{Name: name, Why: "purego cannot call it as C does"})
		names = append(names, name)
	}
	sort.Strings(names)
	// The contract refuses an exception on a skipped function.
	keep := []Exception{}
	for _, e := range c.Exceptions {
		if _, ok := refused[e.Function]; !ok {
			keep = append(keep, e)
		}
	}
	c.Exceptions = keep
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return data, names
}
