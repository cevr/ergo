package stdlib_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/api"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdCopyAgrees is the copy property of the rule "the standard library
// gets no special treatment" (NORTH_STAR.md, Owner rules; docs/spec/stdlib.md,
// Std is ordinary): a std package P copied, file for file, into a user
// package copy/P checks with the same result, has the same exported
// surface, and lowers to the same Go for the same callers, after a
// normalisation that folds only how std is shipped and named (copyNormalise).
//
// For each std package, and for copyFixture (a std package the embedded
// library does not have, checked as std through check.StdFixture, so the
// property has a witness whatever the library holds):
//
//  1. check: the copy has no diagnostic;
//  2. surface: the checker's summary of the exported functions (api.Text),
//     types and laws is the same;
//  3. lower: for seeded caller programs (a main that takes each exported
//     function of P as a value, generic ones at int64 or string, in a
//     drawn order and subset), the lowered Go of the program against P and
//     against copy/P is equal after copyNormalise. Equal Go is equal work,
//     so this is the counted-speed half too, in process, with no build.
//
// Every std package's copy is the same: one that differs fails with its
// stage and first difference (std has no privileges; the baseline of
// packages that differed went with STD-COPY-ZERO).
//
// ERGO_STD_COPY_SEED=<n> runs only seed n; ERGO_STD_COPY_PKG=<path> only
// one package. A failing seed prints the program, both first differing
// lines and the replay command.
func TestStdCopyAgrees(t *testing.T) {
	copyNormaliseKeepsMeaning(t)
	seeds := []uint64{0, 1, 2}
	if s := os.Getenv("ERGO_STD_COPY_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_STD_COPY_SEED=%q: %v", s, err)
		}
		seeds = []uint64{n}
	}
	only := os.Getenv("ERGO_STD_COPY_PKG")
	for _, path := range append([]string{copyFixturePath}, stdlib.Packages()...) {
		if only != "" && path != only {
			continue
		}
		if v := copyVerdict(t, path, seeds); !v.same() {
			t.Errorf("std package %s: its copy %s: a std package must be ordinary", path, v)
		}
	}
}

