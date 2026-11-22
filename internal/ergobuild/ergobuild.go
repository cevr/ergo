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
package ergobuild

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Cgo is the environment entry of every build of the ergo tool.
const Cgo = "CGO_ENABLED=0"

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

// Command is `go build FLAGS -o out pkg`, pkg the path of cmd/ergo as the
// caller's directory sees it, in the process's environment with Cgo. A
// caller that sets its own Env passes it through Env.
func Command(out, pkg string, flags ...string) *exec.Cmd {
	return CommandContext(context.Background(), out, pkg, flags...)
}

// CommandContext is Command, killed when ctx is done.
func CommandContext(ctx context.Context, out, pkg string, flags ...string) *exec.Cmd {
	args := append(append([]string{"build"}, flags...), "-o", out, pkg)
	command := exec.CommandContext(ctx, "go", args...)
	command.Env = Env(nil)
	return command
}
