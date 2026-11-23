package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// providesLine is whether l is a diagnostic of the providing check
// (S3-PROVIDES): an S106 (an implements clause, its bounds, a context
// entry's provider), or the S108 of an operation type that has a generic
// operation named where a value goes (types.md, Interface assignability
// 8).
func providesLine(l checkLine) bool {
	return l.Code == check.CodeProvider || (l.Code == check.CodeType && strings.Contains(l.Message, "has a generic operation, so it is a constraint"))
}

func providesLines(ls []checkLine) []string {
	var out []string
	for _, l := range ls {
		if providesLine(l) {
			out = append(out, l.String())
		}
	}
	return out
}

// TestCompilerProvidesAgreesWithChecker is S3-PROVIDES' agreement: the
// providing check compiler/check reports (checkdump) is the Go checker's
// (checkImplementsClauses, clauseBounds, implementsAt, a context entry's
// provider and genericOpOnly), text, position and order, over
//   - the Go checker's negative fixtures that hold such a line, and
//   - a seeded property: an operation type (with or without a bracket
//     parameter and its bound, with generic operations and rows), a
//     claimant (a record, with or without parameters) and its clause,
//     each operation's providing function either right or broken one way
//     (missing, an extra parameter, a wrong result, a wider error or
//     capability row, a mut receiver, own type parameters miscounted or
//     constrained differently), a structural provider in a context entry,
//     and the operation type named as a value.
//
// A failing seed prints the source, both outputs and the replay command.
//
// It builds Go because checkdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerProvidesAgreesWithChecker(t *testing.T) {
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
	type pcase struct {
		name  string
		src   string
		seed  uint32
		want  []string
		ext   []string
		frgn  []string
		lines int
	}
	var cases []pcase
	add := func(name, src string, seed uint32) int {
		pkg, ds := check.CheckSource(src)
		if pkg == nil || (len(ds) > 0 && ds[0].Code == check.CodeSyntax) {
			t.Fatalf("%s: does not parse: %v\n%s", name, ds, src)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		want := providesLines(goCheckLines(ds, []string{"p.ergo"}))
		cases = append(cases, pcase{name: name, src: src, seed: seed, want: want, ext: ext, frgn: foreign})
		return len(want)
	}
	fixtures := 0
	for _, glob := range []string{"internal/sketch/check/testdata/fixtures/negative/*.ergo", "internal/sketch/check/testdata/v1/*.neg.ergo"} {
		paths, err := filepath.Glob(filepath.Join(repoRoot(), glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			_, ds := check.CheckSource(string(b))
			if len(providesLines(goCheckLines(ds, []string{"p.ergo"}))) == 0 {
				continue
			}
			add(rel(p), string(b), 0)
			fixtures++
		}
	}
	if fixtures == 0 {
		t.Fatal("no negative fixture holds a providing diagnostic")
	}
	seeds, reported := 0, 0
	for seed := base; seed < base+300; seed++ {
		if add(fmt.Sprintf("seed %d", seed), providesProgram(seed), seed) > 0 {
			reported++
		}
		seeds++
	}
	if reported == 0 || reported == seeds {
		t.Fatalf("the Go checker reports a providing diagnostic in %d of %d seeds: the draw reaches one side only", reported, seeds)
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
		writeLines(t, fo, c.frgn)
		manifest = append(manifest, strings.Join([]string{"main", e, fo, f}, " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	bin := filepath.Join(t.TempDir(), "checkdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/checkdump"); err != nil {
		t.Fatalf("ergo build ./cmd/checkdump: %v\n%s", err, o)
	}
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-corpus", m) {
		if strings.HasPrefix(l, "@pkg ") {
			at++
			continue
		}
		var c checkLine
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatalf("checkdump wrote a line that is no diagnostic object: %v\n%s", err, l)
		}
		if at >= 0 && at < len(cases) && providesLine(c) {
			c.File = "p.ergo"
			got[at] = append(got[at], c.String())
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("checkdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	failed := 0
	for i, c := range cases {
		if strings.Join(c.want, "\n") == strings.Join(got[i], "\n") {
			continue
		}
		failed++
		if failed > 5 {
			continue
		}
		msg := fmt.Sprintf("%s: providing diagnostics differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		if c.seed != 0 {
			msg += fmt.Sprintf("\n--- source\n%s\nreplay: %s=%d go test -count=1 -run '^TestCompilerProvidesAgreesWithChecker$' ./internal/selfhostcheck (the first seed is the failing one)", c.src, seedVariable, c.seed)
		}
		t.Error(msg)
	}
	if failed > 0 {
		t.Errorf("%d of %d cases differ", failed, len(cases))
	}
	t.Logf("%d fixtures and %d seeds (%d with a providing diagnostic) compared", fixtures, seeds, reported)
}

// The ways a drawn providing function is broken (provGen.fn).
const (
	provRight     = iota // the operation's signature at the claim
	provMissing          // no function
	provExtraArg         // one parameter more
	provResult           // another result type
	provWiderErr         // fails a type the operation does not
	provWiderUses        // needs a capability the operation does not
	provMut              // takes a mut receiver
	provOwnCount         // one own type parameter more
	provOwnBound         // its own type parameter comparable
	provForms
)

// provOp is one drawn operation: its name, whether it is generic (Fold[A])
// and whether it fails.
type provOp struct {
	name    string
	generic bool
	fails   bool
}

type provGen struct {
	r *rand.Rand
}

func (g *provGen) pick(n int) int { return g.r.IntN(n) }

// providesProgram is the program of seed: see
// TestCompilerProvidesAgreesWithChecker.
func providesProgram(seed uint32) string {
	g := &provGen{r: rand.New(rand.NewPCG(uint64(seed), 0x70726f76))}
	var b strings.Builder
	param := g.pick(3) != 0 // R[T]
	bound := ""
	if param && g.pick(4) == 0 {
		bound = " comparable"
	}
	elem := "int"
	if param {
		elem = "T"
	}
	b.WriteString("type Bad {}\n\ntype Wide {}\n\ntype Other { Need() int }\n\n")
	var ops []provOp
	for i := range 1 + g.pick(3) {
		ops = append(ops, provOp{name: fmt.Sprintf("Op%d", i), generic: g.pick(3) == 0, fails: g.pick(3) == 0})
	}
	head := "R"
	if param {
		head = "R[T" + bound + "]"
	}
	fmt.Fprintf(&b, "type %s {\n", head)
	for _, o := range ops {
		if o.generic {
			fmt.Fprintf(&b, "\t%s[A](z A, f func(acc A, x %s) A) A", o.name, elem)
		} else {
			fmt.Fprintf(&b, "\t%s(x %s) int", o.name, elem)
		}
		if o.fails {
			b.WriteString(" fails Bad")
		}
		b.WriteString(",\n")
	}
	b.WriteString("}\n\n")
	// The claimant: C[T] or C, claiming R[T], R[int] or R.
	cparam := param && g.pick(4) != 0
	ctype, claim, celem := "C", "R", "int"
	switch {
	case param && cparam:
		ctype, claim, celem = "C[T]", "R[T]", "T"
	case param:
		claim = "R[int]"
	}
	cdecl := "C"
	if cparam {
		cdecl = "C[T]"
	}
	fmt.Fprintf(&b, "type %s { V %s } implements %s\n\n", cdecl, celem, claim)
	tps := ""
	if cparam {
		tps = "T"
	}
	for _, o := range ops {
		form := provRight
		if g.pick(3) == 0 {
			form = 1 + g.pick(provForms-1)
		}
		if form == provMissing {
			continue
		}
		own := []string{}
		if o.generic {
			own = append(own, "A")
		}
		switch form {
		case provOwnCount:
			own = append(own, "B")
		case provOwnBound:
			if len(own) > 0 {
				own[0] = "A comparable"
			} else {
				own = append(own, "A comparable")
			}
		}
		all := own
		if tps != "" {
			all = append([]string{tps}, own...)
		}
		brackets := ""
		if len(all) > 0 {
			brackets = "[" + strings.Join(all, ", ") + "]"
		}
		recv := "c " + ctype
		if form == provMut {
			recv = "c mut " + ctype
		}
		var params, result, body string
		if o.generic {
			params, result, body = fmt.Sprintf(", z A, f func(acc A, x %s) A", celem), "A", "z"
		} else {
			params, result, body = fmt.Sprintf(", x %s", celem), "int", "0"
		}
		switch form {
		case provExtraArg:
			params += ", y int"
		case provResult:
			result, body = "string", `""`
		}
		row := ""
		if o.fails {
			row = " fails Bad"
		}
		switch form {
		case provWiderErr:
			if o.fails {
				row = " fails Bad | Wide"
			} else {
				row = " fails Wide"
			}
		case provWiderUses:
			row += " needs Other"
			body = "{\n\tlet _ = Other.Need()\n\t" + body + "\n}"
		}
		if !strings.HasPrefix(body, "{") {
			body = "{ " + body + " }"
		}
		fmt.Fprintf(&b, "func %s%s(%s%s) %s%s %s\n\n", o.name, brackets, recv, params, result, row, body)
	}
	generic := false
	for _, o := range ops {
		generic = generic || o.generic
	}
	// A value of the operation type: refused when it has a generic
	// operation.
	if g.pick(2) == 0 {
		at := "R"
		if param {
			at = "R[int]"
		}
		fmt.Fprintf(&b, "func hold(r %s) {}\n\n", at)
	}
	// A structural provider in a context entry, for an operation type
	// that has values.
	if !param && !generic && g.pick(2) == 0 {
		b.WriteString("type D {}\n\n")
		for _, o := range ops {
			if g.pick(3) == 0 {
				continue // missing
			}
			row := ""
			if o.fails {
				row = " fails Bad"
			}
			if g.pick(4) == 0 {
				row = " fails Wide"
			}
			fmt.Fprintf(&b, "func %s(_ D, x int) int%s { 0 }\n\n", o.name, row)
		}
		b.WriteString("let Live = context {\n\tR: D,\n}\n\n")
	}
	b.WriteString("func main() {}\n")
	return b.String()
}
