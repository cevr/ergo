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
	"sync"
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
	// The command's streams are files, so cmd.Wait returns when the command
	// exits, not when the last process holding a stream does: a process the
	// command left behind would hold Run (and the cores) until it ended.
	var p pipes
	defer p.close()
	if cmd.Stdin, err = p.in(stdin); err == nil {
		if cmd.Stdout, err = p.out(stdout); err == nil {
			cmd.Stderr, err = p.out(stderr)
		}
	}
	if err != nil {
		return 1, err
	}
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(g.Width()), g.Env())
	cmd.SysProcAttr = groupAttr()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	err = cmd.Start()
	p.started()
	if err != nil {
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
	// The group is dead, so its ends of the output pipes close: the copies
	// end once they have read everything the command wrote.
	p.drain()
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

// pipes gives a command files for its streams: a stream that is not a
// file goes through an os.Pipe and a copy Run owns.
type pipes struct {
	child  []*os.File // the command's ends, closed in Run once it started
	parent []*os.File // Run's ends, closed when Run ends
	copies sync.WaitGroup
}

func (p *pipes) in(r io.Reader) (*os.File, error) {
	if r == nil {
		return nil, nil
	}
	if f, ok := r.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.child, p.parent = append(p.child, pr), append(p.parent, pw)
	// Not waited for: a reader may never end. Closing pw ends the copy.
	go func() { io.Copy(pw, r) }()
	return pr, nil
}

func (p *pipes) out(w io.Writer) (*os.File, error) {
	if w == nil {
		return nil, nil
	}
	if f, ok := w.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.child, p.parent = append(p.child, pw), append(p.parent, pr)
	p.copies.Add(1)
	go func() {
		defer p.copies.Done()
		io.Copy(w, pr)
	}()
	return pw, nil
}

// started closes the command's ends in Run, so a copy sees the end of its
// stream when the command's processes are gone.
func (p *pipes) started() {
	for _, f := range p.child {
		f.Close()
	}
	p.child = nil
}

func (p *pipes) drain() { p.copies.Wait() }

func (p *pipes) close() {
	p.started()
	for _, f := range p.parent {
		f.Close()
	}
}
