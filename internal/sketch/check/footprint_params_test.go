package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// fpParamCaps is the pool of capabilities of the generated programs.
const fpParamCaps = 3

// fpCallee is a generated function hI(n int, p func(x int) int) int (H2-fp):
// the capabilities its body calls, an opaque call of its own (a local
// function value), the calls of p, and its calls of other generated
// functions (itself and earlier ones included, so recursion and mutual
// recursion occur) that pass p on, either directly or inside a literal
// that may call p. An escaping callee's p is `escaping`, so its argument
// is unknown code.
type fpCallee struct {
	caps     []int
	opaque   bool
	pcalls   int
	escaping bool
	forwards []fpForward
}

// fpForward is a call of callee to passing p on: direct passes p itself;
// otherwise a literal with caps that calls p when callsP is set.
type fpForward struct {
	to     int
	direct bool
	caps   []int
	callsP bool
}

// fpLit is a literal argument of the subject's call: its capabilities,
// and a call of callee inner (with a pure literal) when inner >= 0. named
// passes the function `named` instead, whose body calls named's caps.
type fpLit struct {
	caps     []int
	inner    int
	named    bool
	trailing bool
}

// fpSubjectCall is a call of callee to by the subject, with arg.
type fpSubjectCall struct {
	to  int
	arg fpLit
}

type fpParamCase struct {
	callees  []fpCallee
	calls    []fpSubjectCall
	named    []int
	imported bool
}

func genFpParamCase(r *rand.Rand) fpParamCase {
	var c fpParamCase
	pick := func() []int {
		var out []int
		for k := range fpParamCaps {
			if r.Intn(3) == 0 {
				out = append(out, k)
			}
		}
		return out
	}
	n := 1 + r.Intn(5)
	for range n {
		h := fpCallee{caps: pick(), opaque: r.Intn(6) == 0, pcalls: r.Intn(3), escaping: r.Intn(8) == 0}
		for range r.Intn(3) {
			to := r.Intn(n)
			if h.escaping {
				continue
			}
			h.forwards = append(h.forwards, fpForward{to: to, direct: r.Intn(2) == 0, caps: pick(), callsP: r.Intn(2) == 0})
		}
		c.callees = append(c.callees, h)
	}
	// A forward passes p to a non-escaping parameter only; drop those
	// that reach an escaping callee (it cannot take p).
	for i := range c.callees {
		c.callees[i].forwards = slices.DeleteFunc(c.callees[i].forwards, func(f fpForward) bool { return c.callees[f.to].escaping })
	}
	c.named = pick()
	for range 1 + r.Intn(3) {
		lit := fpLit{caps: pick(), inner: -1, named: r.Intn(5) == 0, trailing: r.Intn(2) == 0}
		if r.Intn(3) == 0 {
			lit.inner = r.Intn(n)
			if c.callees[lit.inner].escaping {
				lit.inner = -1
			}
		}
		c.calls = append(c.calls, fpSubjectCall{r.Intn(n), lit})
	}
	c.imported = r.Intn(2) == 0
	return c
}

func fpRow(q string) string {
	var parts []string
	for k := range fpParamCaps {
		parts = append(parts, fmt.Sprintf("%sC%d", q, k))
	}
	return " needs " + strings.Join(parts, " | ")
}

func fpCapCalls(q string, caps []int) string {
	var b strings.Builder
	for _, k := range caps {
		fmt.Fprintf(&b, "let _ = %sC%d.Get%d(); ", q, k, k)
	}
	return b.String()
}

