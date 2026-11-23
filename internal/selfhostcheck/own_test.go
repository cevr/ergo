package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// ownTemplates are the S108 messages compiler/check/diag reports for the
// own mark (MODES-OWN-CHECK; types.md, Generics 9; resource.go
// checkOwnMarks): a resource, or an own type parameter, bound to a type
// parameter with no mark, a function's or a generic type's (GEN9-GENERAL);
// and S112 for a map key type
// that may hold a resource, a resource or an own type parameter
// (MODES-OWN-BODY; resources R40).
var ownTemplates = []*regexp.Regexp{
	regexp.MustCompile(`^\S+ cannot take .+ as its \S+: \S+ is not marked own, so it binds plain data only; `),
	regexp.MustCompile(`^map key type .+ holds a resource, and a map key has no owner to release it \(resources R40\)`),
}

// goOwnMarks is the Go checker's diagnostics of ds that compiler/check/diag
// reports, "FILE OFFSET LENGTH S108 MESSAGE" in the checker's order.
func goOwnMarks(file string, ds []check.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Code != check.CodeType && d.Code != "S112" {
			continue
		}
		for _, re := range ownTemplates {
			if re.MatchString(d.Message) {
				out = append(out, fmt.Sprintf("%s %d %d %s %s", file, d.Offset, d.Length, d.Code, d.Message))
				break
			}
		}
	}
	return out
}

// ownCase is one package of the own-mark agreement: its source, the Go
// checker's lines and its tables.
type ownCase struct {
	name         string
	src          string
	want         []string
	ext, foreign []string
	seed         uint32
}

