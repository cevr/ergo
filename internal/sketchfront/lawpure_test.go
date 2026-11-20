package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// lawPureMain is lane SIMD's scan kernel bound as a module body, with its
// Ergo reference and the law that relates them, and a site of the
// reference.
const lawPureMain = `import strconv

extern go example.com/app/gohost.IndexByte func indexByte(s []byte, c byte) int

func indexByteRef(s []byte, c byte) int {
	for i, b in s {
		if b == c {
			return i
		}
	}
	-1
}

law IndexByteIsRef(s []byte, c byte) { indexByteRef(s, c) } == { indexByte(s, c) }

func Find(s []byte) int { indexByteRef(s, 7) }

func main() needs Console {
	Console.Println(strconv.Itoa(Find([]byte{1, 7, 7})))
}
`

// lawPureWrong is the kernel with a planted fault: the last match, not
// the first, which a case with two matches tells apart.
const lawPureWrong = `package gohost

func IndexByte(s []byte, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
`

// TestModuleBodyPureByLaw (LAWFFI LF2m; docs/spec/interop.md, Trusted Go
// 3; types.md, Laws 14): a module's own Go body, effect-free and bound
// with no trust line and no mark, is pure only by its law against an Ergo
// reference. The law is in the rewrite's scope (no S108); ergo test's
// licence run calls the Go body through its driver, validates the law,
// and the site of the reference is rewritten to the Go kernel on the IR
// path. With a planted fault in the body the licence run refutes the law
// and the site keeps its reference.
//
// It builds Go because the licence run's verdict over a Go body is the
// body's own run, which only the compiled driver gives (the evaluator has
// no native of the module's Go).
//
// Mutants (sketchfront mutants.json): lf2m-module-body-impure,
// lf2m-driver-ignored, lf2m-ir-module-call-refused,
// lf2m-effectful-untrusted-bound.
func TestModuleBodyPureByLaw(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	for _, tc := range []struct {
		name, kernel, verdict, site string
	}{
		{"right", scanKernel, "validated", "current  applied"},
		{"wrong", lawPureWrong, "refuted", "refuted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{
				"go.mod":           "module example.com/app\n\ngo 1.27\n",
				"ergo.mod":         "module app\n",
				"gohost/gohost.go": tc.kernel,
				"main.ergo":        lawPureMain,
			})
			a, err := CheckDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := diagnosticLines(a); got != "" {
				t.Fatalf("the law over the module body is not in the rewrite's scope:\n%s", got)
			}
			ledger := filepath.Join(dir, lower.LedgerFile)
			var stderr strings.Builder
			_, err = UpdateLedger(a, BuildRequest{Source: filepath.Join(dir, "main.ergo"), Ledger: ledger, Toolchain: build.Toolchain{Go: "go"}, Cache: build.Cache{Root: t.TempDir()}, Stderr: &stderr})
			if err != nil && tc.verdict == "validated" {
				t.Fatalf("ergo test: %v\n%s", err, stderr.String())
			}
			data, _ := os.ReadFile(ledger)
			if !strings.Contains(string(data), "law app.IndexByteIsRef "+tc.verdict) {
				t.Fatalf("ergo.laws:\n%s\nwant app.IndexByteIsRef %s; ergo test said:\n%s", data, tc.verdict, stderr.String())
			}
			sites, err := InspectRewrites(a, filepath.Join(dir, "main.ergo"), ledger)
			if err != nil && tc.verdict == "validated" {
				t.Fatal(err)
			}
			got := RewritesText(sites)
			if !strings.Contains(got, "app.IndexByteIsRef") || !strings.Contains(got, tc.site) {
				t.Errorf("ergo inspect rewrites:\n%s\nwant the site of Find %s", got, tc.site)
			}
		})
	}
}
