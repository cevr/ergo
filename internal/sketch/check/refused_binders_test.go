package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Refused binders (rows.md, Totality 6; diagnostics.md, D3): a refused type
// test declares a silent placeholder, and a binder D-D3 refuses still binds
// with the value's type. A refused binder changes no other verdict: a read
// of the name in a value position adds nothing (P1); everywhere else the
// name means what it would mean without the binder (P2); typing and the
// reference walk agree (P3); a refused type adds no diagnostic where it is
// used (P4); a package-level let binder named after a prelude name is
// D-D3's, one S108 at the binder (P5).

// rbCodes is each diagnostic of src as CODE@line:col, sorted.
func rbCodes(src string) []string {
	_, ds := check.CheckSource(src)
	var out []string
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s@%d:%d", d.Code, d.Line, d.Column))
	}
	sort.Strings(out)
	return out
}

// TestRefusedBinderCases pins the reported programs (fuzz-17d6, fuzz-863c)
// and the scoping risks: each program's diagnostics are exactly want, and
// the reference walk agrees with typing.
func TestRefusedBinderCases(t *testing.T) {
	// reads names, per case, a refused binder whose last occurrence in the
	// program is a value read.
	reads := map[string]string{"catch-tbad": "tbad"}
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		// A refused let test (the placeholder) is not seen by a later
		// pattern: `case tn:` is the type test it is without the let.
		{"sequential", "type tn {}\n\nfunc host(v int) {\n\tlet (tn) = v\n\tmatch v {\n\tcase tn:\n\t\tlet _ = 0\n\t}\n}\n", []string{"S108@4:7", "S108@6:7"}},
		// A refused type test in a catch arm declares the placeholder, so
		// the arm's read adds nothing (rows.md, Catch 7).
		{"catch-int", "type E {}\n\nfunc n(v int) int fails E { v }\n\nfunc host(v int) int {\n\tn(v) catch {\n\tcase int:\n\t\tint\n\t}\n}\n", []string{"S103@6:7", "S108@7:7"}},
		{"catch-Console", "type E {}\n\nfunc n(v int) int fails E { v }\n\nfunc host(v int) int {\n\tn(v) catch {\n\tcase Console:\n\t\tlet _ = Console\n\t\t0\n\t}\n}\n", []string{"S103@6:7", "S108@7:7"}},
		// A refused type: the program's one diagnostic is its declaration's.
		{"tbad-match", "type tbad = Nope\n\nfunc host(v int) {\n\tmatch v {\n\tcase tbad:\n\t\tlet _ = 0\n\tcase _:\n\t\tlet _ = 1\n\t}\n}\n", []string{"S104@1:13"}},
		{"tbad-let", "type tbad = Nope\n\nfunc host(v int) int {\n\tlet tbad = v\n\tlet _ = tbad\n\t0\n}\n", []string{"S104@1:13"}},
		{"tbad-literal", "type tbad = Nope\n\nfunc t() int { 1 }\n\nfunc host() int {\n\tlet _ = tbad[0]{{t}}\n\t0\n}\n", []string{"S104@1:13"}},
		// A catch arm whose test type does not resolve declares the
		// placeholder, so the arm's read of tbad uses it (P1, P3).
		{"catch-tbad", "type tbad = Nope\n\ntype E {}\n\nfunc n(v int) int fails E { v }\n\nfunc host(v int) int {\n\tn(v) catch {\n\tcase tbad:\n\t\tlet _ = tbad\n\t\t0\n\tcase _:\n\t\t1\n\t}\n}\n", []string{"S104@1:13"}},
		// A refused type a cycle reaches before its refusal adds nothing
		// as a machine's state, in either declaration order (FuzzCheck
		// seed b7fef5a76c229eda).
		{"tbad-machine-cycle", "func A() {}\ntype e = d\ntype r machine[e,A]{initial: 0,\nstep: 0}\ntype d {A(r)}\n", []string{"S108@3:16", "S108@3:18", "S108@4:7"}},
		{"tbad-machine-cycle-permuted", "type d {A(r)}\ntype r machine[e,A]{initial: 0,\nstep: 0}\ntype e = d\nfunc A() {}\n", []string{"S108@2:16", "S108@2:18", "S108@3:7"}},
		{"defined-machine-cycle", "func A() {}\ntype e d\ntype r machine[e,A]{initial: 0,\nstep: 0}\ntype d {A(r)}\n", []string{"S108@2:8", "S108@3:18", "S108@4:7"}},
		// An imported package as a catch rest: one S108, the import used.
		{"catch-import", "import trace\n\ntype E {}\n\nfunc n(v int) int fails E { v }\n\nfunc host(v int) int {\n\tn(v) catch {\n\tcase trace:\n\t\t0\n\t}\n}\n", []string{"S108@9:7"}},
		// A placeholder does not leak into the next arm, nor out of a for
		// body: there tr is the type again, and a read of it is S108.
		{"sibling-arm", "type tr { A int }\n\nfunc host(v int) int {\n\tmatch v {\n\tcase tr:\n\t\tlet _ = tr\n\tcase _:\n\t\tlet _ = tr\n\t}\n\t0\n}\n", []string{"S108@5:7", "S108@8:11"}},
		{"for-body", "type tr { A int }\n\nfunc host(xs []int) int {\n\tfor x in xs {\n\t\tlet tr = x\n\t\tlet _ = tr\n\t}\n\tlet _ = tr\n\t0\n}\n", []string{"S108@5:7", "S108@8:10"}},
		// Catch 7's same-names rule still fires beside a refused test.
		{"catch-several", "type E {}\n\ntype Bang {}\n\nfunc n(v int) int fails E | Bang { v }\n\nfunc host(v int) int {\n\tn(v) catch {\n\tcase int, Bang as b:\n\t\tlet _ = int\n\t\t0\n\tcase _:\n\t\t1\n\t}\n}\n", []string{"S108@9:20", "S108@9:7"}},
		// A D-D3 binder keeps D-D3 in a value position, and a type position
		// sees through it (a local, a parameter, a package let).
		{"dd3-read", "func host() int {\n\tlet (len) = 5\n\tlen + 1\n}\n", []string{"S108@2:7"}},
		{"dd3-local-type", "func host(v int) int {\n\tlet (int) = v\n\tlet fl = func(x int) int { x }\n\tfl(int)\n}\n", []string{"S108@2:7"}},
		{"dd3-param-type", "func host(int int) int {\n\tlet fl = func(x int) int { x }\n\tfl(int)\n}\n", []string{"S108@1:11"}},
		{"dd3-package-type", "let int = 1\n\nfunc host(x int) int {\n\tx + int\n}\n", []string{"S108@1:5"}},
		// A placeholder is not a binding a later binder shadows.
		{"placeholder-no-shadow", "type tr { A int }\n\nfunc host(v int, b bool) int {\n\tlet tr = v\n\tif b {\n\t\tlet fl = func(tr int) int { tr }\n\t\tlet _ = fl(1)\n\t}\n\t0\n}\n", []string{"S108@4:6"}},
		// A refused binder in a block is not a declaration a later binder
		// of the same block repeats: `let f` gets no "already declared".
		{"same-block-after-refused", "func f() {}\n\nfunc host(v int) int {\n\tmatch v {\n\tcase f:\n\t\tlet f = 2\n\t\tf\n\t}\n}\n", []string{"S108@5:7"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := rbCodes(c.src); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("want %v, got %v\n%s", c.want, got, c.src)
			}
			if p := check.RefWalkProblems(c.src); len(p) > 0 {
				t.Errorf("reference walk problems %v\n%s", p, c.src)
			}
			if read, ok := reads[c.name]; ok {
				// The last occurrence of read is a value read of a refused
				// binder: typing and the walk agree that it uses a local.
				info, walk, found := check.BinderMeaning(c.src, strings.LastIndex(c.src, read))
				if !found || info != "uses a local" || walk != "value" {
					t.Errorf("the read of %s: want Info %q walk %q, got %q %q (found %v)\n%s", read, "uses a local", "value", info, walk, found, c.src)
				}
			}
		})
	}
}

