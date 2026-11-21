package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// unusedTemplates are the S110 messages compiler/check/diag reports: an
// unread local, and an import nothing reads (S3-DIAG); a match or catch
// arm's binding its arm never reads, plain, `as` or a catch's rest, with
// the hint of a capitalised name (S3-DIAG2). The agreement compares the Go
// checker's S110 lines whose message has one of these forms; its other
// messages are later units'.
var unusedTemplates = []*regexp.Regexp{
	regexp.MustCompile(`^\S+ is declared and not used$`),
	regexp.MustCompile(`^package \S+ is imported and not used$`),
	regexp.MustCompile(`^the binding \S+ is read nowhere in its arm; (write _ in its place|drop ` + "`as \\S+`" + `|write case _)(; no type is named \S+, so the pattern binds a new name)? \(rows\.md, Totality 1\)$`),
}

// goUnused is the Go checker's diagnostics of ds that compiler/check/diag
// reports, as "L:C: S110 MESSAGE" in the checker's order.
func goUnused(ds []check.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if d.Code != check.CodeUnused {
			continue
		}
		for _, re := range unusedTemplates {
			if re.MatchString(d.Message) {
				out = append(out, d.String())
				break
			}
		}
	}
	return out
}

// unusedCase is one package of the agreement: its files and the Go
// checker's lines ("FILE L:C: S110 MESSAGE"), or for a corpus folder none.
// exempt is the positions ("FILE L:C") of the locals the Go checker counts
// as read because their let's value holds another of its diagnostics (D3:
// the local has the invalid type, so no cascade). compiler/check/diag
// reports no such error yet (S104, S108 are later units), so the Ergo
// side's line there is dropped and counted.
type unusedCase struct {
	name   string
	files  []string
	want   []string
	exempt map[string]bool
	src    string // a generated case's source, printed when it fails
	seed   uint32
}

// d3Exempt is the "FILE L:C" of each local of src whose let's value holds
// a diagnostic of ds other than S110, and of each name of an arm whose
// patterns hold one: the Go checker binds a refused pattern's names at the
// invalid type, as placeholders, so they add no S110 (D3).
func d3Exempt(file, src string, ds []check.Diagnostic) map[string]bool {
	out := map[string]bool{}
	f, err := syntax.Parse(src)
	if err != nil {
		return out
	}
	holds := func(from, to int) bool {
		for _, g := range ds {
			if g.Code != check.CodeUnused && g.Offset >= from && g.Offset < to {
				return true
			}
		}
		return false
	}
	exemptNames := func(pats []syntax.Expr) {
		for _, x := range pats {
			syntax.Inspect(x, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					line, col := lineCol(src, int(id.Pos()))
					out[fmt.Sprintf("%s %d:%d", file, line, col)] = true
				}
				return true
			})
		}
	}
	arm := func(list, written []syntax.Expr, colon syntax.Pos) {
		pats := list
		if written != nil {
			pats = written
		}
		if len(pats) > 0 && holds(int(pats[0].Pos()), int(colon)) {
			exemptNames(pats)
		}
	}
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CaseClause:
			arm(n.List, n.Written, n.Colon)
			return true
		case *syntax.CatchArm:
			arm(n.Types, n.Written, n.Colon)
			return true
		}
		d, ok := n.(*syntax.VarDecl)
		if !ok {
			return true
		}
		for _, sp := range d.Specs {
			if len(sp.Values) == 0 {
				continue
			}
			if !holds(int(sp.Values[0].Pos()), int(sp.Values[len(sp.Values)-1].End())) {
				continue
			}
			for _, id := range sp.Names {
				line, col := lineCol(src, int(id.Pos()))
				out[fmt.Sprintf("%s %d:%d", file, line, col)] = true
			}
		}
		return true
	})
	return out
}

// lineCol is the 1-based line and byte column of offset in src.
func lineCol(src string, offset int) (int, int) {
	line, start := 1, 0
	for i := 0; i < offset && i < len(src); i++ {
		if src[i] == '\n' {
			line, start = line+1, i+1
		}
	}
	return line, offset - start + 1
}

