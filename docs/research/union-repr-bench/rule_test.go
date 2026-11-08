package unionreprbench

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The rule G contract: every form that RuleG picks, for fixed and seeded
// union shapes, is real Go that builds and vets; each C and D struct has
// the size RuleG computed and is one that Go keeps in registers; no A
// union holds a C or D union by value; and the form=G benchmarks use the
// forms RuleG picks.

// ---- Shape helpers ----

var (
	tI = Type{Kind: KInt}
	tS = Type{Kind: KString}
	tB = Type{Kind: KBool}
)

func tU(name string) Type       { return Type{Kind: KUnion, Name: name} }
func tR(name string) Type       { return Type{Kind: KRecord, Name: name} }
func tSl(e Type) Type           { return Type{Kind: KSlice, Elem: &e} }
func fd(n string, t Type) Field { return Field{Name: n, Type: t} }

func vr(name string, fs ...Field) Variant  { return Variant{Name: name, Fields: fs} }
func evr(name string, fs ...Field) Variant { return Variant{Name: name, Fields: fs, Error: true} }
func un(name string, vs ...Variant) Union  { return Union{Name: name, Variants: vs} }

// decision is the prelude's Decision[S, O] instance.
func decision(name string, s, o Type) Union {
	return un(name, vr("Enter", fd("Next", s)), vr("Stay", fd("Next", s)), vr("Ignore"), vr("Reject"), vr("Done", fd("Output", o)))
}

type want struct {
	form Form
	why  Why
}

type fixture struct {
	name string
	p    Program
	want map[string]want
}

func breakerProgram() Program {
	return Program{Unions: []Union{
		un("State", vr("Closed", fd("Failures", tI)), vr("Open", fd("Endpoint", tS))),
		un("Signal", vr("Failure", fd("Endpoint", tS)), vr("Retry")),
		decision("Decision", tU("State"), tS),
	}}
}

func counterProgram() Program {
	return Program{Unions: []Union{
		un("State", vr("Idle"), vr("Counting", fd("N", tI))),
		un("Signal", vr("Tick"), vr("Reset"), vr("Read")),
		decision("Decision", tU("State"), tI),
	}}
}

func tidewalkProgram() Program {
	return Program{
		Records: []Record{
			{Name: "Pos", Fields: []Field{fd("X", tI), fd("Y", tI)}},
			{Name: "Walk", Fields: []Field{fd("At", tR("Pos")), fd("Tide", tI), fd("Turn", tI), fd("Shells", tI), fd("Left", tS)}},
		},
		Unions: []Union{
			un("Move", vr("North"), vr("East"), vr("South"), vr("West"), vr("Wait")),
			un("Event", vr("Walked", fd("To", tR("Pos"))), vr("Waited"), vr("ShellCollected", fd("At", tR("Pos"))), vr("TideRose", fd("Tide", tI)), vr("WalkerDrowned"), vr("PeakReached")),
			un("Game", vr("Playing", fd("W", tR("Walk"))), vr("Won", fd("W", tR("Walk"))), vr("Drowned", fd("W", tR("Walk")))),
			decision("Decision", tU("Game"), tS),
		},
	}
}