// TestCompilerOwnMarksAgreeWithChecker is MODES-OWN-CHECK's agreement: the
// own-mark S108s compiler/check/diag reports (exprdump -corpus, its O
// lines) are the Go checker's, text, position and order, over
//   - testdata/own: MODES-5's accept probes tparam-unmarked (a resource
//     bound to an unmarked T is S108 at the call) and tparam-own (checks
//     clean), the type rules (an unmarked T in a type argument; a
//     generic type's own T filled by an unmarked one asks nothing) and
//     user unions marked and unmarked, a function over one and an alias
//     of one (unions.ergo, GEN9-GENERAL); each file's
//     "//want: own" lines are the Go checker's, so a fixture that drifts
//     from the rule fails too;
//   - a seeded property: a program drawn from calls of a marked and an
//     unmarked callee with arguments of every shape the walk reads (plain
//     data, a Closer, a record, a union, a slice, a Map, Option, an
//     instance of a marked and an unmarked generic type, the caller's own
//     and unmarked T) and type arguments of those shapes.
//
// Every corpus folder the Go checker accepts has none
// (TestCompilerExprAgreesWithChecker reads its O lines).
//
// A failing seed prints the source, both outputs and the replay command.
//
// It builds Go because exprdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerOwnMarksAgreeWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	var cases []ownCase
	add := func(name, src string, seed uint32) []string {
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("%s: does not parse: %v\n%s", name, ds, src)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		want := goOwnMarks("p.ergo", ds)
		cases = append(cases, ownCase{name: name, src: src, want: want, ext: ext, foreign: foreign, seed: seed})
		return want
	}
	fixtures, err := filepath.Glob(filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "own", "*.ergo"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no testdata/own fixture: %v", err)
	}
	for _, p := range fixtures {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		want := add(rel(p), src, 0)
		var lines []int
		for _, w := range want {
			off, _ := strconv.Atoi(strings.Fields(w)[1])
			l, _ := lineCol(src, off)
			lines = append(lines, l)
		}
		var marked []int
		for i, l := range strings.Split(src, "\n") {
			if strings.Contains(l, "//want: own") {
				marked = append(marked, i+1)
			}
		}
		if fmt.Sprint(lines) != fmt.Sprint(marked) {
			t.Errorf("%s: the Go checker's own-mark S108s are on lines %v, the fixture marks %v", rel(p), lines, marked)
		}
	}
	seeds, reported := 0, 0
	for seed := base; seed < base+120; seed++ {
		src := ownProgram(seed)
		if len(add(fmt.Sprintf("seed %d", seed), src, seed)) > 0 {
			reported++
		}
		seeds++
	}
	if reported == 0 || reported == seeds {
		t.Fatalf("the Go checker reports an own mark in %d of %d seeds: the draw reaches one side only", reported, seeds)
	}
	tmp := t.TempDir()
	var manifest []string
	for i, c := range cases {
		dir := filepath.Join(tmp, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f, e, fo := filepath.Join(dir, "p.ergo"), filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, f, []string{strings.TrimSuffix(c.src, "\n")})
		writeLines(t, e, c.ext)
		writeLines(t, fo, c.foreign)
		manifest = append(manifest, strings.Join([]string{"main", e, fo, f}, " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, exprDump(t, ctx), "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(cases) && strings.HasPrefix(l, "O "):
			got[at] = append(got[at], l[2:])
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	for i, c := range cases {
		if strings.Join(c.want, "\n") == strings.Join(got[i], "\n") {
			continue
		}
		msg := fmt.Sprintf("%s: own-mark diagnostics differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		if c.seed != 0 {
			msg += fmt.Sprintf("\n--- source\n%s\nreplay: %s=%d go test -count=1 -run '^TestCompilerOwnMarksAgreeWithChecker$' ./internal/selfhostcheck (the first seed is the failing one)", c.src, seedVariable, c.seed)
		}
		t.Error(msg)
	}
	t.Logf("%d fixtures and %d seeds (%d with an own mark) compared", len(fixtures), seeds, reported)
}

// ownDecls are the declarations every drawn program holds: a resource,
// plain data, a record and a union over them, a marked and an unmarked
// generic type, and a marked and an unmarked callee.
const ownDecls = `type Res { n int } implements Closer

func Close(r own Res) {}

type Plain { n int }

type Holder { r Res }

type Either = Left { r Res } | Right { n int }

type Wrap { e Either }

type Box[T own] { v T }

type Cell[T] { v T }

func id[T](x T) T { x }

func keep[T own](x own T) T { x }
`

// ownArgs are the argument shapes of a drawn call: its expression and its
// type's text (the Go checker's).
var ownArgs = [][2]string{
	{"Plain{n: 1}", "Plain"},
	{"Res{n: 1}", "Res"},
	{"Holder{r: Res{n: 1}}", "Holder"},
	{"Wrap{e: Right{n: 2}}", "Wrap"},
	{"[]Res{}", "[]Res"},
	{"[]int{}", "[]int"},
	{"Map[string, Res]{}", "Map[string, Res]"},
	{"Map[string, int]{}", "Map[string, int]"},
	{"Some[Res]{Value: Res{n: 1}}", "Some[Res]"},
	{"Box[Res]{v: Res{n: 1}}", "Box[Res]"},
	{"Box[int]{v: 1}", "Box[int]"},
	{"Cell[int]{v: 1}", "Cell[int]"},
}

// ownTypes are the type arguments of a drawn type site.
var ownTypes = []string{"int", "Plain", "Res", "Holder", "Wrap", "[]Res", "[]Plain", "Map[string, Res]", "Option[Res]", "Option[int]", "Box[Res]", "Cell[int]"}

// ownProgram is the program of seed: ownDecls, then drawn calls in main,
// drawn generic callers passing their T on, and drawn type sites.
func ownProgram(seed uint32) string {
	r := rand.New(rand.NewPCG(uint64(seed), 0x6f776e))
	var b strings.Builder
	b.WriteString(ownDecls)
	n := 0
	for i := range 1 + r.IntN(3) {
		// A generic caller: its T marked or not, passed to either callee.
		mark, callee := []string{"", " own"}[r.IntN(2)], []string{"id", "keep"}[r.IntN(2)]
		mode := ""
		if mark != "" || callee == "keep" {
			mode = "own "
		}
		fmt.Fprintf(&b, "\nfunc via%d[T%s](x %sT) T { %s(x) }\n", i, mark, mode, callee)
	}
	for i := range r.IntN(3) {
		// A type site: a field of Cell or Box at a drawn argument, or a
		// generic function's parameter of Box at its T.
		switch r.IntN(3) {
		case 0:
			fmt.Fprintf(&b, "\ntype Site%d { c Cell[%s] }\n", i, ownTypes[r.IntN(len(ownTypes))])
		case 1:
			fmt.Fprintf(&b, "\ntype Site%d { c Box[%s] }\n", i, ownTypes[r.IntN(len(ownTypes))])
		default:
			fmt.Fprintf(&b, "\nfunc site%d[U%s](b own Box[U]) {}\n", i, []string{"", " own"}[r.IntN(2)])
		}
	}
	b.WriteString("\nfunc main() {\n")
	for range 1 + r.IntN(4) {
		a := ownArgs[r.IntN(len(ownArgs))]
		callee := []string{"id", "keep"}[r.IntN(2)]
		if r.IntN(4) == 0 {
			fmt.Fprintf(&b, "\tlet _ = %s[%s](%s)\n", callee, a[1], a[0])
		} else {
			fmt.Fprintf(&b, "\tlet _ = %s(%s)\n", callee, a[0])
		}
		n++
	}
	b.WriteString("}\n")
	return b.String()
}
