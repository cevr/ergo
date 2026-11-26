// Package ergobuild is the one place that says how the ergo tool itself is
// built: every build of cmd/ergo in this repository (the gate's bin/ergo,
// the tests' CLI, the mutant runner's tool, the receipts' scripts) takes its
// command or its environment from here. internal/residue refuses a build
// of cmd/ergo that does not (TestErgoBuildsReadOneSetting).
//
// The tool is built without cgo, a static binary: ergo itself needs no C
// (only runtime/cgo, net and os/user import "C", and net and os/user have
// pure Go forms), and a dynamically linked ergo pays the dynamic loader and
// cgo's thread setup on every start, 0.46 M instructions of `ergo version`'s
// 3.10 M (ERGO-STARTUP, measured at 5f4c5ba24). Without cgo, Go's own
// resolver answers host names on Linux (no nsswitch modules) and os/user
// reads /etc/passwd and /etc/group. This is the ergo tool only: the
// programs ergo builds keep their own cgo setting (the GUI hosts need it).
//
// The tool is built reproducibly (Flags): the same sources give the same
// bytes in every checkout, so every workspace of a repository runs one
// compiler identity (internal/producer: the digest of the executing
// image), and the answers each stores under that identity (law verdicts,
// warm answers, test results) serve all of them; Go's build cache serves
// the second checkout's build of the tool as well (SHARED-RESULT-CACHE:
// the same tree in a second checkout built cmd/ergo in 3.8 s against
// 66.1 s, and `ergo check compiler/syntax` there took 9.17 G instructions
// against 21.09 G, measured at b76bd4d80).
package ergobuild

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Cgo is the environment entry of every build of the ergo tool.
const Cgo = "CGO_ENABLED=0"

// Flags are the build flags of every build of the ergo tool: -trimpath
// writes the checkout's folder out of the binary, -buildvcs=false its
// commit, so the binary is a function of its sources and the Go release.
// A binary without a GOROOT of its own types Go's standard library from
// the release its builds run (sketchfront.UseStdCache).
var Flags = []string{"-trimpath", "-buildvcs=false"}

// Env is env with Cgo in place of any CGO_ENABLED entry; nil env is the
// process's environment.
func Env(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "CGO_ENABLED=") {
			out = append(out, entry)
		}
	}
	return append(out, Cgo)
}

// Command is `go build Flags FLAGS -o out pkg`, pkg the path of cmd/ergo
// as the caller's directory sees it, in the process's environment with
// Cgo. A caller that sets its own Env passes it through Env.
func Command(out, pkg string, flags ...string) *exec.Cmd {
	return CommandContext(context.Background(), out, pkg, flags...)
}

// CommandContext is Command, killed when ctx is done.
func CommandContext(ctx context.Context, out, pkg string, flags ...string) *exec.Cmd {
	args := append(append(append([]string{"build"}, Flags...), flags...), "-o", out, pkg)
	command := exec.CommandContext(ctx, "go", args...)
	command.Env = Env(nil)
	return command
}
