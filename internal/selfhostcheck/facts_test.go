package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// goRefinements is the Go checker's refinement of each refined type
// declaration of pkg, "FILE OFFSET NAME it|len LO..HI…" or "FILE OFFSET
// NAME -" where the checker gives it none (refine.go), sorted.
func goRefinements(pkg *check.Package, unnamed string) []string {
	byDecl := map[*syntax.TypeDecl]*check.TypeName{}
	for _, tn := range pkg.Types {
		if tn.Decl != nil {
			byDecl[tn.Decl] = tn
		}
	}
	var out []string
	for sf, name := range pkgFiles(pkg, unnamed) {
		for _, d := range sf.Decls {
			td, ok := d.(*syntax.TypeDecl)
			if !ok || td.Where == nil {
				continue
			}
			text := "-"
			if tn := byDecl[td]; tn != nil && tn.Refine != nil {
				subject := "it"
				if tn.Refine.Len {
					subject = "len"
				}
				parts := []string{subject}
				for _, s := range tn.Refine.Spans {
					parts = append(parts, s.Lo.String()+".."+s.Hi.String())
				}
				text = strings.Join(parts, " ")
			}
			out = append(out, fmt.Sprintf("%s %d %s %s", name, td.Name.Pos(), td.Name.Name, text))
		}
	}
	slices.Sort(out)
	return out
}

// hasWhere reports whether a .ergo file of dir declares a refined type.
func hasWhere(t *testing.T, dir string) bool {
	for _, f := range ergoFiles(t, dir) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), " where ") {
			return true
		}
	}
	return false
}

// TestCompilerRefinementsAgreeWithChecker is S3-FACTS slice 1: each
// refined type declaration's refinement that compiler/check/facts
// normalises (exprdump -corpus, its R lines) is the Go checker's (spans,
// whether the subject is a length, or none), over
//   - the corpus folders that declare a refined type (examples and std);
//   - a seeded property: packages of refined types drawn over the integer
//     kinds, string and a slice, their predicates from the fragment's
//     grammar (comparisons either way round with decimal, hex, octal,
//     binary and out-of-range literals, signs, &&, || and !, parentheses,
//     true and false, len(it)) and a few forms outside it.
//
// A failing seed prints the source, both outputs and the replay command.
//
// It builds Go because exprdump is the Ergo program under test: only its
// lowered, built form runs.
func TestCompilerRefinementsAgreeWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	base := uint64(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = n
	}
	type rcase struct {
		name, src string
		seed      uint64
		want      []string
	}
	tmp := t.TempDir()
	var cases []rcase
	var manifest []string
	corpus := 0
	for _, dir := range corpusDirs(t) {
		if !hasWhere(t, dir) {
			continue
		}
		pkg := typesPackage(dir)
		if pkg == nil {
			continue
		}
		n := len(cases)
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", n)), filepath.Join(tmp, fmt.Sprintf("foreign%d", n))
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		manifest = append(manifest, strings.Join(append([]string{importPath(dir, pkg), e, f}, ergoFiles(t, dir)...), " "))
		cases = append(cases, rcase{name: rel(dir), want: goRefinements(pkg, "")})
		corpus++
	}
	refused, given := 0, 0
	for seed := base; seed < base+200; seed++ {
		src := refinedProgram(seed)
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			t.Fatalf("seed %d: does not parse: %v\n%s", seed, ds, src)
		}
		want := goRefinements(pkg, "p.ergo")
		for _, w := range want {
			if strings.HasSuffix(w, " -") {
				refused++
			} else {
				given++
			}
		}
		n := len(cases)
		dir := filepath.Join(tmp, strconv.Itoa(n))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		file, e, f := filepath.Join(dir, "p.ergo"), filepath.Join(tmp, fmt.Sprintf("ext%d", n)), filepath.Join(tmp, fmt.Sprintf("foreign%d", n))
		writeLines(t, file, []string{strings.TrimSuffix(src, "\n")})
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		manifest = append(manifest, strings.Join([]string{"main", e, f, file}, " "))
		cases = append(cases, rcase{name: fmt.Sprintf("seed %d", seed), src: src, seed: seed, want: want})
	}
	if corpus == 0 {
		t.Fatal("no corpus folder declares a refined type")
	}
	if refused == 0 || given == 0 {
		t.Fatalf("the Go checker refines %d and refuses %d drawn types: the draw reaches one side only", given, refused)
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, exprDump(t, ctx), "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(cases) && strings.HasPrefix(l, "R "):
			got[at] = append(got[at], l[2:])
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	failed, lines := 0, 0
	for i, c := range cases {
		slices.Sort(got[i])
		lines += len(c.want)
		if strings.Join(c.want, "\n") == strings.Join(got[i], "\n") {
			continue
		}
		failed++
		if failed > 5 {
			continue
		}
		msg := fmt.Sprintf("%s: refinements differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		if c.seed != 0 {
			msg += fmt.Sprintf("\n--- source\n%s\nreplay: %s=%d go test -count=1 -run '^TestCompilerRefinementsAgreeWithChecker$' ./internal/selfhostcheck (the first seed is the failing one)", c.src, seedVariable, c.seed)
		}
		t.Error(msg)
	}
	if failed > 0 {
		t.Errorf("%d of %d packages differ", failed, len(cases))
	}
	t.Logf("%d corpus folders and %d seeds compared: %d refined types (%d drawn refined, %d drawn refused)", corpus, len(cases)-corpus, lines, given, refused)
}

