package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// oneModule is a generated program of packages (TEST-one): package pI
// imports some pJ, J < I, and holds a test of a value its imports feed, a
// law that holds, and sometimes a failing test, a refuted law or a defect.
type oneModule struct {
	root    string
	sources string // every file, for a failing seed's report
	pkgs    []string
	// reaches holds the branches the program takes (TestModuleTestMatchesEachPackage).
	reaches map[string]bool
}

// writeOneModule writes the program of seed under a new folder.
func writeOneModule(t *testing.T, seed uint64) oneModule {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed^0x7e57))
	m := oneModule{root: physicalTempDir(t), reaches: map[string]bool{}}
	smokeWrite(t, m.root, "ergo.mod", "module gen\n")
	n := 2 + r.IntN(3)
	defect := -1
	if r.IntN(2) == 0 {
		defect = r.IntN(n)
	}
	// The defect's kind is drawn from its own stream, so the rest of a
	// seed's program does not move with it.
	scriptDefect := rand.New(rand.NewPCG(seed, 0x5c71)).IntN(2) == 0
	ownScript := false
	values := make([]int, n)
	imported, escapes, records := make([]bool, n), make([]bool, n), make([]bool, n)
	var all strings.Builder
	for i := range n {
		var b strings.Builder
		if i == defect && scriptDefect {
			b.WriteString("import testing\n")
		}
		var imports []int
		for j := range i {
			if r.IntN(2) == 0 {
				imports = append(imports, j)
				imported[j] = true
				fmt.Fprintf(&b, "import gen/p%d\n", j)
			}
		}
		c := 1 + r.IntN(9)
		values[i] = c
		sum := fmt.Sprintf("F%d(0)", i)
		for _, j := range imports {
			values[i] += values[j]
			sum += fmt.Sprintf(" + p%d.G%d()", j, j)
		}
		fmt.Fprintf(&b, "\nfunc F%d(n int) int { n + %d }\n\nfunc G%d() int { %s }\n\n", i, c, i, sum)
		want := values[i]
		if r.IntN(4) == 0 {
			want++ // a failing test
			m.reaches["fail"] = true
		}
		fmt.Fprintf(&b, "test T%d { assert G%d() == %d }\n\nlaw Keeps%d(n int) { F%d(n) - n == %d }\n", i, i, want, i, i, c)
		if r.IntN(4) == 0 {
			fmt.Fprintf(&b, "\nlaw Even%d(n int) { n %% 2 == 0 }\n", i)
			m.reaches["refuted"] = true
		}
		// A typed failure that escapes a test names its type, and a failed
		// property case prints its record argument: what a package's name
		// could reach in its report.
		fmt.Fprintf(&b, "\ntype Bad%d {}\n\nfunc Fail%d() fails Bad%d { Bad%d }\n\ntype Pt%d { X int }\n", i, i, i, i, i)
		if r.IntN(2) == 0 {
			fmt.Fprintf(&b, "\ntest E%d { try Fail%d() }\n", i, i)
			escapes[i] = true
		}
		if len(imports) > 0 && r.IntN(2) == 0 {
			fmt.Fprintf(&b, "\ntest X%d { try p%d.Fail%d() }\n", i, imports[0], imports[0])
			m.reaches["escaped import"] = true
		}
		if r.IntN(2) == 0 {
			fmt.Fprintf(&b, "\ntest Q%d(p Pt%d) { assert p.X < 3 }\n", i, i)
			records[i] = true
		}
		fmt.Fprintf(&b, "\ntype Photos%d {\n\tGet(k string) int,\n}\n", i)
		switch {
		case i == defect && scriptDefect:
			// A Script's no-answer defect prints the operation type's
			// qualified name: its own package's as the root's own run
			// names it, main, or an import's under that import's name.
			photos := fmt.Sprintf("Photos%d", i)
			if len(imports) > 0 {
				photos = fmt.Sprintf("p%d.Photos%d", imports[0], imports[0])
				m.reaches["script defect over an imported type"] = true
			} else {
				ownScript = true
			}
			fmt.Fprintf(&b, "\ntest D%d {\n\tlet s = testing.Script[%s]{}\n\tlet n = with context { %s: s } { %s.Get(\"a\") }\n\tassert n == 0\n}\n", i, photos, photos, photos)
			m.reaches["defect"] = true
		case i == defect:
			fmt.Fprintf(&b, "\nfunc at%d(xs []int, i int) int { xs[i] }\n\ntest D%d { assert at%d([]int{}, 1) == 0 }\n", i, i, i)
			m.reaches["defect"] = true
		}
		name := fmt.Sprintf("p%d/p.ergo", i)
		smokeWrite(t, m.root, name, b.String())
		fmt.Fprintf(&all, "// %s\n%s\n", name, b.String())
		m.pkgs = append(m.pkgs, fmt.Sprintf("p%d", i))
	}
	// The branches that matter most are in a package another package
	// imports: one load checks it once, for both roles.
	for i := range n {
		if imported[i] && escapes[i] {
			m.reaches["escape in an imported package"] = true
		}
		if imported[i] && records[i] {
			m.reaches["record in an imported package"] = true
		}
		if imported[i] && i == defect && ownScript {
			m.reaches["script defect in an imported package"] = true
		}
		if imported[i] && i == defect {
			m.reaches["defect in an imported package"] = true
		}
	}
	m.sources = all.String()
	return m
}