// TestRefusedBinderMeaning: after a refused binder, a later pattern or
// binder of the name classifies it as it would without the binder (P2), in
// typing and in the reference walk alike (P3). The checked occurrence is
// the name's last; want is its (Info, walk) meaning and the diagnostics of
// its line.
func TestRefusedBinderMeaning(t *testing.T) {
	const decls = "type tr { A int }\n\n"
	for _, c := range []struct {
		name, body, use string
		info, walk      string
		codes           []string
	}{
		// A placeholder's name in a nested let is the type test again.
		{"placeholder/let", "\tlet tr = v\n\tif b {\n\t\tlet tr = v\n\t\tlet _ = 0\n\t}\n", "tr", "names", "names", []string{"S108"}},
		// A D-D3 binder's name in a later case is the type test again.
		{"dd3/case-int", "\tlet int = v\n\tmatch v {\n\tcase int:\n\t\tlet _ = 0\n\t}\n", "int", "names", "names", nil},
		{"dd3/case-Console", "\tlet (Console) = v\n\tmatch v {\n\tcase Console:\n\t\tlet _ = 0\n\tcase _:\n\t\tlet _ = 1\n\t}\n", "Console", "names", "names", []string{"S108"}},
	} {
		src := decls + "func host(v int, b bool) {\n" + c.body + "}\n"
		off := strings.LastIndex(src, c.use)
		line := strings.Count(src[:off], "\n") + 1
		info, walk, ok := check.BinderMeaning(src, off)
		if !ok || info != c.info || walk != c.walk {
			t.Errorf("%s: want Info %q walk %q, got %q %q (found %v)\n%s", c.name, c.info, c.walk, info, walk, ok, src)
		}
		_, ds := check.CheckSource(src)
		var codes []string
		for _, d := range ds {
			if d.Line == line {
				codes = append(codes, d.Code)
			}
		}
		if strings.Join(codes, " ") != strings.Join(c.codes, " ") {
			t.Errorf("%s: want %v on the use's line, got %v\n%s", c.name, c.codes, codes, src)
		}
	}
}

