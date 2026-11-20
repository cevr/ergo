package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/toolchain"
)

// TestBuildIdentityCarriesLevel: the CPU level `ergo build --platform
// OS/ARCH/LEVEL` states (U3 CPU-LEVEL) reaches the Go build action's
// environment, as GOAMD64 or GOARM64 of the target's architecture, which
// the direct path's plan, its compile keys and its pin set names read
// (build/direct_plan.go makePlan, pin.go pinKey); and two levels are two
// warm requests, so a v3 build never answers with a v1 executable. No
// level leaves the action's environment and the warm key as before. A
// level the architecture has no such level of is an invalid invocation.
//
// Mutants (main ledger, level-*): actionFor drops the level; the action's
// environment keeps the process's level; the warm key leaves the level
// out; checkLevel admits every spelling.
func TestBuildIdentityCarriesLevel(t *testing.T) {
	t.Setenv("GOAMD64", "")
	t.Setenv("GOARM64", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte("func main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	levelOf := func(env []string, name string) string {
		i := slices.IndexFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
		if i < 0 {
			return ""
		}
		return strings.TrimPrefix(env[i], name+"=")
	}
	keys := map[string]string{}
	for _, c := range []struct{ platform, variable, level string }{
		{"linux/amd64", "GOAMD64", ""},
		{"linux/amd64/v1", "GOAMD64", "v1"},
		{"linux/amd64/v3", "GOAMD64", "v3"},
		{"darwin/arm64/v8.2,lse", "GOARM64", "v8.2,lse"},
	} {
		opts, err := parseFileOptions("build", []string{"--platform", c.platform, dir})
		if err != nil {
			t.Fatalf("--platform %s: %v", c.platform, err)
		}
		tc := actionFor(opts, toolchain.Toolchain{Go: "go"})
		env := tc.Command(build.Module{Dir: dir}, filepath.Join(dir, "out")).Env
		if got := levelOf(env, c.variable); got != c.level {
			t.Errorf("--platform %s: the Go build action has %s=%q, want %q", c.platform, c.variable, got, c.level)
		}
		w, _ := prepareWarm(opts, &tc)
		if w == nil {
			t.Fatalf("--platform %s: no warm request", c.platform)
		}
		keys[c.platform] = w.key
	}
	if keys["linux/amd64/v1"] == keys["linux/amd64/v3"] {
		t.Errorf("--platform linux/amd64/v1 and linux/amd64/v3 are one warm request")
	}
	for _, bad := range []string{"linux/amd64/v5", "linux/amd64/v8.0", "linux/arm64/v3", "linux/arm64/v8.0,lse,lse"} {
		if _, err := parseFileOptions("build", []string{"--platform", bad, dir}); err == nil || !strings.Contains(err.Error(), "is not a CPU level of") {
			t.Errorf("--platform %s: got %v, want the CPU level refusal", bad, err)
		}
	}
	// A phone module is linked by its packager, which picks the levels.
	if _, err := parseFileOptions("build", []string{"--platform", "ios/arm64/v8.2", dir}); err == nil || !strings.Contains(err.Error(), "takes no CPU level") {
		t.Errorf("--platform ios/arm64/v8.2: got %v, want the phone refusal", err)
	}
}
