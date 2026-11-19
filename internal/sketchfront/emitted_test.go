package sketchfront

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestEmittedDigestIsThePublishedModule is EmittedDigest's law: for each
// mode, it is the digest of the module Build publishes for the same
// request, every output by path and bytes but the runtime files it
// cannot link (the examples runner's early
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
		// The runtime files the module holds but cannot link are not in
		// the digest (lower.LinkedRuntime, CS-CUTOFF).
		var lowered []lower.File
		for _, out := range module.Manifest.Outputs {
			if !strings.HasPrefix(out.Path, "ergo/") {
				data, err := os.ReadFile(filepath.Join(module.Dir, filepath.FromSlash(out.Path)))
				if err != nil {
					t.Fatal(err)
				}
				lowered = append(lowered, lower.File{Path: out.Path, Data: data})
			}
		}
		linked := map[string]bool{}
		for _, f := range lower.LinkedRuntime(lower.Runtime(), lowered) {
			linked[f.Path] = true
		}
		h := sha256.New()
		fmt.Fprintf(h, "mode %s\n", mode)
		for _, out := range module.Manifest.Outputs { // sorted by path
			if strings.HasPrefix(out.Path, "ergo/") && !linked[out.Path] {
				continue
			}
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

// TestRuntimeEditOutsideTheLinkCutsOff is the cutoff the emitted digest
// gives (CI-SELECT CS-CUTOFF): for a program as built and as a test
// build, an edit of a runtime file changes its emitted digest exactly when
// the module can link the file (lower.LinkedRuntime), so an example keyed
// on it replays across a runtime edit of a package it does not import.
// Red at the base: the digest hashed the whole runtime, so every runtime
// edit reran every example. Mutant linked-runtime-is-whole.
func TestRuntimeEditOutsideTheLinkCutsOff(t *testing.T) {
	analysis := Check("func main() needs Console {\n\tConsole.Println(\"main\")\n}\n\ntest Runs { assert 1 + 1 == 2 }\n")
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", analysis.Diagnostics)
	}
	runtime := lower.Runtime()
	for _, mode := range []build.Mode{build.ModeBuild, build.ModeTest} {
		files, err := emittedFiles(analysis, BuildRequest{Mode: mode, Source: "/x/main.ergo"})
		if err != nil {
			t.Fatal(err)
		}
		linked := map[string]bool{}
		for _, f := range lower.LinkedRuntime(runtime, files) {
			linked[f.Path] = true
		}
		base := emittedDigest(mode, runtime, files)
		outside := 0
		for i, f := range runtime {
			edited := slices.Clone(runtime)
			edited[i] = lower.File{Path: f.Path, Data: append(slices.Clone(f.Data), "\n// an edit\n"...)}
			moved := emittedDigest(mode, edited, files) != base
			if moved != linked[f.Path] {
				t.Errorf("%s: an edit of %s moves the digest: %v; the module links it: %v", mode, f.Path, moved, linked[f.Path])
			}
			if !linked[f.Path] {
				outside++
			}
		}
		if outside == 0 {
			t.Errorf("%s: the program links every runtime file; the property needs one it does not", mode)
		}
		t.Logf("%s: %d of %d runtime files are outside the link", mode, outside, len(runtime))
	}
}
