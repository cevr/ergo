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

// fakeProc is a program for the lifecycle model: the test writes its bytes
// and ends it; a signal ends it as the case says (SIGTERM exits termStatus unless
// the program ignores it; SIGKILL exits 137). It logs every signal and
// reap, and whether a signal came after the reap.
type fakeProc struct {
	mu         sync.Mutex
	done       chan struct{}
	ended      bool
	status     int
	ignoreTerm bool
	termStatus int
	log        []string
	reaps      int
	outW, errW *fakeStream
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
			p.end(p.termStatus)
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
	for _, w := range []*fakeStream{p.outW, p.errW} {
		if w != nil {
			close(w.in)
		}
	}
	close(p.done)
}

// fakeStream is a stream of a fakeProc as the Running's reader reads it.
// A write hands the bytes to a Read and then waits until the reader calls
// Read again, so when write returns the reader has done with the bytes:
// a reader that delivers as it reads has sent them to the caller.
type fakeStream struct {
	in   chan []byte // unbuffered; nil is a handshake, not bytes
	rest []byte
}

func (s *fakeStream) Read(b []byte) (int, error) {
	for len(s.rest) == 0 {
		next, ok := <-s.in
		if !ok {
			return 0, io.EOF
		}
		s.rest = next
	}
	n := copy(b, s.rest)
	s.rest = s.rest[n:]
	return n, nil
}

func (s *fakeStream) Close() error { return nil }

func (s *fakeStream) write(text string) {
	s.in <- []byte(text)
	s.in <- nil
}

// countSleeper is a Scheduler whose every sleep ends at once: a deadline
// passes as soon as it is armed. It counts the sleeps.
type countSleeper struct {
	mu sync.Mutex
	n  int
}

func (s *countSleeper) Sleep(*rt.Task, time.Duration) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func (s *countSleeper) armed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// lifeCase is one generated case: the sinks, the grace, the program's
// answer to SIGTERM, the steps before the end, and how the caller ends.
type lifeCase struct {
	outCaller, errCaller bool
	grace                bool
	ignoreTerm           bool
	termStatus           int      // a TERM trap may choose any exit status
	steps                []string // "out TEXT", "err TEXT" ('|' is a line feed), "lines K", "stop-lines K", "chunks", "waitfor", "exit N", "drain lines", "drain chunks"
	ending               string   // "wait", "close", "stopped wait", "stop", "stopped stop"
	stoppedRead          bool     // a stopped caller calls Lines before the end
}

func genLifeCase(r *rand.Rand) lifeCase {
	c := lifeCase{outCaller: r.IntN(3) > 0, errCaller: r.IntN(3) > 0, grace: r.IntN(2) == 0, ignoreTerm: r.IntN(2) == 0, termStatus: r.IntN(256)}
	written := 0
	for range r.IntN(12) {
		text := strconv.Itoa(written) + []string{"", "|", "|x|", "||"}[r.IntN(4)]
		switch r.IntN(7) {
		case 0, 1:
			if c.outCaller {
				c.steps = append(c.steps, "out o"+text)
				written++
			}
		case 2:
			if c.errCaller {
				c.steps = append(c.steps, "err e"+text)
				written++
			}
		case 3:
			c.steps = append(c.steps, "lines "+strconv.Itoa(1+r.IntN(3)))
		case 4:
			c.steps = append(c.steps, "stop-lines "+strconv.Itoa(1+r.IntN(2)))
		case 5:
			c.steps = append(c.steps, "chunks")
		case 6:
			c.steps = append(c.steps, "waitfor")
		}
	}
	if r.IntN(2) == 0 {
		c.steps = append(c.steps, "exit "+strconv.Itoa(r.IntN(4)))
		if r.IntN(2) == 0 {
			c.steps = append(c.steps, []string{"drain lines", "drain chunks"}[r.IntN(2)])
		}
		if r.IntN(3) == 0 {
			// Last: a WaitFor that gives the status drops the bytes not
			// read, as Wait does.
			c.steps = append(c.steps, "waitfor")
		}
	}
	c.ending = []string{"wait", "close", "stopped wait", "stop", "stopped stop"}[r.IntN(5)]
	c.stoppedRead = r.IntN(3) == 0
	if c.ending == "wait" && !slices.ContainsFunc(c.steps, func(s string) bool { return strings.HasPrefix(s, "exit ") }) {
		c.ending = "close" // a Wait on a program that never ends would not return
	}
	return c
}

// lifeModel is what the case must give: the signals in order and the
// status. Each stream's bytes are checked as runLife reads them.
type lifeModel struct {
	signals []string
	status  int
}

