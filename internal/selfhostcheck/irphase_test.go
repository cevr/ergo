package selfhostcheck

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
