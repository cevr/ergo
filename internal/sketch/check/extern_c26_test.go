package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

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

// TestExternTargetDomain is A5.2 of check-v1 stage E4 (C26), as IG4 leaves
// it: a user extern names a function of a binding. A dotted path is #202's
// until module bindings (IG7), only std binds the Ergo runtime, and a path
// or a name no binding has is S104; a declaration the binding refuses is
// S109 with its reason, and a type is no function target. TestLevel2AdapterRelation
// holds the signature of an admitted target.
func TestExternTargetDomain(t *testing.T) {
	for _, tc := range []struct{ decl, code, text string }{
		{"extern go strings.Count func f(s string, sub string) int", "", ""},
		{"extern go os.Getpid func f() int needs go.Foreign", "", ""},
		{"extern go example.com/x.F func f(n int) int", "S109", "#202"},
		{"extern go ergo.local/prototype/runtime/rt.F func f(n int) int", "S108", "only std binds the Ergo runtime"},
		{"extern go ergo.local/prototype/runtime/v1.F func f(n int) int", "S108", "only std binds the Ergo runtime"},
		{"extern go unsafe.F func f(n int) int", "S108", "Ergo has no unsafe; unsafe stays at the FFI edge"},
		{"extern go strings/x.F func f(n int) int", "S104", `no binding for Go package "strings/x"`},
		{"extern go strings.F func f(n int) int", "S104", "Go package strings has no exported function F"},
		{"extern go fmt.Sprint func f(n int) string needs go.Foreign", "S109", "extern go fmt.Sprint is not imported from Go: parameter 1: Ergo has no any"},
		{"extern go time.Duration func f(x float64) duration needs go.Foreign", "S109", "time.Duration is Ergo's duration"},
		{"extern go strings.Builder func f() int", "S108", "names a Go type, constant or variable"},
	} {
		t.Run(tc.decl, func(t *testing.T) {
			_, diags := check.CheckSource(tc.decl + "\n")
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
