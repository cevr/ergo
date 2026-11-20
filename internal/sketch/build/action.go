package build

import (
	"cmp"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"ergo.local/prototype/internal/receipt"
)

// Toolchain is the go command and the target environment of the Go build
// action. The action always runs: Go's own action cache, keyed by the
// toolchain's identity, the target, the flags and every compiled file,
// decides what to recompile and relink. So a change that keeps the
// emitted module (another target, other flags, another toolchain, or the
// implementation of a package the module links) still produces the
// executable of the changed action, and the module is reused as it is.
type Toolchain struct {
	// Go is the go command, a path or a name looked up on PATH.
	Go string
	// Root is the GOROOT of a managed toolchain (internal/toolchain), or
	// empty. When set, the action runs with GOROOT=Root and
	// GOTOOLCHAIN=local, so neither the user's GOROOT nor a toolchain
	// switch picks another Go than the pinned one.
	Root string
	// GOOS, GOARCH and GOFLAGS are set in the action's environment; an
	// empty value leaves Go's own default.
	GOOS, GOARCH, GOFLAGS string
	// Level is the target's CPU level: GOAMD64 (v1 to v4) on amd64,
	// GOARM64 (v8.0, v9.2,lse, …) on arm64, set in the action's
	// environment under the variable of GOARCH (LevelVariable); empty
	// leaves the environment's, else Go's default (v1, v8.0). It chooses
	// instructions and runtime files, so it keys every compile and the
	// pin sets (direct.go toolchainKey, pin.go pinKey), never the emitted
	// module: the lowering writes code whose results do not depend on it
	// (a float product rounds, ir_order.go rounded).
	Level string
	// PGO is the absolute path of a CPU profile the build compiles with
	// (go build -pgo; U4 PGO-RELEASE), from `ergo build --release`; empty
	// is none. A profile rebuilds every package of the program with it,
	// std and the runtime too, so such a build takes no pin set and goes
	// through go build (the direct path refuses it); the emitted module is
	// the same with and without one.
	PGO string
	// Tags are the build tags of the action (-tags), such as ergoexplore
	// for `ergo test --explore` (runtime.md, Explored schedules). They
	// choose runtime files at link time and are no input of the lowering:
	// the emitted module is the same with and without them.
	Tags []string
	// GoBuild selects go build (Command), the reference path, over the
	// direct path of Build (direct.go).
	GoBuild bool
	// Strip links without the symbol table and DWARF (`-ldflags=-s -w`):
	// the default of `ergo build`, `run` and `test`, which `--debug` turns
	// off (compile-speed CS7). The pclntab
	// stays, so tracebacks and the defect report do not change. The flag
	// stands first in the action's GOFLAGS, so an -ldflags of the user's
	// own GOFLAGS replaces it, as a later flag does in the go command.
	// The direct path passes -s -w to the linker where go build does.
	Strip bool
	// Cover is the directory of coverage builds (CoverVariable, cover.go),
	// or empty: Build then links the module as it is.
	Cover string
	// Pins are the pin archive sets the direct path may take std's and
	// the runtime's archives from (pin.go); the zero value is none.
	Pins Pins
	// Profile is how the module's own packages compile: Release, as go
	// build does, or Dev, with inlining off and no DWARF (test-speed FT5).
	Profile Profile
	// Stored builds only from the direct path's store: a compile or a
	// link the store does not hold, or a refusal of the direct path, ends
	// the build with ErrNotStored, and no tool or go command runs
	// (TEQ: `ergo test --count 1 ./...` reuses the packages' own stored
	// test binaries).
	Stored bool
}

// Profile is a build's compile profile (test-speed FT5).
type Profile string

const (
	// Release compiles every package as go build does: the profile of
	// `ergo build`, and of any build with --debug.
	Release Profile = ""
	// Dev compiles the packages of the module's directory with inlining
	// off and no DWARF (-l -dwarf=false; about -40 % of a compile,
	// test-speed plan §3.3a): the profile of `ergo run` and `ergo test`.
	// The emitted module skips go/format too (sketchfront). The standard
	// library compiles as in Release, so its archives are shared.
	Dev Profile = "dev"
)

// devGcflags is the -gcflags value of the Dev profile: its compile flags
// for every package whose directory is in the module's directory, as go
// build matches the pattern ./... against a package's directory.
const devGcflags = "./...=-l -dwarf=false"

// CompileFlags are the flags the profile adds to the compile of a package
// of the module's directory: none for Release.
func (t Toolchain) CompileFlags() []string {
	if t.Profile == Dev {
		return []string{"-l", "-dwarf=false"}
	}
	return nil
}

// stripFlags is the GOFLAGS word of a stripped link, quoted as the go
// command splits GOFLAGS.
const stripFlags = `'-ldflags=-s -w'`

// HostToolchain is the go command on PATH with the target and flags of the
// process environment, the action an unconfigured go build would run. It
// takes the direct path unless ERGO_GO_BUILD=go selects go build.
func HostToolchain() Toolchain {
	return Toolchain{Go: "go", GOOS: os.Getenv("GOOS"), GOARCH: os.Getenv("GOARCH"), GOFLAGS: os.Getenv("GOFLAGS"), GoBuild: os.Getenv(GoBuildVariable) == "go", Cover: os.Getenv(CoverVariable)}
}

