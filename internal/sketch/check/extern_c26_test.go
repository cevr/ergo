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

func main() needs Console | Ambient {
	let s = NewLiveScheduler()
	nap(1ms, s)
	Console.Println("ok")
}
`,
		"extern-same-signature": `extern go ergo.local/prototype/runtime/rt.Sleep func nap(s LiveScheduler, d duration)

func main() needs Console | Ambient {
	let s = NewLiveScheduler()
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

// TestExternUnsafeTarget is the C26 target-domain property for Go's unsafe
// (check-v1 spec amendment 2026-11-04, after E4-2 r3 M4): a user extern of
// unsafe is exactly one S108 at the declaration, "Ergo has no unsafe;
// unsafe stays at the FFI edge", and no S109, for each exported name of
// unsafe and every admitted basic signature (a parameter, a result, both).
// The target is refused before the signature, so a signature outside the
// domain (a type parameter, a fails row, a slice) is the same S108. A call
// of the extern adds no diagnostic. The fixed cases are E4-2 r3 M4's
// inputs: unsafe.Sizeof and unsafe.Alignof as Size(x int64) uintptr, called
// in an interpolation.
func TestExternUnsafeTarget(t *testing.T) {
	const want = "Ergo has no unsafe; unsafe stays at the FFI edge"
	names := []string{"Sizeof", "Alignof", "Offsetof", "Add", "Slice", "String", "StringData", "SliceData", "Pointer"}
	basic := []string{"bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128", "duration"}
	type form struct{ sig, use string }
	var forms []form
	for _, ty := range basic {
		forms = append(forms,
			form{"(p " + ty + ")", "func Use(p " + ty + ") { f(p) }\n"},
			form{"() " + ty, "func Use() { _ = f() }\n"},
			form{"(p " + ty + ") " + ty, "func Use(p " + ty + ") " + ty + " { f(p) }\n"},
			form{"(p " + ty + ") uintptr", "func Use(p " + ty + ") uintptr { f(p) }\n"},
		)
	}
	forms = append(forms,
		form{"()", "func Use() { f() }\n"},
		form{"[T](p T) T", ""},
		form{"(p int) int fails E", ""},
		form{"(p []int) int", ""},
	)
	check1 := func(t *testing.T, src string) {
		t.Helper()
		_, diags := check.CheckSource(src)
		if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 1 || diags[0].Message != want {
			t.Fatalf("want exactly one S108 %q at line 1, got\n\t%s", want, describe(diags))
		}
	}
	for _, name := range names {
		for _, fm := range forms {
			decl := "extern go unsafe." + name + " func f" + fm.sig
			t.Run(decl, func(t *testing.T) {
				check1(t, decl+"\n"+fm.use+"type E {}\n")
			})
		}
	}
	for _, name := range []string{"Sizeof", "Alignof"} {
		src := "extern go unsafe." + name + " func Size(x int64) uintptr\nfunc main() needs Console {\n\tConsole.Println(\"\\(Size(1))\")\n}\n"
		t.Run("M4/"+name, func(t *testing.T) { check1(t, src) })
	}
}

// TestExternTargetDomain is A5.2 of check-v1 stage E4 (C26): a user
// extern binds a function of Go's standard library (a path whose first
// element has no dot); a dotted path is #202's, and only std binds the
// Ergo runtime.
func TestExternTargetDomain(t *testing.T) {
	for _, tc := range []struct{ path, code, text string }{
		{"strings", "", ""},
		{"unicode/utf8", "", ""},
		// std-text-runes: math/bits joined the pure allowlist.
		{"math/bits", "", ""},
		// G9: a standard-library package outside the pure allowlist.
		// G2: a pure package not listed yet says so.
		{"math/cmplx", "S109", "extern go math/cmplx.F: math/cmplx is not on the pure allowlist ("},
		{"unicode", "", ""},
		{"unicode/utf16", "", ""},
		{"os", "S109", "extern go os.F reaches the host; wait for std os.Process or #202"},
		{"example.com/x", "S109", "#202"},
		{"ergo.local/prototype/runtime/rt", "S108", "only std binds the Ergo runtime"},
		{"ergo.local/prototype/runtime/v1", "S108", "only std binds the Ergo runtime"},
		{"unsafe", "S108", "Ergo has no unsafe; unsafe stays at the FFI edge"},
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
