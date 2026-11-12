package selfhostcheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestSelfhostFormatAgrees is SH1's acceptance: the formatter rewritten in
// Ergo (testdata/selfhost/format) gives what the Go formatter gives. For
// every input and limit, the driver calls format.SourceBounded and the
// Ergo library's Format and both outcomes must be equal: the same bytes,
// or the same located syntax failure (code, message, offset, length, line,
// column), or the same refused limit; neither returns partial text. For
// every input the Go side formats, the formatter's laws, stated in Ergo
// (format/laws.ergo), must hold: idempotence, the tree shape and the
// comment order kept, and the limit refused exactly above the size.
//
// Inputs: every examples/**/*.ergo and pilot .ergo file, the positive and
// superseded corpus, the formatter's fuzz seeds, seeded generated grammar
// inputs (generate_test.go), a whitespace perturbation of each and seeded
// edits (invalid UTF-8, truncation, a random byte), and random bytes.
// Limits: 0 and a negative one for every input, and the size less one,
// the size and the size plus one where Go formats it. Every node family of
// the parser must be reached by some input.
//
// Mutants it alone catches: testdata/mutants.json, killer
// selfhostcheck:TestSelfhostFormatAgrees.
//
// It builds Go because the property executes the Ergo formatter through
// its exported Go ABI: only a built library runs the rewritten algorithm.
func TestSelfhostFormatAgrees(t *testing.T) {
	needsBuild(t)
	seed := selfhostSeed(t)
	inputs := formatInputs(t, seed)
	if len(inputs) == 0 {
		t.Fatal("no inputs")
	}
	checkFamiliesReached(t, inputs)
	type probe struct {
		in    int
		limit int
	}
	var probes []probe
	var lawCases [][]string
	var lawInputs []int
	for i, in := range inputs {
		probes = append(probes, probe{i, 0}, probe{i, -1 - i%3})
		out, err := format.SourceBounded(in.Src, 0)
		if err != nil {
			continue
		}
		n := len(out)
		probes = append(probes, probe{i, n}, probe{i, n + 1})
		if n > 1 {
			probes = append(probes, probe{i, n - 1})
		}
		lawCases = append(lawCases, []string{in.Src})
		lawInputs = append(lawInputs, i)
	}
	if len(lawCases) == 0 {
		t.Fatal("no input formats")
	}
	cases := make([][]string, len(probes))
	for k, p := range probes {
		cases[k] = []string{inputs[p.in].Src, strconv.Itoa(p.limit)}
	}
	exe := buildDriver(t, "formatapi")
	answers := runMode(t, exe, "format", cases, 2)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestSelfhostFormatAgrees$' ./internal/selfhostcheck"
	failed := 0
	for k, a := range answers {
		if a[0] == a[1] {
			continue
		}
		failed++
		if failed > 3 {
			continue
		}
		p := probes[k]
		line, g, e := firstDiff(a[0], a[1])
		t.Errorf("seed %d: input %s, limit %d: the outcomes differ at line %d\n  go:   %q\n  ergo: %q\ninput: %q\n--- go\n%s--- ergo\n%s%s",
			seed, inputs[p.in].Name, p.limit, line, g, e, inputs[p.in].Src, clip(a[0]), clip(a[1]), replay)
	}
	if failed > 0 {
		t.Errorf("%d of %d probes differ", failed, len(answers))
	}
	verdicts := runMode(t, exe, "laws", lawCases, 1)
	broken := 0
	for k, v := range verdicts {
		if v[0] == "ok\n" {
			continue
		}
		broken++
		if broken > 3 {
			continue
		}
		in := inputs[lawInputs[k]]
		t.Errorf("seed %d: input %s: a law of the Ergo formatter fails:\n%s\ninput: %q\n%s", seed, in.Name, clip(v[0]), in.Src, replay)
	}
	if broken > 0 {
		t.Errorf("laws fail on %d of %d inputs", broken, len(verdicts))
	}
}

// selfhostSeed is the seed of the generated inputs; the default is
// deterministic.
func selfhostSeed(t *testing.T) uint64 {
	t.Helper()
	s := os.Getenv(seedVariable)
	if s == "" {
		return 1
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("%s=%q: %v", seedVariable, s, err)
	}
	return n
}

// formatInputs is the input domain of TestSelfhostFormatAgrees: the
// bridge's domain, the superseded corpus, the formatter's fuzz seeds and
// generated grammar inputs with their perturbations and edits.
func formatInputs(t *testing.T, seed uint64) []sketchtest.Input {
	t.Helper()
	inputs := bridgeInputs(t, seed)
	_, superseded, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, superseded...)
	inputs = append(inputs, fuzzSeeds(t)...)
	r := rand.New(rand.NewPCG(seed, 0x6f72))
	for i := range generatedInputs {
		src := generate(r)
		name := "generated/" + strconv.Itoa(i)
		inputs = append(inputs, sketchtest.Input{Name: name, Src: src})
		if p, ok := sketchtest.Perturb(src, seed+uint64(i)); ok {
			inputs = append(inputs, sketchtest.Input{Name: name + "/perturbed", Src: p})
		}
		inputs = append(inputs, sketchtest.Input{Name: name + "/edited", Src: edit(r, src)})
	}
	return inputs
}

// generatedInputs is the number of generated grammar inputs per seed.
const generatedInputs = 400

// fuzzSeeds are the formatter's checked-in fuzz inputs.
func fuzzSeeds(t *testing.T) []sketchtest.Input {
	t.Helper()
	dir := filepath.Join(repoRoot(), "internal", "sketch", "format", "testdata", "fuzz", "FuzzFormat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []sketchtest.Input
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if q, ok := strings.CutPrefix(line, "string("); ok {
				s, err := strconv.Unquote(strings.TrimSuffix(q, ")"))
				if err != nil {
					t.Fatalf("fuzz seed %s: %v", e.Name(), err)
				}
				out = append(out, sketchtest.Input{Name: "fuzz/" + e.Name(), Src: s})
			}
		}
	}
	return out
}

// checkFamiliesReached fails when a node type of the parser (a type of
// package syntax with a Pos method) occurs in no parsed input, so a new
// construct cannot go untested.
func checkFamiliesReached(t *testing.T, inputs []sketchtest.Input) {
	t.Helper()
	want := syntaxFamilies(t)
	seen := map[string]bool{}
	for _, in := range inputs {
		f, err := syntax.Parse(in.Src)
		if err != nil {
			var se *syntax.Error
			if !errors.As(err, &se) {
				t.Fatalf("input %s: %v", in.Name, err)
			}
			continue
		}
		reachTypes(reflect.ValueOf(f), seen)
	}
	var missing []string
	for name := range want {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("no input reaches the syntax node types %s: add them to generate_test.go", strings.Join(missing, ", "))
	}
}

// syntaxFamilies reads the parser's node types from its source: every type
// with a Pos method.
func syntaxFamilies(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(repoRoot(), "internal", "sketch", "syntax")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || fd.Name.Name != "Pos" {
					continue
				}
				if star, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no node types found in the syntax package")
	}
	return out
}

// reachTypes records the struct type of every node reached from v.
func reachTypes(v reflect.Value, seen map[string]bool) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		if v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Struct {
			seen[v.Elem().Type().Name()] = true
		}
		reachTypes(v.Elem(), seen)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				reachTypes(v.Field(i), seen)
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			reachTypes(v.Index(i), seen)
		}
	}
}
