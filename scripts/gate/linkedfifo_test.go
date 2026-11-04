package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// TestVariantB2KeyedRootFIFO is the round-2 verifier's reproduction of
// finding B2 (a repeat of round-1 B2), kept as a regression test: a FIFO in
// a fingerprinted installation (a full private copy of the selected GOROOT,
// with the real go), reached through a symlink in the checkout. A one-step
// gate consumes the FIFO's buffer and passes; the next inner run must not
// replay that pass while an uncached run fails on the drained FIFO.
// TestKeyFailsClosed (package replay) is the property over every place a
// link reaches; this test drives the real collector, runner and store.
func TestVariantB2KeyedRootFIFO(t *testing.T) {
	root := fixture(t)
	keyed := t.TempDir()
	output, err := queryTool("", false, realTools["go"], "env", "GOROOT")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.TrimSpace(string(output))
	// A full private copy of the selected installation, so the proof does
	// not rely on an incomplete GOROOT or a substitute tool executable. The
	// walk makes the directories and links; workers copy the files, so the
	// copy of about 250 MB stays within the host package's time budget.
	type copyJob struct {
		name, dest string
		perm       fs.FileMode
	}
	var jobs []copyJob
	err = filepath.WalkDir(source, func(name string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		dest := filepath.Join(keyed, rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(target, dest)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected source kind %s", name)
		}
		jobs = append(jobs, copyJob{name, dest, info.Mode().Perm()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	copyFile := func(job copyJob) error {
		in, err := os.Open(job.name)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(job.dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, job.perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	errs := make([]error, len(jobs))
	work := make(chan int)
	var wg sync.WaitGroup
	for range max(2, runtime.NumCPU()) {
		wg.Go(func() {
			for i := range work {
				errs[i] = copyFile(jobs[i])
			}
		})
	}
	for i := range jobs {
		work <- i
	}
	close(work)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOROOT", keyed)
	t.Setenv("GOTOOLCHAIN", "local")
	bin := t.TempDir()
	if err := os.Symlink(realTools["go"], filepath.Join(bin, "go")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The FIFO's name sorts before every GOROOT entry, so each collection's
	// fingerprint walk stops at it at once (the host-test time bound);
	// where it sits in GOROOT does not change what the test proves.
	fifo := filepath.Join(keyed, "0stream")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if _, err := pipe.Write([]byte("good\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(root, "stream")); err != nil {
		t.Fatal(err)
	}
	steps := []*Step{{Name: "reads linked FIFO", Argv: []string{"python3", "-c", `import os; fd=os.open('stream',os.O_RDONLY|os.O_NONBLOCK); assert os.read(fd,16)==b'good\n'`}}}
	g := wholeFixture(t, root, steps)
	g.programs = []string{"go", "sh", "python3"}
	before, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	code, out := runWhole(t, g)
	t.Logf("prime problem=%q code=%d\n%s", before.Problem(), code, out)
	if code != 0 {
		t.Fatalf("prime failed: %s", out)
	}
	code, out = runWhole(t, g)
	var uncached bytes.Buffer
	rawCode := g.derive(steps, true, g.whole).run(&uncached)
	after, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("keys equal=%t inner=%d uncached=%d end problem=%q\n%s\n%s", before.Key() == after.Key(), code, rawCode, after.Problem(), out, uncached.String())
	if code == 0 && rawCode == 1 {
		t.Fatal("STALE LINKED FIFO PASS inside a fingerprinted root")
	}
}
