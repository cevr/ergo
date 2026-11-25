package irspike

import (
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFallbackOffCorpusPackages is a fast, hermetic regression guard ensuring
// representative examples, compiler packages, and std packages lower with
// Unit.NoFallback = true, zero non-intrinsic fallbacks, and zero CodecText.
func TestFallbackOffCorpusPackages(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	examples := []struct {
		rel   string
		tests bool
		heavy bool
	}{
		{"examples/quiz/cmd/quiz", true, false},
		{"examples/cborpack/cmd/cborpack", false, true},
		{"examples/dash-gui/cmd/dash-gui", false, true},
		{"benchmarks/forms/cmd/irforms", false, false},
	}
	for _, ex := range examples {
		t.Run(ex.rel, func(t *testing.T) {
			if ex.heavy && testing.Short() {
				t.Skip("short mode: heavy example")
			}
			u, err := Load(filepath.Join(root, ex.rel))
			if err != nil {
				t.Fatal(err)
			}
			u.NoFallback = true
			_, rep, err := lower.LowerIR(u)
			if err != nil {
				t.Fatalf("LowerIR(NoFallback=true) failed: %v", err)
			}
			if len(rep.Fallback) != 0 {
				t.Fatalf("non-zero Fallback: %v", rep.Fallback)
			}
			if len(rep.CodecText) != 0 {
				t.Fatalf("non-zero CodecText: %v", rep.CodecText)
			}
			if ex.tests {
				_, rep, err := lower.LowerIRTests(u)
				if err != nil {
					t.Fatalf("LowerIRTests(NoFallback=true) failed: %v", err)
				}
				if len(rep.Fallback) != 0 || len(rep.CodecText) != 0 {
					t.Fatalf("tests fallback=%v codecText=%v", rep.Fallback, rep.CodecText)
				}
			}
		})
	}

	for _, rel := range []string{"compiler/format", "compiler/lower"} {
		t.Run(rel, func(t *testing.T) {
			a, err := sketchfront.CheckDir(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Diagnostics) > 0 {
				t.Fatalf("diagnostics: %v", a.Diagnostics)
			}
			u, err := sketchfront.IRSpikeUnit(a, "")
			if err != nil {
				t.Fatal(err)
			}
			u.NoFallback = true
			_, rep, err := lower.LowerIRLibrary(u, filepath.Base(rel))
			if err != nil {
				t.Fatalf("LowerIRLibrary(NoFallback=true) failed: %v", err)
			}
			if len(rep.Fallback) != 0 || len(rep.CodecText) != 0 {
				t.Fatalf("library fallback=%v codecText=%v", rep.Fallback, rep.CodecText)
			}
		})
	}

	for _, stdPkg := range []string{stdlib.PreludePath, "json"} {
		t.Run("std:"+stdPkg, func(t *testing.T) {
			a, _, err := sketchfront.CheckStd(stdPkg)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Diagnostics) > 0 {
				t.Fatalf("diagnostics: %v", a.Diagnostics)
			}
			u, err := sketchfront.IRSpikeUnit(a, "")
			if err != nil {
				t.Fatal(err)
			}
			u.NoFallback = true
			_, rep, err := lower.LowerIRTests(u)
			if err != nil {
				t.Fatalf("LowerIRTests(NoFallback=true) failed: %v", err)
			}
			if len(rep.Fallback) != 0 || len(rep.CodecText) != 0 {
				t.Fatalf("std tests fallback=%v codecText=%v", rep.Fallback, rep.CodecText)
			}
		})
	}
}
