package sketchfront

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// TestWarmKeyLeavesLocationsOut: two runs that differ only in a location
// variable (the budget wrappers give each run its own GOTMPDIR) have one
// warm key, and two that differ in an answer variable have two. The
// smallest repro of the miss the wrappers caused (2026-11-20: every warm
// answer through light.sh or heavy.sh was missed, never wrong). Mutants
// warm-key-reads-gotmpdir and warm-key-drops-goflags.
func TestWarmKeyLeavesLocationsOut(t *testing.T) {
	key := func(name, value string) string {
		t.Helper()
		t.Setenv(name, value)
		k := WarmKey("check", "/nonexistent/warm-env-probe")
		if k == "" {
			t.Fatal("WarmKey is empty: no shared compiler identity")
		}
		return k
	}
	if a, b := key("GOTMPDIR", "/nonexistent/run-a"), key("GOTMPDIR", "/nonexistent/run-b"); a != b {
		t.Errorf("two runs that differ only in GOTMPDIR have two warm keys")
	}
	if a, b := key("GOFLAGS", "-tags=a"), key("GOFLAGS", "-tags=b"); a == b {
		t.Errorf("two runs that differ in GOFLAGS have one warm key")
	}
	if a, b := key("ERGO_UNCLASSIFIED_PROBE", "a"), key("ERGO_UNCLASSIFIED_PROBE", "b"); a == b {
		t.Errorf("an unclassified variable is not in the warm key")
	}
	if !slices.Contains(WarmUnclassified(), "ERGO_UNCLASSIFIED_PROBE") {
		t.Errorf("WarmUnclassified = %v, want ERGO_UNCLASSIFIED_PROBE in it", WarmUnclassified())
	}
}

// TestWarmEnvClassifiesTheScripts: every GO*, CGO_* and ERGO_* variable
// the repository's scripts set (the budget wrappers, the gate) has a
// class in warmEnv, so a wrapper's new variable fails here until it is
// classified rather than splitting every warm key. Mutant
// warm-env-forgets-a-script-variable.
func TestWarmEnvClassifiesTheScripts(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../scripts/*.sh", "../../scripts/*/*.sh"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no script found")
	}
	set := regexp.MustCompile(`\b((?:GO|CGO_|ERGO_)[A-Z0-9_]+)=`)
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range set.FindAllStringSubmatch(string(data), -1) {
			if _, ok := warmEnv[m[1]]; !ok {
				t.Errorf("%s sets %s, which warmEnv does not classify (an answer or a location)", name, m[1])
			}
		}
	}
}
