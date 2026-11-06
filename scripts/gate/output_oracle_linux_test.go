package main

import (
	"bytes"
	"errors"
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
	// lookupErrors are failed lookups other than of a file or process
	// that is gone: the scan did not see everything.
	lookupErrors []string
	// denied holds this sample's denied lookups by pid (settleDenied).
	denied  map[string][]string
	samples int
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
	r.settleDenied()
}

// memoryFilesOf records the open files in memory of process pid.
func (r *scopeReport) memoryFilesOf(pid string) {
	fds := filepath.Join("/proc", pid, "fd")
	entries, err := os.ReadDir(fds)
	if err != nil {
		r.lookupFailed(fds, err)
		return
	}
	for _, entry := range entries {
		if file, ok := r.pinnedFile(filepath.Join(fds, entry.Name())); ok && file.inMemory() {
			key := file.system + ":" + file.name
			r.memoryFiles[key] = max(r.memoryFiles[key], file.size)
		}
	}
}

// oPath is open(2)'s O_PATH, which the syscall package does not name: a
// handle to a file that reads no data and needs no permission on it.
const oPath = 0x200000

// pinnedFile is what the kernel says of one open file, every fact taken
// from one handle to it.
type pinnedFile struct {
	// system is the memory file system's name, or empty.
	system string
	// name is the file's path, "/memfd:..." for a memfd.
	name string
	// size is the larger of its size and its allocated blocks.
	size int64
}

func (f pinnedFile) inMemory() bool {
	return f.system != "" || strings.HasPrefix(f.name, "/memfd:")
}

// pinnedFile opens the file that the fd link fd names once, as an O_PATH
// handle, and takes its type, size, file system and name from that handle
// alone. A process may close the fd, reuse its number for another file, or
// exit at any time; separate lookups of the link could then mix facts of
// two files (round 3 B1). With one handle, every fact is of the file the
// open found. It reports false for a regular file it did not find, or for a
// file that is not regular.
func (r *scopeReport) pinnedFile(fd string) (pinnedFile, bool) {
	handle, err := syscall.Open(fd, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		r.lookupFailed(fd, err)
		return pinnedFile{}, false
	}
	defer syscall.Close(handle)
	var stat syscall.Stat_t
	if err := syscall.Fstat(handle, &stat); err != nil {
		r.lookupFailed(fd, err)
		return pinnedFile{}, false
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return pinnedFile{}, false
	}
	var system syscall.Statfs_t
	if err := syscall.Fstatfs(handle, &system); err != nil {
		r.lookupFailed(fd, err)
		return pinnedFile{}, false
	}
	name, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(handle))
	if err != nil {
		r.lookupFailed(fd, err)
		return pinnedFile{}, false
	}
	return pinnedFile{system: memoryMagics[int64(system.Type)], name: name, size: max(stat.Size, stat.Blocks*512)}, true
}

// lookupFailed records a failed lookup, unless the fd or its process is
// gone, which a scan of live processes must expect. The kernel also denies
// a process's fds (EACCES) once it has begun to exit and dropped its
// memory, so a denied lookup waits for the end of the sample
// (settleDenied), which keeps it only if its process still runs.
func (r *scopeReport) lookupFailed(name string, err error) {
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH) {
		return
	}
	failure := fmt.Sprintf("%s: %v", name, err)
	if errors.Is(err, syscall.EACCES) {
		if pid, _, ok := strings.Cut(strings.TrimPrefix(name, "/proc/"), "/"); ok {
			if r.denied == nil {
				r.denied = map[string][]string{}
			}
			r.denied[pid] = append(r.denied[pid], failure)
			return
		}
	}
	r.addLookupError(failure)
}

func (r *scopeReport) addLookupError(failure string) {
	if len(r.lookupErrors) < 8 {
		r.lookupErrors = append(r.lookupErrors, failure)
	}
}

