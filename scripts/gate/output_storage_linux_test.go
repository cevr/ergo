package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// The output-storage property: whatever the gate does with a step's output,
// none of it is held in memory charged to the gate. The step is
// gateOutputCase's: 31 MB of output from a step far under its budget, in a
// real scope. Every place
// the gate writes step output is a destination: the output spool, the cache's
// output file, and the gate's own standard output. Each is put, in turn, in
// each storage place (on disk, on tmpfs, a link on tmpfs to disk, a link on
// disk to tmpfs), and the gate runs twice on one cache, to record and then
// to replay. On every layout the gate must finish (gate-exit) and either
//   - pass with every line of the output, in order; or
//   - refuse with exit 2, naming the destination, before any step runs.
// Its resident memory stays under gateResidentLimit, under half the output:
// no step output in its heap. The test's own oracle (output_oracle_linux_test.go)
// watches the whole helper scope, the gate and every step: its shmem stays
// under gateShmemLimit, and no process in it has a file in memory open
// (tmpfs, ramfs, hugetlbfs or a memfd, linked or unlinked) on a layout of
// disks only, or one larger than gateShmemLimit on any layout. The kernel's
// view of the real files, not the gate's own placement check, judges where
// the output went. It records a pass only where the cache's
// output file is on disk; with the cache in memory it says it did not
// record, and the second run executes again. Whatever the runs leave on
// tmpfs is small: no step output in a file in memory.
//
// The output is small so that the property costs little disk (2026-11-05,
// measured): with 1.09 GB, enough to fill the gate's 512M reserve, the
// layouts took 14.2 GB at their peak, eight at a time.

// storagePlace is where a destination's directory is.
type storagePlace int

const (
	onDisk storagePlace = iota
	inMemory
	memoryLinkToDisk
	diskLinkToMemory
	// inCache is the spool's default place: the gate makes output/ in the
	// cache directory, wherever that is.
	inCache
	// toPipe is the default standard output: a pipe the test reads.
	toPipe
)

// storagePlaces are the places a directory can be.
var storagePlaces = []storagePlace{onDisk, inMemory, memoryLinkToDisk, diskLinkToMemory}

func (p storagePlace) String() string {
	return [...]string{"disk", "tmpfs", "tmpfs-link-to-disk", "disk-link-to-tmpfs", "in-cache", "pipe"}[p]
}

// memoryBacked reports whether a file made in the place is on tmpfs.
func (p storagePlace) memoryBacked() bool {
	return p == inMemory || p == diskLinkToMemory
}

// storageLayout puts each destination of step output in a place: the cache
// directory (ERGO_GATE_CACHE), the spool directory (output/ in the cache,
// or a link there to a place of its own), and the gate's standard output
// (a pipe, or a file in a place).
type storageLayout struct {
	cache, spool, stdout storagePlace
}

func (l storageLayout) String() string {
	return fmt.Sprintf("cache=%s,spool=%s,stdout=%s", l.cache, l.spool, l.stdout)
}

// storageLayouts is the whole domain: every cache place with every spool
// place, the gate printing to a pipe; and every place for the gate's
// standard output, with the cache and spool on disk.
func storageLayouts() []storageLayout {
	var layouts []storageLayout
	for _, cache := range storagePlaces {
		for _, spool := range append([]storagePlace{inCache}, storagePlaces...) {
			layouts = append(layouts, storageLayout{cache: cache, spool: spool, stdout: toPipe})
		}
	}
	for _, stdout := range storagePlaces {
		layouts = append(layouts, storageLayout{cache: onDisk, spool: inCache, stdout: stdout})
	}
	return layouts
}

// fixedStorageCases are the instances that reviews found, named in the
// property so that `-run 'TestBudgetOutputStorage/fixed-'` runs them alone.
var fixedStorageCases = map[storageLayout]string{
	// Round 1 M1: the default layout, the output held in the gate's heap.
	{cache: onDisk, spool: inCache, stdout: toPipe}: "fixed-round-1-heap",
	// Round 2 M1 (the reviewer's TestReviewCacheReserve): the cache on
	// tmpfs, its output/ a link to disk, so only the spool was on disk.
	{cache: inMemory, spool: onDisk, stdout: toPipe}: "fixed-round-2-cache-on-tmpfs",
}

// expected is what the gate must do on a layout in one run.
type expected struct {
	// refusal is the line of a gate that refuses to run, or empty.
	refusal string
	// status is the step's status line of a gate that runs.
	status string
	// declined is set when the gate must say it did not record the pass.
	declined bool
}

