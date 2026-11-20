package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// buildKeyKernel is the scan kernel written once per build: the stock
// file is the scalar loop, the GOEXPERIMENT=simd file the same answer by
// another loop, as modules/simd's stock.go and vector_amd64.go are. Each
// build compiles one, so each build's licence run validates its own.
const buildKeyKernel = `package gohost

func IndexByte(s []byte, c byte) int {
	%s
	return -1
}
`

// TestLawEntryNamesItsBuild (LAW-BUILD-KEY; runtime.md, Tests 10): a law
// ledger entry whose Go code differs by build configuration names the
// configuration in its key (`under goexperiment.simd`), so the stock
// build's licence and the GOEXPERIMENT=simd build's stand side by side
// in one ergo.laws and each build reads its own: after both builds
// validated the law, the site of the reference is rewritten in each.
// Found in lane SIMD's simd-scan, where the second build's licence run
// overwrote the first's, so the stock build's entry went stale.
//
// It builds Go because the licence run over a module Go body calls the
// compiled driver (TestModuleBodyPureByLaw).
//
// Mutants (sketchfront mutants.json): lbk-entry-unkeyed-build,
// lbk-build-on-every-entry.
func TestLawEntryNamesItsBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	stockLoop := "for i := 0; i < len(s); i++ {\n\t\tif s[i] == c {\n\t\t\treturn i\n\t\t}\n\t}"
	vectorLoop := "for i, b := range s {\n\t\tif b == c {\n\t\t\treturn i\n\t\t}\n\t}"
	dir := writeTree(t, map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":         "module app\n",
		"gohost/stock.go":  "//go:build !goexperiment.simd\n\n" + strings.Replace(buildKeyKernel, "%s", stockLoop, 1),
		"gohost/vector.go": "//go:build goexperiment.simd\n\n" + strings.Replace(buildKeyKernel, "%s", vectorLoop, 1),
		"main.ergo":        lawPureMain,
		// A law whose Go code is the same in every build keeps one
		// entry with no build in its key.
		"plain/plain.go": "package plain\n\nfunc Twice(n int) int { return n + n }\n",
		"plain.ergo":     "extern go example.com/app/plain.Twice func twice(n int) int\n\nfunc twiceRef(n int) int { n * 2 }\n\nlaw TwiceIsRef(n int) { twiceRef(n) } == { twice(n) }\n",
	})
	ledger := filepath.Join(dir, lower.LedgerFile)
	check := func(experiment string) Analysis {
		t.Helper()
		t.Setenv("GOEXPERIMENT", experiment)
		a, err := CheckDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); got != "" {
			t.Fatalf("GOEXPERIMENT=%q: %s", experiment, got)
		}
		return a
	}
	for _, experiment := range []string{"", "simd"} {
		a := check(experiment)
		var stderr strings.Builder
		if _, err := UpdateLedger(a, BuildRequest{Source: filepath.Join(dir, "main.ergo"), Ledger: ledger, Toolchain: build.Toolchain{Go: "go"}, Cache: build.Cache{Root: t.TempDir()}, Stderr: &stderr}); err != nil {
			t.Fatalf("GOEXPERIMENT=%q: ergo test: %v\n%s", experiment, err, stderr.String())
		}
	}
	data, _ := os.ReadFile(ledger)
	for _, want := range []string{"law app.IndexByteIsRef validated ", "law app.IndexByteIsRef under goexperiment.simd validated "} {
		if !strings.Contains(string(data), want) {
			t.Errorf("ergo.laws after a stock and a GOEXPERIMENT=simd ergo test:\n%s\nwant a line %q", data, want)
		}
	}
	if strings.Contains(string(data), "app.TwiceIsRef under") || !strings.Contains(string(data), "law app.TwiceIsRef validated ") {
		t.Errorf("ergo.laws:\n%s\nwant law app.TwiceIsRef validated once, with no build: its Go code is the same in both builds", data)
	}
	for _, experiment := range []string{"", "simd"} {
		a := check(experiment)
		sites, err := InspectRewrites(a, filepath.Join(dir, "main.ergo"), ledger)
		if err != nil {
			t.Fatalf("GOEXPERIMENT=%q: %v", experiment, err)
		}
		if got := RewritesText(sites); !strings.Contains(got, "current  applied") {
			t.Errorf("GOEXPERIMENT=%q: ergo inspect rewrites:\n%s\nwant the site of Find current and applied from its own build's entry", experiment, got)
		}
	}
}