// LevelVariable is the environment variable of goarch's CPU level, or
// empty for an architecture without one Ergo builds for.
func LevelVariable(goarch string) string {
	switch goarch {
	case "amd64":
		return "GOAMD64"
	case "arm64":
		return "GOARM64"
	}
	return ""
}

// DefaultLevel is Go's default CPU level of goarch: what a build with no
// level stated targets.
func DefaultLevel(goarch string) string {
	switch goarch {
	case "amd64":
		return "v1"
	case "arm64":
		return "v8.0"
	}
	return ""
}

// TargetLevel is the CPU level the toolchain builds for: Level, else the
// environment's variable of the target's architecture, else Go's default.
// It names the build in every key that holds the target (the warm answer,
// a receipt).
func (t Toolchain) TargetLevel() string {
	goarch := cmp.Or(t.GOARCH, os.Getenv("GOARCH"), runtime.GOARCH)
	if v := LevelVariable(goarch); v != "" {
		return cmp.Or(t.Level, os.Getenv(v), DefaultLevel(goarch))
	}
	return ""
}

// Command is the Go build action that links the module's root package to
// output: go build with receipt.GoBuildFlags, outside any workspace, in the
// module directory, with exactly the toolchain's target and flags, and
// with cgo off: the Go bindings a program checks against are typed with
// CGO_ENABLED=0 (docs/spec/interop.md, Platforms), and a cgo-free link
// cross-compiles with no C toolchain for the target. The strip flags stand
// first in GOFLAGS when Strip is set, then the Dev profile's -gcflags.
func (t Toolchain) Command(module Module, output string) *exec.Cmd {
	args := append([]string{"build"}, receipt.GoBuildFlags...)
	if len(t.Tags) > 0 {
		args = append(args, "-tags", strings.Join(t.Tags, ","))
	}
	if t.PGO != "" {
		args = append(args, "-pgo="+t.PGO)
	}
	for _, rel := range irDirs(module) {
		// The pattern "." is the package in the module directory, the
		// root, and "./D" the package in its folder D; the quotes keep a
		// path with a space one word. Go's action cache keys the compile
		// on the flag and the Go files, never on the IR file, so the flag
		// names it relative to the module's folder, where go build runs
		// the compile: the same in every module an edit writes. The IR's
		// digest is in a Go file of its package (lower.irKeyed), so its
		// content changes the key (GP-SPLIT-EDIT).
		ir := ergoIR(filepath.Join(module.Dir, rel))
		args = append(args, "-gcflags="+irPattern(rel)+"='"+ir[0]+"="+irPattern(path.Join(rel, IRFile))+"'")
	}
	args = append(args, "-o", output, ".")
	child := exec.Command(t.Go, args...)
	child.Dir = module.Dir
	goflags := t.GOFLAGS
	if t.Profile == Dev {
		goflags = strings.TrimSpace("'-gcflags=" + devGcflags + "' " + goflags)
	}
	if t.Strip {
		goflags = strings.TrimSpace(stripFlags + " " + goflags)
	}
	child.Env = t.env(goflags)
	return child
}

// IRFile is the typed IR of a package of a module (lower.IRFileName), in
// the package's folder: the root's beside its go.mod.
const IRFile = "ergoir.json"

// ergoIR is the compiler flag of the package in folder dir that reads its
// IR bodies, `-ergoir FILE`, which only ergo's patched compile knows
// (internal/toolchain, Patch); nil for a package without IR.
func ergoIR(dir string) []string {
	file := filepath.Join(dir, IRFile)
	if _, err := os.Stat(file); err != nil {
		return nil
	}
	return []string{"-ergoir", file}
}

// irDirs are the folders of the module's packages that have an IR file,
// relative to the module's folder ("." the root), in path order.
func irDirs(module Module) []string {
	var dirs []string
	filepath.WalkDir(module.Dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == IRFile {
			if rel, err := filepath.Rel(module.Dir, filepath.Dir(p)); err == nil {
				dirs = append(dirs, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	slices.Sort(dirs)
	return dirs
}

// irPattern is the go command's package pattern of the folder rel.
func irPattern(rel string) string {
	if rel == "." {
		return "."
	}
	return "./" + rel
}

// env is the environment of the go command for this toolchain: the
// process's, outside any workspace, with cgo off, exactly the toolchain's
// target, and goflags as GOFLAGS.
func (t Toolchain) env(goflags string) []string {
	env := os.Environ()
	set := [][2]string{{"GOWORK", "off"}, {"CGO_ENABLED", "0"}, {"GOOS", t.GOOS}, {"GOARCH", t.GOARCH}, {"GOFLAGS", goflags}}
	if t.Root != "" {
		set = append(set, [2]string{"GOROOT", t.Root}, [2]string{"GOTOOLCHAIN", "local"})
	}
	if v := LevelVariable(cmp.Or(t.GOARCH, os.Getenv("GOARCH"), runtime.GOARCH)); v != "" && t.Level != "" {
		set = append(set, [2]string{v, t.Level})
	}
	for _, kv := range set {
		prefix := kv[0] + "="
		env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, prefix) })
		if kv[1] != "" {
			env = append(env, prefix+kv[1])
		}
	}
	return env
}
