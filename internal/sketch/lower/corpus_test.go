package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A program that checks clean builds (stage C round 3 review M1, the
// structural guard; round 4 review T1 to T3). The check package's tests
// hold the checker's corpus inline, as Go strings. This test reads them
// from ../check/*_test.go with go/parser, so a new check test joins the
// corpus with no list to keep.
//
// The enumeration rule (checkCorpus): in each test file, a named string is
// a const or var whose value is a string literal, or a sum of literals and
// named strings, at package level or in a function. The candidates are
// every named string, every string literal anywhere in the file (case
// tables, struct fields, call arguments, package-level declarations), and
// each literal after each named string of its file (the tests' decls
// prefixes). Only strings with a top-level func, type or let are kept.
//
// Every candidate that parses and checks clean is lowered and built with
// go vet and go build, in one Go module. A lowering panic or error fails,
// and so does any refusal that is not a documented deferred family whose
// owner is a later stage (deferredOwners; refusalReasons and
// refusedFamilies of TestAcceptedNodesLower). An internal compiler error
// always fails.
//
// Bounds: at stage C the enumerated corpus is about 2,600 candidates;
// about 550 check clean and lower, and one go command vets and builds them
// in about ten seconds. checkCorpus counts the candidates first; past
// maxCandidates it builds only every k-th candidate, in a stable order,
// so neither enumeration nor checking and lowering grow past the cap, and
// the test says so (round 4 review T3, round 5 residual).
// The .ergo fixtures under ../check/testdata are lowered and built by
// TestAcceptedNodesLower.
//
// The test runs in parts (corpusSplit): each part lowers every corpusParts-th
// candidate and builds its clean programs in a module of its own.
// TestCheckCorpusBuildsDomain checks the corpus reader.
const maxCandidates = 8000

// corpusCandidate is one candidate of the corpus, with its index in it.
type corpusCandidate struct {
	index int
	src   string
}

// corpusDomain is the corpus, read once per test process.
var corpusDomain = sync.OnceValues(func() ([]string, error) {
	sources, _, _, _, err := readCorpus("../check", maxCandidates)
	return sources, err
})

// corpusSplit's domain is read from ../check's test files, which other
// units edit, so it is not pinned; its parts cover it.
var corpusSplit = newSplit(split[corpusCandidate]{
	name:  "TestCheckCorpusBuilds",
	parts: corpusParts,
	cases: func(yield func(corpusCandidate) bool) {
		sources, err := corpusDomain()
		if err != nil {
			return // TestCheckCorpusBuildsDomain reports it; a part then builds too few programs
		}
		for i, src := range sources {
			if !yield(corpusCandidate{i, src}) {
				return
			}
		}
	},
	key: func(c corpusCandidate) string { return fmt.Sprintf("c%d\n%s", c.index, c.src) },
	run: runCorpus,
})

// corpusParts: building the 707 clean programs took 63 s in one go command
// at load 40 (measured at f79be34b), so a part builds for about 6 s.
const corpusParts = 12

func TestCheckCorpusBuildsPart0(t *testing.T)  { corpusSplit.Part(t, 0) }
func TestCheckCorpusBuildsPart1(t *testing.T)  { corpusSplit.Part(t, 1) }
func TestCheckCorpusBuildsPart2(t *testing.T)  { corpusSplit.Part(t, 2) }
func TestCheckCorpusBuildsPart3(t *testing.T)  { corpusSplit.Part(t, 3) }
func TestCheckCorpusBuildsPart4(t *testing.T)  { corpusSplit.Part(t, 4) }
func TestCheckCorpusBuildsPart5(t *testing.T)  { corpusSplit.Part(t, 5) }
func TestCheckCorpusBuildsPart6(t *testing.T)  { corpusSplit.Part(t, 6) }
func TestCheckCorpusBuildsPart7(t *testing.T)  { corpusSplit.Part(t, 7) }
func TestCheckCorpusBuildsPart8(t *testing.T)  { corpusSplit.Part(t, 8) }
func TestCheckCorpusBuildsPart9(t *testing.T)  { corpusSplit.Part(t, 9) }
func TestCheckCorpusBuildsPart10(t *testing.T) { corpusSplit.Part(t, 10) }
func TestCheckCorpusBuildsPart11(t *testing.T) { corpusSplit.Part(t, 11) }

// TestCheckCorpusBuildsDomain: the corpus reader finds the check tests.
func TestCheckCorpusBuildsDomain(t *testing.T) {
	sources, files, total, k := checkCorpus(t, "../check", maxCandidates)
	if files < 30 || len(sources) < 200 {
		t.Fatalf("the corpus has %d candidates from %d files; the check tests moved?", len(sources), files)
	}
	if k > 1 {
		t.Logf("sampled every %dth of %d candidates", k, total)
	}
	t.Logf("candidates %d from %d files", len(sources), files)
}

// TestCorpusCompilesWithGc is gc's side of typecheckGo (test-speed plan
// T1): the parts of TestCheckCorpusBuilds and TestAcceptedNodesLower check
// their compile-only programs with go/types in process; this test compiles
// every clean corpus program and every accepted-node program without an
// oracle with gc, in one go command (no vet, no link), so a program that
// go/types accepts and gc refuses still fails the full tier.
//
// It builds Go because its question is gc's verdict.
func TestCorpusCompilesWithGc(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	sources, err := corpusDomain()
	if err != nil {
		t.Fatal(err)
	}
	var programs []*lowered
	clean := func(o *lowered) bool {
		return o.file != nil && len(o.diags) == 0 && o.panicked == nil && o.err == nil && len(o.refused) == 0
	}
	for i, src := range sources {
		if o := lowerSource(fmt.Sprintf("c%d", i), src, false); clean(o) {
			programs = append(programs, o)
		}
	}
	corpus := len(programs)
	accepted, err := acceptedSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range accepted {
		if s.want != "" {
			continue // built, linked and run by TestAcceptedNodesLower
		}
		if o := lowerSource(s.name, s.src, false); clean(o) {
			programs = append(programs, o)
		}
	}
	if corpus < 100 || len(programs) == corpus {
		t.Fatalf("%d corpus and %d accepted-node programs: a reader lost its domain?", corpus, len(programs)-corpus)
	}
	if err := unparsedGo(programs); err != nil {
		t.Fatal(err)
	}
	rejected, output := gcRejects(t, programs)
	if len(rejected) > 0 {
		t.Errorf("gc rejects %d programs:\n%s", len(rejected), firstLines(output, 60))
		for _, p := range programs {
			if rejected[p.name] {
				t.Logf("source of %s:\n%s\nGo:\n%s", p.name, p.src, p.mainGo())
				break
			}
		}
	}
	t.Logf("compiled %d corpus and %d accepted-node programs with gc", corpus, len(programs)-corpus)
	if len(rejected) > 0 {
		return
	}
	// go vet runs here once, on the whole corpus, in place of a vet per
	// test (test-speed plan T2: buildAll links without vet).
	m := newGoModuleDir(t)
	for _, p := range programs {
		m.program(p, p.mainGo())
	}
	if out, ok := m.goCommand(vetArgs...); !ok {
		m.report("vet", out)
	}
}

// runCorpus lowers the candidates of one part and builds its clean
// programs.
func runCorpus(t *testing.T, cases iter.Seq[corpusCandidate]) {
	var programs []*lowered
	rejected, deferred, candidates := 0, map[string]int{}, 0
	for c := range cases {
		candidates++
		src := c.src
		o := lowerSource(fmt.Sprintf("c%d", c.index), src, false)
		switch {
		case o.file == nil || len(o.diags) > 0:
			rejected++
		case o.panicked != nil:
			t.Errorf("a clean program's lowering panicked: %v\n%s", o.panicked, src)
		case o.err != nil:
			t.Errorf("a clean program does not lower: %v\n%s", o.err, src)
		case len(o.refused) > 0:
			for _, r := range o.refused {
				if why := undeferred(r.Message); why != "" {
					t.Errorf("%d:%d: %s: %s\n%s", r.Line, r.Column, why, r.Message, src)
					continue
				}
				f := refusalFamily(r.Message)
				deferred[f+" ("+refusedFamilies[f]+")"]++
			}
		default:
			programs = append(programs, o)
		}
	}
	// The unsplit test asked for 100 clean programs of the corpus; a
	// part holds every corpusParts-th candidate.
	if floor := 100 / corpusParts; len(programs) < floor {
		t.Fatalf("only %d clean programs lowered, want %d in a part; the corpus reader lost the check tests?", len(programs), floor)
	}
	var owners []string
	for o, n := range deferred {
		owners = append(owners, fmt.Sprintf("%s: %d", o, n))
	}
	sort.Strings(owners)
	t.Logf("candidates %d: rejected by the parser or checker %d; deferred refusals by family [%s]; built %d (every clean program of the part that lowers)", candidates, rejected, strings.Join(owners, ", "), len(programs))
	buildAll(t, programs, false)
}

// deferredOwners are the open owners a refusal may name: the forms they
// own are not lowered yet. They are the open set of
// TestRefusalsNameOpenOwners; a refusal owned by a stage or unit that has
// landed (check-v1, lower-v1) is a lowering gap.
var deferredOwners = func() map[string]bool {
	m := map[string]bool{}
	for _, o := range openOwners {
		m[o] = true
	}
	return m
}()

// undeferred says why a refusal is not a documented deferral, or "".
func undeferred(message string) string {
	if strings.Contains(message, "internal compiler error") {
		return "an internal compiler error"
	}
	family := refusalFamily(message)
	if family == "" {
		return "an unclassified refusal (add it to refusalReasons with a refused family, or lower the form)"
	}
	owner := refusedFamilies[family]
	switch {
	case owner == "" || !strings.Contains(message, owner):
		return fmt.Sprintf("the refusal stands at %s, documented as owned by %q, and names another owner", family, owner)
	case !deferredOwners[owner]:
		return fmt.Sprintf("a refusal of %s, owned by %s, which has landed", family, owner)
	}
	return ""
}

// corpusFile is one test file's named strings and source literals.
type corpusFile struct{ named, lits []string }

// candidates is the number of candidates the file makes: each named
// string, each literal, and each literal after each named string.
func (f corpusFile) candidates() int {
	return len(f.named) + len(f.lits)*(1+len(f.named))
}

// candidate builds the file's i-th candidate, in the order: the named
// strings, then each literal followed by its prefixed forms. A prefixed
// form equal to its literal is "" (skipped).
func (f corpusFile) candidate(i int) string {
	if i < len(f.named) {
		return f.named[i]
	}
	i -= len(f.named)
	lit, j := f.lits[i/(1+len(f.named))], i%(1+len(f.named))
	if j == 0 {
		return lit
	}
	if pre := f.named[j-1]; pre != lit {
		return pre + lit
	}
	return ""
}

// checkCorpus returns the distinct candidate sources in dir's test files
// by the enumeration rule of TestCheckCorpusBuilds, in a stable order, the
// number of files read, the number of candidates the rule makes, and the
// stride k. It reads each file's named strings and literals, which grow
// with the files' text, then counts the candidates without building them;
// past limit it builds only every k-th, so the work and the memory for
// candidates stay bounded by limit however many prefixes the files hold.
func checkCorpus(t *testing.T, dir string, limit int) (out []string, files, total, k int) {
	t.Helper()
	out, files, total, k, err := readCorpus(dir, limit)
	if err != nil {
		t.Fatal(err)
	}
	return out, files, total, k
}

// readCorpus is checkCorpus, with an error.
func readCorpus(dir string, limit int) (out []string, files, total, k int, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	sort.Strings(paths)
	var cfs []corpusFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, data, parser.SkipObjectResolution)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		cf := corpusFile{named: namedStrings(f)}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && looksLikeSource(s) {
					cf.lits = append(cf.lits, s)
				}
			}
			return true
		})
		cfs = append(cfs, cf)
		total += cf.candidates()
	}
	k = 1
	if total > limit {
		k = (total + limit - 1) / limit
	}
	seen := map[string]bool{}
	index := 0
	for _, cf := range cfs {
		n := cf.candidates()
		// The first index of this file that the stride selects.
		first := (k - index%k) % k
		for i := first; i < n; i += k {
			src := cf.candidate(i)
			if !looksLikeSource(src) {
				continue
			}
			if !strings.HasSuffix(src, "\n") {
				src += "\n"
			}
			if !seen[src] {
				seen[src] = true
				out = append(out, src)
			}
		}
		index += n
	}
	return out, len(paths), total, k, nil
}