func modelLife(c lifeCase) lifeModel {
	var m lifeModel
	exited := false
	for _, s := range c.steps {
		op, arg, _ := strings.Cut(s, " ")
		if op == "exit" {
			exited = true
			m.status, _ = strconv.Atoi(arg)
		}
	}
	if !exited {
		switch {
		case c.grace && !c.ignoreTerm:
			m.signals = []string{"TERM"}
			m.status = c.termStatus
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

// lifeStreams is what runLife wrote to each stream and what the caller
// read of it: the lines, each with its line feed put back, and the chunks,
// in the order read, and how many end chunks it saw.
type lifeStreams struct {
	wrote, read [2]string
	ends        [2]int
}

// add records what the caller read of stream s: text, or, for an end
// chunk, the end.
func (l *lifeStreams) add(stderr bool, text string, end bool) {
	s := 0
	if stderr {
		s = 1
	}
	if end {
		l.ends[s]++
		return
	}
	l.read[s] += text
}

// waiting is how many whole lines the caller has not read yet.
func (l *lifeStreams) waiting() int {
	n := 0
	for s := range 2 {
		n += strings.Count(l.wrote[s][len(l.read[s]):], "\n")
	}
	return n
}

// check reports a stream whose read bytes are not a prefix of what was
// written (whole: not all of it). A last line with no line feed that
// Lines gave at the stream's end comes back with one, so both allow it.
func (l *lifeStreams) check(whole bool) string {
	for s, name := range []string{"stdout", "stderr"} {
		wrote, read := l.wrote[s], l.read[s]
		switch {
		case !strings.HasSuffix(wrote, "\n") && read == wrote+"\n":
		case !whole && strings.HasPrefix(wrote, read):
		case whole && read == wrote:
		default:
			return fmt.Sprintf("%s read %q, wrote %q (all read: %v)", name, read, wrote, whole)
		}
	}
	return ""
}

// queued is how many chunks wait for the caller.
func (r *Running) queued() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.chunks) + len(r.back)
}

// unreadBytes moves the chunks that wait in the channel to the end of
// back, where next gives them in the same order, and returns each
// stream's bytes not given yet. It is called only while no reader sends.
func (r *Running) unreadBytes() (unread [2]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.chunks) > 0 {
		r.back = append(r.back, <-r.chunks)
	}
	for _, c := range r.back {
		if c.Stderr {
			unread[1] += c.Text
		} else {
			unread[0] += c.Text
		}
	}
	return unread
}

