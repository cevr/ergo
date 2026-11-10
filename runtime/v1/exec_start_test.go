package v1

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// fakeProc is a program for the lifecycle model: the test writes its lines
// and ends it; a signal ends it as the case says (SIGTERM exits 143 unless
// the program ignores it; SIGKILL exits 137). It logs every signal and
// reap, and whether a signal came after the reap.
type fakeProc struct {
	mu         sync.Mutex
	done       chan struct{}
	ended      bool
	status     int
	ignoreTerm bool
	log        []string
	reaps      int
	outW, errW *io.PipeWriter
}

func (p *fakeProc) exited() <-chan struct{} { return p.done }

func (p *fakeProc) signal(sig syscall.Signal) {
	p.mu.Lock()
	if p.reaps > 0 {
		p.log = append(p.log, "signal after reap")
	}
	switch sig {
	case syscall.SIGTERM:
		p.log = append(p.log, "TERM")
		p.mu.Unlock()
		if !p.ignoreTerm {
			p.end(128 + 15)
		}
		return
	case syscall.SIGKILL:
		p.log = append(p.log, "KILL")
		p.mu.Unlock()
		p.end(128 + 9)
		return
	}
	p.mu.Unlock()
}

func (p *fakeProc) reap() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.log = append(p.log, "reap before end")
	}
	p.reaps++
	return p.status
}

// end ends the program with status, once: its streams end with it.
func (p *fakeProc) end(status int) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.ended, p.status = true, status
	p.mu.Unlock()
	for _, w := range []*io.PipeWriter{p.outW, p.errW} {
		if w != nil {
			_ = w.Close()
		}
	}
	close(p.done)
}

// lifeCase is one generated case: the sinks, the grace, the program's
// answer to SIGTERM, the steps before the end, and how the caller ends.
type lifeCase struct {
	outCaller, errCaller bool
	grace                bool
	ignoreTerm           bool
	steps                []string // "out TEXT", "err TEXT", "read K", "exit N"
	ending               string   // "wait", "close", "stopped wait"
	stoppedRead          bool     // a stopped caller calls Lines before the end
}

func genLifeCase(r *rand.Rand) lifeCase {
	c := lifeCase{outCaller: r.IntN(3) > 0, errCaller: r.IntN(3) > 0, grace: r.IntN(2) == 0, ignoreTerm: r.IntN(2) == 0}
	unread, written := 0, 0
	for range r.IntN(12) {
		switch r.IntN(4) {
		case 0, 1:
			if c.outCaller {
				c.steps = append(c.steps, "out o"+strconv.Itoa(written))
				unread++
				written++
			}
		case 2:
			if c.errCaller {
				c.steps = append(c.steps, "err e"+strconv.Itoa(written))
				unread++
				written++
			}
		case 3:
			if unread > 0 {
				k := 1 + r.IntN(unread)
				c.steps = append(c.steps, "read "+strconv.Itoa(k))
				unread -= k
			}
		}
	}
	if r.IntN(2) == 0 {
		c.steps = append(c.steps, "exit "+strconv.Itoa(r.IntN(4)))
	}
	c.ending = []string{"wait", "close", "stopped wait"}[r.IntN(3)]
	c.stoppedRead = r.IntN(3) == 0
	if c.ending == "wait" && !slices.ContainsFunc(c.steps, func(s string) bool { return strings.HasPrefix(s, "exit ") }) {
		c.ending = "close" // a Wait on a program that never ends would not return
	}
	return c
}

// lifeModel is what the case must give: the signals in order, the status,
// and, for each stream, the lines the caller reads.
type lifeModel struct {
	signals []string
	status  int
	out     []string
	errs    []string
}

