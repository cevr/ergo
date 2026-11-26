package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// goFootprints is the Go checker's footprint of each function declaration
// with a body, "F NAME caps=[A,B] opaque=BOOL" as footdump writes them (a
// task start counts as opaque: the Ergo facts do not read tasks apart yet),
// then its dropped-result S110s, "D OFFSET MESSAGE".
func goFootprints(pkg *check.Package, ds []check.Diagnostic) []string {
	var out []string
	for _, f := range pkg.Funcs {
		if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil {
			continue
		}
		fp := pkg.Info.Footprints[f.Decl]
		var caps []string
		for _, c := range fp.Caps {
			if n, ok := c.(*check.Named); ok && n.Obj != nil {
				caps = append(caps, n.Obj.Name)
			}
		}
		sort.Strings(caps)
		out = append(out, fmt.Sprintf("F %s caps=[%s] opaque=%t", f.Name, strings.Join(caps, ","), fp.Opaque || fp.Tasks))
	}
	for _, d := range ds {
		if d.Code == check.CodeUnused && strings.HasPrefix(d.Message, "the result of ") {
			out = append(out, fmt.Sprintf("D %d %s", d.Offset, d.Message))
		}
	}
	return out
}

// fpGen draws a package of functions over two capabilities: each body
// charges a capability, calls another function (itself and its callers
// included, so components of several members are drawn), calls a value of
// a function type (unknown code), or computes; some statements drop the
// value of a call (fpProgram).
type fpGen struct {
	r *rand.Rand
	n int
	b strings.Builder
}

func (g *fpGen) line(depth int, s string) {
	g.b.WriteString(strings.Repeat("\t", depth) + s + "\n")
}

func (g *fpGen) stmts(depth, k int) {
	for range k {
		switch g.r.IntN(11) {
		case 0:
			g.line(depth, `Log.Put("x")`)
		case 1:
			g.line(depth, "let _ = Clock.Now()")
		case 2, 3:
			g.line(depth, fmt.Sprintf("let _ = f%d(n)", g.r.IntN(g.n)))
		case 4, 5:
			g.line(depth, fmt.Sprintf("f%d(n)", g.r.IntN(g.n)))
		case 6:
			g.line(depth, "same(n)")
		case 7:
			g.line(depth, "let g = func() int { n }")
			g.line(depth, "let _ = g()")
		case 8:
			if depth < 3 {
				g.line(depth, "if n > 0 {")
				g.stmts(depth+1, 1+g.r.IntN(2))
				g.line(depth, "}")
			}
		case 9:
			if depth < 3 {
				g.line(depth, "for _ in 0..n {")
				g.stmts(depth+1, 1+g.r.IntN(2))
				g.line(depth, "}")
			}
		default:
			g.line(depth, "let _ = id(n) + 1")
		}
	}
}

// fpProgram is the program of seed: two to six functions, each over the
// same signature, so any may call any.
func fpProgram(seed uint32) string {
	g := &fpGen{r: rand.New(rand.NewPCG(uint64(seed), 0x6670)), n: 2 + int(seed%5)}
	g.b.WriteString("type Log { Put(s string) }\n\ntype Clock { Now() int }\n\nfunc id(x int) int { x }\n\nfunc same[T](x T) T { x }\n\n")
	for i := range g.n {
		g.line(0, fmt.Sprintf("func f%d(n int) int needs Log | Clock {", i))
		g.stmts(1, g.r.IntN(4))
		g.line(1, "n")
		g.line(0, "}")
		g.b.WriteString("\n")
	}
	g.b.WriteString("func main() {}\n")
	return g.b.String()
}

// TestCompilerFootprintsAgree is CHECK-FOOTPRINTS' agreement: the
// footprint compiler/check/footprint solves for each function declaration
// (footdump's F lines) is the Go checker's (Info.Footprints; footprint.go),
// and the dropped results compiler/check/diag reports (D lines) are the Go
// checker's (discard.go), over
//   - seeded packages of functions that charge two capabilities, call each
//     other in components of one or more members, call a value of a
//     function type and drop the values of calls (fpProgram): both answers
//     equal;
//   - the ownership-flow programs (flowProgram), whose generic bodies drop
//     the value of keep: the dropped results equal, and no function the
//     Ergo facts call pure is impure to the Go checker (they may be
//     opaque where the Go checker knows more).
//
// A failing seed prints the source, both answers and the replay command.
//
// It builds Go because footdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerFootprintsAgree(t *testing.T) {
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
	type fpCase struct {
		name, src, replay string
		want, ext, foreign []string
		exact              bool
	}
	var cases []fpCase
	add := func(name, src, replay string, exact bool) {
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("%s: does not parse: %v\n%s", name, ds, src)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		cases = append(cases, fpCase{name: name, src: src, replay: replay, want: goFootprints(pkg, ds), ext: ext, foreign: foreign, exact: exact})
	}
	for seed := base; seed < base+200; seed++ {
		add(fmt.Sprintf("footprint seed %d", seed), fpProgram(seed), fmt.Sprintf("%s=%d", seedVariable, seed), true)
	}
	for seed := base; seed < base+300; seed++ {
		add(fmt.Sprintf("flow seed %d", seed), flowProgram(seed), fmt.Sprintf("%s=%d", seedVariable, seed), false)
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
	bin := filepath.Join(t.TempDir(), "footdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/footdump"); err != nil {
		t.Fatalf("ergo build ./cmd/footdump: %v\n%s", err, o)
	}
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(cases):
			got[at] = append(got[at], l)
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("footdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	failed, pure, dropped := 0, 0, 0
	for i, c := range cases {
		want, have := c.want, got[i]
		if !c.exact {
			want, have = drops(want), drops(have)
			if bad := impure(c.want, got[i]); bad != "" {
				want, have = append(want, bad), append(have, "pure: "+bad)
			}
		}
		for _, l := range c.want {
			if strings.HasPrefix(l, "D ") {
				dropped++
			} else if strings.HasSuffix(l, "caps=[] opaque=false") {
				pure++
			}
		}
		if strings.Join(want, "\n") == strings.Join(have, "\n") {
			continue
		}
		failed++
		if failed > 5 {
			continue
		}
		t.Errorf("%s: footprints differ\n--- go\n%s\n--- ergo\n%s\n--- source\n%s\nreplay: %s go test -count=1 -run '^TestCompilerFootprintsAgree$' ./internal/selfhostcheck (the first seed is the failing one)", c.name, strings.Join(want, "\n"), strings.Join(have, "\n"), c.src, c.replay)
	}
	if failed > 0 {
		t.Errorf("%d of %d cases differ", failed, len(cases))
	}
	if pure == 0 || dropped == 0 {
		t.Fatalf("the draw reaches %d pure functions and %d dropped results: a side is not reached", pure, dropped)
	}
	t.Logf("%d cases compared: %d pure functions, %d dropped results", len(cases), pure, dropped)
}

// drops is the D lines of a footprint answer.
func drops(ls []string) []string {
	var out []string
	for _, l := range ls {
		if strings.HasPrefix(l, "D ") {
			out = append(out, l)
		}
	}
	return out
}

// impure is the first function the Ergo answer have calls pure that the
// Go answer want does not, or "".
func impure(want, have []string) string {
	goes := map[string]string{}
	for _, l := range want {
		if f := strings.Fields(l); len(f) == 4 && f[0] == "F" {
			goes[f[1]] = l
		}
	}
	for _, l := range have {
		if f := strings.Fields(l); len(f) == 4 && f[0] == "F" && strings.HasSuffix(l, "caps=[] opaque=false") && goes[f[1]] != l {
			return l
		}
	}
	return ""
}
