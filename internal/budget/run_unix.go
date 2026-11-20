//go:build unix

package budget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// With memory budgeted, the command runs in a scope of its own, in the
	// budget's slice, and its peak is measured: a shell in the scope reads
	// the scope's memory.peak after the command (mem.ergo).
	//   - The slice is capped at the budget, so the real use of all the
	//     budget's commands never passes it (RealWithinMemory).
	//   - A command with a recorded peak, or one that asked, is capped at its
	//     reservation too: a run past it is killed alone (KilledPastOwnPeak).
	//     A command never measured is not (UnknownNotCapped): it runs, and
	//     its peak is recorded for its next take.
	// Elsewhere the peak is the largest process's (rusage), with no cap.
	run, peakFile := argv, ""
	if g.Memory() > 0 && scopes() {
		pf, err := os.CreateTemp("", "ergo-budget-peak-")
		if err != nil {
			return 1, err
		}
		peakFile = pf.Name()
		pf.Close()
		defer os.Remove(peakFile)
		slice := sliceName(b.state)
		exec.Command("systemctl", "--user", "set-property", "--runtime", slice, "MemoryMax="+strconv.FormatInt(g.slice, 10), "MemorySwapMax=0").Run()
		scope := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--slice=" + slice, "-p", "OOMPolicy=continue"}
		if g.capped {
			scope = append(scope, "-p", "MemoryMax="+strconv.FormatInt(g.Memory(), 10), "-p", "MemorySwapMax=0")
		}
		run = append(append(scope, "sh", "-c", peakScript, "sh", peakFile), argv...)
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
	peak, killed := readPeak(peakFile)
	if peak == 0 && cmd.ProcessState != nil {
		peak = maxRSS(cmd.ProcessState)
		killed = code == 128+int(syscall.SIGKILL)
	}
	own := int64(0)
	if g.capped {
		own = g.Memory()
	}
	g.Finish(code, KeptPeak(own, code, killed, peak))
	return code, nil
}

// sliceName is the systemd slice of the budget whose state folder is
// state: one per budget, so a test's budget never caps the host's.
func sliceName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return "ergo-budget-" + hex.EncodeToString(sum[:4]) + ".slice"
}

// peakScript runs in the command's scope: it runs the command, then writes
// the scope's memory peak and its count of OOM kills to the file $1 names,
// one a line, and exits with the command's code (128 plus the signal for
// one a signal ended, as Run's). The kill count, not the exit code, says
// whether the cap killed: a shell that runs the killed process exits 1.
const peakScript = `p=$1; shift; "$@"; rc=$?; c="/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)"; { cat "$c/memory.peak"; awk '$1 == "oom_kill" { print $2 }' "$c/memory.events"; } > "$p" 2>/dev/null; exit $rc`

// KeptPeak is the peak a run leaves for its command's next take, 0 for
// none; own is the run's own cap, 0 for an uncapped run, and killed says
// an OOM kill ended a process of the run.
//   - A run killed at its own cap needed more than the cap, by an amount
//     no one measured: it leaves twice the cap. Only a command that grew
//     past its own recorded peak (and a quarter) is so killed (mem.ergo
//     KilledPastOwnPeak), once per doubling of its growth.
//   - A run that ends at 0, or one the slice killed, leaves its real peak.
//   - A run that failed otherwise leaves none: it may have stopped before
//     its work's peak (2026-11-20: a vet, test and mutants chain that
//     failed at its test left 112M, and its next run was capped at 140M,
//     and its mutants killed there).
func KeptPeak(own int64, code int, killed bool, peak int64) int64 {
	switch {
	case killed && own > 0 && peak >= own:
		return max(peak, 2*own)
	case killed || code == 0:
		return peak
	}
	return 0
}

// readPeak reads the file peakScript wrote: the scope's peak, and whether
// an OOM kill ended a process in it.
func readPeak(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	lines := strings.Fields(string(data))
	if len(lines) == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(lines[0], 10, 64)
	if err != nil {
		return 0, false
	}
	kills := int64(0)
	if len(lines) > 1 {
		kills, _ = strconv.ParseInt(lines[1], 10, 64)
	}
	return v, kills > 0
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