// runLife drives a Running over a fake program by the case's steps. Each
// write returns once the reader has done with it, so a "chunks" step reads
// what is queued and must find every byte written so far.
func runLife(t *testing.T, c lifeCase) (signals []string, status int, l lifeStreams, reaps int, after []ExecChunk, failed string) {
	t.Helper()
	p := &fakeProc{done: make(chan struct{}), ignoreTerm: c.ignoreTerm, termStatus: c.termStatus}
	var outR, errR io.ReadCloser
	if c.outCaller {
		p.outW = &fakeStream{in: make(chan []byte)}
		outR = p.outW
	}
	if c.errCaller {
		p.errW = &fakeStream{in: make(chan []byte)}
		errR = p.errW
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
	fail := func(s string) {
		if failed == "" {
			failed = s
		}
	}
	task := rt.Root("running model")
	lines := func(task *rt.Task, k int, stop bool) {
		n := 0
		for line := range r.Lines(task) {
			l.add(line.Stderr, line.Text+"\n", false)
			n++
			if n == k {
				if !stop {
					break
				}
				task.Interrupt() // the next wait raises it; the bytes held go back
			}
		}
	}
steps:
	for _, s := range c.steps {
		op, arg, _ := strings.Cut(s, " ")
		switch op {
		case "out", "err":
			text := strings.ReplaceAll(arg, "|", "\n")
			w, i := p.outW, 0
			if op == "err" {
				w, i = p.errW, 1
			}
			l.wrote[i] += text
			before := r.queued()
			w.write(text)
			if r.queued() != before+1 {
				// The reader read the bytes and did not pass them on: end
				// the program so that no later read waits for them.
				fail(fmt.Sprintf("%s: the reader took %q and queued %d chunks, want 1", s, text, r.queued()-before))
				p.end(0)
				break steps
			}
		case "lines":
			k, _ := strconv.Atoi(arg)
			if k = min(k, l.waiting()); k > 0 {
				lines(task, k, false)
			}
		case "stop-lines":
			k, _ := strconv.Atoi(arg)
			if k = min(k, l.waiting()); k > 0 {
				stopped := rt.Root("stopped mid-lines")
				func() {
					defer func() {
						if q := recover(); q != rt.Interrupted {
							t.Errorf("a Lines stopped mid-read raised %v, want rt.Interrupted", q)
						}
					}()
					lines(stopped, k, true)
				}()
			}
		case "chunks":
			if r.queued() > 0 {
				for ch := range r.Chunks(task) {
					l.add(ch.Stderr, ch.Text, ch.Text == "")
					if r.queued() == 0 {
						break
					}
				}
			}
			if bad := l.check(false); bad != "" {
				fail("chunks: " + bad)
			} else if l.read != l.wrote {
				fail(fmt.Sprintf("chunks: the bytes written are not all queued: read %q, wrote %q", l.read, l.wrote))
			}
		case "exit":
			n, _ := strconv.Atoi(arg)
			p.end(n)
		case "waitfor":
			// The deadline passes as soon as it is armed. A program that
			// runs gives Expired and keeps running, unsignalled, with its
			// bytes still waiting (the checks below); one that has ended
			// gives its status and arms no deadline.
			p.mu.Lock()
			ended, want := p.ended, p.status
			p.mu.Unlock()
			// A stopped caller's WaitFor ends with the stop and leaves the
			// program, its status and its bytes as they were.
			stopped := rt.Root("stopped waitfor")
			stopped.Interrupt()
			func() {
				defer func() {
					if q := recover(); q != rt.Interrupted {
						fail(fmt.Sprintf("a stopped WaitFor raised %v, want rt.Interrupted", q))
					}
				}()
				ExecWaitFor("waitfor", stopped, &countSleeper{}, r, time.Hour)
			}()
			sl := &countSleeper{}
			code, err := ExecWaitFor("waitfor", task, sl, r, time.Hour)
			switch {
			case ended && (err != nil || code != want || sl.armed() != 0):
				fail(fmt.Sprintf("waitfor after exit %d: %d, %v, %d deadlines armed; want the status, no failure, none armed", want, code, err, sl.armed()))
			case !ended && err != (Expired{}):
				fail(fmt.Sprintf("waitfor before exit: %d, %v; want Expired", code, err))
			}
			if ended {
				continue // the status drops the bytes not read, as Wait does
			}
		case "drain":
			if arg == "lines" {
				lines(task, -1, false)
			} else {
				for ch := range r.Chunks(task) {
					l.add(ch.Stderr, ch.Text, ch.Text == "")
				}
				for s, caller := range []bool{c.outCaller, c.errCaller} {
					if want := map[bool]int{true: 1}[caller]; l.ends[s] != want {
						fail(fmt.Sprintf("drain: stream %d ended %d times, want %d", s, l.ends[s], want))
					}
				}
			}
			if bad := l.check(true); bad != "" {
				fail("drain: " + bad)
			}
		}
		if bad := l.check(false); bad != "" {
			fail(s + ": " + bad)
		}
		if op == "exit" || op == "drain" {
			continue // the readers may still be sending the ends
		}
		// Every byte written and not read waits for the caller, in order:
		// none is lost, held back or added by a reader or by a Lines that
		// broke or was stopped.
		unread := r.unreadBytes()
		for i := range 2 {
			if want := l.wrote[i][min(len(l.read[i]), len(l.wrote[i])):]; failed == "" && unread[i] != want {
				fail(fmt.Sprintf("%s: stream %d has %q waiting, want %q", s, i, unread[i], want))
			}
		}
		if failed != "" {
			p.end(0) // no later read waits for bytes that will not come
			break
		}
	}
	if c.stoppedRead && r.chunks != nil {
		// A stopped caller's Lines and Chunks end with the stop and take
		// nothing; the reads after them still follow each stream's order.
		stopped := rt.Root("stopped reader")
		stopped.Interrupt()
		for _, read := range []func(){
			func() {
				for line := range r.Lines(stopped) {
					t.Errorf("a stopped Lines gave %v", line)
				}
			},
			func() {
				for ch := range r.Chunks(stopped) {
					t.Errorf("a stopped Chunks gave %v", ch)
				}
			},
		} {
			func() {
				defer func() {
					if q := recover(); q != rt.Interrupted {
						t.Errorf("a stopped read raised %v, want rt.Interrupted", q)
					}
				}()
				read()
			}()
		}
	}
	switch c.ending {
	case "stop", "stopped stop":
		if c.ending == "stopped stop" {
			task.Interrupt() // Stop owns cleanup even if its caller is stopped.
		}
		stopped := make(chan struct{})
		go func() { status = r.Stop(task); close(stopped) }()
		if c.grace && c.ignoreTerm {
			select {
			case graceUp <- time.Time{}:
			case <-stopped:
			}
		}
		select {
		case <-stopped:
		case <-testwait.After(t):
			t.Fatalf("Stop did not return: %+v", c)
		}
		p.mu.Lock()
		reaped := p.reaps
		p.mu.Unlock()
		if reaped != 1 {
			// Clean the fake's readers before failing a skipped-cleanup
			// mutant; do not wait for the harness deadline to kill it.
			p.end(0)
			r.Close(rt.Root("failed Stop cleanup"))
			t.Fatalf("Stop returned before its reap: %+v (reaps %d)", c, reaped)
		}
		task = rt.Root("after stop")
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
	for line := range r.Lines(task) {
		after = append(after, ExecChunk(line))
	}
	for ch := range r.Chunks(task) {
		after = append(after, ch)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.log), status, l, p.reaps, after, failed
}

// TestRunningMatchesModel runs 400 generated cases of a Running over a fake
// program against the lifecycle model (exec_start.go): the signals a
// release sends (SIGTERM, the grace, SIGKILL; SIGKILL at once with no
// grace; none to a program that ended, but the kill of what is left of its
// group), the status Wait or Stop gives, one reap before Stop returns and
// no signal after it, and nothing read after Wait or Stop. Stop covers
// before output, mid-stream and after exit; a pending caller stop cannot
// interrupt it. TERM traps choose their status. The bytes (CU4c): the
// program writes fragments with and without line feeds; the caller mixes
// Lines (a break, a stop mid-read) and Chunks; what it reads of each stream
// is always a prefix of what was written, Chunks finds every byte written
// so far queued as soon as the reader has read it (no wait for a line feed
// or the end), and a read to the end gives every byte, the last fragment
// as written, and one end chunk per stream. WaitFor (exec_start.go
// ExecWaitFor), with a deadline that passes at once: a running program
// gives Expired, gets no signal and keeps every unread byte; an ended one
// gives its status and arms no deadline.
func TestRunningMatchesModel(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 5))
	seen := map[string]int{}
	for i := range 400 {
		c := genLifeCase(r)
		want := modelLife(c)
		signals, status, l, reaps, after, failed := runLife(t, c)
		where := fmt.Sprintf("seed 23,5 case %d %+v (replay: go test -run '^TestRunningMatchesModel$' ./runtime/v1)", i, c)
		seen[c.ending+fmt.Sprint(" grace=", c.grace, " ignore=", c.ignoreTerm, " exit=", want.signals[0] == "KILL" && len(want.signals) == 1)]++
		exited := false
		for _, s := range c.steps {
			seen[s[:strings.IndexAny(s+" ", " ")]]++
			if strings.HasPrefix(s, "drain") {
				seen[s]++
			}
			exited = exited || strings.HasPrefix(s, "exit ")
			if s == "waitfor" {
				seen[fmt.Sprint("waitfor exited=", exited)]++
			}
		}
		if strings.Contains(c.ending, "stop") {
			phase := "before output"
			for _, s := range c.steps {
				if strings.HasPrefix(s, "out ") || strings.HasPrefix(s, "err ") {
					phase = "mid-stream"
				}
				if strings.HasPrefix(s, "exit ") {
					phase = "after exit"
				}
			}
			seen[c.ending+" "+phase]++
		}
		if !slices.Equal(signals, want.signals) {
			t.Errorf("%s\nsignals %q, want %q", where, signals, want.signals)
		}
		if status != want.status {
			t.Errorf("%s\nstatus %d, want %d", where, status, want.status)
		}
		if reaps != 1 {
			t.Errorf("%s\nreaped %d times, want once", where, reaps)
		}
		if failed != "" {
			t.Errorf("%s\n%s", where, failed)
		}
		if bad := l.check(false); bad != "" {
			t.Errorf("%s\n%s", where, bad)
		}
		if len(after) != 0 {
			t.Errorf("%s\nLines or Chunks after the end gave %v", where, after)
		}
	}
	for _, k := range []string{"close grace=true ignore=true exit=false", "close grace=true ignore=false exit=false", "close grace=false ignore=false exit=false", "wait grace=false ignore=false exit=true", "lines", "stop-lines", "chunks", "drain lines", "drain chunks", "waitfor exited=false", "waitfor exited=true"} {
		if seen[k] == 0 {
			t.Errorf("no case %q: %v", k, seen)
		}
	}
	for _, ending := range []string{"stop", "stopped stop"} {
		for _, phase := range []string{"before output", "mid-stream", "after exit"} {
			if seen[ending+" "+phase] == 0 {
				t.Errorf("seed 23,5 missed %s %s", ending, phase)
			}
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
