package main

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/toolchain"
)

// TestRunAndTestLinkStripped is the one promised fact of compile-speed CS7:
// `ergo build`, `ergo run` and `ergo test` link without the symbol table
// and DWARF, and `--debug` keeps them. It reads the flags the direct
// path's link adds (build.Toolchain.LinkFlags) for each command's build
// action, in process; the build tests check that the link passes them
// (direct-strip-ignored) and keys on them (direct-key-strip). It kills
// cs-debug-flag-ignored and cs-strip-only-build. It reads the compile flags of
// the profile (build.Toolchain.CompileFlags) too, and kills
// ft-release-gets-l and ft-dwarf-flag-on-link-only (test-speed FT5).
func TestRunAndTestLinkStripped(t *testing.T) {
	for _, command := range []string{"build", "run", "test"} {
		for _, debug := range []bool{false, true} {
			args := []string{"main.ergo"}
			if debug {
				args = append([]string{"--debug"}, args...)
			}
			opts, err := parseFileOptions(command, args)
			if err != nil {
				t.Fatalf("ergo %s %v: %v", command, args, err)
			}
			action := actionFor(opts, toolchain.Toolchain{Go: "go"})
			got := action.LinkFlags()
			want := []string{"-s", "-w"}
			if debug {
				want = nil
			}
			if !slices.Equal(got, want) {
				t.Errorf("ergo %s %s: the link adds %q; want %q", command, strings.Join(args, " "), got, want)
			}
			// FT5: run and test compile with the dev profile, build and --debug
			// with the release one, whose flags hold no -l.
			var wantCompile []string
			if command != "build" && !debug {
				wantCompile = []string{"-l", "-dwarf=false"}
			}
			if gotCompile := action.CompileFlags(); !slices.Equal(gotCompile, wantCompile) {
				t.Errorf("ergo %s %s: the compile adds %q; want %q", command, strings.Join(args, " "), gotCompile, wantCompile)
			}
		}
	}
	if _, err := parseFileOptions("check", []string{"--debug", "main.ergo"}); err == nil || !strings.Contains(err.Error(), "--debug is only supported by build, run and test") {
		t.Errorf("ergo check --debug: %v, want the refusal", err)
	}
}
