package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// The systemd scope. The gate re-executes itself through
//
//	systemd-run --user --scope --slice=ergo-gates.slice -p Delegate=yes \
//	  -p MemoryMax=<gate budget> -p MemorySwapMax=0 -p OOMPolicy=continue
//
// so all its steps share one cgroup v2 subtree capped at the gate budget,
// which systemd delegates to it. Inside, the gate moves itself to a leaf
// cgroup and starts every step directly in a cgroup of its own
// (CLONE_INTO_CGROUP) with memory.max at the step budget and memory.oom.group
// set, so the cap covers go test, every test binary and every process they
// start, from their first byte, and an out-of-memory kill stops the whole
// step, as the RSS watch does. The scope sets MemorySwapMax=0, which holds for
// every cgroup below it, so a step cannot page out past its budget. Every
// gate scope lives in the one user slice ergo-gates.slice, whose MemoryMax
// bounds the gates together, so they cannot take the host either. That
// bound belongs to the host: the first gate to find the slice without one
// gives it the default host bound, and no gate changes a bound once set
// (boundHost). The kernel stops a step when it reaches its
// own budget, or when the gate's steps together reach the gate budget, or
// when the host's gates reach the host budget; in the last two cases it picks
// the largest process under the limit, and two steps can both be stopped
// when the first one's memory is not yet freed as another grows. After the
// step the gate reads the step cgroup's memory.events, which it still owns,
// with the scope's and the slice's memory.events.local, to say which budget
// stopped it; then it removes the cgroup. OOMPolicy=continue keeps systemd
// from stopping the whole gate when the kernel kills one step.
//
// A gate started inside a step of another gate (the gate's own tests, a
// gate run from a gate step) opens no scope: it runs where it is, in the
// outer step's cgroup, whose caps hold, and watches its own steps' RSS.

// gateSlice is the user slice every gate scope runs in.
const gateSlice = "ergo-gates.slice"

// enterScope re-executes the gate in its systemd scope, and returns only if
// it does not: then it says why, and the gate uses the RSS watch.
func enterScope(budget memoryBudget) string {
	if os.Getenv(mechanismVariable) != "" {
		return ""
	}
	if membership, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if step, ok := outerGateStep(string(membership)); ok {
			return "inside step cgroup " + step + " of an outer gate, whose caps hold"
		}
	}
	command, err := exec.LookPath("systemd-run")
	if err != nil {
		return "no systemd-run"
	}
	executable, err := os.Executable()
	if err != nil {
		return "locate the gate executable: " + err.Error()
	}
	probe := exec.Command(command, scopeArguments(gateSlice, "", budget, []string{"true"})...)
	probe.Env = scopeEnvironment("")
	if output, err := probe.CombinedOutput(); err != nil {
		return fmt.Sprintf("systemd-run --user --scope failed (%v): %s", err, firstLine(output))
	}
	unit := scopeUnit()
	argv := append([]string{"systemd-run"}, scopeArguments(gateSlice, unit, budget, append([]string{executable}, os.Args[1:]...))...)
	err = syscall.Exec(command, argv, scopeEnvironment(unit))
	return "re-execute in a systemd scope: " + err.Error()
}

// outerGateStep returns the cgroup that membership, the content of
// /proc/self/cgroup, places the process in, if that cgroup is a step of a
// gate's scope.
func outerGateStep(membership string) (string, bool) {
	for _, line := range strings.Split(membership, "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		segments := strings.Split(path, "/")
		for i := 0; i+1 < len(segments); i++ {
			if strings.HasPrefix(segments[i], "ergo-gate-") && strings.HasSuffix(segments[i], ".scope") && strings.HasPrefix(segments[i+1], "step-") {
				return path, true
			}
		}
	}
	return "", false
}

// scopeUnit is a fresh unit name for one gate's scope.
func scopeUnit() string {
	suffix := make([]byte, 4)
	rand.Read(suffix)
	return fmt.Sprintf("ergo-gate-%d-%s", os.Getpid(), hex.EncodeToString(suffix))
}

