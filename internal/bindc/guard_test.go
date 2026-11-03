package bindc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The names an include guard may not have, drawn without the parser's own
// rule: the C11 and C23 keywords, the preprocessor's own names, the macros
// and typedef names the C standard gives <stdint.h>, <stdbool.h> and
// <stddef.h>, the non-reserved macros clang predefines on darwin, and, when
// cc is on PATH, every macro the host compiler predefines or the three
// headers define (in -std=c11 and -std=gnu11) and every typedef name they
// declare. C would skip a header whose guard is predefined, or break the
// standard header a guard name hides.
var (
	c11Keywords = strings.Fields(`auto break case char const continue default do double else enum extern
		float for goto if inline int long register restrict return short signed sizeof static struct switch
		typedef union unsigned void volatile while _Alignas _Alignof _Atomic _Bool _Complex _Generic
		_Imaginary _Noreturn _Static_assert _Thread_local`)
	c23Keywords = strings.Fields(`alignas alignof bool constexpr false nullptr static_assert thread_local
		true typeof typeof_unqual _BitInt _Decimal32 _Decimal64 _Decimal128`)
	preprocessorNames = strings.Fields(`defined __VA_ARGS__ __VA_OPT__ __has_include __has_c_attribute
		__STDC__ __STDC_VERSION__ __STDC_HOSTED__ __FILE__ __LINE__ __DATE__ __TIME__ __cplusplus`)
	standardTypedefs = strings.Fields(`size_t ptrdiff_t wchar_t max_align_t nullptr_t rsize_t
		intptr_t uintptr_t intmax_t uintmax_t`)
	standardMacros = strings.Fields(`NULL offsetof unreachable bool true false __bool_true_false_are_defined
		INTPTR_MIN INTPTR_MAX UINTPTR_MAX INTMAX_MIN INTMAX_MAX UINTMAX_MAX INTMAX_C UINTMAX_C
		PTRDIFF_MIN PTRDIFF_MAX SIG_ATOMIC_MIN SIG_ATOMIC_MAX SIZE_MAX RSIZE_MAX WCHAR_MIN WCHAR_MAX
		WINT_MIN WINT_MAX`)
	// clang's TargetOSMacros.def and its darwin predefines (inferred from
	// the clang sources; the darwin run of the gate checks them).
	darwinMacros = strings.Fields(`OBJC_NEW_PROPERTIES TARGET_OS_MAC TARGET_OS_OSX TARGET_OS_IPHONE
		TARGET_OS_IOS TARGET_OS_TV TARGET_OS_WATCH TARGET_OS_VISION TARGET_OS_XR TARGET_OS_DRIVERKIT
		TARGET_OS_MACCATALYST TARGET_OS_SIMULATOR TARGET_OS_EMBEDDED TARGET_OS_RTKIT TARGET_OS_NANO
		TARGET_OS_UNIX TARGET_OS_LINUX TARGET_OS_WIN32 TARGET_OS_UIKITFORMAC TARGET_IPHONE_SIMULATOR`)
)

// stdintNames are the width-indexed macros and typedef names of <stdint.h>.
func stdintNames() []string {
	var ns []string
	for _, n := range []string{"8", "16", "32", "64"} {
		for _, f := range []string{"INT%s_MIN", "INT%s_MAX", "UINT%s_MAX", "INT%s_C", "UINT%s_C",
			"INT_LEAST%s_MIN", "INT_LEAST%s_MAX", "UINT_LEAST%s_MAX", "INT_FAST%s_MIN", "INT_FAST%s_MAX", "UINT_FAST%s_MAX",
			"int%s_t", "uint%s_t", "int_least%s_t", "uint_least%s_t", "int_fast%s_t", "uint_fast%s_t"} {
			ns = append(ns, fmt.Sprintf(f, n))
		}
	}
	return ns
}

var (
	ccMacroName   = regexp.MustCompile(`(?m)^#define ([A-Za-z_][A-Za-z0-9_]*)`)
	ccTypedefName = regexp.MustCompile(`typedef\s[^;{}]*?\b([A-Za-z_][A-Za-z0-9_]*)\s*;|}\s*([A-Za-z_][A-Za-z0-9_]*)\s*;`)
)

