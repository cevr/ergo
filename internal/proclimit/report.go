package proclimit

import (
	"bytes"
	"slices"
	"strings"
)

// Cause is how the Go runtime of a program said that the kernel refused it
// memory.
type Cause int

const (
	// NoCause: the program's stderr holds no report of refused memory.
	NoCause Cause = iota
	// RuntimeReport: the Go runtime's own report, "fatal error: runtime: out
	// of memory" or another fatal error that contains OutOfMemory.
	RuntimeReport
	// QueueFault: Go 1.27's collector stored through a nil span-queue ring
	// and the program stopped with SIGSEGV at address 0 (RuntimeIssue).
	QueueFault
)

// RuntimeIssue is the Go runtime bug behind QueueFault. Go 1.27's Green Tea
// collector allocates the ring of a span queue (newSpanSPMC in
// runtime/mgcmark_greenteagc.go) with sysAlloc and does not check for nil.
// When the kernel refuses that mapping (under RLIMIT_DATA: the limit is
// reached), the collector later stores through the nil ring, and the
// program stops with "SIGSEGV: segmentation violation", addr=0x0, in a
// runtime.(*spanQueue) frame, not with the out-of-memory report.
// Measured on go1.27.0 under a 256 MiB and a 2 GiB limit; the issue's
// reporter saw it on go1.27.3. go.mod notes the first Go release that
// fixes it, when one exists.
const RuntimeIssue = "golang/go#82667"

// RuntimeIssueURL is the address of RuntimeIssue.
const RuntimeIssueURL = "https://github.com/golang/go/issues/82667"

// CauseOf reads a program's whole stderr.
func CauseOf(stderr []byte) Cause {
	var s scanner
	s.write(stderr)
	s.write([]byte{'\n'})
	return s.cause
}

// The lines of a QueueFault, as the Go runtime prints a fatal signal on a
// system stack (runtime/signal_unix.go, sighandler; measured sample in
// proclimit_test.go):
//
//	SIGSEGV: segmentation violation
//	PC=0x42d21c m=3 sigcode=1 addr=0x0
//
//	goroutine 0 gp=0x16d1809db2c0 m=3 mp=0x16d180a13008 [idle]:
//	runtime.(*spanQueue).tryDrain(0x16d100000400?, 0x435545?, 0x55fa48?)
//
// All three facts are needed: the fatal signal (a Go panic of a nil
// dereference is "[signal SIGSEGV: ...]" and recoverable code, not the
// runtime), the address 0 (the nil ring), and the first frame of the
// faulting stack in the span queue. Any other segmentation fault is a
// crash, not a refused allocation.
const (
	signalLine = "SIGSEGV: segmentation violation"
	faultFrame = "runtime.(*spanQueue)."
	// lineKeep is how much of a line the scanner keeps: more than each of
	// the prefixes and the PC line it reads.
	lineKeep = 256
)

// queueFaultReport is the shortest stderr that CauseOf reads as a
// QueueFault: what a Watcher hands to Check.
const queueFaultReport = signalLine + "\nPC=0x0 addr=0x0\n\n" + faultFrame + "\n"

// scanner reads stderr as it arrives, in writes of any size.
type scanner struct {
	carry []byte // the last len(OutOfMemory)-1 bytes, for a report split across writes
	line  []byte // the start of the current line, at most lineKeep bytes
	state int    // 0: before the signal line; 1: after it; 2: after the PC line
	cause Cause
}

func (s *scanner) write(p []byte) {
	if s.cause != NoCause {
		return
	}
	joined := append(s.carry, p...)
	if bytes.Contains(joined, []byte(OutOfMemory)) {
		s.cause = RuntimeReport
		return
	}
	keep := min(len(joined), len(OutOfMemory)-1)
	s.carry = append(s.carry[:0:0], joined[len(joined)-keep:]...)
	for len(p) > 0 && s.cause == NoCause {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if room := lineKeep - len(s.line); room > 0 {
			s.line = append(s.line, part[:min(room, len(part))]...)
		}
		if end < 0 {
			return
		}
		s.endLine(string(s.line))
		s.line = s.line[:0]
		p = p[end+1:]
	}
}

func (s *scanner) endLine(line string) {
	switch s.state {
	case 1:
		if strings.HasPrefix(line, "PC=") && slices.Contains(strings.Fields(line), "addr=0x0") {
			s.state = 2
			return
		}
	case 2:
		if line == "" || strings.HasPrefix(line, "goroutine ") {
			return
		}
		if strings.HasPrefix(line, faultFrame) {
			s.cause = QueueFault
			return
		}
	}
	s.state = 0
	if line == signalLine {
		s.state = 1
	}
}