// copyNormaliseKeepsMeaning checks the normaliser itself: what it must fold
// (directives, the call table, the context namespace, positions and files,
// the order of declarations, a uniqueness suffix in a name, which of two
// declarations of one text a body names) normalises equal, and one planted
// difference of each kind it must keep (a callee, a type, a hidden
// argument, an allocation, a rewritten operand, a file, a variant name, a
// suffixed variant name, a value) normalises different. A
// normaliser that folds too much would let a privilege through.
func copyNormaliseKeepsMeaning(t *testing.T) {
	t.Helper()
	const base = `package main

//line /x/std/copyfixture/fixture.ergo:3:1
func f(env *Env, p Pair) int64 {
	q := &Pair{Left: p.Left}
	if q.Left < 0 {
		panic("no case of the match at std/copyfixture/fixture.ergo:4:2 matched")
	}
	return g(env, q.Left, "std:copyfixture")
}

func g(env *Env, n int64, ns string) int64 { return n }

func h(env *Env, n int64, ns string) int64 { return n }

type Pair struct{ Left int64 }

func (x Pair) ErgoVariant() string { return [...]string{"Pair"}[0] }

type Site struct{ File string }

var site = Site{File: "std/copyfixture/fixture.ergo"}

var called = "f calls copyfixture.g at 4:9"

const none int64 = -1

const none_1 int64 = -1

func k() int64 { return none }

type ergoCall = struct{ Func string }

var ergoCalls = [...]ergoCall{{Func: "f"}}
`
	folded := map[string][2]string{
		"directive":   {"//line /x/std/copyfixture/fixture.ergo:3:1\n", "//line /ergo-copy/std/copyfixture/fixture.ergo:9:1\n"},
		"call table":  {`{{Func: "f"}}`, `{{Func: "f"}, {Func: "g"}}`},
		"namespace":   {`"std:copyfixture"`, `"copy/copyfixture"`},
		"callee path": {"calls copyfixture.g", "calls copy/copyfixture.g"},
		"position":    {"at std/copyfixture/fixture.ergo:4:2", "at /ergo-copy/std/copyfixture/fixture.ergo:4:2"},
		"file":        {`File: "std/copyfixture/fixture.ergo"`, `File: "/ergo-copy/std/copyfixture/fixture.ergo"`},
		"order":       {"type Pair struct{ Left int64 }\n", ""},
		"suffix":      {"Pair", "Pair_1"},
		"same text":   {"return none }", "return none_1 }"},
	}
	kept := map[string][2]string{
		"callee":         {"return g(", "return h("},
		"type":           {"n int64, ns string) int64 { return n }\n\nfunc h", "n int32, ns string) int64 { return n }\n\nfunc h"},
		"hidden":         {"g(env, q.Left", "g(nil, q.Left"},
		"allocation":     {"q := &Pair{", "q := Pair{"},
		"rewrite":        {"return g(env, q.Left", "return g(env, p.Left"},
		"file":           {`File: "std/copyfixture/fixture.ergo"`, `File: "std/copyfixture/other.ergo"`},
		"variant name":   {`[...]string{"Pair"}`, `[...]string{"Left"}`},
		"variant suffix": {`[...]string{"Pair"}`, `[...]string{"Pair_1"}`},
		"value":          {"const none int64 = -1", "const none int64 = -2"},
		"callee package": {"calls copyfixture.g", "calls other.g"},
		"callee name":    {"calls copyfixture.g", "calls copyfixture.h"},
	}
	want := copyNormalise(base, copyFixturePath, false)
	for kind, e := range folded {
		text := strings.Replace(base, e[0], e[1], 1)
		copied := kind == "namespace" || kind == "callee path"
		switch kind {
		case "order":
			text += e[0]
		case "suffix":
			// The Go names: the variant's name stays its Ergo name.
			text = strings.ReplaceAll(base, e[0], e[1])
			text = strings.Replace(text, `{"Pair_1"}`, `{"Pair"}`, 1)
		case "callee path":
			// A copy's text: its namespace is the copy's too.
			text = strings.Replace(text, `"std:copyfixture"`, `"copy/copyfixture"`, 1)
		}
		if text == base {
			t.Fatalf("copyNormalise self-test: the %s plant does not apply", kind)
		}
		if got := copyNormalise(text, copyFixturePath, copied); got != want {
			x, y := firstDiff(want, got)
			t.Errorf("copyNormalise keeps a %s difference it must fold: %q, %q", kind, x, y)
		}
	}
	for kind, e := range kept {
		text := strings.Replace(base, e[0], e[1], 1)
		if text == base {
			t.Fatalf("copyNormalise self-test: the %s plant does not apply", kind)
		}
		if copyNormalise(text, copyFixturePath, false) == want {
			t.Errorf("copyNormalise folds a planted %s difference: it would let a privilege through", kind)
		}
	}
}

// copyFixturePath is the std package path copyFixture is checked under.
const copyFixturePath = "copyfixture"

// copyFixture is a small std package of records and functions, a generic
// one among them: one the property holds same whatever the embedded
// library holds, so a lowering keyed on membership is caught.
const copyFixture = `type Pair { Left int64, Right int64 }

func Sum(p Pair) int64 { p.Left + p.Right }

func Swap(p Pair) Pair { Pair{Left: p.Right, Right: p.Left} }

type Counter {
	Add(n int64) Counter,
	count() int64,
}

func Twice(c Counter) int64 { c.Add(1).Add(1).count() }

func First[T](xs []T, d T) T {
	if len(xs) == 0 {
		return d
	}
	return xs[0]
}

func Label(n int64) string {
	if n < 0 {
		return "negative"
	}
	return "n=\(n)"
}
`

// copyVerdictT is a package's measured verdict.
type copyVerdictT struct {
	stage  string // "" when same; check, surface or lower
	reason string
	detail string // a lowering's program, first differing lines and replay command
}

func (v copyVerdictT) same() bool { return v.stage == "" }

func (v copyVerdictT) String() string {
	if v.same() {
		return "is the same"
	}
	s := "differs at " + v.stage + ": " + v.reason
	if v.detail != "" {
		s += "\n" + v.detail
	}
	return s
}

// copySource is one std file of a package under test.
type copySource struct{ name, text string }

// copySources are the files of the std package at path, by name.
func copySources(t *testing.T, path string) []copySource {
	t.Helper()
	if path == copyFixturePath {
		return []copySource{{"fixture.ergo", copyFixture}}
	}
	srcs, ok := stdlib.Package(path)
	if !ok {
		t.Fatalf("std.Package(%q): not embedded", path)
	}
	var out []copySource
	for _, name := range slices.Sorted(mapsKeys(srcs)) {
		out = append(out, copySource{name, srcs[name]})
	}
	return out
}

