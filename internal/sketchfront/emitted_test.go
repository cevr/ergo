package sketchfront

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// TestEmittedDigestIsThePublishedModule is EmittedDigest's law: for each
// mode, it is the digest of the module Build publishes for the same
// request, every output by path and bytes (the examples runner's early
// cutoff keys on it, gate-speed GS8). The link is a go command that does
// nothing (true), so the test reads the module and builds no Go.
//
// Mutants it catches, which no other test does (internal/sketchfront's
// ledger): gs8-digest-no-runtime, gs8-digest-build-lowering.
func TestEmittedDigestIsThePublishedModule(t *testing.T) {
	noop, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true command")
	}
	analysis := Check("func main() needs Console {\n\tConsole.Println(\"main\")\n}\n\ntest Runs { assert 1 + 1 == 2 }\n")
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", analysis.Diagnostics)
	}
	cache := t.TempDir()
	for _, mode := range []build.Mode{build.ModeBuild, build.ModeTest} {
		var stderr bytes.Buffer
		req := BuildRequest{Mode: mode, Source: "/x/main.ergo", Output: filepath.Join(t.TempDir(), "program"), Cache: build.Cache{Root: cache}, Toolchain: build.Toolchain{Go: noop}, Stderr: &stderr}
		module, err := Build(analysis, req)
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, stderr.String())
		}
		h := sha256.New()
		fmt.Fprintf(h, "mode %s\n", mode)
		for _, out := range module.Manifest.Outputs { // sorted by path
			data, err := os.ReadFile(filepath.Join(module.Dir, filepath.FromSlash(out.Path)))
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%d:%s%d:", len(out.Path), out.Path, len(data))
			h.Write(data)
		}
		got, err := EmittedDigest(analysis, req)
		if err != nil {
			t.Fatal(err)
		}
		if want := hex.EncodeToString(h.Sum(nil)); got != want {
			t.Fatalf("%s: EmittedDigest %s, the published module's %s (%d outputs)", mode, got, want, len(module.Manifest.Outputs))
		}
	}
}
