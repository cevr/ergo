//go:build unix

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// rssInterval is how often the RSS watch samples the steps. A step can
// allocate past its budget between two samples; the next one stops it.
var rssInterval = 250 * time.Millisecond

// rssWatch enforces the budget where the gate has no systemd scope (darwin,
// containers): each step runs in its own process group, one sampler reads
// the resident memory of every process (ps, which darwin and Linux both
// have), and a group over the step budget is killed whole. When the groups
// together pass the gate budget, the largest is killed, as the kernel's
// out-of-memory killer picks the largest task under a cgroup limit. A step
// can pass its budget between two samples: at the 250 ms interval a step
// allocating fast was seen at up to 41% over it (peaks of 639.9M to 724.5M
// under a 512M budget) before the kill. The watch sees only its own gate's
// steps, so it bounds no sum across gates. A process that leaves its step's
// process group (setpgid, setsid) leaves the watch too.
type rssWatch struct {
	budget memoryBudget
	reason string

	mu     sync.Mutex
	groups map[int]*rssGroup

	stop    chan struct{}
	stopped chan struct{}
	signals chan os.Signal
}

type rssGroup struct {
	current, peak int64
	verdict       string
}

func openRSSWatch(budget memoryBudget, reason string) (limiter, error) {
	if _, err := sampleGroups(); err != nil {
		return nil, fmt.Errorf("memory budget: no mechanism: the RSS watch cannot read process memory: %w", err)
	}
	w := &rssWatch{budget: budget, reason: reason, groups: map[int]*rssGroup{}, stop: make(chan struct{}), stopped: make(chan struct{})}
	w.forwardSignals()
	go w.sample()
	return w, nil
}

func (w *rssWatch) describe() string {
	return fmt.Sprintf("RSS watch %s, no bound across gates (%s)", describeBudget(w.budget), w.reason)
}

func (w *rssWatch) prepare(name string, command *exec.Cmd) (stepWatch, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &rssStep{watch: w}, nil
}

func (w *rssWatch) close() {
	close(w.stop)
	<-w.stopped
	if w.signals != nil {
		signal.Stop(w.signals)
		close(w.signals)
	}
}

// forwardSignals keeps terminal signals reaching the steps, now that each
// runs in its own process group: the gate passes SIGINT, SIGTERM and, unless
// it was started ignoring it (nohup), SIGHUP to every running step, then
// takes the signal's default action itself.
func (w *rssWatch) forwardSignals() {
	forwarded := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		forwarded = append(forwarded, syscall.SIGHUP)
	}
	w.signals = make(chan os.Signal, 1)
	signal.Notify(w.signals, forwarded...)
	go func() {
		received, ok := <-w.signals
		if !ok {
			return
		}
		number := received.(syscall.Signal)
		w.mu.Lock()
		for pgid := range w.groups {
			syscall.Kill(-pgid, number)
		}
		w.mu.Unlock()
		signal.Reset(received)
		syscall.Kill(os.Getpid(), number)
	}()
}

// sample runs until close, checking the registered groups at each tick.
func (w *rssWatch) sample() {
	defer close(w.stopped)
	ticker := time.NewTicker(rssInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		w.mu.Lock()
		idle := len(w.groups) == 0
		w.mu.Unlock()
		if idle {
			continue
		}
		resident, err := sampleGroups()
		w.mu.Lock()
		w.check(resident, err)
		w.mu.Unlock()
	}
}

// check applies one sample; w.mu is held. A group is killed only while it is
// registered, and a step's group stays registered until its leader has been
// reaped and finish runs, so the group ID cannot yet belong to anyone else
// unless the host cycles through every process ID in between.
func (w *rssWatch) check(resident map[int]int64, err error) {
	if err != nil {
		for _, group := range w.groups {
			if group.verdict == "" {
				group.verdict = "memory budget not enforced: the RSS watch could not read process memory: " + err.Error()
			}
		}
		return
	}
	var total int64
	largest := -1
	for pgid, group := range w.groups {
		if group.verdict != "" {
			continue
		}
		group.current = resident[pgid]
		group.peak = max(group.peak, group.current)
		if group.current > w.budget.step {
			group.verdict = stepExceeded(w.budget)
			syscall.Kill(-pgid, syscall.SIGKILL)
			continue
		}
		total += group.current
		if largest < 0 || group.current > w.groups[largest].current {
			largest = pgid
		}
	}
	if largest >= 0 && total > w.budget.gate {
		w.groups[largest].verdict = gateExceeded(w.budget)
		syscall.Kill(-largest, syscall.SIGKILL)
	}
}

// rssStep is one step under the RSS watch.
type rssStep struct {
	watch *rssWatch
	pgid  int
}

func (s *rssStep) started(pid int) {
	s.pgid = pid
	s.watch.mu.Lock()
	s.watch.groups[pid] = &rssGroup{}
	s.watch.mu.Unlock()
}

func (s *rssStep) finish() usage {
	if s.pgid == 0 {
		return usage{}
	}
	s.watch.mu.Lock()
	group := s.watch.groups[s.pgid]
	delete(s.watch.groups, s.pgid)
	s.watch.mu.Unlock()
	if group == nil {
		return usage{}
	}

	u := usage{peak: group.peak, verdict: group.verdict}
	members, inspectErr := processGroupMembers(s.pgid)
	if inspectErr != nil {
		addProcessIssue(&u, "could not inspect step process group after leader exit: "+inspectErr.Error())
	} else if len(members) > 0 {
		addProcessIssue(&u, "leftover processes at leader exit: "+strings.Join(members, ", "))
	}

	if err := syscall.Kill(-s.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		addProcessIssue(&u, "could not stop step process group: "+err.Error())
		u.stopFailed = true
	}
	if err := waitForProcessGroup(s.pgid); err != nil {
		addProcessIssue(&u, "could not confirm step process group stopped: "+err.Error())
		u.stopFailed = true
	}
	return u
}

func processGroupMembers(pgid int) ([]string, error) {
	output, err := exec.Command("ps", "-A", "-o", "pid=,pgid=,stat=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var members []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil {
			return nil, fmt.Errorf("ps printed %q", scanner.Text())
		}
		if strings.HasPrefix(fields[2], "Z") || strings.HasPrefix(fields[2], "X") {
			continue
		}
		if group == pgid {
			name := strings.Join(fields[3:], " ")
			members = append(members, fmt.Sprintf("%s (pid %d)", name, pid))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Strings(members)
	return members, nil
}

func waitForProcessGroup(pgid int) error {
	for {
		members, err := processGroupMembers(pgid)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sampleGroups returns the resident memory, in bytes, of every process
// group on the host.
func sampleGroups() (map[int]int64, error) {
	output, err := exec.Command("ps", "-A", "-o", "pgid=,rss=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	resident := map[int]int64{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pgid, err1 := strconv.Atoi(fields[0])
		kilobytes, err2 := strconv.ParseInt(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("ps printed %q", scanner.Text())
		}
		resident[pgid] += kilobytes << 10
	}
	return resident, nil
}
