package std_test

import (
	"bufio"
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
	"ergo.local/prototype/internal/sketch/std"
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
// The verdict of each package is a row of testdata/std-copy.txt: `same`,
// or `differs` with the stage and first reason and the unit that removes
// it. A package that differs and is listed same fails; a package listed
// differs that is now the same fails (drop the row to same); a package with
// no row fails, and a differs row outside stdCopyBaseline fails: the file
// only moves to same.
//
// ERGO_STD_COPY_SEED=<n> runs only seed n; ERGO_STD_COPY_PKG=<path> only
// one package. A failing seed prints the program, both first differing
// lines and the replay command.
func TestStdCopyAgrees(t *testing.T) {
	copyNormaliseKeepsMeaning(t)
	rows := readStdCopy(t)
	seeds := []uint64{0, 1, 2}
	if s := os.Getenv("ERGO_STD_COPY_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_STD_COPY_SEED=%q: %v", s, err)
		}
		seeds = []uint64{n}
	}
	only := os.Getenv("ERGO_STD_COPY_PKG")
	pkgs := append([]string{copyFixturePath}, std.Packages()...)
	seen := map[string]bool{}
	verdicts := map[string]copyVerdictT{}
	for _, path := range pkgs {
		if only != "" && path != only {
			continue
		}
		seen[path] = true
		v := copyVerdict(t, path, seeds)
		verdicts[path] = v
		row, ok := rows[path]
		switch {
		case !ok:
			t.Errorf("std package %s has no row in testdata/std-copy.txt; its copy %s", path, v)
		case v.same() && !row.same():
			t.Errorf("std package %s: its copy is the same now; set its row of testdata/std-copy.txt to same (it was: %s)", path, row.reason)
		case !v.same() && row.same():
			t.Errorf("std package %s: its copy differs, and testdata/std-copy.txt says same: %s", path, v)
		case !v.same() && v.stage != row.stage:
			t.Errorf("std package %s: its copy differs at %s, and testdata/std-copy.txt says %s: %s", path, v.stage, row.stage, v)
		}
		if !v.same() && !slices.Contains(stdCopyBaseline, path) {
			t.Errorf("std package %s differs and was not in testdata/std-copy.txt's differs rows when the rule landed: a std package must be ordinary (%s)", path, v)
		}
	}
	if only == "" {
		for _, path := range slices.Sorted(mapsKeys(rows)) {
			if !seen[path] {
				t.Errorf("testdata/std-copy.txt lists %s, which is no std package", path)
			}
		}
	}
	copyAgreesWithSnapshot(t, pkgs, verdicts, seeds)
}

