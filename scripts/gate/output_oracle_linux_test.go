package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// The output-placement oracle. The test, not the gate, judges where step
// output went, and it judges the whole helper scope: the gate's leaf
// cgroup and every step cgroup under it. A tmpfs page is charged to the
// cgroup of the process that first touched it, so a leaf alone misses
// output that a step writes (round 2 M1, mutant m10). The watch therefore
// looks at two things the kernel keeps, for the scope and all of its
// processes:
//   - the scope's memory.stat shmem, which counts its descendants; and
//   - every open file of every process in the scope and its children
//     (/proc/<pid>/fd for each pid in cgroup.procs): a regular file on
//     tmpfs, ramfs or hugetlbfs, or a memfd, is output in memory, whatever
//     process opened it and whatever the gate's own placement check says.
// TestBudgetOutputOracle shows that the watch sees output in memory for
// each process that can allocate it: the gate, a step, and a file the gate
// opens and passes to a step.

// Memory file systems by statfs(2) magic.
var memoryMagics = map[int64]string{testTmpfsMagic: "tmpfs", 0x858458f6: "ramfs", 0x958458f6: "hugetlbfs"}

// scopeReport is what a scopeWatch saw of one helper scope.
type scopeReport struct {
	// scope is the scope's cgroup directory, or empty if the watch never
	// found it.
	scope string
	// shmem is the largest shmem of the scope's memory.stat, or -1.
	shmem int64
	// memoryFiles maps each open file in memory to the most bytes it held.
	memoryFiles map[string]int64
	samples     int
}

// scopeWatch samples the scope of a started helper every millisecond.
type scopeWatch struct {
	done   chan struct{}
	result chan scopeReport
}

// watchScope starts a watch on the scope named unit that the process pid
// joins.
func watchScope(pid int, unit string) *scopeWatch {
	w := &scopeWatch{done: make(chan struct{}), result: make(chan scopeReport, 1)}
	go func() {
		report := scopeReport{shmem: -1, memoryFiles: map[string]int64{}}
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			report.sample(pid, unit)
			select {
			case <-w.done:
				report.sample(pid, unit)
				w.result <- report
				return
			case <-tick.C:
			}
		}
	}()
	return w
}

// stop ends the watch and returns what it saw.
func (w *scopeWatch) stop() scopeReport {
	close(w.done)
	return <-w.result
}

func (r *scopeReport) sample(pid int, unit string) {
	if r.scope == "" {
		membership, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if err != nil {
			return
		}
		path, ok := strings.CutPrefix(strings.TrimSpace(string(membership)), "0::")
		before, _, found := strings.Cut(path, "/"+unit+".scope")
		if !ok || !found {
			return
		}
		r.scope = filepath.Join(cgroupRoot, before, unit+".scope")
	}
	r.samples++
	if stat, err := os.ReadFile(filepath.Join(r.scope, "memory.stat")); err == nil {
		if value := shmemIn(stat); value >= 0 {
			r.shmem = max(r.shmem, value)
		}
	}
	_ = filepath.WalkDir(r.scope, func(dir string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if err != nil {
			return nil
		}
		for _, field := range strings.Fields(string(procs)) {
			r.memoryFilesOf(field)
		}
		return nil
	})
}

// memoryFilesOf records the open files in memory of process pid.
func (r *scopeReport) memoryFilesOf(pid string) {
	fds := filepath.Join("/proc", pid, "fd")
	entries, err := os.ReadDir(fds)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := filepath.Join(fds, entry.Name())
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		target, _ := os.Readlink(name)
		var stat syscall.Statfs_t
		if err := syscall.Statfs(name, &stat); err != nil {
			continue
		}
		if memoryMagics[int64(stat.Type)] == "" && !strings.HasPrefix(target, "/memfd:") {
			continue
		}
		size := info.Size()
		if system, ok := info.Sys().(*syscall.Stat_t); ok {
			size = max(size, system.Blocks*512)
		}
		key := memoryMagics[int64(stat.Type)] + ":" + target
		r.memoryFiles[key] = max(r.memoryFiles[key], size)
	}
}

// shmemIn is the shmem bytes of a memory.stat, or -1.
func shmemIn(stat []byte) int64 {
	for _, line := range bytes.Split(stat, []byte("\n")) {
		if value, ok := bytes.CutPrefix(line, []byte("shmem ")); ok {
			if size, err := strconv.ParseInt(string(value), 10, 64); err == nil {
				return size
			}
		}
	}
	return -1
}