func fixtures() []fixture {
	return []fixture{
		{"list", Program{Unions: []Union{
			un("List", vr("Cons", fd("Head", tI), fd("Tail", tU("List"))), vr("Nil")),
		}}, map[string]want{"List": {FormA, WhyRecursive}}},
		{"mutual", Program{Unions: []Union{
			un("Even", vr("ESucc", fd("Next", tU("Odd"))), vr("EZero")),
			un("Odd", vr("OSucc", fd("Next", tU("Even"))), vr("OZero")),
		}}, map[string]want{"Even": {FormA, WhyRecursive}, "Odd": {FormA, WhyRecursive}}},
		{"record", Program{
			Records: []Record{{Name: "Pair", Fields: []Field{fd("L", tU("Tree")), fd("R", tU("Tree"))}}},
			Unions:  []Union{un("Tree", vr("Node", fd("Kids", tR("Pair"))), vr("Leaf"))},
		}, map[string]want{"Tree": {FormA, WhyRecursive}}},
		// Box[T] = Full{ V T } | Empty{}, instantiated at Rec.
		{"generic", Program{Unions: []Union{
			un("Rec", vr("Link", fd("Next", tU("Box_Rec"))), vr("End")),
			un("Box_Rec", vr("Full", fd("V", tU("Rec"))), vr("Empty")),
			un("Box_int", vr("Full", fd("V", tI)), vr("Empty")),
		}}, map[string]want{"Rec": {FormA, WhyRecursive}, "Box_Rec": {FormA, WhyRecursive}, "Box_int": {FormC, WhyOption}}},
		{"slice", Program{Unions: []Union{
			un("Rose", vr("Branch", fd("Kids", tSl(tU("Rose")))), vr("Bud")),
		}}, map[string]want{"Rose": {FormA, WhyRecursive}}},
		{"errors", Program{Unions: []Union{
			un("Lookup", evr("NotFound"), evr("Bad", fd("Msg", tS))),
			un("Net", evr("Timeout"), evr("Refused")),
			un("Io", evr("E1", fd("Code", tI)), evr("E2", fd("Msg", tS)), evr("E3")),
		}}, map[string]want{"Lookup": {FormA, WhyError}, "Net": {FormA, WhyError}, "Io": {FormA, WhyError}}},
		{"breaker", breakerProgram(), map[string]want{
			"State": {FormA, WhyHeld}, "Signal": {FormC, WhyOption}, "Decision": {FormA, WhySize},
		}},
		{"counter", counterProgram(), map[string]want{
			"State": {FormC, WhyOption}, "Signal": {FormB, WhyEnum}, "Decision": {FormD, WhyFlat},
		}},
		{"tidewalk", tidewalkProgram(), map[string]want{
			"Move": {FormB, WhyEnum}, "Event": {FormD, WhyFlat}, "Game": {FormA, WhySize}, "Decision": {FormA, WhySize},
		}},
		{"options", Program{
			Records: []Record{{Name: "Halves", Fields: []Field{fd("Before", tS), fd("After", tS)}}},
			Unions: []Union{
				un("Option_int", vr("Some", fd("Value", tI)), vr("None")),
				un("Option_string", vr("Some", fd("Value", tS)), vr("None")),
				un("Option_Halves", vr("Some", fd("Value", tR("Halves"))), vr("None")),
			},
		}, map[string]want{"Option_int": {FormC, WhyOption}, "Option_string": {FormC, WhyOption}, "Option_Halves": {FormA, WhySize}}},
		{"nested", Program{Unions: []Union{
			// D in D: the inner union fits inside the outer one.
			un("Inner", vr("I1", fd("On", tB)), vr("I2"), vr("I3")),
			un("Outer", vr("O1", fd("In", tU("Inner"))), vr("O2", fd("N", tI)), vr("O3")),
			// B in A: an enum stays B in a field of an A union.
			un("Dir", vr("Up"), vr("Down")),
			un("Big", vr("G1", fd("D", tU("Dir")), fd("Note", tS), fd("Extra", tS)), vr("G2", fd("Id", tI))),
			// A cascade: Small fits, but Option_Small (40 bytes) is A and
			// holds it, so Small is A too.
			un("Small", vr("S1", fd("A", tI)), vr("S2", fd("B", tS))),
			un("Option_Small", vr("Some", fd("Value", tU("Small"))), vr("None")),
		}}, map[string]want{
			"Inner": {FormD, WhyFlat}, "Outer": {FormD, WhyFlat},
			"Dir": {FormB, WhyEnum}, "Big": {FormA, WhySize},
			"Small": {FormA, WhyHeld}, "Option_Small": {FormA, WhySize},
		}},
		{"through-record", Program{
			Records: []Record{{Name: "Box", Fields: []Field{fd("In", tU("Small"))}}},
			Unions: []Union{
				un("Small", vr("T1", fd("A", tI)), vr("T2", fd("B", tB))),
				un("Wide", vr("W1", fd("B", tR("Box")), fd("S", tS)), vr("W2", fd("N", tI)), vr("W3")),
				// A slice element is not held by value.
				un("Tag", vr("T1", fd("A", tI)), vr("T2", fd("B", tB))),
				un("Tags", vr("Many", fd("Xs", tSl(tU("Tag"))), fd("S", tS), fd("Z", tS)), vr("None", fd("N", tI))),
			},
		}, map[string]want{"Small": {FormA, WhyHeld}, "Wide": {FormA, WhySize}, "Tag": {FormD, WhyFlat}, "Tags": {FormA, WhySize}}},
		{"unit", Program{
			Records: []Record{{Name: "Unit"}},
			Unions: []Union{
				// Slots int64 and Unit: a struct whose last field has
				// size 0 gets one byte of padding (24 bytes, not 16).
				un("U", vr("A1", fd("N", tI)), vr("A2", fd("X", tR("Unit"))), vr("A3")),
			},
		}, map[string]want{"U": {FormD, WhyFlat}}},
		{"fields5", Program{Unions: []Union{
			// 32 bytes but 5 fields: Go does not keep it in registers.
			un("Y", vr("Y1", fd("P", tB), fd("Q", tB)), vr("Y2", fd("N", tI)), vr("Y3", fd("S", tS))),
			un("Y4", vr("Y1", fd("P", tB), fd("Q", tB)), vr("Y2", fd("S", tS))),
		}}, map[string]want{"Y": {FormA, WhySize}, "Y4": {FormD, WhyFlat}}},
	}
}