// refinedProgram is a package of six refined types drawn from seed: each
// over an integer kind, string or []int, its where from the fragment's
// grammar, now and then with a form outside it.
func refinedProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	bases := []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune", "string", "[]int"}
	var b strings.Builder
	for i := range 6 {
		base := bases[r.IntN(len(bases))]
		subject := "it"
		if base == "string" || base == "[]int" {
			subject = "len(it)"
		}
		fmt.Fprintf(&b, "type R%d %s where %s\n\n", i, base, drawPredicate(r, subject, 3))
	}
	return b.String()
}

// drawLiteral is an integer literal: small, at an integer kind's edge or
// one past it, past uint64, in decimal, hex, octal or binary, signed or
// not.
func drawLiteral(r *rand.Rand) string {
	edges := []string{"0", "1", "7", "127", "128", "255", "256", "32767", "65535", "2147483647", "4294967295", "9223372036854775807", "9223372036854775808", "18446744073709551615", "18446744073709551616", "99999999999999999999999"}
	v := edges[r.IntN(len(edges))]
	switch r.IntN(6) {
	case 0:
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			v = "0x" + strconv.FormatUint(n, 16)
		}
	case 1:
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			v = "0o" + strconv.FormatUint(n, 8)
		}
	case 2:
		v = "0b" + strconv.FormatUint(uint64(r.IntN(64)), 2)
	case 3:
		v = strconv.Itoa(r.IntN(40) - 20)
	}
	switch r.IntN(4) {
	case 0:
		v = "-" + v
	case 1:
		if !strings.HasPrefix(v, "-") {
			v = "+" + v
		}
	}
	return v
}

// drawPredicate is a where over subject, nested at most depth deep.
func drawPredicate(r *rand.Rand, subject string, depth int) string {
	ops := []string{"==", "!=", "<", "<=", ">", ">="}
	k := r.IntN(10)
	if depth == 0 {
		k = r.IntN(4)
	}
	switch k {
	case 0, 1, 2:
		lit := drawLiteral(r)
		op := ops[r.IntN(len(ops))]
		if r.IntN(3) == 0 {
			return lit + " " + op + " " + subject
		}
		return subject + " " + op + " " + lit
	case 3:
		switch r.IntN(12) {
		case 0:
			return "true"
		case 1:
			return "false"
		case 2:
			return subject + " + 1 < 3"
		case 3:
			return "(" + subject + ") < " + drawLiteral(r)
		}
		return subject + " " + ops[r.IntN(len(ops))] + " " + drawLiteral(r)
	case 4, 5:
		return drawPredicate(r, subject, depth-1) + " && " + drawPredicate(r, subject, depth-1)
	case 6, 7:
		return drawPredicate(r, subject, depth-1) + " || " + drawPredicate(r, subject, depth-1)
	case 8:
		return "!(" + drawPredicate(r, subject, depth-1) + ")"
	default:
		return "(" + drawPredicate(r, subject, depth-1) + ")"
	}
}
