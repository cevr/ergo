//go:build unix

package budget

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
)

// Run takes req's cores, runs argv in them and gives them back: `ergo
// budget -- <cmd>`. The command runs with GOMAXPROCS set to the width and
// the grant lent (GrantEnv), in its own process group, which Run kills
// when the command ends, so no process the command left behind keeps
// working outside the budget (a leaked process group). On Linux the
// command also dies with Run (Pdeathsig). SIGINT and SIGTERM go to the
// group. The result is the command's exit code, 128 plus the signal for
// one a signal ended.
func Run(ctx context.Context, b *Budget, req Request, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return 2, errors.New("budget: no command")
	}
	g, err := b.Take(ctx, req)
	if err != nil {
		return 1, err
	}
	defer g.Release()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(req.Width), g.Env())
	cmd.SysProcAttr = groupAttr()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 127, err
	}
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	for waiting := true; waiting; {
		select {
		case s := <-signals:
			syscall.Kill(-pgid, s.(syscall.Signal))
		case werr = <-done:
			waiting = false
		}
	}
	// The group this Run started, and only it.
	syscall.Kill(-pgid, syscall.SIGKILL)
	var exit *exec.ExitError
	if errors.As(werr, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	if werr != nil {
		return 1, werr
	}
	return 0, nil
}
