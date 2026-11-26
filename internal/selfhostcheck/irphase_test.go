package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// irDumpResult is irdump's output lines, or why there are none, and its
// own wall and CPU time.
type irDumpResult struct {
	lines     []string
	err       error
	wall, cpu time.Duration
	stored    bool // the lines are the store's, irdump did not run
}

// irDumpRequest is irdump's arguments, the key of its answer
// (irDumpKey) and the run's temporary folder, which no kept answer names.
type irDumpRequest struct {
	args     []string
	key, tmp string
}

// startIRDump builds compiler/cmd/irdump into bin with the ergo command
// cli and, once a request arrives, runs it with its arguments, or reads
// its answer from the store (irDumpStored) and keeps a fresh one, off the test's
// goroutine: the Go side lowers its programs meanwhile, so the run costs
// the longer of the two, not their sum. A closed requests ends it unrun.
// Both programs run in their own process group under limit, as
// runCompiler and runDump run them.
func startIRDump(ctx context.Context, cli, bin string, limit uint64, requests <-chan irDumpRequest) <-chan irDumpResult {
	done := make(chan irDumpResult, 1)
	group := func(cmd *exec.Cmd) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	}
	go func() {
		build := exec.CommandContext(ctx, cli, "build", "-o", bin, "./cmd/irdump")
		build.Dir = filepath.Join(repoRoot(), "compiler")
		var out strings.Builder
		build.Stdout, build.Stderr = &out, &out
		group(build)
		if err := proclimit.Run(build, limit); err != nil {
			done <- irDumpResult{err: fmt.Errorf("ergo build ./cmd/irdump: %v\n%s", err, out.String())}
			return
		}
		req, ok := <-requests
		if !ok {
			done <- irDumpResult{err: context.Canceled}
			return
		}
		if kept, ok := irDumpStored(bin, req.key); ok {
			done <- irDumpResult{lines: kept, stored: true}
			return
		}
		a := req.args
		start := time.Now()
		cmd := exec.CommandContext(ctx, bin, a...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		group(cmd)
		if err := proclimit.Run(cmd, limit); err != nil {
			done <- irDumpResult{err: fmt.Errorf("irdump %v: %v\n%s", a, err, stderr.String())}
			return
		}
		r := irDumpResult{lines: lines(stdout.String()), wall: time.Since(start)}
		if s := cmd.ProcessState; s != nil {
			r.cpu = s.UserTime() + s.SystemTime()
		}
		irDumpKeep(bin, req.key, r.lines, req.tmp)
		done <- r
	}()
	return done
}

// irPhases logs, per phase of the IR agreement run, its wall time, the
// CPU time of the test process and of the programs it waited for, and
// the peak resident set of both: the run's profile, read from its log.
type irPhases struct {
	t     *testing.T
	name  string
	wall  time.Time
	self  time.Duration
	child time.Duration
}

func cpuOf(who int) (time.Duration, int64) {
	var u syscall.Rusage
	if err := syscall.Getrusage(who, &u); err != nil {
		return 0, 0
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano()), u.Maxrss
}

func newIRPhases(t *testing.T) *irPhases {
	p := &irPhases{t: t}
	p.start("setup")
	return p
}

func (p *irPhases) start(name string) {
	p.name, p.wall = name, time.Now()
	p.self, _ = cpuOf(syscall.RUSAGE_SELF)
	p.child, _ = cpuOf(syscall.RUSAGE_CHILDREN)
}

// next ends the current phase, logs it and starts name.
func (p *irPhases) next(name string) {
	self, selfRSS := cpuOf(syscall.RUSAGE_SELF)
	child, childRSS := cpuOf(syscall.RUSAGE_CHILDREN)
	p.t.Logf("phase %-12s wall %8.1fs cpu %8.1fs children %8.1fs (peak rss %d MiB, children %d MiB)",
		p.name, time.Since(p.wall).Seconds(), (self - p.self).Seconds(), (child - p.child).Seconds(), selfRSS>>10, childRSS>>10)
	p.start(name)
}

// irSlice keeps the folders under one of ERGO_IR_AGREE_SLICE's
// comma-separated repo-relative prefixes; unset keeps them all.
func irSlice(dirs []string) []string {
	spec := os.Getenv("ERGO_IR_AGREE_SLICE")
	if spec == "" {
		return dirs
	}
	var out []string
	for _, dir := range dirs {
		r := filepath.ToSlash(rel(dir))
		for _, prefix := range strings.Split(spec, ",") {
			if prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/"); prefix != "" && (r == prefix || strings.HasPrefix(r, prefix+"/")) {
				out = append(out, dir)
				break
			}
		}
	}
	return out
}