// scopeArguments are systemd-run's arguments that run argv in a gate scope
// in slice, named unit unless unit is empty.
func scopeArguments(slice, unit string, budget memoryBudget, argv []string) []string {
	arguments := []string{"--user", "--scope", "--quiet", "--collect", "--slice=" + slice}
	if unit != "" {
		arguments = append(arguments, "--unit="+unit)
	}
	arguments = append(arguments,
		"-p", "Delegate=yes",
		"-p", "MemoryMax="+strconv.FormatInt(budget.gate, 10),
		"-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue",
		"--")
	return append(arguments, argv...)
}

// scopeEnvironment is the environment of systemd-run and systemctl: the
// gate's, naming the scope's unit to the gate it starts. They find the
// user's service manager through XDG_RUNTIME_DIR, which a caller with a
// filtered environment (a Go test step) lacks; the conventional directory
// stands in.
func scopeEnvironment(unit string) []string {
	environment := os.Environ()
	if unit != "" {
		environment = append(environment, scopeVariable+"="+unit)
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" && os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		runtime := fmt.Sprintf("/run/user/%d", os.Getuid())
		if _, err := os.Stat(filepath.Join(runtime, "bus")); err == nil {
			environment = append(environment, "XDG_RUNTIME_DIR="+runtime)
		}
	}
	return environment
}

// cgroupRoot is where the cgroup v2 hierarchy is mounted.
const cgroupRoot = "/sys/fs/cgroup"

// scopeLimiter runs steps in cgroups of the gate's scope.
type scopeLimiter struct {
	budget memoryBudget
	// dir is the scope's cgroup, and slice the cgroup of the slice that
	// holds it.
	dir, slice string
	// hostNote describes the slice's bound for the budget line.
	hostNote string
	steps    atomic.Int64
}

// openScope takes over the scope the gate runs in, checking that it is the
// named unit and enforces the gate budget, and that a step can start in a
// cgroup of it, and bounds the gates' slice by the host budget.
func openScope(unit string, budget memoryBudget) (limiter, error) {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	var path string
	for _, line := range strings.Split(string(membership), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			path = rest
		}
	}
	if filepath.Base(path) != unit+".scope" {
		return nil, fmt.Errorf("the gate is in cgroup %q, not in its scope %s.scope", path, unit)
	}
	s := &scopeLimiter{budget: budget, dir: filepath.Join(cgroupRoot, path)}
	s.slice = filepath.Dir(s.dir)
	if limit, err := readTrimmed(filepath.Join(s.dir, "memory.max")); err != nil || limit != strconv.FormatInt(budget.gate, 10) {
		return nil, fmt.Errorf("the scope's memory.max is %q (%v), not the gate budget %d", limit, err, budget.gate)
	}
	if swap, err := readTrimmed(filepath.Join(s.dir, "memory.swap.max")); err == nil && swap != "0" {
		return nil, fmt.Errorf("the scope's memory.swap.max is %q, not 0", swap)
	}
	// A cgroup that enables controllers for its children holds no process,
	// so the gate first moves itself to a leaf.
	leaf := filepath.Join(s.dir, "gate")
	if err := os.Mkdir(leaf, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte("0"), 0); err != nil {
		return nil, fmt.Errorf("move the gate to its leaf cgroup: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "cgroup.subtree_control"), []byte("+memory"), 0); err != nil {
		return nil, fmt.Errorf("enable the memory controller for the steps: %w", err)
	}
	probe := exec.Command("true")
	watch, err := s.prepare("probe", probe)
	if err != nil {
		return nil, err
	}
	err = probe.Run()
	watch.finish()
	if err != nil {
		return nil, fmt.Errorf("start a process in a step cgroup: %w", err)
	}
	s.boundHost()
	return s, nil
}

