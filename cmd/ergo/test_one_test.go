package main

import (
	"bytes"
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
	// reaches holds the branches the program takes: "fail", "refuted", "defect".
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
	values := make([]int, n)
	var all strings.Builder
	for i := range n {
		var b strings.Builder
		var imports []int
		for j := range i {
			if r.IntN(2) == 0 {
				imports = append(imports, j)
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
		if i == defect {
			fmt.Fprintf(&b, "\nfunc at%d(xs []int, i int) int { xs[i] }\n\ntest D%d { assert at%d([]int{}, 1) == 0 }\n", i, i, i)
			m.reaches["defect"] = true
		}
		name := fmt.Sprintf("p%d/p.ergo", i)
		smokeWrite(t, m.root, name, b.String())
		fmt.Fprintf(&all, "// %s\n%s\n", name, b.String())
		m.pkgs = append(m.pkgs, fmt.Sprintf("p%d", i))
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
		for _, branch := range []string{"fail", "refuted", "defect"} {
			if !reached[branch] {
				t.Errorf("no seed reaches a %s; choose seeds that do", branch)
			}
		}
	}()
	for _, seed := range []uint64{3, 11, 42} {
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
// over K generated packages loads the module once, checks each package once
// under its import path when another package imports it and once as its
// own test build's root, and links once; each package's analysis reads
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
		imported := map[string]bool{}
		for _, a := range analyses {
			for _, p := range a.Packages()[:len(a.Packages())-1] {
				imported[p.Path] = true
			}
		}
		if loads, checks := checked.Loads-before.Loads, checked.Checks-before.Checks; loads != 1 || checks != int64(len(dirs)+len(imported)) {
			t.Fatalf("seed %d: %d loads and %d checks of %d packages, %d of them imported; want 1 load and one check of each identity\n%s", seed, loads, checks, len(dirs), len(imported), m.sources)
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
