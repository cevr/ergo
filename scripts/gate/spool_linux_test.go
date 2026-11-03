package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// memoryDir is a new directory on /dev/shm, removed when the test ends; the
// test is skipped where /dev/shm is not tmpfs.
func memoryDir(t *testing.T) string {
	t.Helper()
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/dev/shm", &stat); err != nil || uint32(stat.Type) != 0x01021994 {
		t.Skipf("/dev/shm is not tmpfs here (%v)", err)
	}
	dir, err := os.MkdirTemp("/dev/shm", "gate-spool-test-")
	if err != nil {
		t.Skipf("no directory on /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// diskDir is t.TempDir, skipped where it is not on a disk file system the
// gate admits.
func diskDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	if _, ok := diskFileSystems[uint32(stat.Type)]; !ok {
		t.Skipf("the test temp directory is on file system %#x, not one the gate admits", stat.Type)
	}
	return dir
}

func create(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// outputDestination judges the opened file, not its path: a pipe, a device
// and a disk file are admitted; a file on tmpfs is refused, also when it is
// opened through a link on disk, and the refusal names the file system.
func TestOutputDestination(t *testing.T) {
	disk, memory := diskDir(t), memoryDir(t)
	link := filepath.Join(disk, "link")
	if err := os.Symlink(memory, link); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	for name, file := range map[string]*os.File{"pipe": writer, "device": null, "disk file": create(t, filepath.Join(disk, "out"))} {
		if err := outputDestination(file); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, file := range map[string]*os.File{"tmpfs file": create(t, filepath.Join(memory, "out")), "tmpfs file through a disk link": create(t, filepath.Join(link, "linked"))} {
		err := outputDestination(file)
		if err == nil || !strings.Contains(err.Error(), "is on file system tmpfs (magic 0x1021994)") {
			t.Errorf("%s: %v; want a refusal naming tmpfs", name, err)
		}
	}
}

// A gate whose output spool would be on tmpfs refuses to run: the spool's
// pages would be charged to the gate's memory, which is what the spool is
// there to bound. No step runs, and the gate says why and how to fix it.
func TestGateRefusesAnOutputSpoolInMemory(t *testing.T) {
	cache := memoryDir(t)
	root := fixture(t)
	code, out := runGate(t, root, cache, []*Step{{Name: "counted", Argv: count("counted")}})
	if code != 2 || !strings.Contains(out, "gate: output spool in "+cache+"/output is on file system tmpfs") || !strings.Contains(out, spoolRemedy) {
		t.Fatalf("exit %d, want 2 with the refusal and its remedy\n%s", code, out)
	}
	if runs(t, root, "counted") != 0 {
		t.Fatalf("a step ran with its output spool in memory\n%s", out)
	}
}

// A gate whose standard output is a file on tmpfs refuses to run, and says so
// in that file with the remedy. No step runs.
func TestGateRefusesStandardOutputInMemory(t *testing.T) {
	name := filepath.Join(memoryDir(t), "gate.out")
	out := create(t, name)
	root := fixture(t)
	g := &gate{root: root, cacheDir: t.TempDir(), steps: []*Step{{Name: "counted", Argv: count("counted")}}}
	code := g.run(out)
	printed, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	text := string(printed)
	if code != 2 || !strings.Contains(text, "gate: standard output is on file system tmpfs") || !strings.Contains(text, "redirect standard output to a disk file or a pipe") {
		t.Fatalf("exit %d, want 2 with the refusal and its remedy\n%s", code, text)
	}
	if runs(t, root, "counted") != 0 {
		t.Fatalf("a step ran with standard output in memory\n%s", text)
	}
}

// With the cache's receipts on tmpfs and its spools on disk, a pass is not
// recorded: the step's output is printed whole, the gate says why it did not
// record and how to fix it, and the next run executes the step again. No
// step output is left on tmpfs.
func TestGateDeclinesACacheInMemory(t *testing.T) {
	memory := memoryDir(t)
	cache := diskDir(t)
	if err := os.Symlink(memory, filepath.Join(cache, cacheVersion)); err != nil {
		t.Fatal(err)
	}
	root := fixture(t)
	steps := []*Step{{Name: "checked", Argv: count("checked"), Inputs: &Inputs{Paths: []string{"src/"}}}}
	for executions := 1; executions <= 2; executions++ {
		code, out := runGate(t, root, cache, steps)
		if code != 0 || !strings.Contains(out, "== checked: ok (") || !strings.Contains(out, "ran checked\n") {
			t.Fatalf("exit %d, want a pass with its output\n%s", code, out)
		}
		if !strings.Contains(out, "gate: checked: pass not recorded: cache output file in ") || !strings.Contains(out, "is on file system tmpfs") || !strings.Contains(out, "set ERGO_GATE_CACHE to a directory on disk") {
			t.Fatalf("the gate does not say it declined to record, and why\n%s", out)
		}
		if got := runs(t, root, "checked"); got != executions {
			t.Fatalf("executions = %d, want %d\n%s", got, executions, out)
		}
	}
	if size := regularBytes(t, memory); size != 0 {
		t.Fatalf("%d bytes of files on tmpfs, want none", size)
	}
}
