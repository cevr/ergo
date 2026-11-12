package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestKmeansAgreesWithControl is the agreement property of examples/kmeans
// (corpus wave HP1): on seeded inputs, `kmeans run` of the Ergo program and
// of the hand Go control (testdata/kmeans/control) print exactly what a
// plain serial reference prints (kmeansReference: the README's seeding,
// rounds and report over [8]int points, no chunks, no flat centroids):
// stdout, stderr and the exit status, byte for byte. The inputs reach
// clustered and uniform points, repeated points and inputs whose points
// are all one (the seeding's zero-weight draw), K from 1 to the number of
// points, 1 to 9 workers (more workers than points too), runs that
// converge and runs stopped at ROUNDS, and lines that are not points. A
// failing seed prints its replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program's assignment runs on the task runtime and maps its
// input (fs.MapFile), and the control is Go; no in-process check runs
// either. Each run is bounded by proclimit, and the test waits for its
// exit.
//
// It kills the example's mutants that no law sees (examples/kmeans/
// testdata/mutants.json).
func TestKmeansAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildExample(t, "kmeans")
	control := buildExampleGo(t, work, "kmeans", "control")
	dir := t.TempDir()
	seeds := 80
	first := uint64(1)
	if s := os.Getenv("KMEANS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("KMEANS_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	path := filepath.Join(dir, "points.txt")
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x6b6d))
		text, points := kmeansInput(r)
		if err := os.WriteFile(path, text, 0o644); err != nil {
			t.Fatal(err)
		}
		k := 1 + r.IntN(max(1, min(len(points), 12)))
		if r.IntN(10) == 0 {
			k = len(points) + 1
		}
		rounds := 1 + r.IntN(30)
		workers := 1 + r.IntN(9)
		kseed := r.Uint64() >> 1
		args := []string{"run", path, strconv.Itoa(k), strconv.Itoa(rounds), strconv.Itoa(workers), strconv.FormatUint(kseed, 10)}
		want := kmeansReference(text, path, k, rounds, kseed)
		for _, bin := range []string{ergoBin, control} {
			if got := runKmeans(t, bin, args); got != want {
				t.Fatalf("seed %d (%d points, %v): %s printed\n%s\nthe reference\n%s\nreplay: KMEANS_SEED=%d go test -count=1 -run '^TestKmeansAgreesWithControl$' ./cmd/ergo",
					seed, len(points), args[2:], filepath.Base(bin), clip(got), clip(want), seed)
			}
		}
	}
}

