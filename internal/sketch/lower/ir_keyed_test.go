package lower

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"testing"
)

// TestIRDigestInItsPackage: every IR file of a lowering, split
// (LowerIRSplit), one package (LowerIR, LowerIRTests) or a library
// (LowerIRLibrary: `ergo go` builds it at a stable path), has its sha256 in
// a Go file of its own package (irKeyed). go build keys a compile on the
// Go files and the -ergoir flag, which names the IR file by a path
// relative to the module (build.Toolchain.Command), the same in every
// module an edit writes; without the digest an edited IR body would reuse
// the stale compile. It runs on genSplitProgram's programs and checks in
// process.
//
// Mutants: ir-keyed-none (irKeyed writes no digest); lib-ir-keyed-none
// (LowerIRLibrary returns its files without irKeyed: GO-DROPIN-PROBE
// step 10, an IR edited at the same path built the old body).
func TestIRDigestInItsPackage(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		u := genSplitProgram(seed).check(t)
		lowerings := map[string]func() ([]File, error){
			"split":       func() ([]File, error) { f, _, _, err := LowerIRSplit(u, false); return f, err },
			"split tests": func() ([]File, error) { f, _, _, err := LowerIRSplit(u, true); return f, err },
			"one":         func() ([]File, error) { f, _, err := LowerIR(u); return f, err },
			"one tests":   func() ([]File, error) { f, _, err := LowerIRTests(u); return f, err },
			"library":     func() ([]File, error) { f, _, err := LowerIRLibrary(u, "lib"); return f, err },
		}
		for name, lower := range lowerings {
			files, err := lower()
			if err != nil {
				t.Fatal(err)
			}
			irs := 0
			for _, ir := range files {
				if path.Base(ir.Path) != IRFileName {
					continue
				}
				irs++
				sum := sha256.Sum256(ir.Data)
				digest := []byte(hex.EncodeToString(sum[:]))
				found := false
				for _, f := range files {
					if path.Dir(f.Path) == path.Dir(ir.Path) && strings.HasSuffix(f.Path, ".go") && !strings.HasSuffix(f.Path, "_test.go") && bytes.Contains(f.Data, digest) {
						found = true
					}
				}
				if !found {
					t.Fatalf("seed %d %s: no Go file of %s's package holds its sha256 %s\nreplay: go test -run TestIRDigestInItsPackage ./internal/sketch/lower", seed, name, ir.Path, digest)
				}
			}
			if irs == 0 {
				t.Fatalf("seed %d %s: no IR file", seed, name)
			}
		}
	}
}