// copyPair is a std package and its copy.
type copyPair struct {
	path        string
	std, copied *check.Package
	unit        lower.Unit // the copy's unit
	diags       []check.Diagnostic
}

// copyRoot is the directory of a copy's files: their position text is
// the std file's, under it.
const copyRoot = "/ergo-copy/"

// checkCopy checks the std package at path and its copy, copy/<path>.
func checkCopy(t *testing.T, path string) copyPair {
	t.Helper()
	return checkCopyEdited(t, path, nil)
}

// checkCopyEdited is checkCopy with edit, when not nil, applied to the
// text of each of the copy's files (the std package stays as it ships).
func checkCopyEdited(t *testing.T, path string, edit func(name, text string) string) copyPair {
	t.Helper()
	srcs := copySources(t, path)
	parse := func(prefix string) ([]*syntax.File, []lower.Source) {
		var files []*syntax.File
		var sources []lower.Source
		for _, s := range srcs {
			f, err := syntax.Parse(s.text)
			if err != nil {
				t.Fatalf("%s/%s: %v", path, s.name, err)
			}
			f.Name = prefix + s.name
			files = append(files, f)
			sources = append(sources, lower.Source{Path: f.Name, Text: s.text})
		}
		return files, sources
	}
	pair := copyPair{path: path}
	if path == copyFixturePath {
		files, _ := parse("")
		p, ds := check.StdFixture(path, files)
		if len(ds) > 0 {
			t.Fatalf("the std fixture %s: %v", path, ds)
		}
		pair.std = p
	} else {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		pair.std = p
	}
	if edit != nil {
		edited := make([]copySource, len(srcs))
		for i, s := range srcs {
			edited[i] = copySource{s.name, edit(s.name, s.text)}
		}
		srcs = edited
	}
	files, sources := parse(copyRoot + "std/" + path + "/")
	var under *check.Package
	if base, ok := check.TestedPath(path); ok {
		if p, err := check.Std.Import(base); err == nil {
			under = p
		}
	}
	pair.copied, pair.diags = check.CheckTest("copy/"+path, files, copyTrust{}, under)
	pair.unit = lower.Unit{Package: pair.copied, Files: files, Sources: sources}
	return pair
}