// reservedGuardNames maps each name to where it comes from.
func reservedGuardNames(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(from string, names ...string) {
		for _, n := range names {
			if _, ok := out[n]; !ok {
				out[n] = from
			}
		}
	}
	add("C11 keyword", c11Keywords...)
	add("C23 keyword", c23Keywords...)
	add("preprocessor name", preprocessorNames...)
	add("standard typedef", standardTypedefs...)
	add("standard macro", standardMacros...)
	add("<stdint.h> name", stdintNames()...)
	add("darwin predefined macro", darwinMacros...)
	cc, err := exec.LookPath("cc")
	if err != nil {
		if os.Getenv("ERGO_BINDC_REQUIRE_CC") == "1" {
			t.Fatalf("cc is not on PATH and ERGO_BINDC_REQUIRE_CC=1: %v", err)
		}
		t.Logf("cc is not on PATH: only the listed names are checked")
		return out
	}
	src := filepath.Join(t.TempDir(), "std.c")
	writeFile(t, src, "#include <stdint.h>\n#include <stdbool.h>\n#include <stddef.h>\n")
	for _, std := range []string{"c11", "gnu11"} {
		macros, err := exec.Command(cc, "-std="+std, "-dM", "-E", src).Output()
		if err != nil {
			t.Fatalf("cc -dM -E: %v", err)
		}
		for _, m := range ccMacroName.FindAllStringSubmatch(string(macros), -1) {
			add("cc -std="+std+" macro", m[1])
		}
	}
	pre, err := exec.Command(cc, "-std=c11", "-E", src).Output()
	if err != nil {
		t.Fatalf("cc -E: %v", err)
	}
	for _, m := range ccTypedefName.FindAllStringSubmatch(string(pre), -1) {
		add("cc typedef name", m[1]+m[2])
	}
	return out
}

// TestReservedGuardProperty: an include guard whose name is reserved,
// predefined or standard gives exactly one diagnostic, BC031 at the name
// after #ifndef, and no model; a generated header with an ordinary guard
// name gives a model with that guard. Every reserved name is tried, each in
// a generated valid header, and an ordinary name alternates with it.
func TestReservedGuardProperty(t *testing.T) {
	reserved := reservedGuardNames(t)
	names := make([]string, 0, len(reserved))
	for n := range reserved {
		names = append(names, n)
	}
	sort.Strings(names)
	cases := *propCases
	if cases < 2*len(names) {
		cases = 2 * len(names)
	}
	var ordinary []string
	for i := 0; i < cases; i++ {
		seed := *propSeed + uint64(i)
		g := newGen(seed)
		name := fmt.Sprintf("GUARD_%d_H", i)
		if i%2 == 0 {
			name = names[(i/2)%len(names)]
		}
		g.guard = name
		h := g.header(2 + i%10)
		m, err := Parse("gen.h", []byte(h.src))
		if i%2 != 0 {
			if err != nil || m.Guard != name {
				t.Fatalf("seed %d: ordinary guard %s: %v\n%s", seed, name, err, numbered(h.src))
			}
			if len(ordinary) < 3 {
				ordinary = append(ordinary, h.src)
			}
			continue
		}
		at := strings.Index(h.src, "#ifndef "+name+"\n") + len("#ifndef ")
		line, col := lineCol(h.src, at)
		want := fmt.Sprintf("gen.h:%d:%d: %s", line, col, CodeMacroIdentifier)
		var ds Diagnostics
		errors.As(err, &ds)
		if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), want+" ") {
			t.Fatalf("seed %d: guard %s (%s): want one diagnostic %s and no model; got model %v and:\n%v\n--- source:\n%s",
				seed, name, reserved[name], want, m != nil, err, numbered(h.src))
		}
	}
	t.Logf("%d cases over %d reserved guard names", cases, len(names))
	// C sees the declarations under an ordinary guard: cc checks the model.
	cc := requireCC(t)
	dir := t.TempDir()
	for _, src := range ordinary {
		m, err := Parse("gen.h", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "gen.h"), src)
		if err := runCC(t, cc, dir, "oracle.c", oracleSource(m, "gen.h", nil)); err != nil {
			t.Fatalf("cc refuses the model of a header with an ordinary guard:\n%v", err)
		}
	}
}

// TestReservedGuard is the round 1 counterexample (finding M2): C11
// predefines __STDC__, so C skips this header, and the parser must not
// model S.
func TestReservedGuard(t *testing.T) {
	src := "#ifndef __STDC__\n#define __STDC__\n#include <stdint.h>\ntypedef struct { uint32_t a; } S;\n#endif\n"
	m, err := Parse("probe.h", []byte(src))
	var ds Diagnostics
	errors.As(err, &ds)
	if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), "probe.h:1:9: "+string(CodeMacroIdentifier)+" ") {
		t.Fatalf("want one diagnostic probe.h:1:9: %s and no model; got model %v and %v", CodeMacroIdentifier, m != nil, err)
	}
}
