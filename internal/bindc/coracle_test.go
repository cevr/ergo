package bindc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The cc oracle: the host C compiler checks every layout, constant and
// prototype of the model against the header itself. The C file includes
// the header and states the model's claims as _Static_assert and as
// redeclarations; cc accepts it only when every claim holds.

// requireCC finds cc, or skips the test unless ERGO_BINDC_REQUIRE_CC=1.
func requireCC(t *testing.T) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		if os.Getenv("ERGO_BINDC_REQUIRE_CC") == "1" {
			t.Fatalf("cc is not on PATH and ERGO_BINDC_REQUIRE_CC=1: %v", err)
		}
		t.Skip("cc is not on PATH (set ERGO_BINDC_REQUIRE_CC=1 to make this a failure)")
	}
	return cc
}

// cLiteral spells a constant's value as a C expression of a type wide
// enough to compare exactly.
func cLiteral(c Constant) string {
	if !c.Signed() {
		return fmt.Sprintf("%dULL", c.Uint64())
	}
	v := c.Int64()
	if v == -1<<63 {
		return "(-9223372036854775807LL - 1)"
	}
	if v < 0 {
		return fmt.Sprintf("(-%dLL)", -v)
	}
	return fmt.Sprintf("%dLL", v)
}

// cTypeClass is the class a C11 _Generic gives the type of a constant:
// int and unsigned int, then the 64-bit types (long or long long, as the
// target's stdint.h chooses).
var cTypeClass = map[Scalar]int{Int32: 1, Uint32: 2, Int64: 3, Uint64: 4}