// TestModuleTestMatchesEachPackage is the TEST-one property: on generated
// programs of packages, `ergo test DIR/...`, which checks the module once
// and runs one test binary, prints for each package what `ergo test` of
// that package alone prints, writes the same receipt, and exits as the
// recursive command's rule reads the packages' own exits (a defect stops
// the run). The packages' own runs are the reference: each builds its own
// test binary. It builds Go because the claim is about the linked test
// binary's reports, receipts and exit status, which no in-process check
// produces. A failing seed prints the program and the replay command.
func TestModuleTestMatchesEachPackage(t *testing.T) {
	needsBuild(t)
	bin := buildTestCLI(t)
	reached := map[string]bool{}
	defer func() {
		// The seeds reach a failed test, a refuted law and a defect,
		// which stops the run before later packages.
		for _, branch := range []string{"fail", "refuted", "defect", "escaped import", "escape in an imported package", "record in an imported package", "defect in an imported package", "script defect in an imported package", "script defect over an imported type"} {
			if !reached[branch] {
				t.Errorf("no seed reaches a %s; choose seeds that do", branch)
			}
		}
	}()
	for _, seed := range []uint64{3, 11, 42, 7, 23, 58} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			m := writeOneModule(t, seed)
			for branch := range m.reaches {
				reached[branch] = true
			}
			xdg, tmp := filepath.Join(t.TempDir(), "xdg"), t.TempDir()
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("seed %d: %s\n%sreplay: go test ./cmd/ergo -count=1 -run '^TestModuleTestMatchesEachPackage/%d$'", seed, fmt.Sprintf(format, args...), m.sources, seed)
			}
			packages, err := sketchfront.DiscoverPackages(m.root)
			if err != nil {
				t.Fatal(err)
			}
			flags := []string{"--seed", fmt.Sprint(seed)}
			// The reference: each package alone, composed by the recursive
			// command's rule.
			var wantOut, wantErr strings.Builder
			wantCode := 0
			for _, pkg := range packages {
				receipt := filepath.Join(m.root, "want", filepath.FromSlash(pkg.Path)+".json")
				if err := os.MkdirAll(filepath.Dir(receipt), 0o755); err != nil {
					t.Fatal(err)
				}
				out, errText, code := testRunCLI(t, bin, m.root, xdg, tmp, append([]string{"test", pkg.Dir, "--receipt", receipt}, flags...)...)
				wantOut.WriteString("package " + pkg.Path + "\n" + out)
				wantErr.WriteString(errText)
				if code > 1 {
					wantCode = code
					break
				}
				wantCode = max(wantCode, code)
			}
			// The shared run's go command logs each call: one link, and no
			// package built alone after it.
			calls := filepath.Join(t.TempDir(), "go.log")
			command := exec.Command(bin, append([]string{"test", "./...", "--receipt", filepath.Join(m.root, "got")}, flags...)...)
			command.Dir = m.root
			command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg, "TMPDIR="+tmp, "PATH="+goLogger(t, calls)+string(os.PathListSeparator)+os.Getenv("PATH"))
			stdout, stderr, code := runSmokeCommand(t, command)
			out, errText := string(stdout), string(stderr)
			if out != wantOut.String() || code != wantCode {
				fail("ergo test ./...: exit %d, want %d\n--- stdout\n%s--- each package\n%s", code, wantCode, out, wantOut.String())
			}
			if errText != wantErr.String() {
				fail("ergo test ./...: stderr\n%s--- each package\n%s", errText, wantErr.String())
			}
			if log, err := os.ReadFile(calls); err != nil || strings.Count(string(log), "\n") != 1 {
				fail("ergo test ./... ran the go command %d times (%v), want one link of every package", strings.Count(string(log), "\n"), err)
			}
			for _, pkg := range packages {
				rel := filepath.FromSlash(pkg.Path) + ".json"
				want, wantRead := os.ReadFile(filepath.Join(m.root, "want", rel))
				got, gotRead := os.ReadFile(filepath.Join(m.root, "got", rel))
				if (wantRead == nil) != (gotRead == nil) || !bytes.Equal(got, want) {
					fail("receipt %s:\n%s\nwant\n%s", rel, got, want)
				}
			}
		})
	}
}