// TestRefusedBinderLetY is fuzz-863c's let-y: the literal's type y is the
// package type, as without the let (P2), so its diagnostics are those of
// the program without the let, plus the let's own; the walk reaches t (P3).
func TestRefusedBinderLetY(t *testing.T) {
	src := "type y{}\nfunc t(){let y = 1; y[0]{{t} }}\n"
	bare := "type y{}\nfunc t(){let _ = 1; y[0]{{t} }}\n"
	want := append([]string{"S108@2:14"}, rbCodes(bare)...)
	sort.Strings(want)
	if got := rbCodes(src); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("want %v, got %v", want, got)
	}
	if p := check.RefWalkProblems(src); len(p) > 0 {
		t.Errorf("reference walk problems %v", p)
	}
	off := strings.LastIndex(src, "y[")
	info, walk, _ := check.BinderMeaning(src, off)
	bi, bw, _ := check.BinderMeaning(bare, strings.LastIndex(bare, "y["))
	if info != bi || walk != bw {
		t.Errorf("the literal's type: want Info %q walk %q (as without the let), got %q %q", bi, bw, info, walk)
	}
}

// TestRefusedBinderPackageLet is P5: a package-level let binder named after
// a prelude name means what the same program means with the fresh name q,
// at the binder and at a read, apart from one S108 at the binder.
func TestRefusedBinderPackageLet(t *testing.T) {
	for _, form := range []string{"let (@) = 1", "let P{X: @} = P{X: 1}", "let @ = 1"} {
		for _, name := range []string{"Console", "Random", "int", "len"} {
			prog := "type P { X int }\n" + form + "\nfunc host() int {\n\t@ + 1\n}\n"
			src := strings.ReplaceAll(prog, "@", name)
			fresh := strings.ReplaceAll(prog, "@", "q")
			label := form + "/" + name
			binder := strings.Index(prog, "@")
			want := []string{fmt.Sprintf("S108@2:%d", binder-strings.Index(prog, "\n"))}
			if got := rbCodes(src); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("%s: want %v, got %v\n%s", label, want, got, src)
			}
			if got := rbCodes(fresh); len(got) > 0 {
				t.Fatalf("%s: the fresh program has %v", label, got)
			}
			read := strings.LastIndex(prog, "@")
			// The offsets of the binder and the read shift by the name's
			// length past the first @.
			shift := len(name) - len("@")
			for _, at := range []struct {
				what     string
				off, fre int
			}{{"binder", binder, binder}, {"read", read + shift, read + len("q") - len("@")}} {
				info, walk, ok := check.BinderMeaning(src, at.off)
				fi, fw, fok := check.BinderMeaning(fresh, at.fre)
				if !ok || !fok {
					t.Errorf("%s: no identifier at the %s", label, at.what)
					continue
				}
				if info != fi || walk != fw {
					t.Errorf("%s: at the %s want Info %q walk %q (as the fresh name), got %q %q", label, at.what, fi, fw, info, walk)
				}
			}
		}
	}
}
