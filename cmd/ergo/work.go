package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// workProgram is the program `ergo work` runs: the binary of module work
// (modules/work), built from examples/work. It is found on PATH, as
// git finds git-NAME: the module is Ergo, and until the mixed binary links
// an Ergo module into this Go one (capstone S1) the tool is its own binary.
const workProgram = "work"

const workHelp = `usage: ergo work COMMAND [ARGS...]

ergo work manages copy-on-write workspaces of one base checkout. It runs
work from PATH with the arguments; build it with
  ergo build examples/work/cmd/work -o DIR/work
for DIR on PATH. work --help lists its commands (init, new, adopt,
list, rm, gc, du).

Exit status: work's own; 1 when work is not on PATH.
`

// workCommand runs work with args, its standard streams this
// process's, and exits with its status.
func workCommand(args []string) error {
	path, err := exec.LookPath(workProgram)
	if err != nil {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo work: %s is not on PATH; build it with ergo build examples/work/cmd/work -o DIR/%s", workProgram, workProgram)}
	}
	child := exec.Command(path, args...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	err = child.Run()
	var exited *exec.ExitError
	if errors.As(err, &exited) && exited.ExitCode() > 0 {
		return exitStatusError{code: exited.ExitCode(), handled: true}
	}
	return err
}
