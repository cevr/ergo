package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
)

// TestEventsStreamCounts is the counted guard of json.Elements' per-value
// cost (corpus-wave SJ2s) on examples/events: `events stream`, which reads
// a JSON array of events one at a time through a json.Stream, runs no
// more than eventsBound times the user-space instructions per event of
// the hand Go control (examples/events/testdata/control, encoding/json's
// Decoder: Token, More, Decode), as a ratio of counts, never of time. It
// builds Go because the claim is about the machine code gc makes of the
// lowered std json, which no in-process check of the lowered Go shows: it
// links `ergo build` of the example and `go build` of the control with the
// go line of the emitted module, so one toolchain compiles both. Each
// program runs bounded by proclimit, and the test waits for its exit.
//
// Method: TestOneBRCCountsAsHandGo's (internal/opcount's counter, one
// worker, GOMAXPROCS=1): a run on 20,000 events is subtracted from one on
// 220,000, which leaves 200,000 events and none of the start-up or the
// report; the programs alternate over 5 rounds and the medians are
// compared. Both print the same tally of each input (checked first,
// uncounted).
//
// It also bounds the heap allocations per event, an exact count: the
// program built again with internal/opcount's exit hook reports the
// runtime's Mallocs at each size, and the slope is at most eventsAllocs.
//
// The bounds are 1.6 and 2.1 (SJ2s, 2026-11-12: 10,927 instructions per
// event to the control's 7,436, 1.47, and 2.07 allocations to its 1.00;
// 34,274 and 43.8 before). The rest is the lowering's: each Reader
// operation is a sync.Ref Modify that copies the Stream's state in and
// out, each element's decode converts the Stream to a Reader (one
// adapter), and an event is a union value (one box)
// (corpus-wave/ST/sj2-speed-gaps.md); the bounds come down as they land.
//
// Mutants (std ledger): sj2s-read-marks-shared and sj2s-queue-regrows put
// events above both bounds; sj2s-number-built and sj2s-string-built above
// the allocation bound.
func TestEventsStreamCounts(t *testing.T) {
	needsBuild(t)
	if err := opcount.Available(); err != nil {
		t.Skipf("no hardware counter of user-space instructions: %v", err)
	}
	const eventsBound, eventsAllocs = 1.6, 2.1
	program := filepath.Join(smokeWorkspace(t, "examples/events"), "examples", "events", "cmd", "events")
	work := t.TempDir()
	ergoBin := filepath.Join(work, "events")
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", ergoBin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	work2 := workDir(t, stderr.String())
	control := buildEventsControl(t, work2)
	hooked := buildEventsHooked(t, work2, work)
	low, high := 20_000, 220_000
	inputs := map[int]string{}
	for _, n := range []int{low, high} {
		path := filepath.Join(work, strconv.Itoa(n)+".json")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gen := exec.Command(ergoBin, "gen", strconv.Itoa(n))
		gen.Stdout = f
		err = proclimit.Run(gen, 1<<30)
		f.Close()
		if err != nil {
			t.Fatalf("events gen %d: %v", n, err)
		}
		inputs[n] = path
		if a, b := eventsRun(t, ergoBin, path, nil), eventsRun(t, control, path, nil); !bytes.Equal(a, b) {
			t.Fatalf("%s: the hand control printed %q, events %q", path, b, a)
		}
	}
	const rounds = 5
	var ergo, hand []float64
	for range rounds {
		for _, p := range []struct {
			bin string
			per *[]float64
		}{{ergoBin, &ergo}, {control, &hand}} {
			var lo, hi uint64
			eventsRun(t, p.bin, inputs[low], &lo)
			eventsRun(t, p.bin, inputs[high], &hi)
			*p.per = append(*p.per, float64(hi-lo)/float64(high-low))
		}
	}
	allocs := (eventsAllocations(t, hooked, inputs[high], work) - eventsAllocations(t, hooked, inputs[low], work)) / float64(high-low)
	t.Logf("per event: events stream %.2f allocations", allocs)
	if allocs > eventsAllocs {
		t.Errorf("events stream makes %.2f allocations per event; want at most %.1f", allocs, eventsAllocs)
	}
	ratio := brcMedian(ergo) / brcMedian(hand)
	t.Logf("per event over %d events: events stream %.2f instructions, hand Go %.2f; ratio %.3f", high-low, brcMedian(ergo), brcMedian(hand), ratio)
	if ratio > eventsBound {
		t.Errorf("events stream runs %.3f of the hand control's instructions per event; want at most %.1f", ratio, eventsBound)
	}
}