func modelLife(c lifeCase) lifeModel {
	var m lifeModel
	exited := false
	var outQ, errQ []string // written, unread, per stream
	for _, s := range c.steps {
		op, arg, _ := strings.Cut(s, " ")
		switch op {
		case "out":
			outQ = append(outQ, arg)
		case "err":
			errQ = append(errQ, arg)
		case "read":
			// The two streams' readers race, so the model takes the k reads
			// as any split that keeps each stream's order; runLife checks
			// each stream's reads are a prefix of what it wrote.
		case "exit":
			exited = true
			m.status, _ = strconv.Atoi(arg)
		}
	}
	_, _ = outQ, errQ
	if !exited {
		switch {
		case c.grace && !c.ignoreTerm:
			m.signals = []string{"TERM"}
			m.status = 143
		case c.grace:
			m.signals = []string{"TERM", "KILL"}
			m.status = 137
		default:
			m.signals = []string{"KILL"}
			m.status = 137
		}
	}
	// finish kills what is left of the group, once, after the end.
	m.signals = append(m.signals, "KILL")
	return m
}

// runLife drives a Running over a fake program by the case's steps.
func runLife(t *testing.T, c lifeCase) (signals []string, status int, out, errs []string, reaps int, after []ExecLine) {
	t.Helper()
	p := &fakeProc{done: make(chan struct{}), ignoreTerm: c.ignoreTerm}
	var outR, errR io.ReadCloser
	if c.outCaller {
		outR, p.outW = io.Pipe()
	}
	if c.errCaller {
		errR, p.errW = io.Pipe()
	}
	grace := time.Duration(0)
	if c.grace {
		grace = time.Hour // the model's timer below decides, not the clock
	}
	r := newRunning(p, grace, outR, errR, nil)
	graceUp := make(chan time.Time)
	never := make(chan time.Time)
	r.after = func(d time.Duration) <-chan time.Time {
		if d == grace {
			return graceUp
		}
		return never // the fake's streams end with it, so finish never waits on time
	}
	task := rt.Root("running model")
	for _, s := range c.steps {
		op, arg, _ := strings.Cut(s, " ")
		switch op {
		case "out":
			_, _ = io.WriteString(p.outW, arg+"\n")
		case "err":
			_, _ = io.WriteString(p.errW, arg+"\n")
		case "read":
			k, _ := strconv.Atoi(arg)
			n := 0
			for l := range r.Lines(task) {
				if l.Stderr {
					errs = append(errs, l.Text)
				} else {
					out = append(out, l.Text)
				}
				n++
				if n == k {
					break
				}
			}
		case "exit":
			n, _ := strconv.Atoi(arg)
			p.end(n)
		}
	}
	if c.stoppedRead && r.lines != nil {
		// A stopped caller's Lines ends with the stop and takes no line;
		// the reads after it still follow each stream's order.
		stopped := rt.Root("stopped reader")
		stopped.Interrupt()
		func() {
			defer func() {
				if q := recover(); q != rt.Interrupted {
					t.Errorf("a stopped Lines raised %v, want rt.Interrupted", q)
				}
			}()
			for l := range r.Lines(stopped) {
				t.Errorf("a stopped Lines gave %v", l)
			}
		}()
	}
	switch c.ending {
	case "wait":
		status = r.Wait(task)
		r.Close(task)
	case "stopped wait":
		stopped := rt.Root("stopped caller")
		stopped.Interrupt()
		func() {
			defer func() {
				if q := recover(); q != rt.Interrupted {
					t.Errorf("a stopped Wait raised %v, want rt.Interrupted", q)
				}
			}()
			r.Wait(stopped)
		}()
		fallthrough
	case "close":
		closed := make(chan struct{})
		go func() { r.Close(task); close(closed) }()
		if c.grace && c.ignoreTerm {
			select {
			case graceUp <- time.Time{}:
			case <-closed:
			}
		}
		select {
		case <-closed:
		case <-testwait.After(t):
			t.Fatalf("Close did not return: %+v", c)
		}
		status = r.Wait(task)
	}
	for l := range r.Lines(task) {
		after = append(after, l)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.log), status, out, errs, p.reaps, after
}

