package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFallbackDefaultRefusesFallback verifies that IR builds and tests refuse
// text fallbacks by default (IR-FALLBACK-OFF-ENFORCE), while -fallback=on and
// ERGO_FALLBACK=on explicitly allow text fallback for differential testing.
//
// Mutants (testdata/mutants.json): fallback-default-on,
// fallback-test-mode-dropped.
func TestFallbackDefaultRefusesFallback(t *testing.T) {
	t.Setenv("ERGO_FALLBACK", "")

	if !fallbackOff(fileOptions{}) {
		t.Fatal("default fallbackOff should be true when ERGO_FALLBACK is unset")
	}
	if fallbackKey(fileOptions{}) != "off" {
		t.Fatalf("default fallbackKey = %q, want \"off\"", fallbackKey(fileOptions{}))
	}
	if fallbackOff(fileOptions{fallback: "on"}) {
		t.Fatal("-fallback=on should allow fallback")
	}
	if fallbackKey(fileOptions{fallback: "on"}) != "on" {
		t.Fatalf("-fallback=on fallbackKey = %q, want \"on\"", fallbackKey(fileOptions{fallback: "on"}))
	}
	if !fallbackOff(fileOptions{fallback: "off"}) {
		t.Fatal("-fallback=off should refuse fallback")
	}

	t.Setenv("ERGO_FALLBACK", "on")
	if fallbackOff(fileOptions{}) {
		t.Fatal("ERGO_FALLBACK=on should allow fallback when no flag is given")
	}
	if fallbackKey(fileOptions{}) != "on" {
		t.Fatalf("ERGO_FALLBACK=on fallbackKey = %q, want \"on\"", fallbackKey(fileOptions{}))
	}
	if !fallbackOff(fileOptions{fallback: "off"}) {
		t.Fatal("explicit -fallback=off must override ERGO_FALLBACK=on")
	}

	t.Setenv("ERGO_FALLBACK", "off")
	if fallbackOff(fileOptions{fallback: "on"}) {
		t.Fatal("explicit -fallback=on must override ERGO_FALLBACK=off")
	}

	t.Setenv("ERGO_FALLBACK", "")
	for _, cmd := range []string{"build", "run", "test"} {
		for _, val := range []string{"on", "off"} {
			opts, err := parseFileOptions(cmd, []string{"-fallback=" + val, "p.ergo"})
			if err != nil {
				t.Fatalf("parseFileOptions(%q, -fallback=%s): %v", cmd, val, err)
			}
			if opts.fallback != val {
				t.Fatalf("parseFileOptions(%q, -fallback=%s).fallback = %q", cmd, val, opts.fallback)
			}
		}
	}
	if _, err := parseFileOptions("build", []string{"-fallback=maybe", "p.ergo"}); err == nil {
		t.Fatal("expected invalid -fallback=maybe to fail")
	}
	if _, err := parseFileOptions("check", []string{"-fallback=on", "p.ergo"}); err == nil {
		t.Fatal("expected -fallback=on on check to fail")
	}

	const stops = `type Ids []int

func First(xs Ids) int {
	xs[0]
}

func main() {
	let _ = First(Ids([]int{1}))
}

test FirstReturnsHead {
	assert First(Ids([]int{7})) == 7
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module fallbackcheck\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(stops), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := load(srcPath, report.Target)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []build.Mode{build.ModeBuild, build.ModeTest} {
		req := emissionRequest(fileOptions{file: srcPath}, l, mode)
		if !req.NoFallback {
			t.Fatalf("emissionRequest(%v) default NoFallback = false, want true", mode)
		}
		reqOn := emissionRequest(fileOptions{file: srcPath, fallback: "on"}, l, mode)
		if reqOn.NoFallback {
			t.Fatalf("emissionRequest(%v, -fallback=on) NoFallback = true, want false", mode)
		}
	}

	cache := build.Cache{Root: filepath.Join(t.TempDir(), "cache")}
	for _, mode := range []build.Mode{build.ModeBuild, build.ModeTest} {
		req := emissionRequest(fileOptions{file: srcPath}, l, mode)
		req.Output = filepath.Join(t.TempDir(), "out")
		req.Cache = cache
		_, err := sketchfront.Build(l.analysis, req)
		var refused *lower.FallbackRefused
		if !errors.As(err, &refused) {
			t.Fatalf("default sketchfront.Build(%v) did not refuse fallback: %v", mode, err)
		}
		if !strings.Contains(err.Error(), "First") || !strings.Contains(err.Error(), "type defined over a non-basic type") {
			t.Fatalf("default sketchfront.Build(%v) refusal missing First/reason:\n%s", mode, err.Error())
		}
	}

	analyses, err := sketchfront.CheckPackages([]string{dir}, gobind.Platform{})
	if err != nil {
		t.Fatalf("CheckPackages: %v", err)
	}
	_, err = sketchfront.BuildTests(analyses, sketchfront.BuildRequest{
		Output:     filepath.Join(t.TempDir(), "shared.test"),
		Cache:      cache,
		IR:         true,
		NoFallback: fallbackOff(fileOptions{}),
	})
	var refused *lower.FallbackRefused
	if !errors.As(err, &refused) {
		t.Fatalf("default sketchfront.BuildTests did not refuse fallback: %v", err)
	}
	if !strings.Contains(err.Error(), "First") {
		t.Fatalf("default sketchfront.BuildTests refusal missing First:\n%s", err.Error())
	}

	u, err := sketchfront.IRSpikeUnit(l.analysis, l.source.Admitted())
	if err != nil {
		t.Fatal(err)
	}
	u.NoFallback = fallbackOff(fileOptions{fallback: "on"})
	if _, _, err := lower.LowerIR(u); err != nil {
		t.Fatalf("-fallback=on lowering failed unexpectedly: %v", err)
	}
}
