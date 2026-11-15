package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestRaytraceAgreesWithControl is the agreement property of
// examples/raytrace (corpus wave HP3): on seeded jobs (image sizes from
// 1×1 up, 1 to 16 samples, scenes of 0 to 80 objects, 1 to 6 workers,
// tiles from 1 pixel to wider than the image), the Ergo program and the
// hand Go control (testdata/raytrace/control) write the same PPM, byte for
// byte, and the same exit status. The control is an independent
// implementation of the README's scene, tree, materials and sampling, so
// a change to either that changes a pixel shows here.
//
// The bytes must be equal on every host: Go may fuse a multiply and an add
// into one instruction on some architectures, so the Ergo lowering rounds
// every product of floats (types.md, Operators 5) and the control writes
// each product as float64(a*b), which Go rounds too.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program renders its tiles on the task runtime and the control
// is Go; no in-process check runs either. Each run is bounded by
// proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/raytrace/
// testdata/mutants.json). A failing seed prints its replay command.
func TestRaytraceAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildExample(t, "raytrace")
	control := buildExampleGo(t, work, "raytrace", "control")
	seeds, first := 24, uint64(1)
	if s := os.Getenv("RAYTRACE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("RAYTRACE_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x7274))
		w, h := 1+r.IntN(48), 1+r.IntN(32)
		args := []string{strconv.Itoa(w), strconv.Itoa(h), strconv.Itoa(1 + r.IntN(16)), strconv.FormatUint(r.Uint64()>>1, 10),
			strconv.Itoa(r.IntN(81)), strconv.Itoa(1 + r.IntN(6)), strconv.Itoa(1 + r.IntN(max(w, h)+4))}
		got, code := runRaytrace(t, ergoBin, args)
		want, wantCode := runRaytrace(t, control, args)
		replay := fmt.Sprintf("replay: RAYTRACE_SEED=%d go test -count=1 -run '^TestRaytraceAgreesWithControl$' ./cmd/ergo", seed)
		if code != 0 || wantCode != 0 {
			t.Fatalf("seed %d %v: exit %d, control %d\n%s", seed, args, code, wantCode, replay)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("seed %d %v: the images differ (%s)\n%s", seed, args, firstDiff(got, want), replay)
		}
	}
}

// runRaytrace runs bin with args and is its stdout and exit status.
func runRaytrace(t *testing.T, bin string, args []string) ([]byte, int) {
	t.Helper()
	command := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", bin, err)
		}
		return stdout.Bytes(), exit.ExitCode()
	}
	return stdout.Bytes(), 0
}

// firstDiff names the first byte where a and b differ.
func firstDiff(a, b []byte) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return fmt.Sprintf("byte %d: %d, control %d", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("lengths %d and %d", len(a), len(b))
}
