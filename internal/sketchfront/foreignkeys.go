package sketchfront

import (
	"cmp"
	"path/filepath"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// Foreign keys (lower/foreignkey.go; LAWFFI r2 §3.5, LFL): the ledger's
// pin of the Go code a law reaches. The keys are read from the module's
// go.mod and go.sum as the build reads them (gomod.go), the pinned
// toolchain, and, for a local replacement or go.mod's own module, the
// package's Go and assembly files, whose declarations the function
// reaches are keyed (lower/goclosure.go). Prior art: Go's own build cache
// keys an action by the toolchain and the hashes of its inputs, and go.sum
// pins a module by the h1: hash of its files; the ledger takes the same
// keys.

// goToolchain is the toolchain a key of Go's standard library names; a
// test replaces it, as a toolchain upgrade would.
var goToolchain = toolchain.Version

// foreignKeys are the foreign keys of a's program, its files read through
// fsys (the host's when nil).
func foreignKeys(a Analysis, fsys FS) lower.ForeignKeys {
	src := lower.ForeignKeySource{Toolchain: goToolchain}
	var platform gobind.Platform
	if a.program != nil {
		src.Module, platform = a.program.goModule, a.program.platform
	}
	if fsys == nil {
		fsys = hostFS{}
	}
	experiments := goExperiments()
	src.ReadDir = func(dir string) []lower.GoSourceFile { return readGoDir(fsys, dir, platform, experiments) }
	// The builds a ledger entry's Go code may differ in (LAW-BUILD-KEY):
	// each other supported platform, which this platform is named apart
	// from. A build with experiments names them in every key of Go code
	// it compares (LEDGER-EXP-KEY): it never shares an entry with the
	// stock build.
	for _, e := range experiments {
		src.Always = append(src.Always, "goexperiment."+e)
	}
	here := gobind.Platform{GOOS: cmp.Or(platform.GOOS, runtime.GOOS), GOARCH: cmp.Or(platform.GOARCH, runtime.GOARCH)}
	src.Target = &lower.GoTarget{GOOS: here.GOOS, GOARCH: here.GOARCH, Experiments: experiments}
	for _, other := range gobind.Platforms {
		if other != here {
			src.Variants = append(src.Variants, lower.BuildVariant{Tag: here.String(), Read: func(dir string) []lower.GoSourceFile { return readGoDir(fsys, dir, other, experiments) },
				Target: lower.GoTarget{GOOS: other.GOOS, GOARCH: other.GOARCH, Experiments: experiments}})
		}
	}
	return src.Keys()
}

// buildTags are this build's names in a key's build part: its platform
// and its Go experiments (foreignKeys' variants).
func buildTags(a Analysis) []string {
	var platform gobind.Platform
	if a.program != nil {
		platform = a.program.platform
	}
	tags := []string{gobind.Platform{GOOS: cmp.Or(platform.GOOS, runtime.GOOS), GOARCH: cmp.Or(platform.GOARCH, runtime.GOARCH)}.String()}
	for _, e := range goExperiments() {
		tags = append(tags, "goexperiment."+e)
	}
	return tags
}

// readGoDir is the Go and assembly files of the package in dir that go
// build compiles for platform and experiments (goBuilds), its tests
// excluded; nil when dir cannot be read. A function's body is the one the
// build compiles, so its key is too (U1 SIMD-SCAN): a law that reaches a
// vector kernel in a GOEXPERIMENT=simd build has another key than the same
// law over the stock twin, and neither verdict answers for the other.
func readGoDir(fsys FS, dir string, platform gobind.Platform, experiments []string) []lower.GoSourceFile {
	entries, err := fsys.ReadDir(filepath.FromSlash(dir))
	if err != nil {
		return nil
	}
	out := []lower.GoSourceFile{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasSuffix(n, "_test.go") || !strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, ".s") {
			continue
		}
		data, err := fsys.ReadFile(filepath.Join(filepath.FromSlash(dir), n))
		if err != nil {
			return nil
		}
		if !goBuilds(platform, experiments, n, data) {
			continue // go build leaves the file out for this target
		}
		out = append(out, lower.GoSourceFile{Name: n, Data: data})
	}
	return out
}