// namedStrings returns the values of the file's string consts and vars,
// at package level and in functions, whose values are literals or sums of
// literals and earlier named strings, in source order.
func namedStrings(f *ast.File) []string {
	known := map[string]string{}
	var out []string
	var eval func(e ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		case *ast.ParenExpr:
			return eval(e.X)
		case *ast.Ident:
			s, ok := known[e.Name]
			return s, ok
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return "", false
			}
			a, ok1 := eval(e.X)
			b, ok2 := eval(e.Y)
			return a + b, ok1 && ok2
		}
		return "", false
	}
	record := func(name string, value ast.Expr) {
		if s, ok := eval(value); ok && looksLikeSource(s) {
			known[name] = s
			out = append(out, s)
		}
	}
	ast.Inspect(f, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.GenDecl:
			if x.Tok != token.CONST && x.Tok != token.VAR {
				return true
			}
			for _, spec := range x.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						record(name.Name, vs.Values[i])
					}
				}
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE && len(x.Lhs) == len(x.Rhs) {
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						record(id.Name, x.Rhs[i])
					}
				}
			}
		}
		return true
	})
	return out
}

// looksLikeSource keeps the strings that may be Ergo programs: those with
// a top-level declaration keyword.
func looksLikeSource(s string) bool {
	for _, kw := range []string{"func ", "type ", "let "} {
		if strings.HasPrefix(s, kw) || strings.Contains(s, "\n"+kw) {
			return true
		}
	}
	return false
}