// TestRunningMatchesModel runs 400 generated cases of a Running over a fake
// program against the lifecycle model (exec_start.go): the signals a
// release sends (SIGTERM, the grace, SIGKILL; SIGKILL at once with no
// grace; none to a program that ended, but the kill of what is left of its
// group), the status Wait gives, one reap after the end and no signal after
// it, the caller's lines in each stream's order, and no line after Wait.
func TestRunningMatchesModel(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 5))
	seen := map[string]int{}
	for i := range 400 {
		c := genLifeCase(r)
		want := modelLife(c)
		signals, status, out, errs, reaps, after := runLife(t, c)
		where := fmt.Sprintf("case %d %+v", i, c)
		seen[c.ending+fmt.Sprint(" grace=", c.grace, " ignore=", c.ignoreTerm, " exit=", want.signals[0] == "KILL" && len(want.signals) == 1)]++
		if !slices.Equal(signals, want.signals) {
			t.Errorf("%s\nsignals %q, want %q", where, signals, want.signals)
		}
		if status != want.status {
			t.Errorf("%s\nstatus %d, want %d", where, status, want.status)
		}
		if reaps != 1 {
			t.Errorf("%s\nreaped %d times, want once", where, reaps)
		}
		var wroteOut, wroteErr []string
		for _, s := range c.steps {
			op, arg, _ := strings.Cut(s, " ")
			switch op {
			case "out":
				wroteOut = append(wroteOut, arg)
			case "err":
				wroteErr = append(wroteErr, arg)
			}
		}
		if len(out) > len(wroteOut) || !slices.Equal(out, wroteOut[:len(out)]) {
			t.Errorf("%s\nstdout lines read %q, not a prefix of %q", where, out, wroteOut)
		}
		if len(errs) > len(wroteErr) || !slices.Equal(errs, wroteErr[:len(errs)]) {
			t.Errorf("%s\nstderr lines read %q, not a prefix of %q", where, errs, wroteErr)
		}
		if len(after) != 0 {
			t.Errorf("%s\nLines after the end gave %v", where, after)
		}
	}
	for _, k := range []string{"close grace=true ignore=true exit=false", "close grace=true ignore=false exit=false", "close grace=false ignore=false exit=false", "wait grace=false ignore=false exit=true"} {
		if seen[k] == 0 {
			t.Errorf("no case %q: %v", k, seen)
		}
	}
}