// settleDenied keeps the denied lookups of processes that still run: not
// gone, not a zombie and not dead.
func (r *scopeReport) settleDenied() {
	for pid, failures := range r.denied {
		if !scannedProcessEnded(pid) {
			for _, failure := range failures {
				r.addLookupError(failure)
			}
		}
		delete(r.denied, pid)
	}
}

// scannedProcessEnded reports whether process pid is gone, a zombie or dead, or
// exiting: its memory is gone (no VmSize in its status).
func scannedProcessEnded(pid string) bool {
	stat, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return true
	}
	// The state follows the command name, which is in parentheses and may
	// hold any byte.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return true
	}
	if state := stat[end+2]; state == 'Z' || state == 'X' || state == 'x' {
		return true
	}
	status, err := os.ReadFile("/proc/" + pid + "/status")
	return err != nil || !bytes.Contains(status, []byte("\nVmSize:"))
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
// stays under it. The watch must have seen the scope and read its shmem,
// unless refused is set: the gate refused before any step ran, so there was
// no step output to place, and such a gate can end before the watch's
// first sample (measured at load 15-38: 2 of 960 helper runs, both
// refusals), or after the watch found the scope but before it read the
// scope's memory.stat (shmem -1: batch-8 gate-0a8e52fd-try1.log:131). The
// other checks still hold for a refused gate.
func checkPlacement(t *testing.T, run int, report scopeReport, diskLayout, refused bool) {
	t.Helper()
	t.Logf("run %d: scope %s, %d samples, shmem peak %d, open files in memory %v", run, report.scope, report.samples, report.shmem, report.memoryFiles)
	if report.scope == "" || report.shmem < 0 {
		if !refused {
			t.Errorf("run %d: the watch did not see the helper's scope (scope %q, shmem %d)", run, report.scope, report.shmem)
			return
		}
		t.Logf("run %d: the gate refused before the watch read the scope (scope %q, shmem %d)", run, report.scope, report.shmem)
	}
	if len(report.lookupErrors) > 0 {
		t.Errorf("run %d: the watch could not look at every open file: %v", run, report.lookupErrors)
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

// placementCaseVariable carries one case of TestCheckPlacementExemptions
// to its helper: "refused,scope,shmem,violation", each 0 or 1 (scope 1 is
// a scope seen, shmem 1 a shmem read, violation 1 a file in memory on a
// disk layout).
const placementCaseVariable = "GATE_TEST_PLACEMENT_CASE"

// TestCheckPlacementCaseHelper runs checkPlacement on the case that
// placementCaseVariable names; it passes only if checkPlacement reports
// nothing.
func TestCheckPlacementCaseHelper(t *testing.T) {
	value := os.Getenv(placementCaseVariable)
	if value == "" {
		t.Skip("TestCheckPlacementExemptions runs it with a case")
	}
	var flags [4]bool
	for i, field := range strings.Split(value, ",") {
		flags[i] = field == "1"
	}
	refused, scopeSeen, shmemRead, violation := flags[0], flags[1], flags[2], flags[3]
	report := scopeReport{shmem: -1, memoryFiles: map[string]int64{}, samples: 3}
	if scopeSeen {
		report.scope = "/sys/fs/cgroup/nonexistent/loop-probe-x.scope"
	}
	if shmemRead {
		report.shmem = 0
	}
	if violation {
		report.memoryFiles["tmpfs:/nonexistent/loop-probe-x"] = 1
	}
	checkPlacement(t, 1, report, true, refused)
}

// checkPlacement exempts a refused gate, and only a refused gate, from
// seeing the scope whole: the scope not found, or found with no shmem read.
// Every other check still holds. The property covers every combination of
// refused, scope seen, shmem read and an output file in memory; each runs
// in a child test process, as checkPlacement reports through its
// testing.T.
func TestCheckPlacementExemptions(t *testing.T) {
	bit := func(set bool) string {
		if set {
			return "1"
		}
		return "0"
	}
	for _, refused := range []bool{false, true} {
		for _, scopeSeen := range []bool{false, true} {
			for _, shmemRead := range []bool{false, true} {
				for _, violation := range []bool{false, true} {
					value := strings.Join([]string{bit(refused), bit(scopeSeen), bit(shmemRead), bit(violation)}, ",")
					seen := scopeSeen && shmemRead
					wantPass := !violation && (seen || refused)
					command := exec.Command(os.Args[0], "-test.run=^TestCheckPlacementCaseHelper$", "-test.count=1", "-test.v")
					command.Env = append(os.Environ(), placementCaseVariable+"="+value)
					output, err := command.CombinedOutput()
					var exitErr *exec.ExitError
					if err != nil && !errors.As(err, &exitErr) {
						t.Fatalf("case %s: run the helper: %v", value, err)
					}
					if passed := err == nil; passed != wantPass {
						t.Errorf("case refused,scope,shmem,violation=%s: checkPlacement passed=%t, want %t:\n%s", value, passed, wantPass, output)
					}
				}
			}
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
				if len(report.lookupErrors) > 0 {
					t.Errorf("the watch could not look at every open file: %v", report.lookupErrors)
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

// reuseSize is the size of reuseDescriptor's sparse disk file: over
// gateShmemLimit, with no block allocated.
const reuseSize = 31 << 20

// churnStepLife is how long each step of the "exit" churn reuses its
// descriptor before it exits.
const churnStepLife = 20 * time.Millisecond

// reuseDescriptor puts, for life, a sparse disk file of reuseSize bytes in
// dir and an empty memfd at one descriptor number in turn. No file in
// memory ever holds a byte, so a watch that says one does mixed the facts
// of the two files.
func reuseDescriptor(dir string, life time.Duration) error {
	disk, err := oracleFileIn(dir)
	if err != nil {
		return err
	}
	defer disk.Close()
	if err := disk.Truncate(reuseSize); err != nil {
		return err
	}
	memory, err := oracleFile("memfd")
	if err != nil {
		return err
	}
	defer memory.Close()
	fd, err := syscall.Dup(int(disk.Fd()))
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	for until := time.Now().Add(life); time.Now().Before(until); {
		if err := syscall.Dup3(int(disk.Fd()), fd, syscall.O_CLOEXEC); err != nil {
			return err
		}
		if err := syscall.Dup3(int(memory.Fd()), fd, syscall.O_CLOEXEC); err != nil {
			return err
		}
	}
	if info, err := memory.Stat(); err != nil || info.Size() != 0 {
		return fmt.Errorf("the memfd: %v, %v; want 0 bytes", info, err)
	}
	return nil
}

// oracleFileIn makes an unlinked file in dir.
func oracleFileIn(dir string) (*os.File, error) {
	file, err := os.CreateTemp(dir, "oracle-")
	if err != nil {
		return nil, err
	}
	return file, os.Remove(file.Name())
}

// checkReuseReport checks a watch of descriptor reuse: it saw the empty
// memfd, so the scan ran while it was open, and every file in memory it
// reports is a memfd with no bytes, so it never mixed the disk file's size
// or name with the memfd's file system.
func checkReuseReport(t *testing.T, report scopeReport) {
	t.Helper()
	t.Logf("%d samples, shmem peak %d, open files in memory %v, lookup errors %v", report.samples, report.shmem, report.memoryFiles, report.lookupErrors)
	if len(report.lookupErrors) > 0 {
		t.Errorf("the watch could not look at every open file: %v", report.lookupErrors)
	}
	sawMemfd := false
	for file, size := range report.memoryFiles {
		if strings.HasPrefix(file, "tmpfs:/memfd:gate-oracle") {
			sawMemfd = true
		}
		if size != 0 || !strings.HasPrefix(file, "tmpfs:/memfd:") {
			t.Errorf("the watch reported %s with %d bytes in memory; the only file in memory is an empty memfd", file, size)
		}
	}
	if !sawMemfd {
		t.Errorf("the watch never saw the memfd: the scan did not run while it was open")
	}
}

// TestBudgetOutputOracleFDReuse scans this process 2000 times while one of
// its descriptors switches between a sparse disk file and an empty memfd
// (the verifier's round-3 TestVerifyFDReuse).
func TestBudgetOutputOracleFDReuse(t *testing.T) {
	if _, ok := memfdCreateNumber[runtime.GOARCH]; !ok {
		t.Skipf("no memfd_create number for %s", runtime.GOARCH)
	}
	t.Setenv("GATE_ORACLE_DISK", t.TempDir())
	done, ended := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			if err := reuseDescriptor(os.Getenv("GATE_ORACLE_DISK"), 10*time.Millisecond); err != nil {
				ended <- err
				return
			}
			select {
			case <-done:
				ended <- nil
				return
			default:
			}
		}
	}()
	report := scopeReport{shmem: 0, memoryFiles: map[string]int64{}}
	pid := strconv.Itoa(os.Getpid())
	for range 2000 {
		report.samples++
		report.memoryFilesOf(pid)
		report.settleDenied()
	}
	close(done)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	checkReuseReport(t, report)
}

// TestBudgetOutputOracleChurn runs the watch on real gate scopes whose
// processes reuse descriptors ("reuse": the gate, as in the verifier's
// round-3 TestVerifyScopeFDReuse) and exit while they are scanned
// ("exit": steps that each reuse a descriptor for churnStepLife and exit,
// four at a time). The watch must report no mixed file and no failure.
func TestBudgetOutputOracleChurn(t *testing.T) {
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
	for _, allocator := range []string{"reuse", "exit"} {
		t.Run(allocator, func(t *testing.T) {
			h := newScopeHelper(t, slice, oracleCase, budget)
			for name, value := range map[string]string{"GATE_ORACLE_ALLOCATOR": allocator, "GATE_ORACLE_DISK": t.TempDir()} {
				h.command.Env = setEnvironmentVariable(h.command.Env, name, value)
			}
			report := runWatched(t, h)
			text := h.output.String()
			t.Logf("helper output:\n%s", text)
			if !strings.Contains(text, "oracle-exit=0\n") {
				t.Fatalf("the oracle helper failed:\n%s", text)
			}
			if report.scope == "" || report.shmem < 0 {
				t.Fatalf("the watch did not see the helper's scope (scope %q, shmem %d)", report.scope, report.shmem)
			}
			if report.shmem > gateShmemLimit {
				t.Errorf("the scope's shmem peak is %d, over %d, with no byte in memory", report.shmem, int64(gateShmemLimit))
			}
			checkReuseReport(t, report)
		})
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
	if os.Getenv("GATE_ORACLE_STEP") == "churn" {
		return reuseDescriptor(os.Getenv("GATE_ORACLE_DISK"), churnStepLife)
	}
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
	cpus, err := currentCPUs()
	if err != nil {
		return err
	}
	limiter, err := openScope(scope, budget, cpus)
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
	case "reuse":
		return reuseDescriptor(os.Getenv("GATE_ORACLE_DISK"), oracleHold)
	case "exit":
		// Steps that each reuse a descriptor and exit, four at a time,
		// for oracleHold: processes leave the scope while it is scanned.
		until := time.Now().Add(oracleHold)
		started := 0
		for time.Now().Before(until) {
			errs := make(chan error, 4)
			for range 4 {
				command := exec.Command(os.Args[0], "-test.run=^TestBudgetScopeHelper$", "-test.count=1")
				command.Env = append(os.Environ(), "GATE_ORACLE_STEP=churn")
				go func() { errs <- step(command) }()
			}
			for range 4 {
				if err := <-errs; err != nil {
					return err
				}
			}
			started += 4
		}
		fmt.Printf("oracle: %d churn steps\n", started)
		return nil
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
