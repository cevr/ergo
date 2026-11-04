//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The cap fixtures write an exact count of bytes on any host. BSD head, the
// one of darwin, refuses `head -c 0` with "illegal byte count -- 0" on stderr
// (GNU head writes nothing), which put a test-leg reply of 0 and N-1 bytes
// over the cap on darwin (examples-cap r1 darwin check). This test puts a
// head with that refusal first on PATH and runs the case writer and the fake
// compiler's TESTSIZE reply with a zero count on each stream beside N-1 and N
// bytes on the other: each must pass. Not parallel: it sets PATH, which the
// runner passes to the compiler and to each case.
func TestCapFixturesWriteExactCountsWithBSDHead(t *testing.T) {
	real, err := exec.LookPath("head")
	if err != nil {
		t.Skip("no head on PATH")
	}
	bin := t.TempDir()
	strict := "#!/bin/sh\nif [ \"$1\" = -c ] && [ \"$2\" -le 0 ]; then echo \"head: illegal byte count -- $2\" >&2; exit 1; fi\nexec " + real + " \"$@\"\n"
	// sh writes the script, not this process (ETXTBSY, golang/go#22315).
	write := exec.Command("sh", "-c", `printf '%s' "$1" > "$2" && chmod +x "$2"`, "sh", strict, filepath.Join(bin, "head"))
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("write the strict head: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := newFixture(t)
	pairs := [][2]int{{0, outputCap - 1}, {outputCap - 1, 0}, {0, outputCap}, {outputCap, 0}, {0, 0}}
	cases := map[string]map[string]string{}
	for i, sizes := range pairs {
		files := map[string]string{"args": fmt.Sprintf("%d\n%d\n", sizes[0], sizes[1]), "expected.out": string(make([]byte, sizes[0]))}
		if sizes[1] > 0 {
			files["expected.err"] = string(make([]byte, sizes[1]))
		}
		cases[fmt.Sprintf("c%d", i)] = files
		f.program(fmt.Sprintf("t%d", i), fmt.Sprintf("#!/bin/sh\necho hi\nexit 0\n# TESTSIZE %d %d\ntest Fixture {\n}\n", sizes[0], sizes[1]), "slow\n",
			map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
	}
	f.program("sizer", writer, "slow\n", cases, nil)
	code, out := f.run("-tags", "slow")
	if code != 0 {
		t.Fatalf("zero-byte writes under BSD head: exit %d\n%.3000s", code, out)
	}
}

// Every fixture loop ends by itself 60 s after it started, so a runner that
// failed to stop a group cannot leak it for long (spec D6). Each row runs a
// fixture's loop with a date on PATH that moves 20 s ahead at each call, so
// a loop with the bound ends within a few ticks; a loop without it would run
// on. The row's command leads its own process group, which the test watches
// until it is empty, and stops (it is the test's own group) if it is not.
func TestFixtureLoopsEndByThemselves(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		argv func(f *fixture, dir string) []string
	}{
		{"ticker", func(f *fixture, dir string) []string {
			return []string{"sh", "-c", ticker(`"$TICKS"`)}
		}},
		{"fake ergo test", func(f *fixture, dir string) []string {
			source := filepath.Join(dir, "main.ergo")
			if err := os.WriteFile(source, []byte("# TESTTICK\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return []string{f.ergo, "test", source}
		}},
		{"fake ergo build", func(f *fixture, dir string) []string {
			source := filepath.Join(dir, "main.ergo")
			if err := os.WriteFile(source, []byte("# BUILDTICK\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return []string{f.ergo, "build", source, "-o", filepath.Join(dir, "out")}
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			clock := "#!/bin/sh\nn=$(cat \"$CLOCK\" 2>/dev/null || echo 0)\nn=$((n + 1))\necho \"$n\" > \"$CLOCK\"\necho $((1000000 + n * 20))\n"
			// sh writes the fake date, not this process: a parallel test's
			// fork could hold this process's write descriptor of it until
			// its exec, and then running it fails with "text file busy"
			// (ETXTBSY, golang/go#22315).
			write := exec.Command("sh", "-c", `printf '%s' "$1" > "$2" && chmod +x "$2"`, "sh", clock, filepath.Join(bin, "date"))
			if out, err := write.CombinedOutput(); err != nil {
				t.Fatalf("write the fake date: %v\n%s", err, out)
			}
			argv := row.argv(f, dir)
			command := exec.Command(argv[0], argv[1:]...)
			command.Dir = dir
			command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "CLOCK=" + filepath.Join(dir, "clock"), "TICKS=" + filepath.Join(dir, "ticks")}
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			group := command.Process.Pid
			_ = command.Wait()
			for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
				if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
					break
				}
				if time.Now().After(deadline) {
					_ = syscall.Kill(-group, syscall.SIGKILL) // the group this test started
					t.Fatal("the fixture loop did not end by itself: it has no time bound")
				}
			}
		})
	}
}
