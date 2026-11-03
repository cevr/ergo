package lower

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage E3b (196l; ledger row C20; spec D-E4): a law is checked and never
// run, so neither build lowers it. Its facts are OwnerLaw's
// (check/owners.go), and no build view sees that owner (lower/owners.go).
// Each program of testdata/e3b declares at least one law and states the
// stdout of ergo run (`// run> `); a program with tests states its test
// binary's stdout and exit (`// test> `, `// test exit N`) as testdata/e3
// does. TestLawSweep runs them; TestBuildsOmitLaws proves both builds of
// each are the builds of the same source with its laws blanked out.

// e3bProgram is one program of testdata/e3b and its expected outputs.
type e3bProgram struct {
	name, src string
	run       []string // ergo run's stdout lines
	test      []string // the test binary's stdout lines, if it has tests
	exit      int      // the test binary's exit status, -1 without tests
}

func e3bPrograms(t *testing.T) []e3bProgram {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "e3b", "*.ergo"))
	if err != nil || len(paths) < 12 {
		t.Fatalf("testdata/e3b holds %d programs (%v); the sweep needs 12 or more", len(paths), err)
	}
	var out []e3bProgram
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		prog := e3bProgram{name: "e3b_" + strings.TrimSuffix(filepath.Base(p), ".ergo"), src: string(data), exit: -1}
		for _, line := range strings.Split(prog.src, "\n") {
			switch {
			case strings.HasPrefix(line, "// run> "):
				prog.run = append(prog.run, strings.TrimPrefix(line, "// run> "))
			case strings.HasPrefix(line, "// test> "):
				prog.test = append(prog.test, strings.TrimPrefix(line, "// test> "))
			case strings.HasPrefix(line, "// test exit "):
				prog.exit, err = strconv.Atoi(strings.TrimPrefix(line, "// test exit "))
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
		}
		if len(prog.run) == 0 {
			t.Fatalf("%s states no run output", p)
		}
		if (len(prog.test) == 0) != (prog.exit < 0) {
			t.Fatalf("%s states test output without a test exit, or the reverse", p)
		}
		out = append(out, prog)
	}
	return out
}

// TestLawSweep builds the run build of every program of testdata/e3b and
// runs it: each prints exactly its run lines, so a law changes nothing
// ergo run does. A program with tests also runs its test build, which runs
// its tests and no law.
func TestLawSweep(t *testing.T) {
	progs := e3bPrograms(t)
	var builds []*lowered
	tested := 0
	for _, p := range progs {
		run := mustLower(t, p.name+"_run", p.src)
		if len(run.pkg.Laws) == 0 {
			t.Errorf("%s declares no law", p.name)
		}
		builds = append(builds, run)
		if p.exit >= 0 {
			if len(run.pkg.Tests) == 0 {
				t.Errorf("%s states test output and declares no test", p.name)
			}
			builds = append(builds, lowerTestBuild(t, p.name+"_test", p.src))
			tested++
		}
	}
	if tested == 0 {
		t.Errorf("no program of testdata/e3b has tests and laws (sweep program 11)")
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name+"_run"])
		if code != 0 || strings.Join(lines(stdout), "\n") != strings.Join(p.run, "\n") {
			t.Errorf("%s: ergo run: exit %d, stdout %q, stderr %q; want %q", p.name, code, stdout, firstLines(stderr, 8), p.run)
		}
		if p.exit < 0 {
			continue
		}
		stdout, stderr, code = runExe(t, exes[p.name+"_test"])
		if code != p.exit || strings.Join(lines(stdout), "\n") != strings.Join(p.test, "\n") {
			t.Errorf("%s: test binary: exit %d (want %d), stdout:\n%s\nwant:\n%s\nstderr:\n%s", p.name, code, p.exit, stdout, strings.Join(p.test, "\n"), firstLines(stderr, 12))
		}
	}
}

// lawFixture is the spec fixture with laws (C21); it lowers, so the
// erasure property covers it too.
const lawFixture = "../check/testdata/fixtures/spec/types-laws.ergo"

// TestBuildsOmitLaws proves D-E4's "checked, not run" on the emitted Go of
// every sweep program and of the laws fixture, for the run build (Lower)
// and the test build (LowerTests): no //line directive maps into a law,
// and each build is byte-identical to the same build of the source with
// its laws blanked out (blankLaws).
func TestBuildsOmitLaws(t *testing.T) {
	type source struct{ name, src string }
	var srcs []source
	for _, p := range e3bPrograms(t) {
		srcs = append(srcs, source{p.name, p.src})
	}
	data, err := os.ReadFile(lawFixture)
	if err != nil {
		t.Fatal(err)
	}
	srcs = append(srcs, source{"types_laws", string(data)})
	for _, s := range srcs {
		t.Run(s.name, func(t *testing.T) { buildsOmitLaws(t, s.name, s.src) })
	}
}

