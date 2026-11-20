package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/toolchain"
)

// TestReleaseBuildUsesTheProfile: `ergo build --release DIR` compiles
// with DIR/default.pgo (go build -pgo, U4 PGO-RELEASE), through go build
// and never the direct path, whose pin sets hold std and the runtime
// compiled without it; the profile's bytes key the warm request; a build
// without --release carries no profile even when default.pgo is there;
// and --release without a profile stops, naming how to write one.
//
// Mutants (main ledger, release-*): actionFor drops the profile; the
// default build takes default.pgo; the direct path builds a profiled
// toolchain; the warm key leaves the profile's bytes out.
func TestReleaseBuildUsesTheProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte("func main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "default.pgo")
	if _, err := releaseProfile(dir); err == nil || !strings.Contains(err.Error(), "scripts/opcount -profile") {
		t.Errorf("--release with no default.pgo: got %v, want the refusal that names scripts/opcount -profile", err)
	}
	pgoArg := func(tc build.Toolchain) string {
		for _, a := range tc.Command(build.Module{Dir: dir}, filepath.Join(dir, "out")).Args {
			if strings.HasPrefix(a, "-pgo") {
				return a
			}
		}
		return ""
	}
	toolchainOf := func(args ...string) (fileOptions, build.Toolchain) {
		t.Helper()
		opts, err := parseFileOptions("build", append(args, dir))
		if err != nil {
			t.Fatal(err)
		}
		if opts.release {
			if opts.profile, err = releaseProfile(dir); err != nil {
				t.Fatal(err)
			}
		}
		return opts, actionFor(opts, toolchain.Toolchain{Go: "go"})
	}
	keys := map[string]string{}
	for _, c := range []struct{ name, bytes string }{{"a", "profile a"}, {"b", "profile b"}} {
		if err := os.WriteFile(profile, []byte(c.bytes), 0o644); err != nil {
			t.Fatal(err)
		}
		opts, tc := toolchainOf("--release")
		abs, _ := filepath.Abs(profile)
		if got := pgoArg(tc); got != "-pgo="+abs {
			t.Errorf("--release: the Go build action has %q, want -pgo=%s", got, abs)
		}
		w, _ := prepareWarm(opts, &tc)
		if w == nil {
			t.Fatal("--release: no warm request")
		}
		keys[c.name] = w.key
		plainOpts, plain := toolchainOf()
		if got := pgoArg(plain); got != "" {
			t.Errorf("a build without --release has %q beside default.pgo, want no profile", got)
		}
		pw, _ := prepareWarm(plainOpts, &plain)
		if pw == nil || pw.key == w.key {
			t.Errorf("profile %s: the build with --release and without are one warm request", c.name)
		}
		report, _ := build.Toolchain{Go: "/nonexistent/loop-probe-x", PGO: abs}.Build(build.Cache{Root: t.TempDir()}, build.Module{Dir: dir}, filepath.Join(dir, "out"), io.Discard)
		if report.Direct || !strings.Contains(report.Refused, "PGO") {
			t.Errorf("a profiled build: direct %t, refused %q; want go build, refused for its PGO profile", report.Direct, report.Refused)
		}
	}
	if keys["a"] == keys["b"] {
		t.Error("two profiles are one warm request")
	}
	if _, err := parseFileOptions("run", []string{"--release", dir}); err == nil {
		t.Error("ergo run --release: want the refusal, --release is build's")
	}
	// A phone module's packager links it; the profile would be dropped.
	if err := buildCommand([]string{"--release", "--platform", "ios/arm64", dir}); err == nil || !strings.Contains(err.Error(), "phone platform") {
		t.Errorf("ergo build --release --platform ios/arm64: got %v, want the phone refusal", err)
	}
}