// sources writes the callees' package (lib when imported) and the
// subject's (main, or the same file).
func (c fpParamCase) sources() (lib, main string) {
	q := ""
	if c.imported {
		q = "lib."
	}
	var d strings.Builder
	for k := range fpParamCaps {
		fmt.Fprintf(&d, "type C%d { Get%d() int }\n", k, k)
	}
	ft := "func(x int) int" + fpRow("")
	for i, h := range c.callees {
		esc := ""
		if h.escaping {
			esc = "escaping "
		}
		fmt.Fprintf(&d, "\nfunc H%d(n int, p %s%s) int%s {\n\t%s", i, esc, ft, fpRow(""), fpCapCalls("", h.caps))
		if h.opaque {
			d.WriteString("let g = func(x int) int { x }; let _ = g(1); ")
		}
		for range h.pcalls {
			d.WriteString("let _ = p(n); ")
		}
		for _, f := range h.forwards {
			arg := "p"
			if !f.direct {
				call := "x"
				if f.callsP {
					call = "p(x)"
				}
				arg = "func(x int) int { " + fpCapCalls("", f.caps) + call + " }"
			}
			fmt.Fprintf(&d, "if n > 0 { let _ = H%d(n - 1, %s) }; ", f.to, arg)
		}
		d.WriteString("n\n}\n")
	}
	fmt.Fprintf(&d, "\nfunc Named(x int) int%s { %sx }\n", fpRow(""), fpCapCalls("", c.named))
	var s strings.Builder
	fmt.Fprintf(&s, "\nfunc subject() int%s {\n", fpRow(q))
	for _, call := range c.calls {
		a := call.arg
		body := fpCapCalls(q, a.caps)
		if a.inner >= 0 {
			body += fmt.Sprintf("let _ = %sH%d(x, func(y int) int { y }); ", q, a.inner)
		}
		switch {
		case a.named:
			fmt.Fprintf(&s, "\tlet _ = %sH%d(1, %sNamed)\n", q, call.to, q)
		case a.trailing:
			fmt.Fprintf(&s, "\tlet _ = %sH%d(1) func(x int) int { %sx }\n", q, call.to, body)
		default:
			fmt.Fprintf(&s, "\tlet _ = %sH%d(1, func(x int) int { %sx })\n", q, call.to, body)
		}
	}
	s.WriteString("\t0\n}\n")
	if c.imported {
		return d.String(), "import lib\n" + s.String()
	}
	return "", d.String() + s.String()
}

// fpRef is a reference footprint: capabilities and unknown code.
type fpRef struct {
	caps   map[int]bool
	opaque bool
}

func (a *fpRef) join(b fpRef) bool {
	changed := false
	for k := range b.caps {
		if !a.caps[k] {
			a.caps[k] = true
			changed = true
		}
	}
	if b.opaque && !a.opaque {
		a.opaque = true
		changed = true
	}
	return changed
}

func fpOf(caps []int) fpRef {
	r := fpRef{caps: map[int]bool{}}
	for _, k := range caps {
		r.caps[k] = true
	}
	return r
}

// model is the reference: each callee's footprint is own ∪ (runsP ? arg),
// the least fixpoint over its forwards: a direct forward to j adds j's own
// and passes runsP on; a literal forward adds the literal's caps and j's
// own, and passes runsP on when the literal calls p.
func (c fpParamCase) model() (own []fpRef, runsP []bool, subject fpRef) {
	own = make([]fpRef, len(c.callees))
	runsP = make([]bool, len(c.callees))
	for i, h := range c.callees {
		own[i] = fpOf(h.caps)
		own[i].opaque = h.opaque
		runsP[i] = h.pcalls > 0 && !h.escaping
		if h.escaping && h.pcalls > 0 {
			own[i].opaque = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i, h := range c.callees {
			for _, f := range h.forwards {
				changed = own[i].join(own[f.to]) || changed
				if !runsP[f.to] {
					continue
				}
				if f.direct || f.callsP {
					if !runsP[i] {
						runsP[i] = true
						changed = true
					}
				}
				if !f.direct {
					changed = own[i].join(fpOf(f.caps)) || changed
				}
			}
		}
	}
	subject = fpRef{caps: map[int]bool{}}
	for _, call := range c.calls {
		subject.join(own[call.to])
		if !runsP[call.to] {
			continue
		}
		if call.arg.named {
			subject.join(fpOf(c.named))
			continue
		}
		subject.join(fpOf(call.arg.caps))
		if call.arg.inner >= 0 {
			subject.join(own[call.arg.inner])
		}
	}
	return own, runsP, subject
}

func (r fpRef) String() string {
	var caps []string
	for k := range fpParamCaps {
		if r.caps[k] {
			caps = append(caps, fmt.Sprintf("C%d", k))
		}
	}
	return fmt.Sprintf("caps=%v opaque=%v", caps, r.opaque)
}

// fpParamImporter serves the generated lib and the standard library.
type fpParamImporter struct{ lib *check.Package }

func (i fpParamImporter) Import(path string) (*check.Package, error) {
	if path == "lib" && i.lib != nil {
		return i.lib, nil
	}
	return check.Std.Import(path)
}

func fpCheck(t *testing.T, path, src string, imp check.Importer) *check.Package {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, src)
	}
	pkg, ds := check.Check(path, []*syntax.File{f}, imp)
	if len(ds) != 0 {
		t.Fatalf("check %s: %v\n%s", path, ds, src)
	}
	return pkg
}

