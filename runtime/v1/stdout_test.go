package v1

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// stdoutModel is what the host has received of standard output and
// standard error, merged in the order they arrived (one pipe stands for
// both, as `prog 2>&1 | …` sees them), and the writes of standard output
// so far: the reference of TestStdoutFlushModel (stdlib.md io 10).
type stdoutModel struct {
	terminal bool
	pending  string
	stream   strings.Builder
	writes   int
}

// flush is a flush point: what is pending reaches the host in one write.
func (m *stdoutModel) flush() {
	if m.pending != "" {
		m.writes++
		m.stream.WriteString(m.pending)
		m.pending = ""
	}
}

// print is a write of standard output.
func (m *stdoutModel) print(s string) {
	if m.terminal {
		m.writes++
		m.stream.WriteString(s)
		return
	}
	if len(m.pending)+len(s) > stdoutChunk {
		m.flush()
		if len(s) >= stdoutChunk {
			m.writes++
			m.stream.WriteString(s)
			return
		}
	}
	m.pending += s
}

// TestStdoutFlushModel: generated sequences of standard-output writes
// (io.Host's Print and Console's Println, short, long and longer than the
// buffer), standard-error writes, standard-input reads, child processes
// (one run, one started that writes to the inherited stdout), ended by a
// success, a typed failure or a defect, run as main's body under
// mainStatus, against stdoutModel: after each step the host has seen
// exactly the model's writes, and at the end the merged stream is the
// model's, then main's report. A fifth of the cases stand for a terminal,
// where every write goes straight to the host. A failing seed prints its
// steps and the replay command.
//
// Mutants: sb8-always-direct and sb8-terminal-buffered (the mode),
// sb8-no-flush-stderr (order against stderr), sb8-no-flush-stdin,
// sb8-no-flush-run, sb8-no-flush-start (flush points), sb8-no-flush-exit
// (the end of main), sb8-overflow-kept (the 4 KiB bound).
func TestStdoutFlushModel(t *testing.T) {
	cases := 40
	if testing.Short() {
		cases = 12
	}
	for seed := 1; seed <= cases; seed++ {
		if steps, err := stdoutCase(uint64(seed)); err != nil {
			t.Fatalf("seed %d: %v\nsteps: %s\nreplay: go test ./runtime/v1 -run '^TestStdoutFlushModel$' (seed %d)", seed, err, strings.Join(steps, "; "), seed)
		}
	}
}

// stdoutCase runs one generated case and returns its steps and the first
// difference from the model.
func stdoutCase(seed uint64) (steps []string, err error) {
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	text := func() string {
		switch next(8) {
		case 0:
			return ""
		case 1:
			return strings.Repeat("l", 1500+next(1500))
		case 2:
			return strings.Repeat("h", stdoutChunk+next(600))
		}
		return strings.Repeat(string(rune('a'+next(26))), 1+next(60))
	}
	m := &stdoutModel{terminal: next(5) == 0}
	r, w, perr := os.Pipe()
	if perr != nil {
		return nil, perr
	}
	merged := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		merged <- string(b)
	}()
	oldOut, oldErr, oldIn, oldTerm := os.Stdout, os.Stderr, os.Stdin, stdoutIsTerminal
	os.Stdout, os.Stderr = w, w
	stdoutIsTerminal = func(*os.File) bool { return m.terminal }
	var opened []*os.File
	defer func() {
		os.Stdout, os.Stderr, os.Stdin, stdoutIsTerminal = oldOut, oldErr, oldIn, oldTerm
		_ = w.Close()
		for _, f := range opened {
			_ = f.Close()
		}
	}()
	steps = append(steps, fmt.Sprintf("terminal %v", m.terminal))
	stdout.mu.Lock()
	stdout.decide()
	base := stdout.writes
	stdout.mu.Unlock()
	check := func(step string) error {
		stdout.mu.Lock()
		got := stdout.writes - base
		stdout.mu.Unlock()
		if got != m.writes {
			return fmt.Errorf("after %s: %d writes reached the host, want %d", step, got, m.writes)
		}
		return nil
	}
	end := next(3)
	status := mainStatus(func(task *rt.Task) error {
		for range 1 + next(24) {
			var step string
			switch k := next(7); k {
			case 0, 1:
				s := text()
				step = fmt.Sprintf("Print(%d bytes)", len(s))
				IOPrint(s)
				m.print(s)
			case 2:
				s := text()
				step = fmt.Sprintf("Println(%d bytes)", len(s))
				Console{}.Println(s)
				m.print(s + "\n")
			case 3:
				s := text()
				step = fmt.Sprintf("Eprint(%d bytes)", len(s))
				IOEprint(s)
				m.flush()
				m.stream.WriteString(s)
			case 4:
				step = "Lines (one line)"
				in, feed, err := os.Pipe()
				if err != nil {
					return err
				}
				opened = append(opened, in)
				_, _ = feed.WriteString("x\n")
				_ = feed.Close()
				os.Stdin = in
				for range IOLines(task) {
					break
				}
				m.flush()
			case 5:
				step = "exec.Run"
				if _, err := ExecRun(task, "sh", []string{"-c", ":"}, nil, "", "", 0); err != nil {
					return err
				}
				m.flush()
			case 6:
				step = "exec.Start printf c, Wait"
				p, err := ExecStart(task, "sh", []string{"-c", "printf c"}, nil, "", "", 0, execInherit, "", false, execInherit, "", false)
				if err != nil {
					return err
				}
				p.Wait(task)
				p.Close(task)
				m.flush()
				m.stream.WriteString("c")
			}
			steps = append(steps, step)
			if err = check(step); err != nil {
				return nil
			}
		}
		switch end {
		case 1:
			steps = append(steps, "fail")
			return errors.New("boom")
		case 2:
			steps = append(steps, "defect")
			panic("boom")
		}
		steps = append(steps, "return")
		return nil
	})
	if err != nil {
		return steps, err
	}
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = w.Close()
	got := <-merged
	var report string
	switch end {
	case 0:
		m.flush()
	case 1:
		m.flush()
		report = "error: boom\n"
	case 2:
		m.flush()
		report = "panic: boom"
	}
	want := m.stream.String() + report
	if wantStatus := []int{0, 1, 2}[end]; status != wantStatus {
		return steps, fmt.Errorf("status %d, want %d", status, wantStatus)
	}
	if !strings.HasPrefix(got, want) || end == 0 && got != want {
		at := 0
		for at < len(got) && at < len(want) && got[at] == want[at] {
			at++
		}
		return steps, fmt.Errorf("the merged stream differs from the model at byte %d of %d (want %d bytes): got %q, want %q", at, len(got), len(want), clip(got[at:]), clip(want[at:]))
	}
	return steps, nil
}

// clip is the first 40 bytes of s.
func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