// TestCompilerUnusedAgreesWithChecker is S3-DIAG's agreement: the S110
// diagnostics compiler/check/diag reports for unread locals and imports
// (binddump -unused) are the Go checker's, text, position and order, over
//   - every corpus folder the Go checker accepts (none on either side),
//   - the negative fixtures of internal/sketch/check (fixtures/negative,
//     v1/*.neg.ergo), each a package of one file, and
//   - a seeded perturbation of corpus files that check alone: an unread
//     let at the start of a body or of a nested block, a function literal
//     with an unread local and an unread parameter (never reported), an import of a std package nothing reads (by
//     its path, or under a name), an unread import and an unread let
//     together (the order across the two), an unread let holding a
//     match whose case binding is unread (the arm's message, not a
//     local's), or an unread let beside an alias with no name clash (S108,
//     which counts the import as read).
//
// A failing seed prints the source, both outputs and the replay command.
//
// It builds Go because binddump is the Ergo program under test: only its
// lowered, built form runs over the corpus.
func TestCompilerUnusedAgreesWithChecker(t *testing.T) {
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
	tmp := t.TempDir()
	var cases []unusedCase
	// The corpus: every folder the Go checker accepts has no line.
	for _, dir := range corpusDirs(t) {
		if typesPackage(dir) == nil {
			continue
		}
		cases = append(cases, unusedCase{name: rel(dir), files: ergoFiles(t, dir)})
	}
	corpus := len(cases)
	// The negative fixtures, each alone.
	fixtures := 0
	for _, glob := range []string{"internal/sketch/check/testdata/fixtures/negative/*.ergo", "internal/sketch/check/testdata/v1/*.neg.ergo"} {
		paths, err := filepath.Glob(filepath.Join(repoRoot(), glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			_, ds := check.CheckSource(string(src))
			if len(ds) > 0 && ds[0].Code == check.CodeSyntax {
				continue // the parser's, not the checker's
			}
			// One file in a folder of its own: the Ergo side reads the
			// folder's module, which a fixture has none of.
			dir := filepath.Join(tmp, "fixture", strconv.Itoa(fixtures))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(dir, filepath.Base(p))
			writeLines(t, f, []string{strings.TrimSuffix(string(src), "\n")})
			cases = append(cases, unusedCase{name: rel(p), files: []string{f}, want: prefixed(filepath.Base(p), goUnused(ds)), exempt: d3Exempt(filepath.Base(p), string(src), ds)})
			fixtures++
		}
	}
	// The seeded perturbations.
	pool := unusedPool(t)
	if len(pool) == 0 {
		t.Fatal("no corpus file checks alone")
	}
	seeds := 0
	for seed := base; seed < base+300; seed++ {
		src, ok := perturbUnused(pool, seed)
		if !ok {
			continue
		}
		_, ds := check.CheckSource(src)
		if len(ds) > 0 && ds[0].Code == check.CodeSyntax {
			t.Fatalf("seed %d: the perturbation does not parse: %v\n%s", seed, ds[0], src)
		}
		want := goUnused(ds)
		if len(want) == 0 {
			t.Fatalf("seed %d: the Go checker reports no unused name in the perturbation:\n%s", seed, src)
		}
		dir := filepath.Join(tmp, "seed", strconv.Itoa(int(seed)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, "p.ergo")
		writeLines(t, f, []string{strings.TrimSuffix(src, "\n")})
		cases = append(cases, unusedCase{name: fmt.Sprintf("seed %d", seed), files: []string{f}, want: prefixed("p.ergo", want), src: src, seed: seed})
		seeds++
	}
	// One binddump run over every case, a package per manifest line.
	var manifest []string
	for _, c := range cases {
		manifest = append(manifest, strings.Join(c.files, " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	got := make([][]string, len(cases))
	at := -1
	for _, l := range runDump(t, ctx, bindDump(t, ctx), "-std", stdDir(), "-unused", "-batch", m) {
		if strings.HasPrefix(l, "@pkg ") {
			at++
			continue
		}
		if at >= 0 && at < len(cases) {
			got[at] = append(got[at], l)
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("binddump -unused -batch wrote %d packages, want %d", at+1, len(cases))
	}
	failed, reported, dropped := 0, 0, 0
	for i, c := range cases {
		reported += len(c.want)
		var kept []string
		for _, l := range got[i] {
			if fs := strings.SplitN(l, " ", 3); len(fs) == 3 && c.exempt[fs[0]+" "+strings.TrimSuffix(fs[1], ":")] {
				dropped++
				continue
			}
			kept = append(kept, l)
		}
		got[i] = kept
		if strings.Join(c.want, "\n") == strings.Join(got[i], "\n") {
			continue
		}
		failed++
		if failed > 10 {
			continue
		}
		msg := fmt.Sprintf("%s: unused diagnostics differ\n--- go\n%s\n--- ergo\n%s", c.name, strings.Join(c.want, "\n"), strings.Join(got[i], "\n"))
		if c.src != "" {
			msg += fmt.Sprintf("\n--- source\n%s\nreplay: %s=%d go test -count=1 -run '^TestCompilerUnusedAgreesWithChecker$' ./internal/selfhostcheck (the first seed is the failing one)", c.src, seedVariable, c.seed)
		}
		t.Error(msg)
	}
	t.Logf("%d corpus folders, %d fixtures, %d seeds; %d Go lines compared; %d Ergo lines dropped as D3 (a local bound to an erroneous value, or a name of a refused pattern); %d cases differ", corpus, fixtures, seeds, reported, dropped, failed)
	if reported == 0 {
		t.Fatal("no unused diagnostic was compared")
	}
}

// prefixed is each line with "FILE " before it.
func prefixed(file string, ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = file + " " + l
	}
	return out
}

// bindDump builds compiler/cmd/binddump.
func bindDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "binddump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/binddump"); err != nil {
		t.Fatalf("ergo build ./cmd/binddump: %v\n%s", err, o)
	}
	return bin
}

// unusedFile is a corpus file that the Go checker accepts alone, with its
// function bodies' braces and its import block's end.
type unusedFile struct {
	src          string
	bodies       []int // the offset after each body's "{"
	blocks       []int // the offset after each nested block's "{"
	imports      []string
	afterImports int // where an import line goes: after the last import, else 0
}

// unusedPool is the corpus files that check alone with no diagnostic, in
// path order.
func unusedPool(t *testing.T) []unusedFile {
	t.Helper()
	var pool []unusedFile
	for _, dir := range corpusDirs(t) {
		for _, p := range ergoFiles(t, dir) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			if _, ds := check.CheckSource(src); len(ds) > 0 {
				continue
			}
			f, err := syntax.Parse(src)
			if err != nil {
				continue
			}
			u := unusedFile{src: src}
			for _, d := range f.Decls {
				if im, ok := d.(*syntax.ImportDecl); ok {
					for _, s := range im.Specs {
						u.imports = append(u.imports, strings.Trim(s.Path.Value, `"`))
					}
					u.afterImports = max(u.afterImports, int(im.End()))
				}
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				d, ok := n.(*syntax.FuncDecl)
				// A function a where derives has a body with no brace of
				// its own in the source.
				if !ok || d.Body == nil || int(d.Body.Lbrace) >= len(src) || src[d.Body.Lbrace] != '{' {
					return true
				}
				u.bodies = append(u.bodies, int(d.Body.Lbrace)+1)
				syntax.Inspect(d.Body, func(n syntax.Node) bool {
					if blk, ok := n.(*syntax.BlockStmt); ok && blk != d.Body && int(blk.Lbrace) < len(src) && src[blk.Lbrace] == '{' {
						u.blocks = append(u.blocks, int(blk.Lbrace)+1)
					}
					return true
				})
				return false
			})
			if len(u.bodies) > 0 {
				pool = append(pool, u)
			}
		}
	}
	return pool
}

// unusedImports are std packages a perturbation imports and does not
// read.
var unusedImports = []string{"strings", "slices", "maps", "strconv", "cmp", "math"}

// perturbUnused is seed's perturbation of a pool file: one of an unread
// let at a body's start, one at a nested block's start, a function
// literal holding an unread local and an unread parameter (the literal
// itself unread), an unread
// import by its path or under a name (with the package-level let that
// makes the name clash), an unread import with an unread let, an
// unread let of a match whose case binding is unread, or an unread let
// beside a refused alias; or an arm binding nothing reads: a match arm's
// plain or `as` binding, a catch arm's rest, the later arm of an
// or-pattern, or a capitalised name. False when the seed's form does not
// apply to its file.
func perturbUnused(pool []unusedFile, seed uint32) (string, bool) {
	r := rand.New(rand.NewPCG(uint64(seed), 0x5333))
	u := pool[r.IntN(len(pool))]
	name := fmt.Sprintf("zq%d", seed)
	insert := func(at int, text string) string { return u.src[:at] + text + u.src[at:] }
	free := func() string {
		var out []string
		for _, p := range unusedImports {
			if !contains(u.imports, p) {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return ""
		}
		sort.Strings(out)
		return out[r.IntN(len(out))]
	}
	// at is a body's start, for the forms that add a statement there.
	at := u.bodies[r.IntN(len(u.bodies))]
	switch seed % 13 {
	case 8:
		// A match arm's binding its arm never reads.
		return insert(at, "\n\tlet _ = match 0 {\n\tcase "+name+"b:\n\t\t1\n\t}\n"), true
	case 9:
		// An `as` name its arm never reads.
		return insert(at, "\n\tlet "+name+"o = Map[string, int]{}\n\tlet _ = match "+name+"o.Get(\"a\") {\n\tcase Some[int] as "+name+"q:\n\t\t1\n\tcase None:\n\t\t0\n\t}\n"), true
	case 10:
		// A catch arm's rest its arm never reads (with the import it
		// needs, added after the body text so the offset holds).
		out := insert(at, "\n\tlet _ = strconv.Atoi(\"1\") catch {\n\tcase "+name+"e:\n\t\t0\n\t}\n")
		if contains(u.imports, "strconv") {
			return out, true
		}
		if u.afterImports == 0 {
			return "import strconv\n\n" + out, true
		}
		return out[:u.afterImports] + "\nimport strconv" + out[u.afterImports:], true
	case 11:
		// An or-pattern: its name is read through the body, so no line;
		// a later arm's binding is unread.
		return insert(at, "\n\tlet _ = match (1, 2) {\n\tcase (0, "+name+"n), ("+name+"n, 0):\n\t\t"+name+"n\n\tcase ("+name+"a, _):\n\t\t0\n\t}\n"), true
	case 12:
		// A capitalised binding: no type has its name, so it binds (the hint).
		return insert(at, "\n\tlet _ = match 0 {\n\tcase Z"+name+"B:\n\t\t1\n\t}\n"), true
	case 7:
		// An alias with no name clash is refused (S108) and counts as read.
		p := free()
		if p == "" {
			return "", false
		}
		b := u.bodies[r.IntN(len(u.bodies))]
		return "import " + name + "i " + p + "\n\n" + u.src[:b] + "\n\tlet " + name + " = 0\n" + u.src[b:], true
	case 6:
		return insert(u.bodies[r.IntN(len(u.bodies))], "\n\tlet "+name+"m = match 0 {\n\tcase "+name+"b:\n\t\t1\n\t}\n"), true
	case 4:
		// An import under a name: the alias clashes with a package-level
		// let of the path's name (an alias with no clash is S108's).
		p := free()
		if p == "" {
			return "", false
		}
		return "import " + name + "i " + p + "\n\n" + u.src + "\nlet " + p + " = 0\n", true
	case 5:
		// An unread import before an unread let: the Go order is by offset.
		p := free()
		if p == "" {
			return "", false
		}
		b := u.bodies[r.IntN(len(u.bodies))]
		return "import " + p + "\n\n" + u.src[:b] + "\n\tlet " + name + " = 0\n" + u.src[b:], true
	case 0:
		return insert(u.bodies[r.IntN(len(u.bodies))], "\n\tlet "+name+" = 0\n"), true
	case 1:
		if len(u.blocks) == 0 {
			return "", false
		}
		return insert(u.blocks[r.IntN(len(u.blocks))], "\n\tlet "+name+" = 0\n"), true
	case 2:
		return insert(u.bodies[r.IntN(len(u.bodies))], "\n\tlet "+name+"f = func("+name+"p int) int {\n\t\tlet "+name+" = 1\n\t\t2\n\t}\n"), true
	default:
		p := free()
		if p == "" {
			return "", false
		}
		if u.afterImports == 0 {
			return "import " + p + "\n\n" + u.src, true
		}
		return insert(u.afterImports, "\nimport "+p), true
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