// boundHost finds the bound of the gates' slice, which holds this gate and
// every other. The bound belongs to the host, not to one gate: a slice with
// no bound yet gets the default host bound (defaultHostMemory), and a bound
// already set is neither lowered nor raised, since lowering it under
// running gates would make the kernel kill them without a verdict. This
// gate's host budget (ERGO_GATE_HOST_MEMORY) lowers only its own budgets. A
// slice that cannot be bounded leaves the gate without a host bound, which
// the budget line says; the step and gate caps still hold.
func (s *scopeLimiter) boundHost() {
	name := filepath.Base(s.slice)
	if name != gateSlice && !strings.HasPrefix(name, strings.TrimSuffix(gateSlice, ".slice")+"-") {
		s.hostNote = fmt.Sprintf("no bound across gates (the scope is in %s, not in %s)", name, gateSlice)
		return
	}
	if limit, err := readTrimmed(filepath.Join(s.slice, "memory.max")); err == nil && limit == "max" {
		bound := defaultHostMemory()
		if bound <= 0 {
			s.hostNote = fmt.Sprintf("no bound across gates (%s has none, and physical memory is unknown)", name)
			return
		}
		// Gates that find the slice unbound at once all set the same value.
		set := exec.Command("systemctl", "--user", "set-property", "--runtime", name, "MemoryMax="+strconv.FormatInt(bound, 10))
		set.Env = scopeEnvironment("")
		if output, err := set.CombinedOutput(); err != nil {
			s.hostNote = fmt.Sprintf("no bound across gates (systemctl --user set-property %s failed (%v): %s)", name, err, firstLine(output))
			return
		}
	}
	host := sliceBound(s.slice)
	if host == 0 {
		limit, _ := readTrimmed(filepath.Join(s.slice, "memory.max"))
		s.hostNote = fmt.Sprintf("no bound across gates (%s has memory.max %q)", name, limit)
		return
	}
	s.hostNote = fmt.Sprintf("%s across gates in %s", formatBytes(host), name)
	if host < s.budget.gate {
		s.hostNote += fmt.Sprintf(", below this gate's %s", formatBytes(s.budget.gate))
	}
}

// sliceBound is the slice's memory.max in bytes, or 0 when it has none or
// it cannot be read.
func sliceBound(slice string) int64 {
	limit, err := readTrimmed(filepath.Join(slice, "memory.max"))
	if err != nil {
		return 0
	}
	bound, err := strconv.ParseInt(limit, 10, 64)
	if err != nil {
		return 0
	}
	return bound
}

func (s *scopeLimiter) describe() string {
	return "systemd scope " + describeBudget(s.budget) + ", " + s.hostNote
}

func (s *scopeLimiter) prepare(name string, command *exec.Cmd) (stepWatch, error) {
	dir := filepath.Join(s.dir, fmt.Sprintf("step-%d", s.steps.Add(1)))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory budget: %w", err)
	}
	for _, setting := range []struct{ file, value string }{
		{"memory.max", strconv.FormatInt(s.budget.step, 10)},
		// An out-of-memory kill of one process stops every process of the
		// step, so no part of it runs on without the rest.
		{"memory.oom.group", "1"},
	} {
		if err := os.WriteFile(filepath.Join(dir, setting.file), []byte(setting.value), 0); err != nil {
			os.Remove(dir)
			return nil, fmt.Errorf("memory budget: %w", err)
		}
	}
	handle, err := os.Open(dir)
	if err != nil {
		os.Remove(dir)
		return nil, fmt.Errorf("memory budget: %w", err)
	}
	command.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(handle.Fd())}
	return &scopeStep{limiter: s, dir: dir, handle: handle, gateOOM: localOOM(s.dir), hostOOM: localOOM(s.slice)}, nil
}

func (s *scopeLimiter) close() {}

// scopeStep is one step's cgroup.
type scopeStep struct {
	limiter *scopeLimiter
	dir     string
	handle  *os.File
	// gateOOM and hostOOM are the times the scope and the slice had reached
	// their own memory.max when the step started, or -1 if unknown.
	gateOOM, hostOOM int64
}

func (s *scopeStep) started(int) {}