// randomPrograms are seeded programs of 3 to 6 unions and 0 to 2 records,
// with references between them (cycles included).
func randomPrograms(n int, seed uint64) []Program {
	r := newRand(seed)
	pick := func(k int) int { return int(r.next() % uint64(k)) }
	var out []Program
	for pi := 0; pi < n; pi++ {
		nu, nr := 3+pick(4), pick(3)
		var p Program
		var fieldType func(rec int, depth int) Type
		fieldType = func(rec int, depth int) Type {
			switch x := pick(100); {
			case x < 20:
				return tI
			case x < 35:
				return tS
			case x < 45:
				return tB
			case x < 75:
				return tU(fmt.Sprintf("U%d", pick(nu)))
			case x < 88 && rec > 0:
				return tR(fmt.Sprintf("R%d", pick(rec)))
			case depth < 2:
				return tSl(fieldType(rec, depth+1))
			}
			return tI
		}
		for ri := 0; ri < nr; ri++ {
			var fs []Field
			nf := pick(4) // 0 fields: a zero-size record
			for fi := 0; fi < nf; fi++ {
				fs = append(fs, fd(fmt.Sprintf("F%d", fi), fieldType(ri, 0)))
			}
			p.Records = append(p.Records, Record{Name: fmt.Sprintf("R%d", ri), Fields: fs})
		}
		for ui := 0; ui < nu; ui++ {
			u := Union{Name: fmt.Sprintf("U%d", ui)}
			isErr := pick(10) == 0
			for vi := 0; vi <= pick(5); vi++ {
				v := Variant{Name: fmt.Sprintf("V%d", vi), Error: isErr}
				nf := pick(4)
				if pick(3) == 0 {
					nf = 0
				}
				for fi := 0; fi < nf; fi++ {
					v.Fields = append(v.Fields, fd(fmt.Sprintf("F%d", fi), fieldType(nr, 0)))
				}
				u.Variants = append(u.Variants, v)
			}
			p.Unions = append(p.Unions, u)
		}
		out = append(out, p)
	}
	return out
}

// ---- Go emission: each union in its chosen form ----

type emitter struct {
	b   strings.Builder
	pre string
	p   Program
	c   map[string]Choice
}

