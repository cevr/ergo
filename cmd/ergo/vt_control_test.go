//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// vtSeeds is how many seeds TestVTAgreesWithControl plays.
const vtSeeds = 200

// vtSize is the generated input of seed s, in bytes: 2,000 to 62,000
// bytes for most seeds, and 1.5 MB for every 25th, which fills the
// history (1,632 lines) so the oldest pages are dropped and used again.
func vtSize(s int) int {
	if s%25 == 0 {
		return 1500000
	}
	return 2000 + (s*7919)%60000
}

// TestVTAgreesWithControl is the agreement property of examples/vt (DOD
// track DT3). For each seed, `vt gen` writes terminal output, and three
// sides play it: `vt replay` (the dense Ergo screen), `vt replay -plain`
// (the plain Ergo screen, the reference model) and the hand-Go control in
// testdata/vt/control. All three must print the same bytes: the history
// and buffer with their styles, the cursor, pen and title, the parser's
// tally and the unhandled sequences. ERGO_VT_SEED=n plays seed n alone.
// It kills the vt mutants whose killer it is, which every side's shared
// code (the parser, Apply, Sgr) carries to both Ergo screens alike.
//
// It builds Go because the control is a separate Go program, and the
// sides are compared on what the built binaries print; no in-process check
// runs the hand-Go control. Each run is bounded by proclimit (1 GiB).
func TestVTAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergo := buildTestCLI(t)
	dir := physicalTempDir(t)
	vt := filepath.Join(dir, "vt")
	control := filepath.Join(dir, "control")
	cache := filepath.Join(dir, "cache")
	build := exec.Command(ergo, "build", "-o", vt, "./cmd/vt")
	build.Dir = filepath.Join("..", "..", "examples", "vt")
	build.Env = append(os.Environ(), "XDG_CACHE_HOME="+cache)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("ergo build vt: %v\n%s", err, out)
	}
	goBuild := exec.Command("go", "build", "-buildvcs=false", "-o", control, ".")
	goBuild.Dir = filepath.Join("testdata", "vt", "control")
	if out, err := goBuild.CombinedOutput(); err != nil {
		t.Fatalf("go build control: %v\n%s", err, out)
	}
	seeds := make([]int, 0, vtSeeds)
	if only := os.Getenv("ERGO_VT_SEED"); only != "" {
		s, err := strconv.Atoi(only)
		if err != nil {
			t.Fatalf("ERGO_VT_SEED=%q: %v", only, err)
		}
		seeds = append(seeds, s)
	} else {
		for s := 1; s <= vtSeeds; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, s := range seeds {
		input := vtRun(t, nil, vt, "gen", strconv.Itoa(s), strconv.Itoa(vtSize(s)))
		dense := vtRun(t, input, vt, "replay")
		plain := vtRun(t, input, vt, "replay", "-plain")
		hand := vtRun(t, input, control, "replay")
		for _, side := range []struct {
			name string
			out  []byte
		}{{"vt replay -plain", plain}, {"control", hand}} {
			if !bytes.Equal(dense, side.out) {
				t.Fatalf("seed %d (%d bytes): vt replay and %s differ at %s\nreplay: ERGO_VT_SEED=%d go test -run TestVTAgreesWithControl ./cmd/ergo",
					s, len(input), side.name, firstDifference(dense, side.out), s)
			}
		}
	}
}

// vtRun runs a side with input on stdin, under the memory bound, in a
// process group of its own, and returns what it printed.
func vtRun(t *testing.T, input []byte, program string, args ...string) []byte {
	t.Helper()
	command := exec.Command(program, args...)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if command.Process != nil && command.Process.Pid > 1 {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
	})
	if err := proclimit.Run(command, 1<<30); err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(program), strings.Join(args, " "), err, firstLines(stderr.Bytes()))
	}
	return stdout.Bytes()
}

// firstDifference names the first line where a and b differ, with both.
func firstDifference(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n  %q\n  %q", i+1, x, y)
		}
	}
	return "the end"
}