// TestExecStreams is the acceptance check of CU4 on the host: a helper
// program writes on command, read from a FIFO, and after each command the
// sink holds exactly the bytes written so far, in order (every prefix): a
// file sink's file and a ToCaller stream's lines. The release ends the
// program's group: a program that ignores SIGTERM is killed after its
// grace, one that does not exits on SIGTERM, and no process of its group is
// left after Close (each wait is on an event: the next line, the file's
// content, Close's return).
func TestExecStreams(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	r := rand.New(rand.NewPCG(9, 4))
	for i := range 12 {
		dir := t.TempDir()
		ctl, pidfile := filepath.Join(dir, "ctl"), filepath.Join(dir, "pids")
		if err := syscall.Mkfifo(ctl, 0o600); err != nil {
			t.Fatal(err)
		}
		outFile, errFile := filepath.Join(dir, "out"), filepath.Join(dir, "err")
		outKind, errKind := []int{execToFile, execToCaller}[r.IntN(2)], []int{execToFile, execToCaller}[r.IntN(2)]
		ignore := r.IntN(2) == 0
		trap := ""
		grace := time.Hour // SIGTERM ends it: the wait is for its exit event
		if ignore {
			trap = "trap '' TERM\n"
			grace = time.Millisecond // SIGKILL follows: the outcome is the same at any speed
		}
		// The helper: its pid and a grandchild's go to pids; then each line
		// of ctl is a command: "o TEXT" to stdout, "e TEXT" to stderr.
		script := fmt.Sprintf(`%secho $$ >> %[2]s
sleep 600 </dev/null >/dev/null 2>&1 &
echo $! >> %[2]s
while read -r cmd text; do
  case "$cmd" in
    o) printf '%%s\n' "$text" ;;
    e) printf '%%s\n' "$text" >&2 ;;
  esac
done < %[3]s
sleep 600
`, trap, pidfile, ctl)
		task := rt.Root("streams")
		run, err := ExecStart(task, "sh", []string{"-c", script}, nil, "", "", grace, outKind, outFile, false, errKind, errFile, false)
		if err != nil {
			t.Fatal(err)
		}
		w, err := os.OpenFile(ctl, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		lines := make(chan ExecLine)
		go func() {
			defer close(lines)
			for l := range run.Lines(rt.Root("reader")) {
				lines <- l
			}
		}()
		var wroteOut, wroteErr strings.Builder
		for j := range 2 + r.IntN(6) {
			text := strings.Repeat(string(rune('a'+j)), 1+r.IntN(5000))
			stderr := r.IntN(2) == 0
			cmd, b, kind, file := "o", &wroteOut, outKind, outFile
			if stderr {
				cmd, b, kind, file = "e", &wroteErr, errKind, errFile
			}
			if _, err := fmt.Fprintf(w, "%s %s\n", cmd, text); err != nil {
				t.Fatal(err)
			}
			b.WriteString(text + "\n")
			switch kind {
			case execToFile:
				want := b.String()
				testwait.Until(t, func() bool {
					data, _ := os.ReadFile(file)
					if len(data) > len(want) || !strings.HasPrefix(want, string(data)) {
						t.Fatalf("case %d: %s holds %d bytes not a prefix of the %d written", i, file, len(data), len(want))
					}
					return string(data) == want
				})
			case execToCaller:
				select {
				case l := <-lines:
					if l.Text != text || l.Stderr != stderr {
						t.Fatalf("case %d: line %q (stderr %v), want the %d bytes just written (stderr %v)", i, l.Text[:min(len(l.Text), 20)], l.Stderr, len(text), stderr)
					}
				case <-testwait.After(t):
					t.Fatalf("case %d: the line just written never came", i)
				}
			}
		}
		pids := readPids(t, pidfile)
		_ = w.Close() // the helper's loop ends and it sleeps
		run.Close(task)
		for range lines {
		}
		status := run.Wait(task)
		want := 128 + 15
		if ignore {
			want = 128 + 9
		}
		if status != want {
			t.Errorf("case %d (ignore TERM %v): status %d, want %d", i, ignore, status, want)
		}
		if len(pids) != 2 {
			t.Fatalf("case %d: pids %v", i, pids)
		}
		for _, pid := range pids {
			if procExists(pid, pids[0]) {
				t.Errorf("case %d: Close returned and process %d of group %d is left: %q", i, pid, pids[0], procLine(pid))
			}
		}
	}
}

// TestExecStartStopped: a stopped caller starts nothing; the stop is raised
// before the start, so no file sink is made either. (The failures of a
// program that cannot start are lower TestExecStartRuns'.)
func TestExecStartStopped(t *testing.T) {
	stopped := rt.Root("stopped starter")
	stopped.Interrupt()
	made := filepath.Join(t.TempDir(), "out")
	func() {
		defer func() {
			if q := recover(); q != rt.Interrupted {
				t.Errorf("a stopped Start raised %v, want rt.Interrupted", q)
			}
		}()
		_, _ = ExecStart(stopped, "true", nil, nil, "", "", 0, execToFile, made, false, execDiscard, "", false)
	}()
	if _, err := os.Stat(made); err == nil {
		t.Errorf("a stopped Start made its file sink")
	}
}

// TestExecRunGrace: Run's stop follows the command's grace, as a Running's
// release does: SIGTERM first, so the program's TERM trap runs before its
// end. The trap writes to a file; the test waits on the program's start
// (its pid in a file) and on Run's return, events, not time.
func TestExecRunGrace(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	pidfile, termfile := filepath.Join(dir, "pids"), filepath.Join(dir, "term")
	script := fmt.Sprintf("trap 'echo term > %s; exit 0' TERM\necho $$ >> %s\nwhile :; do sleep 0.01; done\n", termfile, pidfile)
	task := rt.Root("run grace")
	done := make(chan bool, 1)
	go func() {
		defer func() { done <- recover() == rt.Interrupted }()
		_, _ = ExecRun(task, "sh", []string{"-c", script}, nil, "", "", time.Hour)
	}()
	testwait.Until(t, func() bool { return len(readPids(t, pidfile)) > 0 })
	task.Interrupt()
	select {
	case interrupted := <-done:
		if !interrupted {
			t.Fatal("the stop did not end Run with rt.Interrupted")
		}
	case <-testwait.After(t):
		t.Fatal("Run did not return after the stop")
	}
	if data, _ := os.ReadFile(termfile); string(data) != "term\n" {
		t.Errorf("the TERM trap did not run before the end (term holds %q)", data)
	}
}