func (e *emitter) ty(t Type) string {
	switch t.Kind {
	case KInt:
		return "int64"
	case KString:
		return "string"
	case KBool:
		return "bool"
	case KSlice:
		return "[]" + e.ty(*t.Elem)
	case KRecord:
		return e.pre + "R_" + t.Name
	case KUnion:
		return e.uName(t.Name)
	}
	panic("kind")
}

func (e *emitter) uName(u string) string       { return e.pre + "U_" + u }
func (e *emitter) vName(u, v string) string    { return e.pre + "V_" + u + "_" + v }
func (e *emitter) fn(kind, u, v string) string { return e.pre + kind + "_" + u + "_" + v }

func (e *emitter) w(format string, a ...any) { fmt.Fprintf(&e.b, format, a...) }

func (e *emitter) emit() {
	for _, r := range e.p.Records {
		e.w("type %sR_%s struct {\n", e.pre, r.Name)
		for _, f := range r.Fields {
			e.w("\t%s %s\n", f.Name, e.ty(f.Type))
		}
		e.w("}\n\n")
	}
	for _, u := range e.p.Unions {
		e.union(u)
	}
	e.w("func %smain() {\n", e.pre)
	for _, u := range e.p.Unions {
		for _, v := range u.Variants {
			e.w("\tprintln(%s(%s(%s{})))\n", e.fn("desc", u.Name, ""), e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name))
			if v.Error {
				e.w("\t{\n\t\tvar err error = %s{}\n\t\t_, ok := %s(err)\n\t\tprintln(ok)\n\t}\n", e.vName(u.Name, v.Name), e.fn("catch", u.Name, ""))
			}
		}
	}
	e.w("}\n\n")
}