// TestModuleCheckMatchesEachPackage is the property of the shared check
// (TEST-one round 2): on the generated programs of packages, `ergo check
// DIR/...` loads the module once and prints, for each package, the
// envelope and evaluation that `ergo check` of that package alone prints,
// and exits as the recursive command's rule reads the packages' own exits.
// The packages' own checks are the reference. It runs in process: check
// builds no Go. A failing seed prints the program and the replay command.
func TestModuleCheckMatchesEachPackage(t *testing.T) {
	reached := map[string]bool{}
	for _, seed := range []uint64{3, 11, 42, 7, 23, 58} {
		m := writeOneModule(t, seed)
		for branch := range m.reaches {
			reached[branch] = true
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\n%sreplay: go test ./cmd/ergo -count=1 -run '^TestModuleCheckMatchesEachPackage$'", seed, fmt.Sprintf(format, args...), m.sources)
		}
		packages, err := sketchfront.DiscoverPackages(m.root)
		if err != nil {
			t.Fatal(err)
		}
		var want []byte
		wantCode := 0
		for _, pkg := range packages {
			out, code := checkInProcess(t, pkg.Dir)
			want = append(want, out...)
			if code > 1 {
				wantCode = code
				break
			}
			wantCode = max(wantCode, code)
		}
		before := sketchfront.WorkDone()
		got, code := checkInProcess(t, filepath.Join(m.root, "..."))
		if loads := sketchfront.WorkDone().Loads - before.Loads; loads != 1 {
			fail("ergo check ./... loaded the module %d times, want once", loads)
		}
		if !bytes.Equal(got, want) || code != wantCode {
			fail("ergo check ./...: exit %d, want %d\n--- stdout\n%s\n--- each package\n%s", code, wantCode, got, want)
		}
	}
	for _, branch := range []string{"escape in an imported package", "script defect in an imported package"} {
		if !reached[branch] {
			t.Errorf("no seed reaches a %s; choose seeds that do", branch)
		}
	}
}

