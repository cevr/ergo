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
	"strings"
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
	if req.Key == "" {
		req.Key = CommandKey(argv)
	}
	g, err := b.Take(ctx, req)
	if err != nil {
		return 1, err
	}
	defer g.Release()
	// The command is capped at its reservation and its peak measured: in a
	// scope of its own with MemoryMax (a run over its reservation is
	// killed alone, mem.ergo), whose cgroup's memory.peak a shell in the
	// scope reads after the command. Elsewhere the peak is the largest
	// process's (rusage), with no cap.
	run, peakFile := argv, ""
	if g.Memory() > 0 && scopes() {
		pf, err := os.CreateTemp("", "ergo-budget-peak-")
		if err != nil {
			return 1, err
		}
		peakFile = pf.Name()
		pf.Close()
		defer os.Remove(peakFile)
		run = append([]string{"systemd-run", "--user", "--scope", "--quiet", "--collect",
			"-p", "MemoryMax=" + strconv.FormatInt(g.Memory(), 10), "-p", "MemorySwapMax=0", "-p", "OOMPolicy=continue",
			"sh", "-c", peakScript, "sh", peakFile}, argv...)
	}
	cmd := exec.Command(run[0], run[1:]...)
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
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(werr, &exit):
		code = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	case werr != nil:
		return 1, werr
	}
	peak := readPeak(peakFile)
	if peak == 0 && cmd.ProcessState != nil {
		peak = maxRSS(cmd.ProcessState)
	}
	g.Finish(code, KeptPeak(g.Memory(), code, peak))
	return code, nil
}

// peakScript runs in the command's scope: it runs the command, then writes
// the scope's memory peak to the file $1 names, and exits with the
// command's code (128 plus the signal for one a signal ended, as Run's).
const peakScript = `p=$1; shift; "$@"; rc=$?; cat "/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)/memory.peak" > "$p" 2>/dev/null; exit $rc`

// KeptPeak is the peak a run leaves for its command's next take. A run
// killed at its reservation (SIGKILL, its peak at the cap) needed more
// than the cap, by an amount no one measured: it leaves twice the cap, so
// a command is killed at most log2(its need / the default) times, not
// once as in mem.ergo, where the real peak is known.
func KeptPeak(reserved int64, code int, peak int64) int64 {
	if reserved > 0 && code == 128+int(syscall.SIGKILL) && peak >= reserved {
		return 2 * reserved
	}
	return peak
}

func readPeak(path string) int64 {
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// scopes reports whether a command can run in a transient scope of the
// user's systemd: systemd-run is on PATH and the user manager answers on
// its private socket.
func scopes() bool {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir + "/systemd/private")
	return err == nil
}

// CommandKey is the key a command's memory peaks are kept under: its
// arguments, with the working folder written as ".", so the same check in
// two workspaces shares its peaks.
func CommandKey(argv []string) string {
	wd, _ := os.Getwd()
	parts := make([]string, len(argv))
	for i, a := range argv {
		if wd != "" && wd != "/" {
			a = strings.ReplaceAll(a, wd, ".")
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
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