func (e *emitter) union(u Union) {
	c := e.c[u.Name]
	U := e.uName(u.Name)
	for _, v := range u.Variants {
		e.w("type %s struct {\n", e.vName(u.Name, v.Name))
		for _, f := range v.Fields {
			e.w("\t%s %s\n", f.Name, e.ty(f.Type))
		}
		e.w("}\n\n")
		if v.Error {
			e.w("func (%s) Error() string { return %q }\n\n", e.vName(u.Name, v.Name), u.Name+"."+v.Name)
		}
	}
	reads := func(x string, fs []Field, slot func(i int) string) {
		for i := range fs {
			e.w("\t\t_ = %s.%s\n", x, slot(i))
		}
	}
	switch c.Form {
	case FormA:
		e.w("type %s interface{ is%s() }\n\n", U, U)
		for _, v := range u.Variants {
			e.w("func (%s) is%s() {}\n", e.vName(u.Name, v.Name), U)
		}
		for _, v := range u.Variants {
			e.w("func %s(x %s) %s { return x }\n", e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name), U)
		}
		e.w("func %s(x %s) int {\n\tswitch v := x.(type) {\n", e.fn("desc", u.Name, ""), U)
		for k, v := range u.Variants {
			e.w("\tcase %s:\n\t\t_ = v\n", e.vName(u.Name, v.Name))
			reads("v", v.Fields, func(i int) string { return v.Fields[i].Name })
			e.w("\t\treturn %d\n", k+1)
		}
		e.w("\t}\n\treturn -1\n}\n\n")
	case FormB:
		e.w("type %s uint8\n\n", U)
		for k, v := range u.Variants {
			e.w("const %s %s = %d\n", e.fn("K", u.Name, v.Name), U, k+1)
			e.w("func %s(x %s) %s { _ = x; return %s }\n", e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name), U, e.fn("K", u.Name, v.Name))
		}
		e.w("func %s(x %s) int {\n\tswitch x {\n", e.fn("desc", u.Name, ""), U)
		for k, v := range u.Variants {
			e.w("\tcase %s:\n\t\treturn %d\n", e.fn("K", u.Name, v.Name), k+1)
		}
		e.w("\t}\n\treturn -1\n}\n\n")
	case FormC:
		pv := payload(u)
		e.w("type %s struct {\n\tv  %s\n\tok bool\n}\n\n", U, e.vName(u.Name, pv.Name))
		pk, ek := 0, 0
		for k, v := range u.Variants {
			if len(v.Fields) > 0 {
				pk = k + 1
				e.w("func %s(x %s) %s { return %s{v: x, ok: true} }\n", e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name), U, U)
			} else {
				ek = k + 1
				e.w("func %s(x %s) %s { _ = x; return %s{} }\n", e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name), U, U)
			}
		}
		e.w("func %s(x %s) int {\n\tif x.ok {\n", e.fn("desc", u.Name, ""), U)
		reads("x.v", pv.Fields, func(i int) string { return pv.Fields[i].Name })
		e.w("\t\treturn %d\n\t}\n\treturn %d\n}\n\n", pk, ek)
	case FormD:
		e.w("type %s struct {\n\ttag uint8\n", U)
		for i, t := range c.Slots {
			e.w("\ts%d %s\n", i, e.ty(t))
		}
		e.w("}\n\n")
		for k, v := range u.Variants {
			var inits []string
			inits = append(inits, fmt.Sprintf("tag: %d", k+1))
			for i, f := range v.Fields {
				inits = append(inits, fmt.Sprintf("s%d: x.%s", c.FieldSlot[k][i], f.Name))
			}
			e.w("func %s(x %s) %s { _ = x; return %s{%s} }\n", e.fn("mk", u.Name, v.Name), e.vName(u.Name, v.Name), U, U, strings.Join(inits, ", "))
		}
		e.w("func %s(x %s) int {\n\tswitch x.tag {\n", e.fn("desc", u.Name, ""), U)
		for k, v := range u.Variants {
			e.w("\tcase %d:\n", k+1)
			reads("x", v.Fields, func(i int) string { return fmt.Sprintf("s%d", c.FieldSlot[k][i]) })
			e.w("\t\treturn %d\n", k+1)
		}
		e.w("\t}\n\treturn -1\n}\n\n")
	}
	if hasError(u) {
		// ConvRow and catch: a Go error back into the union by a type test.
		e.w("func %s(err error) (%s, bool) {\n\tu, ok := err.(%s)\n\treturn u, ok\n}\n\n", e.fn("catch", u.Name, ""), U, U)
	}
}

type emitted struct {
	src     string
	prefix  []string
	progs   []Program
	choices []map[string]Choice
}

func emitAll(progs []Program, rule func(Program) map[string]Choice) emitted {
	var out emitted
	var b strings.Builder
	b.WriteString("// Code generated by TestRuleGBuilds. Each union in the form rule G picks.\npackage main\n\n")
	var mains []string
	for i, p := range progs {
		pre := fmt.Sprintf("P%d", i)
		c := rule(p)
		e := &emitter{pre: pre, p: p, c: c}
		e.emit()
		b.WriteString(e.b.String())
		mains = append(mains, pre+"main()")
		out.prefix = append(out.prefix, pre)
		out.progs = append(out.progs, p)
		out.choices = append(out.choices, c)
	}
	b.WriteString("func main() {\n\t" + strings.Join(mains, "\n\t") + "\n}\n")
	out.src = b.String()
	return out
}