func fpShow(fp check.Footprint) string {
	var caps []string
	for _, c := range fp.Caps {
		s := c.String()
		caps = append(caps, s[strings.LastIndex(s, ".")+1:])
	}
	return fmt.Sprintf("caps=%v opaque=%v", caps, fp.Opaque)
}

// TestFootprintThroughParamsProperty (H2-fp; rows.md, Footprints 3): a
// call of a function with a non-escaping function parameter joins the
// callee's own footprint with the footprint of the argument in place of
// the parameter, when the callee runs it, at any depth of known callees
// that pass the parameter on, directly or inside a literal, through
// recursion and mutual recursion, in the same package or an imported
// one. The argument is a function literal, a trailing function literal,
// or a named function. An escaping parameter stays unknown code. Each
// generated callee's own footprint counts its parameter as unknown code
// (Opaque) and names it in Params exactly when it runs it. The reference
// is a least fixpoint over the generated call graph (model). A failing
// seed prints the Ergo source, both footprints and the replay command.
func TestFootprintThroughParamsProperty(t *testing.T) {
	seeds := 1500
	first := int64(1)
	if s := os.Getenv("ERGO_FP_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	reached := map[string]bool{}
	for seed := first; seed < first+int64(seeds); seed++ {
		c := genFpParamCase(rand.New(rand.NewSource(seed)))
		libSrc, mainSrc := c.sources()
		var lib *check.Package
		if c.imported {
			lib = fpCheck(t, "lib", libSrc, fpParamImporter{})
		}
		pkg := fpCheck(t, "main", mainSrc, fpParamImporter{lib: lib})
		own, runsP, want := c.model()
		replay := fmt.Sprintf("ERGO_FP_SEED=%d go test ./internal/sketch/check -run TestFootprintThroughParamsProperty", seed)
		calleePkg := pkg
		if c.imported {
			calleePkg = lib
		}
		for _, f := range calleePkg.Funcs {
			if !strings.HasPrefix(f.Name, "H") {
				continue
			}
			i, _ := strconv.Atoi(f.Name[1:])
			fp := calleePkg.Info.Footprints[f.Decl]
			wantOpaque := own[i].opaque || runsP[i]
			var wantParams []int
			if runsP[i] {
				wantParams = []int{1}
			}
			if fp.Opaque != wantOpaque || !slices.Equal(fp.Params, wantParams) || fp.OpaqueRest != own[i].opaque {
				t.Fatalf("seed %d: H%d footprint opaque=%v rest=%v params=%v, want opaque=%v rest=%v params=%v\nreplay: %s\n%s\n%s", seed, i, fp.Opaque, fp.OpaqueRest, fp.Params, wantOpaque, own[i].opaque, wantParams, replay, libSrc, mainSrc)
			}
			if runsP[i] && len(c.callees[i].forwards) > 0 && c.callees[i].pcalls == 0 {
				reached["runs p only through a forward"] = true
			}
		}
		for _, f := range pkg.Funcs {
			if f.Name != "subject" {
				continue
			}
			got := fpShow(pkg.Info.Footprints[f.Decl])
			if got != want.String() {
				t.Fatalf("seed %d: subject footprint %s, want %s\nreplay: %s\n%s\n%s", seed, got, want, replay, libSrc, mainSrc)
			}
			if want.opaque {
				reached["subject opaque"] = true
			} else {
				reached["subject known"] = true
			}
		}
		if c.imported {
			reached["imported"] = true
		}
	}
	if os.Getenv("ERGO_FP_SEED") == "" {
		for _, k := range []string{"subject opaque", "subject known", "runs p only through a forward", "imported"} {
			if !reached[k] {
				t.Errorf("no seed reached %q", k)
			}
		}
	}
}