// runWatched runs a scope helper under a scopeWatch.
func runWatched(t *testing.T, h *scopeHelper) scopeReport {
	t.Helper()
	if err := h.command.Start(); err != nil {
		t.Fatalf("start the scope helper: %v", err)
	}
	watch := watchScope(h.command.Process.Pid, h.unit)
	if err := h.command.Wait(); err != nil {
		t.Logf("scope helper exited %v", err)
	}
	return watch.stop()
}

// checkPlacement checks a scopeReport of a gateOutputCase helper. On a
// disk layout no open file in the scope may be in memory at all; on any
// layout none may hold more than gateShmemLimit, and the scope's shmem
// stays under it.
func checkPlacement(t *testing.T, run int, report scopeReport, diskLayout bool) {
	t.Helper()
	t.Logf("run %d: scope %s, %d samples, shmem peak %d, open files in memory %v", run, report.scope, report.samples, report.shmem, report.memoryFiles)
	if report.scope == "" || report.shmem < 0 {
		t.Errorf("run %d: the watch did not see the helper's scope (scope %q, shmem %d)", run, report.scope, report.shmem)
		return
	}
	if report.shmem > gateShmemLimit {
		t.Errorf("run %d: the helper's scope held %d bytes on tmpfs, over %d: step output was written to memory", run, report.shmem, int64(gateShmemLimit))
	}
	for file, size := range report.memoryFiles {
		if diskLayout || size > gateShmemLimit {
			t.Errorf("run %d: a process in the helper's scope had %s open in memory with %d bytes: step output was placed in memory", run, file, size)
		}
	}
}

// oracleAllocators are the processes that can allocate a file for step
// output: the gate writes it, a step makes and writes it, or the gate
// makes it and passes it to the step as its standard output.
var oracleAllocators = []string{"gate", "step", "passed"}

// oracleBackings are where the file is: on disk, on tmpfs, or a memfd.
var oracleBackings = []string{"disk", "tmpfs", "memfd"}

// TestBudgetOutputOracle checks the oracle by property over allocator and
// backing: for each, a helper in a real gate scope (openScope, steps in
// their own cgroups) puts outputBytes of output in an unlinked file and
// holds it open. The watch must see each file in memory both in its fd scan
// and in the scope's shmem, and must see nothing in memory for a file on
// disk.
func TestBudgetOutputOracle(t *testing.T) {
	if _, err := exec.LookPath("seq"); err != nil {
		t.Skip("seq is unavailable")
	}
	if _, ok := memfdCreateNumber[runtime.GOARCH]; !ok {
		t.Skipf("no memfd_create number for %s", runtime.GOARCH)
	}
	slice := testSlice(t)
	setBudgetCaseTemp(t)
	t.Setenv(stepMemoryVariable, gateOutputCase.step)
	t.Setenv(gateMemoryVariable, gateOutputCase.gate)
	t.Setenv(hostMemoryVariable, "4G")
	budget, err := currentBudget()
	if err != nil {
		t.Fatal(err)
	}
	startableScope(t, slice, budget)
	size := seqBytes(outputLines)
	for _, allocator := range oracleAllocators {
		for _, backing := range oracleBackings {
			t.Run(allocator+","+backing, func(t *testing.T) {
				roots := newStorageRoots(t)
				h := newScopeHelper(t, slice, oracleCase, budget)
				for name, value := range map[string]string{"GATE_ORACLE_ALLOCATOR": allocator, "GATE_ORACLE_BACKING": backing, "GATE_ORACLE_DISK": roots.disk, "GATE_ORACLE_MEMORY": roots.memory} {
					h.command.Env = setEnvironmentVariable(h.command.Env, name, value)
				}
				report := runWatched(t, h)
				text := h.output.String()
				t.Logf("helper output:\n%s", text)
				if !strings.Contains(text, "oracle-exit=0\n") {
					t.Fatalf("the oracle helper failed:\n%s", text)
				}
				t.Logf("scope %s, %d samples, shmem peak %d, open files in memory %v", report.scope, report.samples, report.shmem, report.memoryFiles)
				if report.scope == "" || report.shmem < 0 {
					t.Fatalf("the watch did not see the helper's scope (scope %q, shmem %d)", report.scope, report.shmem)
				}
				largest := int64(0)
				for _, held := range report.memoryFiles {
					largest = max(largest, held)
				}
				if backing == "disk" {
					if len(report.memoryFiles) != 0 || report.shmem > gateShmemLimit {
						t.Errorf("a file on disk: the watch saw open files in memory %v and shmem %d", report.memoryFiles, report.shmem)
					}
					return
				}
				// Each detector alone must reach checkPlacement's limit. The
				// fd scan sees the file's full size; the scope's shmem
				// lags, because the kernel flushes cgroup statistics
				// lazily (measured: 30.2M to 30.9M of 30.9M).
				if largest < size {
					t.Errorf("the fd scan saw at most %d bytes open in memory, want the %d of the output", largest, size)
				}
				if report.shmem <= gateShmemLimit {
					t.Errorf("the scope's shmem peak is %d, not over %d: the output in memory is not seen", report.shmem, int64(gateShmemLimit))
				}
			})
		}
	}
}

