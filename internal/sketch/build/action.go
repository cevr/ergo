package build

import (
	"os"
	"os/exec"
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
	// GOOS, GOARCH and GOFLAGS are set in the action's environment; an
	// empty value leaves Go's own default.
	GOOS, GOARCH, GOFLAGS string
}

// HostToolchain is the go command on PATH with the target and flags of the
// process environment, the action an unconfigured go build would run.
func HostToolchain() Toolchain {
	return Toolchain{Go: "go", GOOS: os.Getenv("GOOS"), GOARCH: os.Getenv("GOARCH"), GOFLAGS: os.Getenv("GOFLAGS")}
}

// Command is the Go build action that links the module's root package to
// output: go build with receipt.GoBuildFlags, outside any workspace, in the
// module directory, with exactly the toolchain's target and flags, and
// with cgo off: the Go bindings a program checks against are typed with
// CGO_ENABLED=0 (docs/spec/interop.md, Platforms), and a cgo-free link
// cross-compiles with no C toolchain for the target.
func (t Toolchain) Command(module Module, output string) *exec.Cmd {
	args := append([]string{"build"}, receipt.GoBuildFlags...)
	args = append(args, "-o", output, ".")
	child := exec.Command(t.Go, args...)
	child.Dir = module.Dir
	env := os.Environ()
	for _, kv := range [][2]string{{"GOWORK", "off"}, {"CGO_ENABLED", "0"}, {"GOOS", t.GOOS}, {"GOARCH", t.GOARCH}, {"GOFLAGS", t.GOFLAGS}} {
		prefix := kv[0] + "="
		env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, prefix) })
		if kv[1] != "" {
			env = append(env, prefix+kv[1])
		}
	}
	child.Env = env
	return child
}