// runKmeans runs bin with args and is its stdout, stderr and exit status
// as one text.
func runKmeans(t *testing.T, bin string, args []string) string {
	t.Helper()
	command := exec.Command(bin, args...)
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

type kmeansPoint [8]int

// kmeansInput is a seeded text of points and the points: clustered or
// uniform, with repeats, sometimes all one point, sometimes with one line
// that is not a point.
func kmeansInput(r *rand.Rand) ([]byte, []kmeansPoint) {
	n := []int{1, 2, 3, 10, 64, 500, 3000}[r.IntN(7)]
	var centres []kmeansPoint
	for range 1 + r.IntN(10) {
		var c kmeansPoint
		for d := range c {
			c[d] = r.IntN(4096)
		}
		centres = append(centres, c)
	}
	// Spread 1 makes the seeding's weights small, so its draw often lands
	// exactly on a running total (kmeans-seed-draw-off-by-one).
	spread := []int{0, 1, 3, 60, 900}[r.IntN(5)]
	var points []kmeansPoint
	for i := range n {
		var p kmeansPoint
		switch {
		case spread == 0 || (i > 0 && r.IntN(8) == 0):
			if i > 0 {
				p = points[r.IntN(i)]
			} else {
				p = centres[0]
			}
		default:
			c := centres[r.IntN(len(centres))]
			for d := range p {
				p[d] = min(4095, max(0, c[d]+r.IntN(2*spread+1)-spread))
			}
		}
		points = append(points, p)
	}
	var b []byte
	for _, p := range points {
		for d, v := range p {
			if d > 0 {
				b = append(b, ' ')
			}
			b = strconv.AppendInt(b, int64(v), 10)
		}
		b = append(b, '\n')
	}
	if r.IntN(8) == 0 {
		bad := []string{"1 2 3\n", "1 2 3 4 5 6 7 8 9\n", "1 2 3 4 5 6 7 4096\n", "1  2 3 4 5 6 7 8\n", "a 2 3 4 5 6 7 8\n", "1 2 3 4 5 6 7 8", "1 2 3 4 5 6 7 -8\n", "\n"}
		lines := strings.SplitAfter(string(b), "\n")
		i := r.IntN(len(lines))
		lines = append(lines[:i], append([]string{bad[r.IntN(len(bad))]}, lines[i:]...)...)
		b = []byte(strings.Join(lines, ""))
	}
	return b, points
}

// kmeansReference is what a correct program prints for `run path k rounds
// _ seed` on text: the README's contract, written plainly and serially.
func kmeansReference(text []byte, path string, k, rounds int, seed uint64) string {
	failed := func(msg string) string { return "stdout:\n\nstderr:\nkmeans: " + path + ": " + msg + "\n\nstatus 1\n" }
	var points []kmeansPoint
	for i, line := range strings.SplitAfter(string(text), "\n") {
		if line == "" {
			continue
		}
		where := fmt.Sprintf("line %d: ", i+1)
		if !strings.HasSuffix(line, "\n") {
			// The parser reads a whole line before it looks for the
			// newline, so a last line without one fails first on its
			// fields.
			if why := kmeansFields(line, true); why != "" {
				return failed(where + why)
			}
			return failed(where + "no newline at the end of the input")
		}
		if why := kmeansFields(strings.TrimSuffix(line, "\n"), false); why != "" {
			return failed(where + why)
		}
		var p kmeansPoint
		for d, f := range strings.Split(strings.TrimSuffix(line, "\n"), " ") {
			p[d], _ = strconv.Atoi(f)
		}
		points = append(points, p)
	}
	if len(points) < k {
		return failed(fmt.Sprintf("%d points, fewer than K", len(points)))
	}
	dist := func(p, q kmeansPoint) int {
		s := 0
		for d := range p {
			s += (p[d] - q[d]) * (p[d] - q[d])
		}
		return s
	}
	state := seed
	next := func() uint64 {
		state += 0x9E3779B97F4A7C15
		z := state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return z ^ (z >> 31)
	}
	cents := []kmeansPoint{points[next()%uint64(len(points))]}
	for len(cents) < k {
		weights, total := make([]int, len(points)), 0
		for i, p := range points {
			weights[i] = dist(p, cents[0])
			for _, c := range cents[1:] {
				weights[i] = min(weights[i], dist(p, c))
			}
			total += weights[i]
		}
		pick := 0
		if total == 0 {
			pick = int(next() % uint64(len(points)))
		} else {
			u := int(next() % uint64(total))
			for acc := 0; ; pick++ {
				if acc += weights[pick]; acc > u {
					break
				}
			}
		}
		cents = append(cents, points[pick])
	}
	var out strings.Builder
	for round := 1; ; round++ {
		sum := make([]kmeansPoint, k)
		count := make([]int, k)
		inertia := 0
		for _, p := range points {
			best := 0
			for j := range cents {
				if dist(p, cents[j]) < dist(p, cents[best]) {
					best = j
				}
			}
			for d := range p {
				sum[best][d] += p[d]
			}
			count[best]++
			inertia += dist(p, cents[best])
		}
		fmt.Fprintf(&out, "round %d: inertia %d\n", round, inertia)
		moved := false
		for j := range cents {
			if count[j] == 0 {
				continue
			}
			var c kmeansPoint
			for d := range c {
				// The nearest integer to sum/count, half up: floor((2*sum + count) / (2*count)), all non-negative.
				c[d] = (2*sum[j][d] + count[j]) / (2 * count[j])
			}
			moved = moved || c != cents[j]
			cents[j] = c
		}
		if !moved {
			fmt.Fprintf(&out, "converged in %d rounds\n", round)
			break
		}
		if round >= rounds {
			fmt.Fprintf(&out, "stopped after %d rounds\n", round)
			break
		}
	}
	for j, c := range cents {
		fmt.Fprintf(&out, "centroid %d: %d %d %d %d %d %d %d %d\n", j, c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7])
	}
	return "stdout:\n" + out.String() + "\nstderr:\n\nstatus 0\n"
}

// kmeansFields is why line (without its newline) is not a point, in the
// order the parser finds it, or "". last marks a line at the end of the
// text with no newline: its eighth field is followed by nothing.
func kmeansFields(line string, last bool) string {
	fields := strings.Split(line, " ")
	for i, f := range fields {
		if i == 8 {
			return "not 8 coordinates"
		}
		digits := len(f) - len(strings.TrimLeft(f, "0123456789"))
		if kmeansOver(f[:digits]) {
			return "a coordinate above 4095"
		}
		if digits == 0 || digits < len(f) {
			return "a field that is not a number"
		}
		if last && i == len(fields)-1 {
			return ""
		}
	}
	if len(fields) != 8 {
		return "not 8 coordinates"
	}
	return ""
}

// kmeansOver is whether the digits pass 4095 at some prefix, as the parser
// sees them one at a time.
func kmeansOver(digits string) bool {
	v := 0
	for _, c := range digits {
		v = v*10 + int(c-'0')
		if v > 4095 {
			return true
		}
	}
	return false
}