// copyVerdict runs the three stages for the package at path.
func copyVerdict(t *testing.T, path string, seeds []uint64) copyVerdictT {
	t.Helper()
	pair := checkCopy(t, path)
	if len(pair.diags) > 0 {
		codes := map[string]int{}
		for _, d := range pair.diags {
			codes[d.Code]++
		}
		d := pair.diags[0]
		return copyVerdictT{stage: "check", reason: fmt.Sprintf("%d diagnostics, first %s %s", len(pair.diags), d.Code, firstLine(d.Message))}
	}
	// A copy site's position names the copy's file.
	if a, b := copySurface(pair.std), strings.ReplaceAll(copySurface(pair.copied), copyRoot+"std/"+path+"/", ""); a != b {
		x, y := firstDiff(a, b)
		return copyVerdictT{stage: "surface", reason: fmt.Sprintf("std %q, copy %q", x, y)}
	}
	refs := copyRefs(t, pair)
	if len(refs) == 0 {
		return copyVerdictT{stage: "lower", reason: "no exported function a caller can take as a value"}
	}
	for _, seed := range seeds {
		src := copyProgram(pair, refs, seed)
		a := copyLower(t, pair, src, false)
		b := copyLower(t, pair, src, true)
		if a != b {
			x, y := firstDiff(a, b)
			detail := fmt.Sprintf("seed %d: the lowered Go differs.\nprogram:\n%s\nstd:  %s\ncopy: %s\nreplay: ERGO_STD_COPY_PKG=%s ERGO_STD_COPY_SEED=%d go test -run 'TestStdCopyAgrees$' ./internal/sketch/std", seed, src, x, y, path, seed)
			return copyVerdictT{stage: "lower", reason: fmt.Sprintf("std %q, copy %q", strings.TrimSpace(x), strings.TrimSpace(y)), detail: detail}
		}
	}
	return copyVerdictT{}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// firstDiff is the first line where a and b differ.
func firstDiff(a, b string) (string, string) {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(x) || i < len(y); i++ {
		var l, r string
		if i < len(x) {
			l = x[i]
		}
		if i < len(y) {
			r = y[i]
		}
		if l != r {
			return l, r
		}
	}
	return "", ""
}

// copySurface is the checker's summary of p's exported declarations: its
// functions (copyAPIText, api.Text's view, which moved to std code's
// ApiText in SEM2a), its types with their shapes, and its laws.
func copySurface(p *check.Package) string {
	var b strings.Builder
	b.WriteString(copyAPIText(api.Functions(p)))
	var types []string
	for _, tn := range p.Types {
		if !isExportedName(tn.Name) {
			continue
		}
		shape := ""
		if n, ok := tn.Type.(*check.Named); ok && n.Underlying() != nil {
			shape = n.Underlying().String()
		}
		laws := []string{}
		for _, l := range tn.Laws {
			laws = append(laws, l.Name)
		}
		types = append(types, fmt.Sprintf("type %s %s laws %v", tn.Name, shape, laws))
	}
	sort.Strings(types)
	for _, s := range types {
		b.WriteString(s + "\n")
	}
	for _, l := range p.Laws {
		b.WriteString("law " + l.Name + "\n")
	}
	// A row is a set: its members print in the order of their packages'
	// paths, and copy/P sorts after io where P does not.
	return copyRow.ReplaceAllStringFunc(b.String(), func(m string) string {
		word, members, _ := strings.Cut(m, " ")
		parts := strings.Split(members, " | ")
		sort.Strings(parts)
		return word + " " + strings.Join(parts, " | ")
	})
}

// copyRow matches a fails or needs row of a printed signature.
var copyRow = regexp.MustCompile(`\b(?:fails|needs) [^ |\n]+(?: \| [^ |\n]+)*`)

// copyRefs are the references a caller can take of the package's exported
// functions: each is checked alone, against std, and kept when it checks.
// A function no caller can take as a value (an extern, which the checker
// has called directly, or a generic over rows, which only a call infers)
// is taken through a declared function that calls it (copyCall).
func copyRefs(t *testing.T, pair copyPair) []string {
	t.Helper()
	name := pair.std.Name
	var out []string
	for _, f := range pair.std.Funcs {
		if !isExportedName(f.Name) || f.Decl == nil || f.Decl.Recv != nil {
			continue
		}
		cands := []string{name + "." + f.Name}
		if n := len(f.Sig.TParams); n > 0 {
			cands = nil
			for _, arg := range []string{"int64", "string"} {
				args := slices.Repeat([]string{arg}, n)
				cands = append(cands, name+"."+f.Name+"["+strings.Join(args, ", ")+"]")
			}
		}
		for _, c := range cands {
			src := copyMain(pair.path, []string{c})
			if _, ds := checkMain(src, pair, false); len(ds) == 0 {
				out = append(out, c)
				cands = nil
				break
			}
		}
		if cands == nil {
			continue
		}
		for _, arg := range []string{"int64", "string"} {
			c, ok := copyCall(name, f, arg)
			if !ok {
				break
			}
			if _, ds := checkMain(copyMain(pair.path, []string{c}), pair, false); len(ds) == 0 {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// copyCall is a function declaration, call<F>, that calls pkg's function
// f with its parameters: f's signature with each type parameter bound, a
// type to arg, an error row to copyError and a capability row to the empty
// row. A parameter f writes pure takes a value the checker sees: for a
// Machine, copyMachine's machine, whose state, signal, output and error
// row bind the Machine's parameters. ok is false for a signature it cannot
// spell.
func copyCall(pkg string, f *check.Func, arg string) (string, bool) {
	var pure []bool
	if f.Decl.Type != nil && f.Decl.Type.Params != nil {
		for _, fld := range f.Decl.Type.Params.List {
			for range max(len(fld.Names), 1) {
				pure = append(pure, fld.Pure)
			}
		}
	}
	raw, _, ok := copySplitParams(strings.TrimPrefix(f.Sig.String(), "func"))
	if !ok || len(pure) != len(raw) {
		return "", false
	}
	bind := map[string]string{}
	for _, tp := range f.Sig.TParams {
		switch tp.Kind {
		case check.KindErrors:
			bind[tp.Name] = copyError
		case check.KindUses:
			bind[tp.Name] = copyNoRow
		default:
			bind[tp.Name] = arg
		}
	}
	for i, p := range raw {
		if !pure[i] {
			continue
		}
		inner, ok := strings.CutPrefix(p, "Machine[")
		if !ok {
			return "", false // a pure function parameter: no fixture
		}
		args, _, _ := copySplitParams("(" + strings.TrimSuffix(inner, "]") + ")")
		for k, to := range []string{"CopyState", "CopySignal", "int64", copyError} {
			if k < len(args) {
				bind[args[k]] = to
			}
		}
	}
	sig := f.Sig.String()
	for name, to := range bind {
		sig = regexp.MustCompile(`\b`+regexp.QuoteMeta(name)+`\b`).ReplaceAllString(sig, to)
	}
	// The empty row: a member of a row with others, or the whole row.
	for _, form := range []string{" | " + copyNoRow, copyNoRow + " | ", " needs " + copyNoRow} {
		sig = strings.ReplaceAll(sig, form, "")
	}
	params, rest, ok := copySplitParams(strings.TrimPrefix(sig, "func"))
	if !ok || len(params) != len(raw) {
		return "", false
	}
	var decl, use []string
	for i, p := range params {
		if pure[i] {
			use = append(use, "CopyMachine{}")
			continue
		}
		decl = append(decl, fmt.Sprintf("p%d %s", i, strings.TrimSpace(p)))
		use = append(use, fmt.Sprintf("p%d", i))
	}
	head := "func call" + f.Name + "(" + strings.Join(decl, ", ") + ")"
	if rest = strings.Join(strings.Fields(rest), " "); rest != "" {
		head += " " + rest
	}
	if strings.Contains(head, copyNoRow) {
		return "", false // a capability row inside a type: no spelling
	}
	return head + " { " + pkg + "." + f.Name + "(" + strings.Join(use, ", ") + ") }", true
}

// copyNoRow stands for the empty capability row while copyCall spells a
// signature: a row member it drops.
const copyNoRow = "NoRow\x01"

// copyMachine is the machine copyMain declares for a copyCall that passes
// one to a pure parameter.
const copyMachine = `type CopyState { N int64 }

type CopySignal { N int64 }

func copyStep(s CopyState, e CopySignal) Decision[CopyState, int64] fails ` + copyError + ` { Ignore }

type CopyMachine machine[CopyState, CopySignal] { initial: CopyState{N: 0}, step: copyStep }

`

// copySplitParams splits "(A, B) rest" into its parameter types, at the
// commas outside brackets, and the rest.
func copySplitParams(s string) ([]string, string, bool) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", false
	}
	depth, start := 0, 1
	var params []string
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if p := strings.TrimSpace(s[start:i]); p != "" {
					params = append(params, p)
				}
				return params, s[i+1:], true
			}
		case ',':
			if depth == 1 {
				params = append(params, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, "", false
}

// copyError is the error type copyMain declares when a reference names it:
// the error row a copyCall binds.
const copyError = "Oops"

// copyProgram is the caller program of seed: seed 0 takes every reference
// in order, any other seed a drawn subset in a drawn order.
func copyProgram(pair copyPair, refs []string, seed uint64) string {
	picked := slices.Clone(refs)
	if seed != 0 {
		r := rand.New(rand.NewPCG(seed, 0x5eed))
		r.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
		if len(picked) > 0 {
			picked = picked[:1+r.IntN(len(picked))]
		}
	}
	return copyMain(pair.path, picked)
}

// copyMain is a main of the package at path that takes each reference as
// a value; the import is rewritten to the copy by checkMain. It imports the
// other std packages a reference names (a copyCall's parameter types) and
// declares copyError when one names it.
func copyMain(path string, refs []string) string {
	var b strings.Builder
	imports := []string{path}
	for _, m := range copyQualifier.FindAllStringSubmatch(strings.Join(refs, "\n"), -1) {
		if p := m[1]; !slices.Contains(imports, p) && p != pathBase(path) {
			if _, err := check.Std.Import(p); err == nil {
				imports = append(imports, p)
			}
		}
	}
	for _, p := range imports {
		b.WriteString("import " + p + "\n")
	}
	b.WriteString("\n")
	machine := slices.ContainsFunc(refs, func(r string) bool { return strings.Contains(r, "CopyMachine{}") })
	if machine || slices.ContainsFunc(refs, func(r string) bool { return regexp.MustCompile(`\b` + copyError + `\b`).MatchString(r) }) {
		b.WriteString("type " + copyError + " {}\n\n")
	}
	if machine {
		b.WriteString(copyMachine)
	}
	var uses []string
	for _, r := range refs {
		if name, ok := strings.CutPrefix(r, "func "); ok {
			// A copyCall: declared, and taken by its name.
			b.WriteString(r + "\n\n")
			name, _, _ = strings.Cut(name, "(")
			r = name
		}
		uses = append(uses, r)
	}
	b.WriteString("func main() {\n")
	for i, r := range uses {
		fmt.Fprintf(&b, "\tlet v%d = %s\n\t_ = v%d\n", i, r, i)
	}
	b.WriteString("}\n")
	return b.String()
}

// copyQualifier is a package qualifier in a reference's text.
var copyQualifier = regexp.MustCompile(`\b([a-z][a-z0-9]*)\.[A-Z]`)

// pathBase is the last element of a package path, its default name.
func pathBase(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// copyTrust is the importer of a copy: std, and the root module's trust
// line for the copy's module, `trust go copy` (docs/spec/interop.md, Trusted
// Go), so the copy binds the runtime as std does (NP4e).
type copyTrust struct{}

func (copyTrust) Import(path string) (*check.Package, error) { return check.Std.Import(path) }
func (copyTrust) TrustsGo() (string, bool)                   { return "copy", true }

// copyImporter serves the package under test (std or its copy) and std.
type copyImporter map[string]*check.Package

func (m copyImporter) Import(path string) (*check.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

// checkMain checks the program src against the std package, or with its
// import rewritten to the copy.
func checkMain(src string, pair copyPair, copied bool) (lower.Unit, []check.Diagnostic) {
	imp := copyImporter{pair.path: pair.std}
	if copied {
		src = strings.Replace(src, "import "+pair.path+"\n", "import copy/"+pair.path+"\n", 1)
		imp = copyImporter{"copy/" + pair.path: pair.copied}
	}
	f, err := syntax.Parse(src)
	if err != nil {
		return lower.Unit{}, []check.Diagnostic{{Code: "parse", Message: err.Error()}}
	}
	f.Name = copyRoot + "main.ergo"
	p, ds := check.Check("main", []*syntax.File{f}, imp)
	return lower.Unit{Package: p, Files: []*syntax.File{f}, Sources: []lower.Source{{Path: f.Name, Text: src}}}, ds
}

// copyLower lowers src against the std package or its copy, and returns
// the normalised Go of every file.
func copyLower(t *testing.T, pair copyPair, src string, copied bool) string {
	t.Helper()
	u, ds := checkMain(src, pair, copied)
	if len(ds) > 0 {
		t.Fatalf("std package %s (copy %v): the caller program does not check: %v\n%s", pair.path, copied, ds, src)
	}
	if copied {
		u.Imports = []lower.Unit{pair.unit}
		u.Rewrite = copyEvidence(t, pair)
	}
	files, err := lower.Lower(u)
	if err != nil {
		return "lowering error: " + err.Error()
	}
	var b strings.Builder
	for _, f := range files {
		if f.Path == "ergo_laws.go" {
			// The producer record of the ledger the build read
			// (copyEvidence): how the copy ships its evidence.
			continue
		}
		text := string(f.Data)
		if strings.HasSuffix(f.Path, ".go") {
			text = copyNormalise(text, pair.path, copied)
		}
		b.WriteString("== " + f.Path + "\n" + text)
	}
	return b.String()
}

// copyEvidence is the law evidence of the copy's build: its module's
// ledger, which holds the verdicts of std's ledger (stdlib.Ledger) under
// the copy's names spelled as std's. A sourced package's laws are admitted
// by its module's ledger and std's by the ledger it ships with (lower
// evidenceFor); so a copy whose laws ran as std's did rewrites as std
// does, and a law std's ledger does not admit (stale, unproved) is not
// admitted for the copy either.
func copyEvidence(t *testing.T, pair copyPair) lower.RewriteEvidence {
	t.Helper()
	entries, err := lower.ParseLedger(stdlib.Ledger())
	if err != nil {
		t.Fatal(err)
	}
	return lower.NewLedgerEvidence(pair.copied, lower.LedgerNames{pair.copied: pair.path}, entries, lower.ShippedForeignKeys)
}

// copyNormalise folds what differs between std and a user package by how
// they ship and are named, and nothing else:
//
//   - comments, and so the //line and /*line*/ directives (a std body has
//     none);
//   - the call table: the declarations that name ergoDecls, ergoCalls, ergoCallNames or
//     ergoInlines, and the types ergoDecl and ergoCall (a std body has no
//     entry);
//   - the context namespace: a string "std:P" and the copy's "copy/P";
//   - the package of a callee a site names as text: "calls P.f" and the
//     copy's "calls copy/P.f" (the lowering names a callee by its
//     package's path, std's and a user's alike);
//   - positions and files written as text: the copy's root /ergo-copy/;
//   - the suffix of a name the lowering made unique (copyCanonicalNames),
//     in an identifier, and which of two declarations of the same text a
//     body names; never in a variant's name, which is its Ergo name
//     (lower machine.go, ergoVariantName);
//   - the order of the top-level declarations, which means nothing in Go.
//
// A text that does not parse is returned whole.
func copyNormalise(text, path string, copied bool) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", text, parser.SkipObjectResolution)
	if err != nil {
		return text
	}
	ns := strconv.Quote("std:" + path)
	if copied {
		ns = strconv.Quote("copy/" + path)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING {
			if b.Value == ns {
				b.Value = `"<namespace>"`
			}
			if copied {
				b.Value = strings.ReplaceAll(b.Value, " calls copy/"+path+".", " calls "+path+".")
			}
			b.Value = copyFileOfPosition.ReplaceAllString(b.Value, "$1")
			// A file written as text (a CallSite's File): std's is
			// relative, std/P/f.ergo, the copy's under copyRoot.
			b.Value = strings.Replace(b.Value, `"`+copyRoot+"std/", `"std/`, 1)
		}
		return true
	})
	copyCanonicalNames(fset, f)
	var decls []string
	for _, d := range f.Decls {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, d); err != nil {
			return text
		}
		s := buf.String()
		if callTableDecl(d, s) {
			continue
		}
		// A removed directive leaves its line empty.
		var lines []string
		for _, l := range strings.Split(s, "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		decls = append(decls, strings.Join(lines, "\n"))
	}
	sort.Strings(decls)
	return "package " + f.Name.Name + "\n" + strings.Join(decls, "\n") + "\n"
}

// copyCanonicalNames renames the top-level declarations whose Go names the
// lowering made unique with a suffix (Dir and Dir_1: std fs's and fstest's
// Dir), whose order follows the order in which it names std and user
// packages. The declarations of one base name are ranked by their text with
// every name of the group written as the base, and renamed base_#0,
// base_#1, … in that order, so the same declarations get the same names
// whatever order they were named in, and a declaration whose text differs
// keeps differing. A field, a method and a selector keep their names.
func copyCanonicalNames(fset *token.FileSet, f *ast.File) {
	declOf := map[string]ast.Decl{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				declOf[d.Name.Name] = d
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					declOf[s.Name.Name] = d
				case *ast.ValueSpec:
					for _, n := range s.Names {
						declOf[n.Name] = d
					}
				}
			}
		}
	}
	groups := map[string][]string{}
	stems := map[string][2]string{}
	for name := range declOf {
		stem, inst := copyNameParts(name)
		base := stem + inst
		groups[base] = append(groups[base], name)
		stems[base] = [2]string{stem, inst}
	}
	keep := map[*ast.Ident]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			keep[n.Sel] = true
		case *ast.Field:
			for _, id := range n.Names {
				keep[id] = true
			}
		case *ast.FuncDecl:
			if n.Recv != nil {
				keep[n.Name] = true
			}
		case *ast.CompositeLit:
			for _, e := range n.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						keep[id] = true
					}
				}
			}
		}
		return true
	})
	final := map[string]string{}
	for base, names := range groups {
		member := regexp.MustCompile(`\b` + regexp.QuoteMeta(stems[base][0]) + `(?:_\d+)?` + regexp.QuoteMeta(stems[base][1]) + `\b`)
		keys := map[string]string{}
		for _, n := range names {
			var buf bytes.Buffer
			// Printed with no positions: a copy's line directives move
			// them, and the printer's line breaks follow them.
			_ = printer.Fprint(&buf, token.NewFileSet(), declOf[n])
			keys[n] = member.ReplaceAllString(buf.String(), base)
		}
		sort.Slice(names, func(i, j int) bool {
			if keys[names[i]] != keys[names[j]] {
				return keys[names[i]] < keys[names[j]]
			}
			return names[i] < names[j]
		})
		// Declarations whose text is the same are interchangeable (two
		// packages' `const none int = -1`, arena's and layout's): they
		// share one name, so which of them a body names, which follows
		// the naming order, does not count.
		rank := -1
		for i, n := range names {
			if i == 0 || keys[n] != keys[names[i-1]] {
				rank++
			}
			final[n] = fmt.Sprintf("%s_#%d", base, rank)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if n, ok := n.(*ast.Ident); ok {
			if to, ok := final[n.Name]; ok && !keep[n] {
				n.Name = to
			}
		}
		return true
	})
}