// buildsOmitLaws checks both builds of src, which must declare a law.
func buildsOmitLaws(t *testing.T, name, src string) {
	t.Helper()
	run := mustLower(t, name, src)
	if len(run.pkg.Laws) == 0 {
		t.Fatalf("%s declares no law", name)
	}
	test := lowerTestBuild(t, name+"_test", src)
	spans := lawSpans(run.file, run.pkg)
	blanked := blankLaws(t, name, src)
	kept := func(code string) string { return lawsOnlyKind(t, name, run.file, code) }
	for _, b := range []struct {
		build      string
		code       string
		blankBuild func() string
	}{
		{"run", run.mainGo(), func() string { return kept(mustLower(t, name+"_blank", blanked).mainGo()) }},
		{"test", test.mainGo(), func() string { return kept(lowerTestBuild(t, name+"_blank_test", blanked).mainGo()) }},
	} {
		for _, m := range directiveRe.FindAllStringSubmatch(b.code, -1) {
			line, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			for _, s := range spans {
				if at := [2]int{line, col}; !before(at, s[0]) && before(at, s[1]) {
					t.Errorf("%s: the %s build has Go from the law at %d:%d (%s)", name, b.build, s[0][0], s[0][1], m[0])
				}
			}
		}
		if blank := b.blankBuild(); blank != b.code {
			t.Errorf("%s: the %s build differs from the %s build of the source without its laws:\n%s", name, b.build, b.build, goDiff(blank, b.code))
		}
	}
}

// lawsOnlyKind takes code, the Go of f's blanked source, and undoes the one
// change blanking makes beyond removing laws. A type of laws and no
// operations is an operation type (parser.go, parseMembersType), and its
// blanked body `{ }` is an empty record. So code must declare each such
// type of f exactly once, as a Go struct, and that one declaration becomes
// the Go interface f's build declares. Every other byte is compared as it
// is.
func lawsOnlyKind(t *testing.T, name string, f *syntax.File, code string) string {
	t.Helper()
	for _, d := range f.Decls {
		td, ok := d.(*syntax.TypeDecl)
		if !ok {
			continue
		}
		it, ok := td.Spec.(*syntax.InterfaceType)
		if !ok || len(it.Methods) > 0 || len(it.Laws) == 0 {
			continue
		}
		decl := regexp.MustCompile(`(?m)^((?:/\*line [^*]*\*/)?type ` + regexp.QuoteMeta(td.Name.Name) + `(?:\[[^\]\n]*\])? )struct \{`)
		if n := len(decl.FindAllStringIndex(code, -1)); n != 1 {
			t.Fatalf("%s: the blanked build declares the laws-only type %s as a struct %d times, not once", name, td.Name.Name, n)
		}
		code = decl.ReplaceAllString(code, "${1}interface {")
	}
	return code
}

// lawSpans are the line and column ranges of the laws pkg records for f.
func lawSpans(f *syntax.File, pkg *check.Package) [][2][2]int {
	var spans [][2][2]int
	for _, law := range pkg.Laws {
		l0, c0 := f.Position(law.Decl.Pos())
		l1, c1 := f.Position(law.Decl.End())
		spans = append(spans, [2][2]int{{l0, c0}, {l1, c1}})
	}
	return spans
}

// blankLaws replaces every law declaration of src, with the comma that
// ends it, by spaces, keeping its line feeds, so every other position stays
// where it was. A package that only laws used is then imported and not
// used, so it blanks that import too, and repeats until the source checks
// clean. A blanked source that does not check clean fails the test: the
// comparison would otherwise be vacuous.
func blankLaws(t *testing.T, name, src string) string {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		law, ok := n.(*syntax.LawDecl)
		if !ok {
			return true
		}
		end := int(law.End())
		for j := end; j < len(b); j++ {
			if b[j] == ',' {
				end = j + 1
				break
			}
			if b[j] != ' ' && b[j] != '\t' && b[j] != '\n' {
				break
			}
		}
		blank(int(law.Pos()), end)
		return false
	})
	for {
		f, err := syntax.Parse(string(b))
		if err != nil {
			t.Fatalf("%s: the source without its laws does not parse: %v\n%s", name, err, b)
		}
		if hasLaw(f) {
			t.Fatalf("%s: blankLaws kept a law:\n%s", name, b)
		}
		_, diags := check.Check("main", []*syntax.File{f}, nil)
		if len(diags) == 0 {
			return string(b)
		}
		blanked := false
		for _, d := range diags {
			if !strings.HasPrefix(d.Message, "package ") || !strings.HasSuffix(d.Message, " is imported and not used") {
				continue
			}
			for _, decl := range f.Decls {
				imp, ok := decl.(*syntax.ImportDecl)
				if !ok {
					continue
				}
				for _, spec := range imp.Specs {
					if int(spec.Pos()) <= d.Offset && d.Offset < int(spec.End()) {
						if len(imp.Specs) == 1 {
							blank(int(imp.Pos()), int(imp.End()))
						} else {
							blank(int(spec.Pos()), int(spec.End()))
						}
						blanked = true
					}
				}
			}
		}
		if !blanked {
			t.Fatalf("%s: the source without its laws does not check clean: %v\n%s", name, diags, b)
		}
	}
}

// hasLaw reports whether f still declares a law.
func hasLaw(f *syntax.File) bool {
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.LawDecl); ok {
			found = true
		}
		return !found
	})
	return found
}
