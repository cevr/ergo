package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// G5b (docs/spec/stdlib.md, Pure bodies): a call of a std extern whose Go
// body is marked //ergo:pure is transparent in a footprint. A function that
// calls strings.TrimSpace has the footprint of one that calls nothing, an
// Ergo std function over pure externs (strings.Cut) is pure too, and a
// capability the function calls stays in its footprint. A std extern whose
// body is not marked (slices.WithCapacity) and a user extern of the same Go
// function stay opaque: purity is a reviewed fact of std only.
func TestFootprintsPureStd(t *testing.T) {
	src := `import strings
import strconv
import slices
type Env { Get(key string) string }
extern go strings.TrimSpace func hostTrim(s string) string
func nothing(s string) string { s }
func trims(s string) string { strings.TrimSpace(s) }
func words(s string) []string { strings.Fields(strings.ToLower(s)) }
func cuts(s string) bool {
	match strings.Cut(s, ":") {
	case Some[strings.Halves]: true
	case None: false
	}
}
func parses(s string) int fails strconv.Malformed | strconv.OutOfRange { try strconv.Atoi(s) }
func reads(key string) string needs Env { strings.TrimSpace(Env.Get(key)) }
func loops(s string, n int) string {
	if n == 0 {
		return s
	}
	loops(strings.TrimSpace(s), n - 1)
}
func makes() []int { slices.WithCapacity[int](4) }
func userTrim(s string) string { hostTrim(s) }
` + preludeCollectionFuncs
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	fps := map[string]check.Footprint{}
	for _, f := range pkg.Funcs {
		if f.Decl != nil && f.Decl.Body != nil {
			fp, ok := pkg.Info.Footprints[f.Decl]
			if !ok {
				t.Fatalf("func %s has no footprint", f.Name)
			}
			fps[f.Name] = fp
		}
	}
	want := map[string]string{
		"nothing":  "pure",
		"trims":    "pure",
		"words":    "pure",
		"cuts":     "pure",
		"parses":   "pure",
		"reads":    "Env",
		"loops":    "pure",
		"makes":    "opaque",
		"userTrim": "opaque",
		// lw-pure-prelude: the prelude's Append, Get and Delete are marked
		// pure in runtime/v1 (data.go).
		"appends": "pure",
		"gets":    "pure",
		"deletes": "pure",
	}
	for name, w := range want {
		if got := show(fps[name]); got != w {
			t.Errorf("func %s: footprint %q, want %q", name, got, w)
		}
	}
	// The same footprint as a function that calls nothing, entry placement
	// included: a pure call adds no interruption point.
	if a, b := fps["trims"], fps["nothing"]; a.EntryPoll != b.EntryPoll || show(a) != show(b) {
		t.Errorf("trims %+v differs from nothing %+v", a, b)
	}
	if fps["loops"].EntryPoll {
		t.Error("loops polls at entry; pure recursion never polls")
	}
	for _, name := range []string{"appends", "gets", "deletes"} {
		if fps[name].EntryPoll {
			t.Errorf("%s polls at entry; a pure call adds no interruption point", name)
		}
	}
}

// preludeCollectionFuncs call only the prelude's Append, Get and Delete,
// with loops and arithmetic (lw-pure-prelude).
const preludeCollectionFuncs = `func appends(n int) []int {
	let mut out = []int{}
	for i in 0..n {
		if i % 2 == 0 {
			out = out.Append(i)
		}
	}
	out
}
func gets(m Map[string, int], k string) int {
	match m.Get(k) {
	case Some[int] as s:
		s.Value
	case None:
		0
	}
}
func deletes(m Map[string, int], k string) Map[string, int] { m.Delete(k) }
`

// TestFootprintsPurePreludeOff: with the reference switch off
// (SetPureCollectionsForTest(false)), a call of the prelude's Append, Get
// or Delete is opaque again, as it was before lw-pure-prelude, so the
// reference lowering polls and takes the environment.
func TestFootprintsPurePreludeOff(t *testing.T) {
	for _, pure := range []bool{false, true} {
		restore := check.SetPureCollectionsForTest(pure)
		file, err := syntax.Parse(preludeCollectionFuncs)
		if err != nil {
			restore()
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		restore()
		if len(diags) > 0 {
			t.Fatalf("diagnostics: %v", diags)
		}
		want := "opaque"
		if pure {
			want = "pure"
		}
		for _, f := range pkg.Funcs {
			if f.Decl == nil || f.Decl.Body == nil {
				continue
			}
			if got := show(pkg.Info.Footprints[f.Decl]); got != want {
				t.Errorf("pure collections %v: func %s footprint %q, want %q", pure, f.Name, got, want)
			}
		}
	}
}
