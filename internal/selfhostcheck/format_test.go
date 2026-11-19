package selfhostcheck

import (
	"context"
	"errors"
	"fmt"
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

	"ergo.local/prototype/internal/hostsyntax"
	linked "ergo.local/prototype/internal/linked/compiler"

	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestSelfhostFormatLaws holds the formatter written in Ergo
// (compiler/format, linked as internal/linked/compiler, called through
// hostsyntax) to its laws on every input, in process. Since the Go
// formatter is gone (capstone S1c), TestFormatAgreesWithSeed compares the
// text with the seed's. For an input it formats, the laws stated in Ergo
// (compiler/format/laws.ergo) must hold: idempotence, the tree shape and
// the comment order kept, and the limit refused exactly above the size.
// The facade must keep them: limit 0 and a negative limit give the text,
// the size and the size plus one give the text, the size less one is a
// *hostsyntax.LimitError with no text. For an input it does not format, the
// failure is a located *syntax.Error with no text.
//
// Inputs: every examples/**/*.ergo and compiler/ .ergo file, the positive and
// superseded corpus, the formatter's fuzz seeds, the string literals of the
// formatter's own tests, tapered aligned columns, seeded generated grammar
// inputs (generate_test.go), a whitespace perturbation of each and seeded
// edits (invalid UTF-8, truncation, a random byte), and random bytes.
// Every node family of the parser must be reached by some input.
//
// Mutants it alone catches: testdata/mutants.json, killer
// selfhostcheck:TestSelfhostFormatLaws.
func TestSelfhostFormatLaws(t *testing.T) {
	seed := selfhostSeed(t)
	inputs := formatInputs(t, seed)
	if len(inputs) == 0 {
		t.Fatal("no inputs")
	}
	checkFamiliesReached(t, inputs)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestSelfhostFormatLaws$' ./internal/selfhostcheck"
	var lawCases [][]string
	var lawInputs []int
	failed, probes := 0, 0
	fail := func(in sketchtest.Input, limit int, got, want string) {
		failed++
		if failed > 3 {
			return
		}
		line, g, w := firstDiff(got, want)
		t.Errorf("seed %d: input %s, limit %d: the outcome is not the law's at line %d\n  got:  %q\n  want: %q\ninput: %q\n%s",
			seed, in.Name, limit, line, g, w, in.Src, replay)
	}
	for i, in := range inputs {
		out, err := hostsyntax.SourceBounded(in.Src, 0)
		base := outcome(out, err)
		probes++
		if err != nil {
			var se *syntax.Error
			if !errors.As(err, &se) || out != "" {
				fail(in, 0, base, "a located syntax failure with no text")
			}
			continue
		}
		// A source the formatter formats parses (a swallowed syntax failure
		// would read as an empty text).
		if _, perr := syntax.Parse(in.Src); perr != nil {
			fail(in, 0, base, outcome("", perr))
			continue
		}
		n := len(out)
		limits := map[int]string{-1 - i%3: base, n: base, n + 1: base}
		if n > 1 {
			limits[n-1] = fmt.Sprintf("limit %d\n", n-1)
		}
		for limit, want := range limits {
			probes++
			got, gerr := hostsyntax.SourceBounded(in.Src, limit)
			if o := outcome(got, gerr); o != want {
				fail(in, limit, o, want)
			}
		}
		lawCases = append(lawCases, []string{in.Src})
		lawInputs = append(lawInputs, i)
	}
	if len(lawCases) == 0 {
		t.Fatal("no input formats")
	}
	t.Logf("seed %d: %d inputs, %d formatted, %d probes", seed, len(inputs), len(lawCases), probes)
	if failed > 0 {
		t.Errorf("%d of %d probes break a law", failed, probes)
	}
	verdicts := runMode(t, "laws", lawCases)
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

// runMode answers every case in process: in mode "laws" a case is a
// source, and its answer the verdict of compiler/api's Laws, which parses
// the source and the Ergo formatter's text in Ergo; in mode "steps" a
// command list and a limit, and its answer compiler/api's Steps.
func runMode(t *testing.T, mode string, cases [][]string) [][]string {
	t.Helper()
	ctx := context.Background()
	out := make([][]string, len(cases))
	for i, c := range cases {
		switch mode {
		case "laws":
			v, err := linked.Laws(ctx, c[0])
			out[i] = []string{ergoAnswer(v, err)}
		case "steps":
			limit, err := strconv.Atoi(c[1])
			if err != nil {
				t.Fatal(err)
			}
			v, err := linked.Steps(ctx, c[0], limit)
			out[i] = []string{ergoAnswer(v, err)}
		default:
			t.Fatalf("unknown mode %q", mode)
		}
	}
	return out
}

// outcome is a formatter's result as text: "ok\n" and the text, the
// located syntax failure, the refused limit, or another error.
func outcome(text string, err error) string {
	if err == nil {
		return "ok\n" + text
	}
	if text != "" {
		return "partial " + strconv.Itoa(len(text)) + " " + err.Error() + "\n"
	}
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Sprintf("syntax %s %d:%s %d %d %d %d\n", se.Code, len(se.Message), se.Message, se.Offset, se.Length, se.Line, se.Column)
	}
	var le *hostsyntax.LimitError
	if errors.As(err, &le) {
		return fmt.Sprintf("limit %d\n", le.Limit)
	}
	return "go error " + err.Error() + "\n"
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

// formatInputs is the input domain of TestSelfhostFormatLaws and
// TestFormatAgreesWithSeed: the repository's sources and their edits
// (sourceInputs), the superseded corpus, the formatter's fuzz seeds and
// generated grammar inputs with their perturbations and edits.
func formatInputs(t *testing.T, seed uint64) []sketchtest.Input {
	t.Helper()
	inputs := sourceInputs(t, seed)
	_, superseded, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, superseded...)
	inputs = append(inputs, fuzzSeeds(t)...)
	inputs = append(inputs, formatterRows(t)...)
	inputs = append(inputs, alignInputs()...)
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
	dir := filepath.Join(repoRoot(), "internal", "hostsyntax", "testdata", "fuzz", "FuzzFormat")
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

// formatterRows are the string literals of the formatter's own tests
// (internal/hostsyntax, where the Go formatter's tests moved at S1c): its
// regression rows reach layout branches (a space before a comma, a comment
// between clauses, a one-line body) that well-formatted programs never
// take.
func formatterRows(t *testing.T) []sketchtest.Input {
	t.Helper()
	dir := filepath.Join(repoRoot(), "internal", "hostsyntax")
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []sketchtest.Input
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || s == "" {
				return true
			}
			out = append(out, sketchtest.Input{Name: "rows/" + filepath.Base(file) + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line), Src: s})
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no string literals in %s", dir)
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
