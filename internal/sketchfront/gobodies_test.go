package sketchfront

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestModuleGoBodies (STD-NOPRIV NP4d; docs/spec/interop.md, Trusted Go
// 3): a trusted module's package binds a body of the module's own Go
// package that takes hidden arguments (the call's site; the caller's task,
// on which it waits) and states its waiting policy, and the lowering calls
// it with them, as it calls a runtime/v1 body: the promised form is the
// hidden argument first, before the Ergo one. A body that states no
// policy is refused with the mark to write. Mutants:
// binding-policy-default-nowait (a body with no mark is
// read as one that does not wait), module-hidden-unqualified (v1's hidden
// types are read unqualified only, so v1.Site is an Ergo parameter).
func TestModuleGoBodies(t *testing.T) {
	const goSrc = `package gohost

import (
	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

//ergo:wait none
func Mark(at v1.Site, s string) string { return s + string(at) }

//ergo:wait caller
func Poll(t *rt.Task, n int) int {
	t.Check()
	return n + 1
}

func Unmarked(t *rt.Task) int { return 0 }
`
	files := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27\n",
		"gohost/gohost.go": goSrc,
		"main.ergo": "import strconv\n\n" +
			"extern go example.com/app/gohost.Mark func mark(s string) string needs go.Foreign\n" +
			"extern go example.com/app/gohost.Poll func poll(n int) int needs go.Foreign\n\n" +
			"func main() needs Console | go.Foreign {\n\tConsole.Println(mark(\"here\"))\n\tConsole.Println(strconv.Itoa(poll(41)))\n}\n",
	}
	for _, c := range []struct{ mod, extra, want string }{
		{"module app\n", "", ""},
		{"module app\n", "extern go example.com/app/gohost.Unmarked func unmarked() int needs go.Foreign\n", "main.ergo S108 3:1 extern go example.com/app/gohost.Unmarked: Unmarked states no //ergo:wait KIND"},
	} {
		files["ergo.mod"] = c.mod
		main := files["main.ergo"]
		files["main.ergo"] = strings.Replace(main, "\n\n", "\n\n"+c.extra, 1)
		a, err := CheckDir(writeTree(t, files))
		files["main.ergo"] = main
		if err != nil {
			t.Fatal(err)
		}
		got := diagnosticLines(a)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("ergo.mod %q:\n%s\nwant %q", c.mod, got, c.want)
			continue
		}
		if c.want != "" {
			continue
		}
		out, err := lower.Lower(a.program.unit())
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, f := range out {
			text.Write(f.Data)
		}
		for _, call := range []string{"gohost.Mark(", "gohost.Poll("} {
			i := strings.Index(text.String(), call)
			if i < 0 {
				t.Fatalf("the lowered Go calls no %s)\n%s", call, text.String())
			}
			if rest := text.String()[i+len(call):]; strings.HasPrefix(rest, `"here"`) || strings.HasPrefix(rest, "41") {
				t.Errorf("%s) is called with no hidden argument first: %.80s", call, rest)
			}
		}
	}
}

// TestModuleGoBodiesHonourBuildConstraints (U1 SIMD-SCAN): the checker
// reads a module body from the files go build compiles for the target, so
// a body that two files declare under opposite constraints (a vector
// kernel and its stock twin) is read from the one that is built. Each
// other file here declares Index with a wrong signature, which S108
// reports if the checker reads it: a file for another GOOS by its name,
// and a file for GOEXPERIMENT=simd by its //go:build line, read only when
// the environment sets that experiment. Mutants: body-constraints-ignored
// (every file is read), body-experiment-unread (GOEXPERIMENT's tags are
// not set, so the simd file is never read).
func TestModuleGoBodiesHonourBuildConstraints(t *testing.T) {
	root := kernTree(t)
	t.Setenv("GOEXPERIMENT", "")
	a, err := CheckDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(a); got != "" {
		t.Errorf("a body read from a file go build leaves out:\n%s", got)
	}
	t.Setenv("GOEXPERIMENT", "simd")
	a, err = CheckDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(a); !strings.Contains(got, "S108") {
		t.Errorf("with GOEXPERIMENT=simd the checker did not read the simd file's body:\n%s", got)
	}
}

// TestCheckKeysHoldTheExperiments (U1 SIMD-SCAN): the Go experiments
// choose a module body's file, so every answer a later check may reuse
// sees them: the check cache through the digest of the built files, the
// warm key by name. The check cache, flipped from no experiment to
// GOEXPERIMENT=simd with no file edited, checks the program again and
// reads the simd file's body (S108), where a reused answer would be the
// stock one; the warm key changes when only the go env file's
// GOEXPERIMENT changes, which no environment variable holds. Mutants:
// go-body-edit-unkeyed, warm-experiments-unkeyed.
func TestCheckKeysHoldTheExperiments(t *testing.T) {
	root := kernTree(t)
	t.Setenv("GOEXPERIMENT", "")
	cache := NewCheckCache()
	if a, err := cache.CheckDir(root); err != nil || diagnosticLines(a) != "" {
		t.Fatalf("stock check: %v\n%s", err, diagnosticLines(a))
	}
	t.Setenv("GOEXPERIMENT", "simd")
	checked, _ := checkWork(func() {
		a, err := cache.CheckDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); !strings.Contains(got, "S108") {
			t.Errorf("GOEXPERIMENT=simd with no file edited took the stock answer from the check cache:\n%s", got)
		}
	})
	if checked == 0 {
		t.Errorf("GOEXPERIMENT=simd with no file edited checked no package; want the program checked again")
	}

	old := compilerIdentity
	compilerIdentity = func() (string, error) { return "test-compiler", nil }
	t.Cleanup(func() { compilerIdentity = old })
	env := filepath.Join(t.TempDir(), "env")
	t.Setenv("GOENV", env)
	t.Setenv("GOEXPERIMENT", "")
	os.Unsetenv("GOEXPERIMENT")
	key := func(text string) string {
		if err := os.WriteFile(env, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		k := WarmKey("check", root)
		if k == "" {
			t.Fatal("WarmKey is empty: no shared compiler identity")
		}
		return k
	}
	if key("GOEXPERIMENT=simd\n") == key("") {
		t.Errorf("the warm key is the same with and without GOEXPERIMENT=simd in the go env file")
	}
}