// finish reads the step cgroup's counters. Its own "oom" counts the times
// the step reached its memory.max; "oom_kill" counts its processes the
// kernel killed, for its own budget or for a larger one. Which larger one is
// the limit that was reached while the step ran: the scope's (the gate
// budget) or else the slice's (the host bound, as the slice has it now).
// Only memory.events.local says that a cgroup reached its own limit;
// memory.events also counts its descendants, such as another gate's scope
// in the slice. With other gates in the slice the choice is an inference,
// not a record, and a kill no counter explains is put down to the gate
// budget. Any process left in the step's cgroup after its leader exits fails
// the step; the gate kills the cgroup and waits for it to empty before output
// is drained or the cgroup is removed.
func (s *scopeStep) finish() usage {
	var u usage
	if peak, err := readTrimmed(filepath.Join(s.dir, "memory.peak")); err == nil {
		u.peak, _ = strconv.ParseInt(peak, 10, 64)
	}
	events, err := readEvents(filepath.Join(s.dir, "memory.events"))
	rose := func(before int64, dir string) bool { return before >= 0 && localOOM(dir) > before }
	host := sliceBound(s.limiter.slice)
	switch {
	case err != nil:
		u.verdict = "memory budget not enforced: read the step's memory events: " + err.Error()
	case events["oom"] > 0:
		u.verdict = stepExceeded(s.limiter.budget)
	case events["oom_kill"] == 0:
	case !rose(s.gateOOM, s.limiter.dir) && host > 0 && rose(s.hostOOM, s.limiter.slice):
		u.verdict = hostExceeded(host)
	default:
		u.verdict = gateExceeded(s.limiter.budget)
	}

	members, stopErr := stopStepCgroup(s.dir)
	if len(members) > 0 {
		addProcessIssue(&u, "leftover processes at leader exit: "+strings.Join(members, ", "))
	}
	if stopErr != nil {
		addProcessIssue(&u, "could not stop step cgroup: "+stopErr.Error())
		u.stopFailed = true
	}
	if err := s.handle.Close(); err != nil {
		addProcessIssue(&u, "could not close step cgroup handle: "+err.Error())
	}
	_ = os.Remove(s.dir)
	return u
}

func stopStepCgroup(dir string) ([]string, error) {
	pids, inspectErr := stepCgroupPIDs(dir)
	members := make([]string, 0, len(pids))
	for _, pid := range pids {
		name := "unknown"
		if content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm")); err == nil {
			if value := strings.TrimSpace(string(content)); value != "" {
				name = value
			}
		}
		members = append(members, fmt.Sprintf("%s (pid %d)", name, pid))
	}
	sort.Strings(members)

	killErr := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0)
	if killErr != nil && len(pids) == 0 && inspectErr == nil {
		return members, nil
	}
	if killErr != nil {
		return members, errors.Join(inspectErr, killErr)
	}

	for {
		current, err := stepCgroupPIDs(dir)
		if err != nil {
			return members, errors.Join(inspectErr, err)
		}
		if len(current) == 0 {
			return members, inspectErr
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stepCgroupPIDs(dir string) ([]int, error) {
	var pids []int
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		for _, field := range strings.Fields(string(content)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return fmt.Errorf("%s: invalid process id %q", filepath.Join(path, "cgroup.procs"), field)
			}
			if processIsZombie(pid) {
				continue
			}
			pids = append(pids, pid)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Ints(pids)
	return pids, nil
}

func processIsZombie(pid int) bool {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if state, ok := strings.CutPrefix(line, "State:"); ok {
			return strings.HasPrefix(strings.TrimSpace(state), "Z") || strings.HasPrefix(strings.TrimSpace(state), "X")
		}
	}
	return false
}

// localOOM is the number of times the cgroup dir reached its own
// memory.max, or -1 if unknown.
func localOOM(dir string) int64 {
	events, err := readEvents(filepath.Join(dir, "memory.events.local"))
	if err != nil {
		return -1
	}
	return events["oom"]
}

func readTrimmed(name string) (string, error) {
	content, err := os.ReadFile(name)
	return strings.TrimSpace(string(content)), err
}

// readEvents parses a cgroup "name value" counter file.
func readEvents(name string) (map[string]int64, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	events := map[string]int64{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), " ")
		if !ok {
			return nil, fmt.Errorf("%s: unexpected line %q", name, scanner.Text())
		}
		count, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		events[key] = count
	}
	if len(events) == 0 {
		return nil, errors.New(name + " is empty")
	}
	return events, nil
}

// totalMemory reads MemTotal from /proc/meminfo.
func totalMemory() int64 {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(content), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			fields := strings.Fields(rest)
			if len(fields) == 2 && fields[1] == "kB" {
				kilobytes, err := strconv.ParseInt(fields[0], 10, 64)
				if err == nil {
					return kilobytes << 10
				}
			}
		}
	}
	return 0
}