// oracleSource is the C file of the model's claims. mutate, when not nil,
// changes the claims before they are written (the mutant control).
func oracleSource(m *Model, header string, mutate func(*Model)) string {
	if mutate != nil {
		c := *m
		c.Types = append([]TypeDecl{}, m.Types...)
		mutate(&c)
		m = &c
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#include %q\n#include <stddef.h>\n\n", header)
	b.WriteString("#define CLASS(x) _Generic((x), int: 1, unsigned int: 2, long: 3, long long: 3, unsigned long: 4, unsigned long long: 4, default: 0)\n\n")
	for _, c := range m.Constants {
		// The parentheses keep a body such as `2 | 1` from binding to the
		// `==` (the model's value is the value of the whole body).
		fmt.Fprintf(&b, "_Static_assert((%s) == %s, \"value of %s\");\n", c.Name, cLiteral(c), c.Name)
		fmt.Fprintf(&b, "_Static_assert(CLASS(%s) == %d, \"type of %s\");\n", c.Name, cTypeClass[c.Type], c.Name)
	}
	for _, d := range m.Types {
		switch d.Kind {
		case DeclScalar:
			fmt.Fprintf(&b, "typedef %s %s;\n", d.Scalar, d.Name)
		case DeclOpaque:
			fmt.Fprintf(&b, "typedef struct %s %s;\n", d.Tag, d.Name)
		case DeclCallback:
			fmt.Fprintf(&b, "typedef %s (*%s)(%s);\n", d.Result.Spell(), d.Name, printParams(d.Params))
		case DeclStruct:
			if d.Tag != "" {
				fmt.Fprintf(&b, "typedef struct %s %s;\n", d.Tag, d.Name)
			}
			fmt.Fprintf(&b, "_Static_assert(sizeof(%s) == %d, \"size of %s\");\n", d.Name, d.Size, d.Name)
			fmt.Fprintf(&b, "_Static_assert(_Alignof(%s) == %d, \"alignment of %s\");\n", d.Name, d.Align, d.Name)
			for _, f := range d.Fields {
				fmt.Fprintf(&b, "_Static_assert(offsetof(%s, %s) == %d, \"offset of %s.%s\");\n", d.Name, f.Name, f.Offset, d.Name, f.Name)
				fmt.Fprintf(&b, "_Static_assert(sizeof(((%s *)0)->%s) == %d, \"size of %s.%s\");\n", d.Name, f.Name, f.Size, d.Name, f.Name)
				fmt.Fprintf(&b, "_Static_assert(_Alignof(%s) == %d, \"alignment of %s.%s\");\n", f.Type.Spell(), f.Align, d.Name, f.Name)
				fmt.Fprintf(&b, "_Static_assert(_Generic(&((%s *)0)->%s, %s: 1, default: 0), \"type of %s.%s\");\n", d.Name, f.Name, f.Type.Declare("(*)"), d.Name, f.Name)
			}
		}
	}
	for _, f := range m.Functions {
		fmt.Fprintf(&b, "%s(%s);\n", f.Result.Declare(f.Name), printParams(f.Params))
	}
	return b.String()
}

// ccArgs are the flags of every cc check: the flag policy of the oracle. A
// source is valid C11 when cc gives no error with them. -pedantic-errors makes
// every ISO C constraint violation an error on GCC and on clang (a declaration
// with no type, an array of negative size, a conflicting redeclaration, a
// _Static_assert that fails or whose expression is not an integer constant
// expression). The value, type and layout claims are _Static_assert
// comparisons of exact values, so they need no warning.
//
// There is no -Werror. Each compiler has its own default warnings, and none
// of them makes valid C invalid: Apple clang warns by default about a K&R
// definition (-Wdeprecated-non-prototype), a braced type in a parameter list
// (-Wvisibility) and `a + b << c` (-Wshift-op-parentheses), all measured at
// the macOS gate. TestCCPolicy holds the policy to this on the host's cc and
// under a shim that adds such warnings to every call.
//
// No check needs one named warning as an error: undefined behavior in a
// constant (signed overflow, a shift out of range) makes the static
// assertion's expression not an integer constant expression (C11 6.6p4),
// which GCC reports as an error under -pedantic-errors (measured, GCC 13).
// clang folds such an expression with a warning (-Winteger-overflow, a name
// GCC does not know; measured, clang 18), so the checks that need cc to
// refuse undefined behavior run with GCC only (TestMacroExpansionProperty).
var ccArgs = []string{"-std=c11", "-fsyntax-only", "-pedantic-errors"}

// runCC checks a C source with cc and returns cc's output on failure.
func runCC(t *testing.T, cc, dir, name, src string, extra ...string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append(append(append([]string{}, ccArgs...), "-I", dir), extra...)
	cmd := exec.Command(cc, append(args, path)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v\n%s", err, out)
	}
	return nil
}

func TestCOracle(t *testing.T) {
	cc := requireCC(t)
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			dir, err := filepath.Abs(h.dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := runCC(t, cc, dir, "oracle.c", oracleSource(m, h.header, nil)); err != nil {
				t.Fatalf("cc refuses the model of %s:\n%v", h.header, err)
			}
			// Mutant control: one struct size off by one must fail cc, or
			// the oracle proves nothing.
			mutant := oracleSource(m, h.header, func(c *Model) {
				for i := range c.Types {
					if c.Types[i].Kind == DeclStruct {
						c.Types[i].Size++
						return
					}
				}
			})
			if err := runCC(t, cc, dir, "mutant.c", mutant); err == nil {
				t.Fatalf("cc accepts a struct size off by one: the oracle checks nothing")
			}
		})
	}
	// The fixture library compiles against its header, with each build flag.
	t.Run("fixture.c", func(t *testing.T) {
		dir, err := filepath.Abs("testdata/fixture")
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile(filepath.Join(dir, "fixture.c"))
		if err != nil {
			t.Fatal(err)
		}
		for _, flags := range [][]string{nil, {"-DFIXTURE_ABI=3"}, {"-DFIXTURE_OMIT=fx_node_count"}} {
			if err := runCC(t, cc, dir, "fixture.c", string(src), flags...); err != nil {
				t.Errorf("fixture.c %v:\n%v", flags, err)
			}
		}
	})
}
