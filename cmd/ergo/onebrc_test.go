package main

import (
	"bytes"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestOneBRCAgrees is the correctness property of examples/onebrc (the One
// Billion Row Challenge): on seeded inputs, the Ergo program and the hand
// Go control (testdata/onebrc/control) print exactly what a plain
// reference prints (oneBRCReference, Hoyt's r1 with the contract's
// checks), stdout, stderr and exit status, at a seeded worker count. The
// inputs reach the edges: one station and 10,000 (and 10,001), names of 1
// and 100 bytes, multibyte UTF-8, names that share their first 8 or 16
// bytes, -99.9, 99.9 and -0.0, chunk boundaries anywhere (up to 17
// workers on a few lines), the fast path (lines more than 128 bytes from
// the end) and the byte parser, and each kind of malformed line. A
// failing seed prints its replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program's parallel scan runs on the task runtime and maps its
// input file (fs.MapFile), and the control is Go; no in-process check runs
// either. Each run is bounded by proclimit, and the test waits for its
// exit.
//
// It kills the example's mutants that no law sees (examples/onebrc/
// testdata/mutants.json): a fast path that accepts a malformed
// temperature, a long-name compare that stops at 16 bytes, a split that
// cuts a line, a reduce that keeps one chunk's Stats.
func TestOneBRCAgrees(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildOneBRC(t)
	control := buildOneBRCGo(t, work, "control")
	dir := t.TempDir()
	seeds := 200
	first := uint64(1)
	if s := os.Getenv("ONEBRC_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ONEBRC_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		text, workers := oneBRCInput(seed)
		path := filepath.Join(dir, "in.txt")
		if err := os.WriteFile(path, text, 0o644); err != nil {
			t.Fatal(err)
		}
		want := oneBRCReference(text)
		for _, bin := range []string{ergoBin, control} {
			got := runOneBRC(t, bin, path, workers)
			if got != want {
				t.Fatalf("seed %d (%d bytes, %d workers): %s printed\n%s\nthe reference printed\n%s\nreplay: ONEBRC_SEED=%d go test -count=1 -run '^TestOneBRCAgrees$' ./cmd/ergo",
					seed, len(text), workers, filepath.Base(bin), clip(got), clip(want), seed)
			}
		}
	}
}

func clip(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

// buildOneBRC links examples/onebrc with `ergo build` and returns the
// binary and the emitted module's directory.
func buildOneBRC(t *testing.T) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/onebrc"), "examples", "onebrc", "cmd", "onebrc")
	bin = filepath.Join(t.TempDir(), "onebrc")
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildOneBRCGo links testdata/onebrc/<name> in a module with the go line
// of the example's emitted module in work, so one toolchain compiles both.
func buildOneBRCGo(t *testing.T, work, name string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "onebrc", name, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module "+name+"\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, name)
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of %s: %v\n%s", name, err, out.Bytes())
	}
	return bin
}

// runOneBRC runs bin on the input at path and is its stdout, stderr and
// exit status as one text.
func runOneBRC(t *testing.T, bin, path string, workers int) string {
	t.Helper()
	command := exec.Command(bin, path, strconv.Itoa(workers))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", bin, err)
		}
		code = exit.ExitCode()
	}
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout.Bytes(), stderr.Bytes(), code)
}

// oneBRCInput is the input of a seed and the worker count to run it with.
func oneBRCInput(seed uint64) ([]byte, int) {
	r := rand.New(rand.NewPCG(seed, 0x0b1c))
	counts := []int{1, 2, 7, 60, 413, 9999, 10000, 10001}
	stations := counts[r.IntN(len(counts))]
	if r.IntN(3) > 0 {
		stations = min(stations, 413)
	}
	names := oneBRCNames(r, stations)
	rows := stations
	switch r.IntN(4) {
	case 0:
		rows += r.IntN(40)
	case 1, 2:
		rows += r.IntN(3000)
	case 3:
		rows += 20000 + r.IntN(20000)
	}
	edges := []int{-999, 999, 0, -1, 1, -9, 9, -10, 10, -99, 99, -100, 100}
	var text []byte
	zero := r.IntN(4) == 0
	for i := range rows {
		name := names[r.IntN(len(names))]
		if i < stations {
			name = names[i]
		}
		v := r.IntN(1999) - 999
		if r.IntN(5) == 0 {
			v = edges[r.IntN(len(edges))]
		}
		text = append(text, name...)
		text = append(text, ';')
		if v < 0 || (v == 0 && zero) {
			text = append(text, '-')
		}
		a := max(v, -v)
		text = strconv.AppendInt(text, int64(a/10), 10)
		text = append(text, '.', byte('0'+a%10), '\n')
	}
	if r.IntN(4) == 0 {
		text = oneBRCBreak(r, text)
	}
	workers := 1 + r.IntN(17)
	return text, workers
}