// oracleCase is the GATE_BUDGET_CASE of the oracle helper.
const oracleCase = "output-oracle"

// oracleHold is how long the oracle helper holds a file open, many samples.
const oracleHold = 300 * time.Millisecond

// memfdCreateNumber is memfd_create(2)'s system call number by GOARCH.
var memfdCreateNumber = map[string]uintptr{"amd64": 319, "arm64": 279}

// oracleFile makes an unlinked file in backing.
func oracleFile(backing string) (*os.File, error) {
	switch backing {
	case "memfd":
		name := []byte("gate-oracle\x00")
		fd, _, errno := syscall.Syscall(memfdCreateNumber[runtime.GOARCH], uintptr(unsafe.Pointer(&name[0])), 0, 0)
		if errno != 0 {
			return nil, errno
		}
		return os.NewFile(fd, "memfd:gate-oracle"), nil
	case "disk", "tmpfs":
		dir := os.Getenv("GATE_ORACLE_DISK")
		if backing == "tmpfs" {
			dir = os.Getenv("GATE_ORACLE_MEMORY")
		}
		file, err := os.CreateTemp(dir, "oracle-")
		if err != nil {
			return nil, err
		}
		return file, os.Remove(file.Name())
	}
	return nil, fmt.Errorf("no backing %q", backing)
}

// writeOutput writes outputLines of seq's output to file.
func writeOutput(file *os.File) error {
	buffer := make([]byte, 0, 64<<10)
	for n := 1; n <= outputLines; n++ {
		buffer = strconv.AppendInt(buffer, int64(n), 10)
		buffer = append(buffer, '\n')
		if len(buffer) > 60<<10 {
			if _, err := file.Write(buffer); err != nil {
				return err
			}
			buffer = buffer[:0]
		}
	}
	_, err := file.Write(buffer)
	return err
}

// runOracleHelper is TestBudgetScopeHelper for oracleCase. As a gate, it
// opens its scope and puts the output in a file by GATE_ORACLE_ALLOCATOR;
// as a step (GATE_ORACLE_STEP), it makes, writes and holds the file itself.
func runOracleHelper(scope string) error {
	backing := os.Getenv("GATE_ORACLE_BACKING")
	if os.Getenv("GATE_ORACLE_STEP") != "" {
		file, err := oracleFile(backing)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := writeOutput(file); err != nil {
			return err
		}
		time.Sleep(oracleHold)
		return nil
	}
	budget, err := currentBudget()
	if err != nil {
		return err
	}
	limiter, err := openScope(scope, budget)
	if err != nil {
		return err
	}
	defer limiter.close()
	step := func(command *exec.Cmd) error {
		watch, err := limiter.prepare("oracle", command)
		if err != nil {
			return err
		}
		if err := command.Start(); err != nil {
			watch.finish()
			return err
		}
		watch.started(command.Process.Pid)
		err = command.Wait()
		watch.finish()
		return err
	}
	switch allocator := os.Getenv("GATE_ORACLE_ALLOCATOR"); allocator {
	case "gate":
		file, err := oracleFile(backing)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := writeOutput(file); err != nil {
			return err
		}
		time.Sleep(oracleHold)
		return nil
	case "step":
		command := exec.Command(os.Args[0], "-test.run=^TestBudgetScopeHelper$", "-test.count=1")
		command.Env = append(os.Environ(), "GATE_ORACLE_STEP=1")
		return step(command)
	case "passed":
		file, err := oracleFile(backing)
		if err != nil {
			return err
		}
		defer file.Close()
		command := exec.Command("seq", "1", strconv.Itoa(outputLines))
		command.Stdout = file
		if err := step(command); err != nil {
			return err
		}
		time.Sleep(oracleHold)
		return nil
	default:
		return fmt.Errorf("no allocator %q", allocator)
	}
}
