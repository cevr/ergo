package sketchfront

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testinputs/genprogram"
)

// writeGenerated writes the generated program of n packages for seed under
// a temporary directory and returns the module root.
func writeGenerated(t *testing.T, seed uint64, n int, opts genprogram.Options) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range genprogram.GenerateProgram(seed, n, opts).Files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// freshProcess resets the counters and the checker's std state, as a new
// process starts. No test that uses it is parallel.
func freshProcess(t *testing.T) {
	t.Helper()
	check.ResetStdForTest()
	stage.Reset()
}

var importLine = regexp.MustCompile(`(?m)^import\s+(\S+)`)

// referenceCount is the work a check of the package at root/main must do,
// counted from the files alone: the folders main reaches through the
// import lines of their .ergo files, their files and bytes, and the std
// packages reached the same way through std's sources.
type referenceCount struct {
	files, bytes, packages int64
	stdFiles, stdPackages  int64
	modBytes               int64
}

func countReference(t *testing.T, root, module, main string) referenceCount {
	t.Helper()
	var ref referenceCount
	mod, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
	if err != nil {
		t.Fatal(err)
	}
	ref.modBytes = int64(len(mod))
	seen, stdSeen := map[string]bool{}, map[string]bool{}
	var visitStd func(path string)
	visitStd = func(path string) {
		files, ok := std.Package(path)
		if !ok || stdSeen[path] {
			return
		}
		stdSeen[path] = true
		ref.stdPackages++
		ref.stdFiles += int64(len(files))
		for _, text := range files {
			for _, m := range importLine.FindAllStringSubmatch(text, -1) {
				visitStd(m[1])
			}
		}
	}
	var visit func(rel string)
	visit = func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		ref.packages++
		entries, err := os.ReadDir(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".ergo") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, rel, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			ref.files++
			ref.bytes += int64(len(data))
			for _, m := range importLine.FindAllStringSubmatch(string(data), -1) {
				if dep, ok := strings.CutPrefix(m[1], module+"/"); ok {
					visit(dep)
				} else {
					visitStd(m[1])
				}
			}
		}
	}
	visit(main)
	return ref
}

// TestStageReportCountsAgree: the stage counters of a check of generated
// programs at two sizes equal a count made from the files alone (the
// files, bytes and packages the main package reaches, and the std
// packages they reach through std's sources), with std's parse and check
// work in the totals (cs-count-skips-std). Its full-tier half builds each
// program twice on the direct path and holds the action counters to the
// direct path's own Report: the first build compiles and links, the
// second reuses every compile and the link, lowers nothing and reuses
// the emitted module (cs-count-reused-as-run). It builds Go because the
// action counters count the direct path's compile and link processes,
// which only a real build starts.
func TestStageReportCountsAgree(t *testing.T) {
	for _, n := range []int{6, 13} {
		root := writeGenerated(t, uint64(n), n, genprogram.Options{})
		module := "gen" + strconv.Itoa(n)
		ref := countReference(t, root, module, "cmd/app")
		freshProcess(t)
		a, err := CheckDir(filepath.Join(root, "cmd", "app"))
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("g%d does not check: %v %v", n, err, a.Diagnostics)
		}
		got := StageReport()
		want := map[string]int64{
			"files_read":       ref.files + 1,
			"bytes_read":       ref.bytes + ref.modBytes,
			"files_parsed":     ref.files + ref.stdFiles,
			"bytes_parsed":     ref.bytes,
			"packages_checked": ref.packages + ref.stdPackages,
			"std_files_parsed": ref.stdFiles,
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("g%d: %s = %d, the files say %d", n, k, got[k], v)
			}
		}
		if testing.Short() {
			continue
		}
		cache := build.Cache{Root: t.TempDir()}
		for round := range 2 {
			stage.Reset()
			var report build.Report
			_, err := Build(a, BuildRequest{Mode: build.ModeBuild, Source: filepath.Join(root, "cmd", "app"), Output: filepath.Join(t.TempDir(), "app"),
				Cache: cache, Toolchain: build.Toolchain{Go: "go"}, Report: &report})
			if err != nil || !report.Direct {
				t.Fatalf("g%d build %d: %v (%s)", n, round+1, err, report)
			}
			got := StageReport()
			links, linksReused := int64(0), int64(1)
			if report.Linked {
				links, linksReused = 1, 0
			}
			want := map[string]int64{
				"go_compiles": int64(report.Compiled), "go_compiles_reused": int64(report.Reused),
				"links": links, "links_reused": linksReused,
				"processes": int64(report.Compiled) + links + got["go_commands"],
			}
			if round == 1 {
				want["go_compiles"], want["links"], want["modules_lowered"], want["modules_reused"], want["go_commands"] = 0, 0, 0, 1, 0
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("g%d build %d: %s = %d, want %d (%s)", n, round+1, k, got[k], v, report)
				}
			}
		}
	}
}

// TestFrontEndWorkGrowsLinearly: the check and the emission of generated
// programs of 8, 16 and 32 packages of one shape do work linear in the
// program: for each work counter, the work each added package costs from
// 16 to 32 packages is at most 1.25 times its cost from 8 to 16, so
// doubling a program at most doubles the work beyond the fixed std part.
// It kills cs-dep-summary-hashes-program, a key that hashes the whole
// program once per package: correct, but quadratic. Counts, never time.
func TestFrontEndWorkGrowsLinearly(t *testing.T) {
	counters := []string{"files_read", "bytes_read", "files_parsed", "bytes_parsed", "packages_checked", "decls_checked", "go_bytes_lowered", "bytes_hashed", "bytes_written"}
	var work []map[string]int64
	for _, n := range []int{8, 16, 32} {
		root := writeGenerated(t, 7, n, genprogram.Options{Groups: 1})
		freshProcess(t)
		app := filepath.Join(root, "cmd", "app")
		a, err := CheckDir(app)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("g%d does not check: %v %v", n, err, a.Diagnostics)
		}
		_, err = Build(a, BuildRequest{Mode: build.ModeBuild, Source: app, Output: filepath.Join(t.TempDir(), "app"),
			Cache: build.Cache{Root: t.TempDir()}, Toolchain: build.Toolchain{Go: "go"}, Reuse: func(build.Module) bool { return true }})
		if err != nil {
			t.Fatalf("g%d does not lower: %v", n, err)
		}
		r := StageReport()
		r["own_packages"] = r["packages_checked"] - r["std_packages_checked"]
		work = append(work, r)
	}
	for _, k := range counters {
		p1 := work[1]["own_packages"] - work[0]["own_packages"]
		p2 := work[2]["own_packages"] - work[1]["own_packages"]
		d1, d2 := work[1][k]-work[0][k], work[2][k]-work[1][k]
		if p1 <= 0 || p2 <= 0 || d1 <= 0 {
			t.Fatalf("%s: the programs do not grow: packages %d, %d; work %d", k, p1, p2, d1)
		}
		t.Logf("%s: %d, %d, %d (per added package %.0f, then %.0f)", k, work[0][k], work[1][k], work[2][k], float64(d1)/float64(p1), float64(d2)/float64(p2))
		if float64(d2)/float64(p2) > 1.25*float64(d1)/float64(p1) {
			t.Errorf("%s grows faster than the program: %d, %d, %d at 8, 16, 32 packages (%d, %d, %d checked)", k,
				work[0][k], work[1][k], work[2][k], work[0]["own_packages"], work[1]["own_packages"], work[2]["own_packages"])
		}
	}
}
