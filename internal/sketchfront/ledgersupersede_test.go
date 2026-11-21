package sketchfront

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// needsBuild skips a test that builds Go outside the full tier.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// TestUpdateLedgerDropsSuperseded (LEDGER-SUPERSEDE): when an edit changes
// the form of a law's key, ergo test writes the new entry and drops the
// one it supersedes, so no stale line is left for a hand edit. Here kern's
// linux and darwin files give way to one kern.go (a closure key names
// each declaration's file, so two files never agree): the entry under
// this platform becomes one shared entry.
//
// It builds Go because the drop happens only in UpdateLedger, after the
// law run that builds and runs the law's Go; lower's
// TestLedgerDropsSuperseded is the rule's property, and this is its one
// call.
//
// Mutant (sketchfront mutants.json): lsd-superseded-kept.
func TestUpdateLedgerDropsSuperseded(t *testing.T) {
	needsBuild(t)
	t.Setenv("GOEXPERIMENT", "")
	const body = "package kern\n\nfunc Index(s []byte, c byte) int {\n\tfor i := 0; i < len(s); i++ {\n\t\tif s[i] == c {\n\t\t\treturn i %s\n\t\t}\n\t}\n\treturn -1\n}\n"
	dir := writeTree(t, map[string]string{
		"go.mod":              "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":            "module app\n",
		"kern/kern_linux.go":  fmt.Sprintf(body, ""),
		"kern/kern_darwin.go": fmt.Sprintf(body, "+ 0"),
		"main.ergo": `import strconv

extern go example.com/app/kern.Index func index(s []byte, c byte) int

func indexRef(s []byte, c byte) int {
	for i, b in s {
		if b == c {
			return i
		}
	}
	-1
}

law IndexIsRef(s []byte, c byte) { indexRef(s, c) } == { index(s, c) }

func main() needs Console {
	Console.Println(strconv.Itoa(indexRef([]byte{1, 2}, 2)))
}
`,
	})
	ledger := filepath.Join(dir, lower.LedgerFile)
	run := func() string {
		t.Helper()
		a, err := CheckDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); got != "" {
			t.Fatal(got)
		}
		var stderr strings.Builder
		if _, err := UpdateLedger(a, BuildRequest{Source: filepath.Join(dir, "main.ergo"), Ledger: ledger, Toolchain: build.Toolchain{Go: "go"}, Cache: build.Cache{Root: t.TempDir()}, Stderr: &stderr}); err != nil {
			t.Fatalf("ergo test: %v\n%s", err, stderr.String())
		}
		data, _ := os.ReadFile(ledger)
		return string(data)
	}
	keyed := "law app.IndexIsRef under " + gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}.String() + " validated "
	if got := run(); !strings.Contains(got, keyed) {
		t.Fatalf("ergo.laws:\n%s\nwant a line %q: kern's Index is another file on another platform", got, keyed)
	}
	// One kern.go replaces the pair: every platform compiles one file.
	for _, f := range []string{"kern/kern_linux.go", "kern/kern_darwin.go"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "kern/kern.go"), []byte(fmt.Sprintf(body, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	got := run()
	if !strings.Contains(got, "law app.IndexIsRef validated ") || strings.Contains(got, "app.IndexIsRef under") {
		t.Errorf("ergo.laws after kern's files agree:\n%s\nwant one shared line for app.IndexIsRef and the line under this platform gone", got)
	}
}

// TestExperimentKeysNameThemselves (LEDGER-EXP-KEY): under GOEXPERIMENT,
// every entry of Go code the keys read names the experiments, even where
// the stock build compiles the same files, so a stock and an experiment
// build never write or read one entry by accident; the stock build's key
// of the same law names none. The keys are read from the files, so it
// needs no Go built.
//
// Mutant (sketchfront mutants.json): lxk-experiment-unkeyed.
func TestExperimentKeysNameThemselves(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":       "module app\n",
		"plain/plain.go": "package plain\n\nfunc Twice(n int) int { return n + n }\n",
		"main.ergo":      "import strconv\n\nextern go example.com/app/plain.Twice func twice(n int) int\n\nfunc twiceRef(n int) int { n * 2 }\n\nlaw TwiceIsRef(n int) { twiceRef(n) } == { twice(n) }\n\nfunc main() needs Console {\n\tConsole.Println(strconv.Itoa(twiceRef(1)))\n}\n",
	})
	for _, c := range []struct{ experiment, want string }{
		{"", "app.TwiceIsRef"},
		{"simd", "app.TwiceIsRef under goexperiment.simd"},
	} {
		t.Setenv("GOEXPERIMENT", c.experiment)
		a, err := CheckDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); got != "" {
			t.Fatalf("GOEXPERIMENT=%q: %s", c.experiment, got)
		}
		for _, law := range a.rootPackage().Laws {
			if got := lower.FuncLawEntry(law, ledgerNames(a), "validated", foreignKeys(a, nil)).Key(); got != c.want {
				t.Errorf("GOEXPERIMENT=%q: law %s is keyed %q; want %q", c.experiment, law.Name, got, c.want)
			}
		}
	}
}