// copyAgreesWithSnapshot runs the property again with std decoded from its
// checked snapshot (S3, CHK1b) instead of checked from source, and every
// verdict must be the same (NP5): the snapshot saves work and changes no
// result, so it is no privilege. The snapshot is exported from a fresh
// check of std, as the compiler's cache writes it.
func copyAgreesWithSnapshot(t *testing.T, pkgs []string, verdicts map[string]copyVerdictT, seeds []uint64) {
	t.Helper()
	check.ResetStdForTest()
	t.Cleanup(check.ResetStdForTest)
	data, err := check.ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	check.ResetStdForTest()
	if err := check.InstallStd(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	for _, path := range pkgs {
		want, ran := verdicts[path]
		if !ran {
			continue
		}
		if got := copyVerdict(t, path, seeds); got.String() != want.String() {
			t.Errorf("std package %s: with std from its snapshot the copy %s; from source it %s", path, got, want)
		}
	}
	if w := check.WorkDone(); w.StdPackagesChecked != 0 {
		t.Errorf("the snapshot pass checked %d std packages from source", w.StdPackagesChecked)
	}
}

// copyNormaliseKeepsMeaning checks the normaliser itself: what it must fold
// (directives, the call table, the context namespace, positions, the order
// of declarations, a uniqueness suffix) normalises equal, and one planted
// difference of each kind it must keep (a callee, a type, a hidden
// argument, an allocation, a rewritten operand) normalises different. A
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

type ergoCall = struct{ Func string }

var ergoCalls = [...]ergoCall{{Func: "f"}}
`
	folded := map[string][2]string{
		"directive":  {"//line /x/std/copyfixture/fixture.ergo:3:1\n", "//line /ergo-copy/std/copyfixture/fixture.ergo:9:1\n"},
		"call table": {`{{Func: "f"}}`, `{{Func: "f"}, {Func: "g"}}`},
		"namespace":  {`"std:copyfixture"`, `"copy/copyfixture"`},
		"position":   {"at std/copyfixture/fixture.ergo:4:2", "at /ergo-copy/std/copyfixture/fixture.ergo:4:2"},
		"order":      {"type Pair struct{ Left int64 }\n", ""},
		"suffix":     {"Pair", "Pair_1"},
	}
	kept := map[string][2]string{
		"callee":     {"return g(", "return h("},
		"type":       {"n int64, ns string) int64 { return n }\n\nfunc h", "n int32, ns string) int64 { return n }\n\nfunc h"},
		"hidden":     {"g(env, q.Left", "g(nil, q.Left"},
		"allocation": {"q := &Pair{", "q := Pair{"},
		"rewrite":    {"return g(env, q.Left", "return g(env, p.Left"},
	}
	want := copyNormalise(base, copyFixturePath, false)
	for kind, e := range folded {
		text := strings.Replace(base, e[0], e[1], 1)
		copied := kind == "namespace"
		switch kind {
		case "order":
			text += e[0]
		case "suffix":
			text = strings.ReplaceAll(base, e[0], e[1])
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

// stdCopyRow is one row of testdata/std-copy.txt.
type stdCopyRow struct{ stage, reason, unit string }

func (r stdCopyRow) same() bool { return r.stage == "" }

// readStdCopy reads testdata/std-copy.txt: `path<TAB>same`, or
// `path<TAB>differs<TAB>stage: reason<TAB>unit`.
func readStdCopy(t *testing.T) map[string]stdCopyRow {
	t.Helper()
	f, err := os.Open("testdata/std-copy.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]stdCopyRow{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		switch {
		case len(p) == 2 && p[1] == "same":
			out[p[0]] = stdCopyRow{}
		case len(p) == 4 && p[1] == "differs" && strings.TrimSpace(p[3]) != "":
			stage, reason, ok := strings.Cut(p[2], ": ")
			if !ok || !slices.Contains([]string{"check", "surface", "lower"}, stage) {
				t.Errorf("testdata/std-copy.txt:%d: the reason %q does not start with check:, surface: or lower:", n, p[2])
			}
			out[p[0]] = stdCopyRow{stage: stage, reason: reason, unit: p[3]}
		default:
			t.Errorf("testdata/std-copy.txt:%d: want `path<TAB>same` or `path<TAB>differs<TAB>stage: reason<TAB>unit`, got %q", n, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// stdCopyBaseline are the packages testdata/std-copy.txt held as differs
// when the rule landed. A package may leave; none may join.
var stdCopyBaseline = []string{
	"actor", "arena", "atomic", "base64", "bits", "cmp", "codec", "config",
	"crc32", "csv", "embed", "exec", "explore", "filepath", "flag", "fs",
	"fstest", "hex", "http", "io", "journal", "journal/memory", "json",
	"log", "machines", "maps", "math", "os", "prompt", "queue", "rand",
	"rpc", "schedule", "scope", "sha256", "slices", "strconv", "strings",
	"sync", "task", "tasks", "term", "testing", "time", "timeout", "toml",
	"trace", "utf8",
}

// copySource is one std file of a package under test.
type copySource struct{ name, text string }

// copySources are the files of the std package at path, by name.
func copySources(t *testing.T, path string) []copySource {
	t.Helper()
	if path == copyFixturePath {
		return []copySource{{"fixture.ergo", copyFixture}}
	}
	srcs, ok := std.Package(path)
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
	files, sources := parse(copyRoot + "std/" + path + "/")
	var under *check.Package
	if base, ok := check.TestedPath(path); ok {
		if p, err := check.Std.Import(base); err == nil {
			under = p
		}
	}
	pair.copied, pair.diags = check.CheckTest("copy/"+path, files, nil, under)
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
// functions (api.Text), its types with their shapes, and its laws.
func copySurface(p *check.Package) string {
	var b strings.Builder
	b.WriteString(api.Text(api.Functions(p)))
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
var copyRow = regexp.MustCompile(`\b(?:fails|needs) [^ |]+(?: \| [^ |]+)*`)

// copyRefs are the references a caller can take of the package's exported
// functions: each is checked alone, against std, and kept when it checks.
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
				break
			}
		}
	}
	return out
}

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
// a value; the import is rewritten to the copy by checkMain.
func copyMain(path string, refs []string) string {
	var b strings.Builder
	b.WriteString("import " + path + "\n\nfunc main() {\n")
	for i, r := range refs {
		fmt.Fprintf(&b, "\tlet v%d = %s\n\t_ = v%d\n", i, r, i)
	}
	b.WriteString("}\n")
	return b.String()
}

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
	}
	files, err := lower.Lower(u)
	if err != nil {
		return "lowering error: " + err.Error()
	}
	var b strings.Builder
	for _, f := range files {
		text := string(f.Data)
		if strings.HasSuffix(f.Path, ".go") {
			text = copyNormalise(text, pair.path, copied)
		}
		b.WriteString("== " + f.Path + "\n" + text)
	}
	return b.String()
}

// copyNormalise folds what differs between std and a user package by how
// they ship and are named, and nothing else:
//
//   - comments, and so the //line and /*line*/ directives (a std body has
//     none);
//   - the call table: the declarations that name ergoDecls, ergoCalls or
//     ergoInlines, and the types ergoDecl and ergoCall (a std body has no
//     entry);
//   - the context namespace: a string "std:P" and the copy's "copy/P";
//   - positions written as text: the copy's root /ergo-copy/;
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
			b.Value = copyFileOfPosition.ReplaceAllString(b.Value, "$1")
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
	for name := range declOf {
		base := copySuffix.ReplaceAllString(name, "")
		groups[base] = append(groups[base], name)
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
		member := regexp.MustCompile(`\b` + regexp.QuoteMeta(base) + `(?:_\d+)?\b`)
		keys := map[string]string{}
		for _, n := range names {
			var buf bytes.Buffer
			_ = printer.Fprint(&buf, fset, declOf[n])
			keys[n] = member.ReplaceAllString(buf.String(), base)
		}
		sort.Slice(names, func(i, j int) bool {
			if keys[names[i]] != keys[names[j]] {
				return keys[names[i]] < keys[names[j]]
			}
			return names[i] < names[j]
		})
		for i, n := range names {
			final[n] = fmt.Sprintf("%s_#%d", base, i)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && !keep[id] {
			if to, ok := final[id.Name]; ok {
				id.Name = to
			}
		}
		return true
	})
}

// copySuffix is the suffix the lowering adds to make a Go name unique.
var copySuffix = regexp.MustCompile(`_\d+$`)

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
	for _, name := range []string{"ergoDecls", "ergoCalls", "ergoInlines"} {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
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
