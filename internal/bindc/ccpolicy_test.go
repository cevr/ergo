package bindc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// policyCases hold the flag policy of the cc oracle (ccArgs) to its meaning:
// a source is valid when cc gives no error, whatever warnings cc gives by
// default. Each valid case draws a default warning from some compiler; each
// invalid case breaks an ISO C11 constraint, so cc must refuse it.
var policyCases = []struct {
	name  string
	src   string
	valid bool
	// gccOnly marks a case that only GCC refuses: clang folds undefined
	// behavior in a constant with a warning (measured, clang 18).
	gccOnly bool
}{
	// Apple clang: -Wdeprecated-non-prototype (measured at the macOS gate).
	{"K&R definition", "#include <stdint.h>\nuint32_t f(x) uint32_t x; { return x; }\n", true, false},
	// Apple clang: -Wvisibility (measured at the macOS gate). GCC warns by
	// default about the braced type in the prototype (measured, GCC 13).
	{"braced parameter types", "#include <stdint.h>\n" +
		"uint32_t f(x, y) union u { uint32_t a; float b; } x; enum { A = 1 } y; { return x.a + (uint32_t)y; }\n" +
		"uint32_t g(struct { uint32_t c; } *p);\n", true, false},
	// Apple clang: -Wshift-op-parentheses (measured at the macOS gate).
	{"shift without parentheses", "#define A 1 + 2\n#define B A << 1\n" +
		"_Static_assert((B) == 6, \"B\");\nenum { C = 1 + 2 << 1 };\n_Static_assert(C == 6, \"C\");\n", true, false},
	// C11 6.7.2p2: a declaration has at least one type specifier.
	{"declaration with no type", "x;\n", false, false},
	// C11 6.7.6.2p1: an array size is greater than zero.
	{"array of negative size", "int a[-1];\n", false, false},
	// C11 6.7.10p2: a static assertion that is false. A precedence or value
	// difference fails here with no warning.
	{"false static assertion", "_Static_assert(1 + 2 << 1 == 9, \"precedence\");\n", false, false},
	// C11 6.6p4: the value of a constant expression is in the range of its
	// type. GCC refuses it under -pedantic-errors; clang does not.
	{"signed overflow in a constant", "_Static_assert((2147483647 + 1) < 0, \"overflow\");\n", false, true},
}

// defaultWarnings are warnings a compiler can give by default: the three
// of Apple clang at the macOS gate, and the GCC warnings for the same code.
// The shim adds each one the host's cc knows.
var defaultWarnings = []string{
	"-Wdeprecated-non-prototype", "-Wvisibility", "-Wshift-op-parentheses",
	"-Wparentheses", "-Wold-style-definition", "-Wstrict-prototypes", "-Wall", "-Wextra",
}

// warningShim writes a cc that runs the host's cc with every warning of
// defaultWarnings that it knows, before the caller's flags (as a default
// is), and returns its path and the flags it adds.
func warningShim(t *testing.T, cc string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.c")
	writeFile(t, empty, "int empty;\n")
	var known []string
	for _, w := range defaultWarnings {
		// An unknown warning option is an error on GCC and, with -Werror, on
		// clang.
		if exec.Command(cc, "-Werror", w, "-fsyntax-only", empty).Run() == nil {
			known = append(known, w)
		}
	}
	if len(known) == 0 {
		t.Fatalf("cc knows none of %v", defaultWarnings)
	}
	shim := filepath.Join(dir, "cc")
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' %s \"$@\"\n", cc, strings.Join(known, " "))
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim, known
}

// TestCCPolicy: the oracle's flags (runCC) call each valid case valid and
// each invalid case invalid, on the host's cc and on a cc that gives the
// warnings of defaultWarnings to every call. The shim must warn on each
// valid case, or it proves nothing.
func TestCCPolicy(t *testing.T) {
	cc := requireCC(t)
	gcc := ccIsGCC(cc)
	shim, known := warningShim(t, cc)
	t.Logf("shim warnings: %v", known)
	dir := t.TempDir()
	for _, c := range policyCases {
		if c.valid {
			path := filepath.Join(dir, "warn.c")
			writeFile(t, path, c.src)
			out, err := exec.Command(shim, "-std=c11", "-fsyntax-only", path).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "warning:") {
				t.Errorf("%s: the shim gives no warning (%v), so it proves nothing:\n%s", c.name, err, out)
			}
		}
		if !c.valid && c.gccOnly && !gcc {
			continue
		}
		for _, host := range []struct{ name, cc string }{{"cc", cc}, {"shim", shim}} {
			err := runCC(t, host.cc, dir, "policy.c", c.src)
			switch {
			case c.valid && err != nil:
				t.Errorf("%s (%s): valid C11 is refused:\n%s\n%v", c.name, host.name, c.src, err)
			case !c.valid && err == nil:
				t.Errorf("%s (%s): invalid C11 is accepted:\n%s", c.name, host.name, c.src)
			}
		}
	}
}
