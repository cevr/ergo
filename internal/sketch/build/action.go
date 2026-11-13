package build

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	// Tags are the build tags of the action (-tags), such as ergoexplore
	// for `ergo test --explore` (runtime.md, Explored schedules). They
	// choose runtime files at link time and are no input of the lowering:
	// the emitted module is the same with and without them.
	Tags []string
	// GoBuild selects go build (Command), the reference path, over the
	// direct path of Build (direct.go).
	GoBuild bool
	// Strip links without the symbol table and DWARF (`-ldflags=-s -w`):
	// the default of `ergo build`, which `--debug` turns off. The pclntab
	// stays, so tracebacks and the defect report do not change. The flag
	// stands first in the action's GOFLAGS, so an -ldflags of the user's
	// own GOFLAGS replaces it, as a later flag does in the go command.
	// The direct path passes -s -w to the linker where go build does.
	Strip bool
	// Pins are the pin archive sets the direct path may take std's and
	// the runtime's archives from (pin.go); the zero value is none.
	Pins Pins
}

// stripFlags is the GOFLAGS word of a stripped link, quoted as the go
// command splits GOFLAGS.
const stripFlags = `'-ldflags=-s -w'`

// HostToolchain is the go command on PATH with the target and flags of the
// process environment, the action an unconfigured go build would run. It
// takes the direct path unless ERGO_GO_BUILD=go selects go build.
func HostToolchain() Toolchain {
	return Toolchain{Go: "go", GOOS: os.Getenv("GOOS"), GOARCH: os.Getenv("GOARCH"), GOFLAGS: os.Getenv("GOFLAGS"), GoBuild: os.Getenv(GoBuildVariable) == "go"}
}

// Command is the Go build action that links the module's root package to
// output: go build with receipt.GoBuildFlags, outside any workspace, in the
// module directory, with exactly the toolchain's target and flags, and
// with cgo off: the Go bindings a program checks against are typed with
// CGO_ENABLED=0 (docs/spec/interop.md, Platforms), and a cgo-free link
// cross-compiles with no C toolchain for the target. The strip flags stand
// first in GOFLAGS when Strip is set.
func (t Toolchain) Command(module Module, output string) *exec.Cmd {
	args := append([]string{"build"}, receipt.GoBuildFlags...)
	if len(t.Tags) > 0 {
		args = append(args, "-tags", strings.Join(t.Tags, ","))
	}
	for _, rel := range irDirs(module) {
		// The pattern "." is the package in the module directory, the
		// root, and "./D" the package in its folder D; the quotes keep a
		// path with a space one word. Go's action cache keys the compile
		// on the flag, whose path is in the module's folder, named by
		// the module's key.
		ir := ergoIR(filepath.Join(module.Dir, rel))
		args = append(args, "-gcflags="+irPattern(rel)+"='"+ir[0]+"="+ir[1]+"'")
	}
	args = append(args, "-o", output, ".")
	child := exec.Command(t.Go, args...)
	child.Dir = module.Dir
	goflags := t.GOFLAGS
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
	for _, kv := range set {
		prefix := kv[0] + "="
		env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, prefix) })
		if kv[1] != "" {
			env = append(env, prefix+kv[1])
		}
	}
	return env
}
