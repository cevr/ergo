package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// keepSource reads a JSON array of strings from standard input with
// json.Elements and keeps every value until the end.
const keepSource = `import io
import json

func main() fails json.Malformed | io.ReadFailed | WireWrongKind | WireMissingField | WireUnknownTag needs Ambient {
	let e = json.NewElements[string](json.NewStream(io.NewStdin(), 0, 0))
	let mut kept = []string{}
	for {
		match try e.Next() {
		case Some[string] as s:
			kept = kept.Append(s.Value)
		case None:
			let mut total = 0
			for v in kept {
				total = total + len(v)
			}
			io.NewHost().Print("\(len(kept)) \(total)\n")
			return
		}
	}
}
`

// TestStreamValuesKeepNoWindow is the guard of stdlib.md json 8's copy: a
// string value a json.Stream gives the caller is its own string, not a
// slice of the window it was read from, so a value the caller keeps does
// not keep the window. A program keeps keepValues short values, each read
// from a chunk of its own (a value, then 8 KiB of white space; standard
// input gives chunks of at most 4 KiB), and its peak resident memory
// stays under keepBound; were each value a slice of its window, the kept
// values would hold keepValues windows (about 40 MiB). It builds Go because what a value keeps alive is the
// heap of the running program, which no in-process check of the lowered
// Go shows; it runs bounded by proclimit, and the test waits for its
// exit. The peak is the kernel's count of the program's own memory
// (VmHWM), never a time. It is read while the program waits for the
// array's last byte: the write before it returns once the program has
// read all but the pipe's buffer (64 KiB, a few values), so it holds
// nearly every kept value. Not rusage's ru_maxrss: Linux starts a child's
// at its parent's peak (os/exec forks with CLONE_VM, and exec records the
// old memory's high-water mark), so inside a whole-package run the test
// binary's own 170 MB showed as the program's (I19 full r4 and r5).
//
// Mutant (std ledger): sj2s-value-not-owned (readString gives the window's
// slice) puts the peak above the bound.
func TestStreamValuesKeepNoWindow(t *testing.T) {
	needsBuild(t)
	const keepValues, pad, keepBound = 10000, 8192, 32 << 20
	dir := t.TempDir()
	src := filepath.Join(dir, "keep.ergo")
	writeFile(t, src, keepSource)
	bin := filepath.Join(dir, "keep")
	build := exec.Command(buildTestCLI(t), "build", "-o", bin, src)
	var out bytes.Buffer
	build.Stdout, build.Stderr = &out, &out
	if err := proclimit.Run(build, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s", err, out.Bytes())
	}
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	run := exec.Command(bin)
	// The collector runs by the memory limit alone (GOGC=off, which also
	// turns rt.Pace's 64 MiB floor off): the heap stays near the limit
	// whatever its pace, so the peak is what the kept values hold, not how
	// far a collector starved of CPU lags behind the allocation. At
	// GOGC=100 a loaded host saw peaks of 58 and 174 MB with every value
	// its own (I19 full r3). Kept windows (about 40 MiB) outgrow the limit
	// and the bound.
	run.Env = append(os.Environ(), "GOMAXPROCS=1", "GOGC=off", "GOMEMLIMIT=16MiB")
	run.Stdin = in
	out.Reset()
	run.Stdout, run.Stderr = &out, &out
	watch := proclimit.Watch(run)
	err = proclimit.Start(run, 1<<30)
	in.Close()
	if err != nil {
		t.Fatalf("keep: %v\n%s", err, out.Bytes())
	}
	spaces := strings.Repeat(" ", pad)
	_, werr := io.WriteString(w, "[")
	for i := 0; i < keepValues && werr == nil; i++ {
		sep := ","
		if i == keepValues-1 {
			sep = ""
		}
		_, werr = fmt.Fprintf(w, "%q%s%s", fmt.Sprintf("v%05d", i), spaces, sep)
	}
	peak, perr := residentPeak(run.Process.Pid)
	if werr == nil {
		_, werr = io.WriteString(w, "]")
	}
	w.Close()
	if err := proclimit.Check(run.Path, 1<<30, run.Wait(), watch.Stderr()); err != nil {
		t.Fatalf("keep: %v\n%s", err, out.Bytes())
	}
	if werr != nil {
		t.Fatalf("writing keep's input: %v", werr)
	}
	if perr != nil {
		t.Fatal(perr)
	}
	if got, want := out.String(), fmt.Sprintf("%d %d\n", keepValues, keepValues*6); got != want {
		t.Fatalf("keep printed %q, want %q", got, want)
	}
	t.Logf("peak resident memory keeping %d values: %d KiB", keepValues, peak>>10)
	if peak > keepBound {
		t.Errorf("keeping %d values of 6 bytes, each read from its own chunk, peaked at %d KiB; want at most %d KiB: a kept value keeps its window", keepValues, peak>>10, keepBound>>10)
	}
}

// residentPeak is the peak resident memory of the running process pid,
// in bytes: VmHWM of /proc/pid/status.
func residentPeak(pid int) (int64, error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(status)) {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kib, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kib << 10, err
		}
	}
	return 0, fmt.Errorf("/proc/%d/status has no VmHWM line", pid)
}