// copyNameParts splits a top-level Go name into its stem and the suffix
// of a monomorphised instance (_i1), dropping the suffix the lowering adds
// to make a name unique, which stands before the instance's: Pair_1 is
// Pair, and DecodeMono_1_i1, toml's beside json's DecodeMono_i1, is
// DecodeMono with _i1.
func copyNameParts(name string) (stem, inst string) {
	m := copyNameShape.FindStringSubmatch(name)
	return m[1], m[2]
}

// copyNameShape is a Go name: its stem, a uniqueness suffix, and the suffix
// of a monomorphised instance, each of the suffixes optional.
var copyNameShape = regexp.MustCompile(`^(.+?)(?:_\d+)?(_i\d+)?$`)

// copyFileOfPosition matches the file of a position written as text
// (std/codec/codec.ergo:118:2, or a copy's file): only line and column are
// kept.
var copyFileOfPosition = regexp.MustCompile(`[^\s"']*\.ergo:(\d+:\d+)`)

// callTableDecl reports a declaration of the call table: the types
// ergoDecl and ergoCall, and the declarations that read the tables.
func callTableDecl(d ast.Decl, text string) bool {
	if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
		for _, s := range g.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && (ts.Name.Name == "ergoDecl" || ts.Name.Name == "ergoCall") {
				return true
			}
		}
	}
	for _, name := range []string{"ergoDecls", "ergoCalls", "ergoCallNames", "ergoInlines"} {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}