// goLogger is a folder whose go appends a line to log and runs the go
// command of PATH.
func goLogger(t *testing.T, log string) string {
	t.Helper()
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestModuleTestWorksOnce is TEST-one's counted work guard: a shared run
// over K generated packages loads the module once, checks each package
// once (one check serves as its importers' import and as its own test
// build's root), and links once; each package's analysis reads
// the same texts as checking it alone (its revision). The link is the go
// command's, replaced by a process that builds nothing: the count is the
// claim, not the binary.
func TestModuleTestWorksOnce(t *testing.T) {
	for _, seed := range []uint64{5, 17} {
		m := writeOneModule(t, seed)
		packages, err := sketchfront.DiscoverPackages(m.root)
		if err != nil {
			t.Fatal(err)
		}
		dirs := make([]string, len(packages))
		for i, p := range packages {
			dirs[i] = p.Dir
		}
		before := sketchfront.WorkDone()
		platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
		if err != nil {
			t.Fatal(err)
		}
		analyses, err := sketchfront.CheckPackages(dirs, platform)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, m.sources)
		}
		checked := sketchfront.WorkDone()
		if loads, checks := checked.Loads-before.Loads, checked.Checks-before.Checks; loads != 1 || checks != int64(len(dirs)) {
			t.Fatalf("seed %d: %d loads and %d checks of %d packages; want 1 load and one check of each package\n%s", seed, loads, checks, len(dirs), m.sources)
		}
		for i, a := range analyses {
			alone, err := sketchfront.CheckDir(dirs[i])
			if err != nil {
				t.Fatal(err)
			}
			if a.Text() != alone.Text() {
				t.Fatalf("seed %d: package %s reads other texts than its own check\n%s", seed, packages[i].Path, m.sources)
			}
		}
		linked := sketchfront.WorkDone()
		if _, err := sketchfront.BuildTests(analyses, sketchfront.BuildRequest{
			Output:    filepath.Join(t.TempDir(), "module.test"),
			Cache:     build.Cache{Root: t.TempDir()},
			Toolchain: build.Toolchain{Go: "true"},
			Stderr:    &bytes.Buffer{},
		}); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, m.sources)
		}
		if links := sketchfront.WorkDone().Links - linked.Links; links != 1 {
			t.Fatalf("seed %d: %d links of %d packages; want one", seed, links, len(dirs))
		}
	}
}

// TestModuleTestPackageChecksAsMain is the smallest repro of the one rule
// that reads a root's path, found when TEST-one round 2 checked each
// package once: the builder rule (check/builders.go, rule 2) gives
// celltest, under its import path gen/celltest, the builders of gen/cell's
// operation types, so its property over cell.Cell checks; checked alone,
// as main, it has none and reports S108. A shared load that another
// package's import made check celltest only under its path would pass
// what `ergo test celltest` refuses; it must refuse the shared load, so
// the packages are tested alone.
func TestModuleTestPackageChecksAsMain(t *testing.T) {
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module gen\n")
	smokeWrite(t, root, "cell/cell.ergo", "type Cell {\n\tGet() int,\n}\n")
	smokeWrite(t, root, "celltest/mem.ergo", "import gen/cell\n\ntype Mem { v int }\n\nfunc Get(m Mem) int { m.v }\n\nfunc NewMem() Mem { Mem{v: 7} }\n\ntest Reads(c cell.Cell) { assert c.Get() == 7 }\n")
	smokeWrite(t, root, "user/user.ergo", "import gen/celltest\n\nfunc Seven() int { celltest.Get(celltest.NewMem()) }\n\ntest S { assert Seven() == 7 }\n")
	alone, err := sketchfront.CheckDir(filepath.Join(root, "celltest"))
	if err != nil {
		t.Fatal(err)
	}
	if len(alone.Diagnostics) == 0 {
		t.Fatal("celltest checks alone; the repro no longer reaches the builder rule")
	}
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{filepath.Join(root, "cell"), filepath.Join(root, "celltest"), filepath.Join(root, "user")}
	if _, err := sketchfront.CheckPackages(dirs, platform); !errors.Is(err, sketchfront.ErrNotShared) {
		t.Fatalf("CheckPackages = %v; want ErrNotShared, as celltest alone does not check", err)
	}
}
