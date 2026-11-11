// Package proclimit bounds the memory of a program that a test or a runner
// starts, and makes such a program the first one the kernel's out-of-memory
// killer stops.
//
// A Go test binary is bounded only as far as the scope it runs in: GOMEMLIMIT
// is a soft goal of one Go runtime, and a child program does not inherit it as
// a limit. On 2026-11-08 a generated program that a lowering test started grew
// to 46.7 GB on the 64 GB workbox. It ran at oom_score_adj -900, inherited
// from the workbox's SSH service (a sshd unit, OOMScoreAdjust=-900) through
// the agent session, go test and the test binary, so the kernel killed every
// other process first (docs/tooling.md, "Test memory").
//
// Start therefore does two things to each program it starts, on Linux:
//
//   - it sets the program's oom_score_adj to OOMScore (1000), so a program
//     that grows is the first victim, whatever its parent inherited;
//   - it sets the program's RLIMIT_DATA to the limit, so a Go program that
//     maps more heap gets ENOMEM and stops long before it reaches the host's
//     memory: with "fatal error: runtime: out of memory", or, through a Go
//     1.27 runtime bug (RuntimeIssue), with SIGSEGV in the collector's span
//     queue. Check reads both as the limit.
//
// Both are set before the program's exec, by a helper: the caller's own
// binary, run again, which sets them on itself (setrlimit, its own
// oom_score_adj) and execs the program, which keeps them. The program's
// first instruction already runs under them, however late the caller is
// scheduled (apply_linux.go, start). A process the
// program starts inherits both. testinputs.Main gives every test process
// the same oom_score_adj, so every process a test starts, go build
// included, inherits it from the first byte.
//
// On other systems Start sets neither: Darwin has no oom_score_adj, no
// prlimit for another process, and its kernel does not enforce RLIMIT_DATA
// or RLIMIT_AS on mmap, which the Go heap uses. There a program is bounded
// only by the scope the caller runs in.
package proclimit

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Variable names the environment variable that sets the limit: a size
// such as 2GiB, 512MiB or a number of bytes, or 0 (or off) for no limit.
const Variable = "ERGO_PROGRAM_MEMORY"

// DefaultLimit is the limit of a program a test or a runner starts when
// Variable is not set.
const DefaultLimit uint64 = 2 << 30

// OOMScore is the oom_score_adj of every program Start starts and of every
// test process: the highest, so the kernel stops these first.
const OOMScore = 1000

// Limit returns the limit Variable sets in env (a list of KEY=VALUE, as
// os.Environ returns), or def when env does not set it. 0 means no limit.
func Limit(env []string, def uint64) (uint64, error) {
	value, ok := "", false
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, Variable+"="); found {
			value, ok = v, true
		}
	}
	if !ok {
		return def, nil
	}
	limit, err := ParseSize(value)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %v", Variable, value, err)
	}
	return limit, nil
}

// ParseSize reads a size: a number of bytes, or a number with the suffix
// KiB, MiB or GiB, as Format writes it ("2 GiB") or without the space; 0
// and "off" mean no limit.
func ParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "off" {
		return 0, nil
	}
	shift := 0
	for suffix, n := range map[string]int{"KiB": 10, "MiB": 20, "GiB": 30} {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			s, shift = rest, n
		}
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, errors.New("not a size: use bytes, KiB, MiB or GiB, or 0 for no limit")
	}
	if n > (^uint64(0))>>shift {
		return 0, errors.New("size out of range")
	}
	return n << shift, nil
}

// Format writes a size in the largest binary unit that divides it.
func Format(n uint64) string {
	for _, u := range []struct {
		name  string
		shift uint
	}{{"GiB", 30}, {"MiB", 20}, {"KiB", 10}} {
		if n != 0 && n%(1<<u.shift) == 0 {
			return strconv.FormatUint(n>>u.shift, 10) + " " + u.name
		}
	}
	return strconv.FormatUint(n, 10) + " bytes"
}

// Start starts cmd and applies OOMScore and the limit (0: none) to the
// program. When they cannot be applied, it kills the program, waits for it
// and returns the error: a program never runs without its bound.
func Start(cmd *exec.Cmd, limit uint64) error {
	if err := start(cmd, limit); err != nil {
		if cmd.Process == nil {
			return err
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("bound program %s: %w", cmd.Path, err)
	}
	return nil
}

// Run is Start, then cmd.Wait, with the error Check makes.
func Run(cmd *exec.Cmd, limit uint64) error {
	watch := Watch(cmd)
	if err := Start(cmd, limit); err != nil {
		return err
	}
	return Check(cmd.Path, limit, cmd.Wait(), watch.Stderr())
}

// LimitError is a program that the limit stopped.
type LimitError struct {
	Program string
	Limit   uint64
	// Cause is RuntimeReport or QueueFault.
	Cause Cause
	Err   error
}

func (e *LimitError) Error() string {
	text := fmt.Sprintf("program %s exceeded its memory limit of %s (set %s to change it, 0 for none)",
		e.Program, Format(e.Limit), Variable)
	if e.Cause == QueueFault {
		text += fmt.Sprintf("; the Go runtime stopped it with SIGSEGV in its collector's span queue, not with an out-of-memory report (Go runtime bug %s, %s)",
			RuntimeIssue, RuntimeIssueURL)
	}
	return fmt.Sprintf("%s: %v", text, e.Err)
}

func (e *LimitError) Unwrap() error { return e.Err }

// OutOfMemory is what the Go runtime prints when the kernel refuses it
// memory ("fatal error: runtime: out of memory", or "out of memory" from
// an allocation that cannot grow the heap). Go 1.27 does not always print
// it: see QueueFault.
const OutOfMemory = "out of memory"

// Check returns a *LimitError when the program ended with an error under a
// limit and its stderr says the kernel refused it memory (CauseOf);
// otherwise it returns err.
func Check(program string, limit uint64, err error, stderr []byte) error {
	var exit *exec.ExitError
	if limit == 0 || !errors.As(err, &exit) {
		return err
	}
	cause := CauseOf(stderr)
	if cause == NoCause {
		return err
	}
	return &LimitError{Program: program, Limit: limit, Cause: cause, Err: err}
}