// buildEventsControl links examples/events/testdata/control in a module
// with the go line of the emitted module in work.
func buildEventsControl(t *testing.T, work string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := bytes.TrimSpace(firstLine(mod, "go "))
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "events", "testdata", "control", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module control\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, "control")
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of the control: %v\n%s", err, out.Bytes())
	}
	return bin
}

// firstLine is the first line of text that starts with prefix, or nil.
func firstLine(text []byte, prefix string) []byte {
	for _, line := range bytes.Split(text, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(prefix)) {
			return line
		}
	}
	return nil
}

// eventsRun runs `bin stream` on the input at path, with GOMAXPROCS=1, and
// is its stdout and stderr; with count, under the counter, whose
// user-space instructions it stores there (a refused count fails).
func eventsRun(t *testing.T, bin, path string, count *uint64) []byte {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	command := exec.Command(bin, "stream")
	command.Env = append(os.Environ(), "GOMAXPROCS=1")
	command.Stdin = in
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if count == nil {
		if err := proclimit.Run(command, 1<<30); err != nil {
			t.Fatalf("%s stream < %s: %v\n%s", bin, path, err, out.Bytes())
		}
		return out.Bytes()
	}
	r, err := opcount.Count(command, 1<<30)
	if err != nil {
		t.Fatalf("%s stream < %s: %v\n%s", bin, path, err, out.Bytes())
	}
	if id, why := r.Refusal(); id != "" {
		t.Fatalf("%s stream < %s: refused %s: %s\n%s", bin, path, id, why, out.Bytes())
	}
	*count = r.Instructions
	return out.Bytes()
}

// buildEventsHooked builds the emitted module in work again with
// internal/opcount's exit hook (go build -overlay, as scripts/opcount
// does), into dir.
func buildEventsHooked(t *testing.T, work, dir string) string {
	t.Helper()
	hook := filepath.Join(dir, "hook.go")
	writeFile(t, hook, opcount.Hook)
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(work, opcount.HookFile): hook}})
	if err != nil {
		t.Fatal(err)
	}
	overlayFile := filepath.Join(dir, "overlay.json")
	writeFile(t, overlayFile, string(overlay))
	bin := filepath.Join(dir, "events-hooked")
	command := exec.Command("go", "build", "-trimpath", "-mod=readonly", "-buildvcs=false", "-ldflags=-checklinkname=0", "-overlay", overlayFile, "-o", bin, ".")
	command.Dir = work
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of events with the hook: %v\n%s", err, out.Bytes())
	}
	return bin
}

// eventsAllocations runs the hooked bin on the input at path and is the
// heap allocations its hook reports.
func eventsAllocations(t *testing.T, bin, path, dir string) float64 {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	report := filepath.Join(dir, "allocs.txt")
	command := exec.Command(bin, "stream")
	command.Env = append(os.Environ(), "GOMAXPROCS=1", opcount.HookVariable+"="+report)
	command.Stdin = in
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 1<<30); err != nil {
		t.Fatalf("%s stream < %s: %v\n%s", bin, path, err, out.Bytes())
	}
	text, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(text), "\n") {
		if v, ok := strings.CutPrefix(line, "allocs "); ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatalf("the hook wrote no allocs line:\n%s", text)
	return 0
}
