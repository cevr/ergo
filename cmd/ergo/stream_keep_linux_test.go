package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
// exit. The peak is the kernel's count (rusage), never a time.
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
	in, w := io.Pipe()
	go func() {
		spaces := strings.Repeat(" ", pad)
		_, err := io.WriteString(w, "[")
		for i := 0; i < keepValues && err == nil; i++ {
			sep := ","
			if i == keepValues-1 {
				sep = "]"
			}
			_, err = fmt.Fprintf(w, "%q%s%s", fmt.Sprintf("v%05d", i), spaces, sep)
		}
		w.CloseWithError(err)
	}()
	run := exec.Command(bin)
	run.Env = append(os.Environ(), "GOMAXPROCS=1")
	run.Stdin = in
	out.Reset()
	run.Stdout, run.Stderr = &out, &out
	err := proclimit.Run(run, 1<<30)
	in.Close()
	if err != nil {
		t.Fatalf("keep: %v\n%s", err, out.Bytes())
	}
	if got, want := out.String(), fmt.Sprintf("%d %d\n", keepValues, keepValues*6); got != want {
		t.Fatalf("keep printed %q, want %q", got, want)
	}
	peak := int64(run.ProcessState.SysUsage().(*syscall.Rusage).Maxrss) << 10
	t.Logf("peak resident memory keeping %d values: %d KiB", keepValues, peak>>10)
	if peak > keepBound {
		t.Errorf("keeping %d values of 6 bytes, each read from its own chunk, peaked at %d KiB; want at most %d KiB: a kept value keeps its window", keepValues, peak>>10, keepBound>>10)
	}
}