// oneBRCNames is n distinct station names of 1 to 100 bytes, some
// multibyte, some sharing their first 8 or 16 bytes.
func oneBRCNames(r *rand.Rand, n int) []string {
	runes := []string{"a", "b", "z", "Q", " ", ".", "-", "é", "ø", "ß", "ł", "中", "語", "€", "𝄞", "😀"}
	seen := map[string]bool{}
	var names []string
	prefixes := []string{"Stationary ", "Stationary platform"}
	for len(names) < n {
		var size int
		switch r.IntN(6) {
		case 0:
			size = 1
		case 1:
			size = 100
		case 2:
			size = 8 + r.IntN(17)
		default:
			size = 1 + r.IntN(30)
		}
		var b []byte
		if size > 20 && r.IntN(3) == 0 {
			b = append(b, prefixes[r.IntN(len(prefixes))]...)
		}
		for len(b) < size {
			s := runes[r.IntN(len(runes))]
			if len(b)+len(s) > size {
				s = "x"
			}
			b = append(b, s...)
		}
		if b[0] == ' ' {
			b[0] = 'S'
		}
		if !seen[string(b)] {
			seen[string(b)] = true
			names = append(names, string(b))
		}
	}
	return names
}

// oneBRCBreak makes one line of text malformed, or drops the last newline.
func oneBRCBreak(r *rand.Rand, text []byte) []byte {
	starts := []int{0}
	for i, c := range text {
		if c == '\n' && i+1 < len(text) {
			starts = append(starts, i+1)
		}
	}
	at := starts[r.IntN(len(starts))]
	end := at + bytes.IndexByte(text[at:], '\n')
	semi := at + bytes.IndexByte(text[at:end], ';')
	temps := []string{"1.23", "123.4", "--1.0", "1.", ".5", "1,0", "+1.0", "a.b", "", "-", "1.0\r", "-.5", "99.99", "0.0.0", "100.0", "-123.4", "1.0;", "9 .9", "/1.5", "1/5"}
	var line string
	switch r.IntN(7) {
	case 0:
		line = string(text[at:semi]) + string(text[semi+1:end])
	case 1:
		line = string(text[semi:end])
	case 2:
		line = strings.Repeat("y", 101) + string(text[semi:end])
	case 3:
		line = string(text[at:semi+1]) + temps[r.IntN(len(temps))]
	case 4:
		line = ""
	case 5:
		return text[:len(text)-1]
	case 6:
		line = string(text[at:semi]) + "\n" + string(text[semi+1:end])
	}
	out := append([]byte{}, text[:at]...)
	out = append(out, line...)
	return append(out, text[end:]...)
}

// oneBRCTemp is the form of a temperature.
var oneBRCTemp = regexp.MustCompile(`^-?[0-9]{1,2}\.[0-9]$`)

// oneBRCReference is what a correct program prints for text: Hoyt's r1
// (one pass, a map, the readings sorted at the end), with the contract's
// checks of each line in file order and exact rational means.
func oneBRCReference(text []byte) string {
	type stats struct{ min, max, sum, count int }
	all := map[string]*stats{}
	fail := func(at int, why string) string {
		return fmt.Sprintf("stdout:\n\nstderr:\nonebrc: malformed line at byte %d: %s\n\nstatus 1\n", at, why)
	}
	for at := 0; at < len(text); {
		rest := text[at:]
		end := bytes.IndexByte(rest, '\n')
		semi := bytes.IndexByte(rest, ';')
		if semi < 0 || (end >= 0 && end < semi) {
			return fail(at, "no ';' before the end of the line")
		}
		if semi == 0 {
			return fail(at, "an empty station name")
		}
		if semi > 100 {
			return fail(at, "a station name longer than 100 bytes")
		}
		if end < 0 {
			return fail(at, "no newline at the end of the input")
		}
		name, temp := string(rest[:semi]), string(rest[semi+1:end])
		if !oneBRCTemp.MatchString(temp) {
			return fail(at, "a temperature that is not -99.9 to 99.9 with one decimal")
		}
		v, err := strconv.Atoi(strings.Replace(temp, ".", "", 1))
		if err != nil {
			panic(err)
		}
		if s := all[name]; s != nil {
			s.min, s.max, s.sum, s.count = min(s.min, v), max(s.max, v), s.sum+v, s.count+1
		} else {
			all[name] = &stats{v, v, v, 1}
		}
		at += end + 1
	}
	if len(all) > 10000 {
		return "stdout:\n\nstderr:\nonebrc: more than 10000 stations\n\nstatus 1\n"
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	slices.Sort(names)
	tenths := func(v int) string { return strconv.FormatFloat(float64(v)/10, 'f', 1, 64) }
	var parts []string
	for _, name := range names {
		s := all[name]
		mean := new(big.Rat).SetFrac64(int64(s.sum), int64(s.count))
		mean.Add(mean, big.NewRat(1, 2))
		floor := new(big.Int).Div(mean.Num(), mean.Denom())
		parts = append(parts, name+"="+tenths(s.min)+"/"+tenths(int(floor.Int64()))+"/"+tenths(s.max))
	}
	return "stdout:\n{" + strings.Join(parts, ", ") + "}\n\nstderr:\n\nstatus 0\n"
}