func (l storageLayout) spoolMemoryBacked() bool {
	if l.spool == inCache {
		return l.cache.memoryBacked()
	}
	return l.spool.memoryBacked()
}

// onDiskOnly reports whether every destination of the layout is on disk.
func (l storageLayout) onDiskOnly() bool {
	return !l.cache.memoryBacked() && !l.spoolMemoryBacked() && (l.stdout == toPipe || !l.stdout.memoryBacked())
}

func (l storageLayout) expect(run int) expected {
	switch {
	case l.stdout != toPipe && l.stdout.memoryBacked():
		return expected{refusal: "gate: standard output "}
	case l.spoolMemoryBacked():
		return expected{refusal: "gate: output spool "}
	case l.cache.memoryBacked():
		return expected{status: "== print: ok (", declined: true}
	case run == 1:
		return expected{status: "== print: ok ("}
	default:
		return expected{status: "== print: ok, cached ("}
	}
}

// TestBudgetOutputStorage checks the output-storage property on every
// layout of storageLayouts.
func TestBudgetOutputStorage(t *testing.T) {
	if _, err := exec.LookPath("seq"); err != nil {
		t.Skip("seq is unavailable")
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
	for _, layout := range storageLayouts() {
		name := layout.String()
		if fixed, ok := fixedStorageCases[layout]; ok {
			name = fixed + ":" + name
		}
		// The layouts run one at a time: a layout that records its pass
		// holds the step's output on disk two or three times at its peak
		// (the spool, the recorded output and a standard output file), and
		// each layout that runs at once would add that again.
		t.Run(name, func(t *testing.T) {
			checkStorageLayout(t, slice, budget, layout)
		})
	}
}

// storageDiskLimit is the most disk the output-storage tests may take at
// once: a gate runs them on every run, often several gates at once on one
// host, and on 2026-11-04 their 1.09 GB outputs filled the shared disk.
const storageDiskLimit = 100 << 20

// The output case's step prints enough that a gate that held the output in
// its memory passes gateResidentLimit at least twice over, and little
// enough that a layout, which holds it on disk at most three times (the
// spool, the recorded output and a standard output file), stays under
// storageDiskLimit.
func TestBudgetOutputCaseSize(t *testing.T) {
	t.Parallel()
	size := seqBytes(outputLines)
	if size < 2*gateResidentLimit {
		t.Errorf("the step prints %d bytes, under twice the gate's resident limit %d", size, int64(gateResidentLimit))
	}
	if 3*size > storageDiskLimit {
		t.Errorf("a storage layout holds up to %d bytes of step output on disk, over the %d limit", 3*size, storageDiskLimit)
	}
}

// Test-local statfs(2) magic numbers: the property judges a place by them,
// not by the gate's own admission.
const testTmpfsMagic = 0x01021994

var testDiskMagics = map[int64]string{0xEF53: "ext4", 0x58465342: "xfs", 0x9123683E: "btrfs"}

func fileSystemMagic(t *testing.T, name string) int64 {
	t.Helper()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(name, &stat); err != nil {
		t.Fatal(err)
	}
	return int64(stat.Type)
}

// storageRoots are a directory on disk and one on tmpfs, both removed when
// the test ends; the test is skipped where either cannot be made.
type storageRoots struct{ disk, memory string }

func newStorageRoots(t *testing.T) storageRoots {
	t.Helper()
	disk := t.TempDir()
	if magic := fileSystemMagic(t, disk); testDiskMagics[magic] == "" {
		t.Skipf("the test temp directory is on file system %#x, not a disk file system this test knows", magic)
	}
	if info, err := os.Stat("/dev/shm"); err != nil || !info.IsDir() {
		t.Skipf("no /dev/shm (%v)", err)
	}
	memory, err := os.MkdirTemp("/dev/shm", "gate-storage-")
	if err != nil {
		t.Skipf("no directory on /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(memory) })
	if magic := fileSystemMagic(t, memory); magic != testTmpfsMagic {
		t.Skipf("/dev/shm is on file system %#x, not tmpfs", magic)
	}
	return storageRoots{disk: disk, memory: memory}
}

// at makes the place named name and returns its path: a directory, or a
// link to a directory.
func (r storageRoots) at(t *testing.T, p storagePlace, name string) string {
	t.Helper()
	directory := func(parent string) string {
		dir := filepath.Join(parent, name+".dir")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	link := func(target, name string) string {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
		return name
	}
	switch p {
	case onDisk:
		return directory(r.disk)
	case inMemory:
		return directory(r.memory)
	case memoryLinkToDisk:
		return link(directory(r.disk), filepath.Join(r.memory, name))
	case diskLinkToMemory:
		return link(directory(r.memory), filepath.Join(r.disk, name))
	}
	t.Fatalf("no directory for place %s", p)
	return ""
}

// layoutsRunning counts the storage layouts in checkStorageLayout. The
// disk bound (storageDiskLimit) holds for one layout at a time; a layout
// that starts while another runs fails.
var layoutsRunning atomic.Int32

// checkStorageLayout runs the gate twice on one cache in layout and checks
// each run against the property.
func checkStorageLayout(t *testing.T, slice string, budget memoryBudget, layout storageLayout) {
	if running := layoutsRunning.Add(1); running > 1 {
		t.Errorf("%d storage layouts run at once; they must run one at a time to stay under storageDiskLimit", running)
	}
	defer layoutsRunning.Add(-1)
	roots := newStorageRoots(t)
	cache := roots.at(t, layout.cache, "cache")
	if layout.cache == inMemory || layout.cache == onDisk {
		if want, got := layout.cache.memoryBacked(), fileSystemMagic(t, cache) == testTmpfsMagic; want != got {
			t.Fatalf("cache %s: on tmpfs %v, want %v", cache, got, want)
		}
	}
	if layout.spool != inCache {
		if err := os.Symlink(roots.at(t, layout.spool, "spool"), filepath.Join(cache, "output")); err != nil {
			t.Fatal(err)
		}
	}
	stdout := ""
	if layout.stdout != toPipe {
		stdout = filepath.Join(roots.at(t, layout.stdout, "stdout"), "gate.out")
	}
	root := budgetFixture(t)
	for run := 1; run <= 2; run++ {
		h := newScopeHelper(t, slice, gateOutputCase.name, budget)
		for name, value := range map[string]string{"GATE_BUDGET_ROOT": root, "GATE_BUDGET_CACHE": cache, "GATE_OUTPUT_RUN": strconv.Itoa(run), "GATE_OUTPUT_FILE": stdout} {
			h.command.Env = setEnvironmentVariable(h.command.Env, name, value)
		}
		report := runWatched(t, h)
		// result fails the layout when the gate did not finish: the kernel
		// stopped it, and the output it had not printed is lost.
		code, text := h.result(t)
		gateResident(t, run, text)
		checkPlacement(t, run, report, layout.onDiskOnly())
		want := layout.expect(run)
		if want.refusal != "" {
			if code != 2 || !strings.Contains(text, want.refusal) || strings.Contains(text, "== print:") {
				t.Errorf("run %d: gate exit %d; want 2, the line %q, and no step run", run, code, want.refusal)
			}
		} else {
			complete := fmt.Sprintf("seq-check run=%d lines=%d broken=\"\"\n", run, outputLines)
			if code != 0 || !strings.Contains(text, want.status) || !strings.Contains(text, complete) {
				t.Errorf("run %d: gate exit %d; want 0, %q and every line of the output", run, code, want.status)
			}
			if declined := strings.Contains(text, "gate: print: pass not recorded: "); declined != want.declined {
				t.Errorf("run %d: the gate said it did not record the pass: %v, want %v", run, declined, want.declined)
			}
		}
		if size := regularBytes(t, roots.memory); size > 1<<20 {
			t.Errorf("run %d: %d bytes of files on tmpfs after the gate: step output was written to memory", run, size)
		}
	}
	receipts := receiptsIn(t, cache)
	recorded := layout.expect(1).refusal == "" && !layout.cache.memoryBacked()
	if !recorded {
		if len(receipts) != 0 {
			t.Errorf("receipts %v, want none", receipts)
		}
		return
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts %v, want the one pass", receipts)
	}
	if info, err := os.Stat(strings.TrimSuffix(receipts[0], ".json") + ".out"); err != nil || info.Size() != seqBytes(outputLines) {
		t.Errorf("recorded output: %v, %v; want %d bytes", info, err, seqBytes(outputLines))
	}
	if spools, err := os.ReadDir(filepath.Join(cache, "output")); err != nil || len(spools) != 0 {
		t.Errorf("output spools remain: %v, %v", spools, err)
	}
}

// regularBytes is the size of the regular files under dir, links not
// followed.
func regularBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var size int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			size += info.Size()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return size
}