// TestQueueCopyKeepsItsValues is NP3c's acceptance (STD-NOPRIV plan §1.1):
// std queue copied into a user package checks with no "has no release"
// diagnostic. Queue[T] declares the operation that gives back what it
// keeps (drain), and the release rule reads that declaration, not the
// package. Before, only a std type whose runtime body had a Drain had a
// release, so the copy's Queue of a type parameter had none. The copy's
// other diagnostics (the extern door, NP4e) are not this rule's. Mutant
// keeps-drain-read-by-name (drainMethod accepts only std's) brings the
// diagnostic back.
func TestQueueCopyKeepsItsValues(t *testing.T) {
	pair := checkCopy(t, "queue")
	for _, d := range pair.diags {
		if strings.Contains(d.Message, "has no release") {
			t.Errorf("the queue copy: %s %s", d.Code, d.Message)
		}
	}
}

// TestFSCopyBorrowsItsText is STD-COPY-SAME's acceptance for fs: a
// program that holds a Text of the copy of fs in a field is refused as one
// that holds std fs's is (types.md, Borrows 8, rule 1). The borrow rule
// reads the declaration of Text (it names the runtime's MappedText), not
// the path fs. Mutant mapped-text-read-by-name (IsMappedText asks for std
// fs) lets the copy's field through.
func TestFSCopyBorrowsItsText(t *testing.T) {
	pair := checkCopy(t, "fs")
	const src = "import fs\n\ntype Keep { text fs.Text }\n\nfunc main() {}\n"
	for _, copied := range []bool{false, true} {
		_, ds := checkMain(src, pair, copied)
		found := false
		for _, d := range ds {
			found = found || strings.Contains(d.Message, "is a borrow of its Mapped")
		}
		if !found {
			t.Errorf("a field of fs.Text (copy %v): no borrow refusal; diagnostics %v", copied, ds)
		}
	}
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// copyAPIText is the text of fs as api.Text printed it before SEM2a moved
// the view to std code's ApiText: each function's name, signature, type
// parameters, what it takes and its own marks.
func copyAPIText(fs []api.Function) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s %s", f.Name, f.Signature)
		if len(f.TypeParameters) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(f.TypeParameters, ", "))
		}
		takes := "nothing"
		if len(f.Takes) > 0 {
			takes = strings.Join(f.Takes, ", ")
		}
		origin := "written"
		if f.TakesInferred {
			origin = "inferred"
		}
		fmt.Fprintf(&b, "\n  takes %s (%s)\n", takes, origin)
		for _, n := range f.Own {
			fmt.Fprintf(&b, "  own %s (a resource may bind it)\n", n)
		}
	}
	if len(fs) == 0 {
		b.WriteString("no exported function\n")
	}
	return b.String()
}
