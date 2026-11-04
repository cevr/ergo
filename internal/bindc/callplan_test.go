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
// and returned the bits of 1.5 instead of 7 on amd64.
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
	refused := []struct{ decl, why string }{
		{"uint64_t g_mixed_value(g_mixed value)", "param 0 (value): by-value struct g_mixed: bytes 0 to 7 hold only floating-point fields and it is not an HFA"},
		// Raw-only (no rule covers ctx), and still refused: Load registers it.
		{"uint64_t g_mixed_ctx(void *ctx, g_mixed value)", "param 1 (value): by-value struct g_mixed: bytes 0 to 7"},
		{"g_mixed g_mixed_result(void)", "result: by-value struct g_mixed: bytes 0 to 7"},
		{"uint64_t g_float_pair(g_fpair value)", "by-value struct g_fpair: bytes 0 to 7 hold only floating-point fields"},
		{"uint64_t g_amd64_regs(" + u64s("n", 6) + ", g_pair p)", "param 6 (p): by-value struct g_pair does not fit the registers left on amd64"},
		{"uint64_t g_arm64_stack(" + u64s("n", 9) + ", g_big b)", "param 9 (b): by-value struct g_big comes after an argument on the stack on arm64"},
		{"uint64_t g_arm64_regs(" + f64s(6) + ", g_quad q)", "param 6 (q): by-value struct g_quad does not fit the registers left on arm64"},
		{"uint64_t g_stack(" + u64s("s", 40) + ")", "purego v0.11.3 passes at most 26 stack words on amd64, and this call needs 34"},
	}
	header := callHeaderHead + `
typedef struct { double x; uint64_t y; } g_mixed;
typedef struct { float a; float b; uint32_t c; } g_fpair;
typedef struct { uint64_t a; uint64_t b; } g_pair;
typedef struct { uint64_t a; uint64_t b; uint64_t c; } g_big;
typedef struct { double a; double b; double c; double d; } g_quad;
uint64_t g_pair_sum(g_pair p);
`
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
		case !strings.Contains(why, r.why) || !strings.HasSuffix(why, "; "+callPlanRemedy):
			t.Errorf("Bind refused %s with %q, want %q and %q", name, why, r.why, callPlanRemedy)
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
	if !strings.Contains(string(src), rawName("g_pair_sum")+" ") {
		t.Errorf("the Go package has no raw function of g_pair_sum")
	}
	for _, s := range skips {
		if strings.Contains(string(src), rawName(s.Name)) {
			t.Errorf("the Go package registers %s, which the contract skips", s.Name)
		}
	}
}

// callPlanRefusals maps each function a Bind error refuses for its call
// plan ("function NAME: reason; skip it in the contract") to its reason.
func callPlanRefusals(err error) map[string]string {
	out := map[string]string{}
	if err == nil {
		return out
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "function ")
		if !ok || !strings.HasSuffix(line, callPlanRemedy) {
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
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return data, names
}