// goBuild writes src as a main package and runs go vet and go build on it.
func goBuild(t *testing.T, src string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module rulecheck\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	for _, args := range [][]string{{"vet", "."}, {"build", "-o", os.DevNull, "."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
		outp, err := cmd.CombinedOutput()
		log.WriteString("$ go " + strings.Join(args, " ") + "\n" + string(outp))
		if err != nil {
			return log.String(), fmt.Errorf("go %s: %v", args[0], err)
		}
	}
	return log.String(), nil
}

// typeCheck type-checks src with go/types and returns the package.
func typeCheck(t *testing.T, src string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{Sizes: types.SizesFor("gc", "amd64")}
	pkg, err := conf.Check("main", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type check: %v", err)
	}
	return pkg
}

// canSSA is cmd/compile/internal/ssa CanSSA on a go/types type (amd64).
func canSSA(sizes types.Sizes, t types.Type) bool {
	if sizes.Sizeof(t) > ssaBytes {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Array:
		return u.Len() <= 1 && canSSA(sizes, u.Elem())
	case *types.Struct:
		if u.NumFields() > ssaFields {
			return false
		}
		for i := 0; i < u.NumFields(); i++ {
			if !canSSA(sizes, u.Field(i).Type()) {
				return false
			}
		}
	}
	return true
}

// checkForms checks, on the type-checked Go, each union's form, the size
// RuleG computed, CanSSA, and that no A union's variant holds a C or D
// union by value.
func checkForms(t *testing.T, pkg *types.Package, em emitted) {
	t.Helper()
	sizes := types.SizesFor("gc", "amd64")
	for i, p := range em.progs {
		pre := em.prefix[i]
		formOf := map[types.Type]Form{}
		for _, u := range p.Unions {
			formOf[pkg.Scope().Lookup(pre+"U_"+u.Name).Type()] = em.choices[i][u.Name].Form
		}
		for _, u := range p.Unions {
			c := em.choices[i][u.Name]
			typ := pkg.Scope().Lookup(pre + "U_" + u.Name).Type()
			switch c.Form {
			case FormA:
				if !types.IsInterface(typ) {
					t.Errorf("%s%s: form A is not an interface", pre, u.Name)
				}
				var walk func(types.Type) bool
				walk = func(x types.Type) bool {
					if f, ok := formOf[x]; ok && (f == FormC || f == FormD) {
						return true
					}
					if s, ok := x.Underlying().(*types.Struct); ok && !types.IsInterface(x) {
						for j := 0; j < s.NumFields(); j++ {
							if walk(s.Field(j).Type()) {
								return true
							}
						}
					}
					return false
				}
				for _, v := range u.Variants {
					if walk(pkg.Scope().Lookup(pre + "V_" + u.Name + "_" + v.Name).Type()) {
						t.Errorf("%s%s: A variant %s holds a C or D union by value", pre, u.Name, v.Name)
					}
				}
			case FormB:
				if b, ok := typ.Underlying().(*types.Basic); !ok || b.Kind() != types.Uint8 {
					t.Errorf("%s%s: form B is not a uint8", pre, u.Name)
				}
			case FormC, FormD:
				if got := sizes.Sizeof(typ); got != c.Size {
					t.Errorf("%s%s: form %s is %d bytes in Go, RuleG computed %d", pre, u.Name, c.Form, got, c.Size)
				}
				if !canSSA(sizes, typ) {
					t.Errorf("%s%s: form %s (%d bytes) is above Go's SSA limit", pre, u.Name, c.Form, sizes.Sizeof(typ))
				}
			}
		}
	}
}

func allPrograms() ([]Program, []fixture) {
	fx := fixtures()
	var progs []Program
	for _, f := range fx {
		progs = append(progs, f.p)
	}
	return append(progs, randomPrograms(80, 20261109)...), fx
}

func TestRuleGFixtures(t *testing.T) {
	for _, f := range fixtures() {
		c := RuleG(f.p)
		for name, w := range f.want {
			if got := c[name]; got.Form != w.form || got.Why != w.why {
				t.Errorf("%s: %s is %s (%s), want %s (%s)", f.name, name, got.Form, got.Why, w.form, w.why)
			}
		}
	}
}

// TestRuleGBuilds is the M1 guard of round 2: every outcome of the rule
// builds and vets as Go, with the sizes the rule computed.
func TestRuleGBuilds(t *testing.T) {
	progs, _ := allPrograms()
	em := emitAll(progs, RuleG)
	whys := map[Why]int{}
	for _, c := range em.choices {
		for _, ch := range c {
			whys[ch.Why]++
		}
	}
	for _, w := range []Why{WhyError, WhyRecursive, WhyEnum, WhyOption, WhyFlat, WhySize, WhyHeld} {
		if whys[w] == 0 {
			t.Errorf("no union takes the step %q; the shapes do not reach every outcome", w)
		}
	}
	t.Logf("outcomes over %d programs: %v", len(progs), whys)
	checkForms(t, typeCheck(t, em.src), em)
	if log, err := goBuild(t, em.src); err != nil {
		t.Fatalf("%v\n%s", err, firstLines(log, 30))
	}
}

// TestRound1RuleDoesNotBuild keeps the round-1 rule (recursion and error
// checks only before D, no bound on C, no hold step) to show that the
// build check catches it: a recursive one-payload union is an invalid
// recursive Go type, and an error union in C or B has no Error method for
// `err.(U)` (round-1 findings M1).
func TestRound1RuleDoesNotBuild(t *testing.T) {
	round1 := func(p Program) map[string]Choice { return ruleWith(p, ruleOptions{checksLast: true}) }
	fails := map[string]string{
		"list":    "invalid recursive type",
		"mutual":  "invalid recursive type",
		"record":  "invalid recursive type",
		"generic": "invalid recursive type",
		"errors":  "impossible type assertion",
	}
	for _, f := range fixtures() {
		msg, ok := fails[f.name]
		if !ok {
			continue
		}
		log, err := goBuild(t, emitAll([]Program{f.p}, round1).src)
		if err == nil || !strings.Contains(log, msg) {
			t.Errorf("%s: the round-1 rule's Go should fail with %q; got err %v:\n%s", f.name, msg, err, firstLines(log, 20))
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, "\n")
}

// ---- The M2 guard: form=G benchmarks use the forms RuleG picks ----

func formOfGo(t reflect.Type) Form {
	switch {
	case t.Kind() == reflect.Interface:
		return FormA
	case t.Kind() == reflect.Uint8:
		return FormB
	case t.Kind() == reflect.Struct && t.NumField() == 2 && t.Field(0).Name == "v" && t.Field(1).Name == "ok":
		return FormC
	case t.Kind() == reflect.Struct && t.NumField() > 0 && t.Field(0).Name == "tag":
		return FormD
	}
	return 0
}

func TestRuleGPicksBenchForms(t *testing.T) {
	fn := func(f any) reflect.Type { return reflect.TypeOf(f) }
	cases := []struct {
		name string
		p    Program
		used map[string]reflect.Type // union → the Go type its form=G benchmark uses
	}{
		{"Breaker (BenchmarkBreaker/form=G)", breakerProgram(), map[string]reflect.Type{
			"State": fn(stepG).In(0), "Signal": fn(stepG).In(1), "Decision": fn(stepG).Out(0),
		}},
		{"Counter (BenchmarkCounter/form=G)", counterProgram(), map[string]reflect.Type{
			"State": fn(stepCG).In(0), "Signal": fn(stepCG).In(1), "Decision": fn(stepCG).Out(0),
		}},
		{"Tidewalk (BenchmarkTidewalk/form=G)", tidewalkProgram(), map[string]reflect.Type{
			"Game": fn(tideStepG).In(0), "Move": fn(tideStepG).In(1), "Decision": fn(tideStepG).Out(0),
			"Event": fn(decideG).Out(0).Elem(),
		}},
	}
	for _, c := range cases {
		choice := RuleG(c.p)
		for u, typ := range c.used {
			want := choice[u]
			if got := formOfGo(typ); got != want.Form {
				t.Errorf("%s: %s uses %s (form %s), RuleG picks %s (%s)", c.name, u, typ, got, want.Form, want.Why)
				continue
			}
			if (want.Form == FormC || want.Form == FormD) && int64(typ.Size()) != want.Size {
				t.Errorf("%s: %s is %d bytes, RuleG computed %d", c.name, u, typ.Size(), want.Size)
			}
		}
	}
}
