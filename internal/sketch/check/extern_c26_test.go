package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// externDomainDecls declares the types the C26 enumeration writes in an
// extern's signature. Each is exported, so none is unused.
const externDomainDecls = `type Rec { X int }
type Length int
type A {}
type B {}
type U = A | B
type Op { M() }
type E {}
type R { N() }
`

// TestExternSignatureDomain is A5.1 of check-v1 stage E4 (C26): a user
// extern's signature takes and returns predeclared basic types only, with
// no type parameter and no rows. Oracle: clean iff every part is in the
// domain, and then a call is clean too; otherwise exactly one S109 naming
// #202 at the declaration.
func TestExternSignatureDomain(t *testing.T) {
	basic := []string{"bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128", "duration"}
	other := []string{"Length", "[]int", "Map[string, int]", "Rec", "U", "func(int) int", "Op", "Option[int]"}
	type part struct {
		ty     string
		in     bool
		result bool // only in the result position (a tuple)
		param  bool // only in the parameter position (a borrow, types.md Borrows 1)
	}
	var parts []part
	for _, ty := range basic {
		parts = append(parts, part{ty: ty, in: true})
	}
	for _, ty := range other {
		parts = append(parts, part{ty: ty})
	}
	parts = append(parts, part{ty: "(int, int)", result: true}, part{ty: "*Rec", param: true})
	line := strings.Count(externDomainDecls, "\n") + 1
	for _, p := range parts {
		for _, atResult := range []bool{false, true} {
			if p.result && !atResult || p.param && atResult {
				continue
			}
			for _, row := range []string{"", " fails E", " needs R"} {
				for _, generic := range []bool{false, true} {
					tparams, sig, use := "", "(p "+p.ty+")", "func Use(p "+p.ty+") { f(p) }\n"
					if generic {
						tparams = "[T]"
					}
					if atResult {
						sig, use = "() "+p.ty, "func Use() { _ = f() }\n"
						if p.result {
							use = "func Use() { let a, b = f(); _ = a; _ = b }\n"
						}
					}
					decl := fmt.Sprintf("extern go strings.F func f%s%s%s\n", tparams, sig, row)
					clean := p.in && row == "" && !generic
					src := externDomainDecls + decl
					if clean {
						src += use
					}
					name := strings.TrimSpace(decl)
					t.Run(name, func(t *testing.T) {
						_, diags := check.CheckSource(src)
						if clean {
							if len(diags) != 0 {
								t.Fatalf("want clean, got\n\t%s", describe(diags))
							}
							return
						}
						if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != line || !strings.Contains(diags[0].Message, "#202") {
							t.Fatalf("want one S109 naming #202 at line %d, got\n\t%s", line, describe(diags))
						}
					})
				}
			}
		}
	}
}

// TestExternRuntimeTargetFormerM4 holds the three programs lowering's M4
// refused (lower/regressions_test.go, the extern-* rows): a user extern of
// the runtime is now S108 in the checker, at the declaration, whatever its
// signature (check-v1 stage E4, A2).
func TestExternRuntimeTargetFormerM4(t *testing.T) {
	for name, src := range map[string]string{
		"extern-arity": `extern go ergo.local/prototype/runtime/rt.Sleep func nap(d duration)

func main() needs Console {
	nap(1ms)
	Console.Println("ok")
}
`,
		"extern-swapped": `extern go ergo.local/prototype/runtime/rt.Sleep func nap(d duration, s LiveScheduler)

func main() needs Console {
	let s LiveScheduler
	nap(1ms, s)
	Console.Println("ok")
}
`,
		"extern-same-signature": `extern go ergo.local/prototype/runtime/rt.Sleep func nap(s LiveScheduler, d duration)

func main() needs Console {
	let s LiveScheduler
	nap(s, 1ms)
	Console.Println("ok")
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, diags := check.CheckSource(src)
			if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 1 || !strings.Contains(diags[0].Message, "only std binds the Ergo runtime") {
				t.Fatalf("want one S108 for the runtime target at line 1, got\n\t%s", describe(diags))
			}
		})
	}
}

// TestExternTargetDomain is A5.2 of check-v1 stage E4 (C26): a user
// extern binds a function of Go's standard library (a path whose first
// element has no dot); a dotted path is #202's, and only std binds the
// Ergo runtime.
func TestExternTargetDomain(t *testing.T) {
	for _, tc := range []struct{ path, code, text string }{
		{"strings", "", ""},
		{"math/bits", "", ""},
		{"example.com/x", "S109", "#202"},
		{"ergo.local/prototype/runtime/rt", "S108", "only std binds the Ergo runtime"},
		{"ergo.local/prototype/runtime/v1", "S108", "only std binds the Ergo runtime"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			src := "extern go " + tc.path + ".F func f(n int) int\nfunc Use(n int) int { f(n) }\n"
			_, diags := check.CheckSource(src)
			if tc.code == "" {
				if len(diags) != 0 {
					t.Fatalf("want clean, got\n\t%s", describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != tc.code || diags[0].Line != 1 || !strings.Contains(diags[0].Message, tc.text) {
				t.Fatalf("want one %s (%q) at line 1, got\n\t%s", tc.code, tc.text, describe(diags))
			}
		})
	}
}