// TestCheckCacheReadsAnEditedGoBody (U1 SIMD-SCAN): an edit of a module
// Go body with no .ergo file edited checks the program again: the check
// cache keeps the digest of each module Go package's built files with a
// checked package and takes it only while they are the same. Here the
// edit gives Index a wrong signature, which a fresh check reports (S108).
// Mutant: go-body-edit-unkeyed.
func TestCheckCacheReadsAnEditedGoBody(t *testing.T) {
	root := kernTree(t)
	t.Setenv("GOEXPERIMENT", "")
	cache := NewCheckCache()
	if a, err := cache.CheckDir(root); err != nil || diagnosticLines(a) != "" {
		t.Fatalf("first check: %v\n%s", err, diagnosticLines(a))
	}
	edited := "//go:build !goexperiment.simd\n\npackage kern\n\n//ergo:wait none\nfunc Index(s string) int { return -1 }\n"
	if err := os.WriteFile(filepath.Join(root, "kern", "a.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := cache.CheckDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnosticLines(a); !strings.Contains(got, "S108") {
		t.Errorf("an edited Go body was not read again; the check cache answered with the old one:\n%s", got)
	}
}

// TestLawKeyFollowsTheBuiltBody (U1 SIMD-SCAN): the store's key of a
// law's verdict (lawcache) takes the foreign key of the module Go body the
// law reaches, over the file go build compiles, so a verdict of a stock
// build, where the body is the stock twin, does not answer for a
// GOEXPERIMENT=simd build, where it is the vector file. The light run of
// modules/simd showed the miss: its laws in the vector build printed
// "(cached)" from the stock build's verdicts, so no kernel ran. Mutants:
// law-key-no-foreign (the key is the Ergo declarations' alone),
// foreign-key-all-files (the foreign key reads every file of the package).
func TestLawKeyFollowsTheBuiltBody(t *testing.T) {
	const body = "\npackage kern\n\n//ergo:wait none\nfunc Index(s string, c uint8) int { return %s }\n"
	root := writeTree(t, map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":       "module app\ntrust go app\n",
		"kern/stock.go":  "//go:build !goexperiment.simd\n" + fmt.Sprintf(body, "-1"),
		"kern/vector.go": "//go:build goexperiment.simd\n" + fmt.Sprintf(body, "0"),
		"scan/scan.ergo": "extern go example.com/app/kern.Index func index(s string, c byte) int\n\nlaw IndexIsSmall(s string, c byte) {\n\tindex(s, c) <= 0\n}\n",
	})
	key := func(experiment string) string {
		t.Setenv("GOEXPERIMENT", experiment)
		a, err := CheckDir(filepath.Join(root, "scan"))
		if err != nil {
			t.Fatal(err)
		}
		pkg := a.rootPackage()
		if pkg == nil {
			t.Fatalf("GOEXPERIMENT=%q: no checked package:\n%s", experiment, diagnosticLines(a))
		}
		targets := lower.RunTargets(pkg)
		if len(targets) != 1 {
			t.Fatalf("GOEXPERIMENT=%q: %d law runs, want 1", experiment, len(targets))
		}
		return targetKey(pkg, targets[0], 1, 0, runnerNative, foreignKeys(a, nil))
	}
	if stock, simd := key(""), key("simd"); stock == simd {
		t.Errorf("law IndexIsSmall has one verdict key in the stock build and the GOEXPERIMENT=simd build; want the built body's")
	}
}

// kernTree is a module whose package kern declares Index three times: in
// a.go for every build without GOEXPERIMENT=simd, and with a wrong
// signature, which S108 reports if the checker reads it, in a file for
// another GOOS by its name and in a file for GOEXPERIMENT=simd by its
// //go:build line.
func kernTree(t *testing.T) string {
	const good = "//go:build !goexperiment.simd\n\npackage kern\n\n//ergo:wait none\nfunc Index(s string, c uint8) int { return -1 }\n"
	const bad = "\npackage kern\n\n//ergo:wait none\nfunc Index(s string) int { return -1 }\n"
	files := map[string]string{
		"go.mod":          "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":        "module app\ntrust go app\n",
		"kern/a.go":       good,
		"kern/z_plan9.go": bad,
		"kern/z_simd.go":  "//go:build goexperiment.simd" + bad,
		"main.ergo":       "import strconv\n\nextern go example.com/app/kern.Index func index(s string, c byte) int\n\nfunc main() needs Console {\n\tConsole.Println(strconv.Itoa(index(\"ab\", 98)))\n}\n",
	}
	return writeTree(t, files)
}
