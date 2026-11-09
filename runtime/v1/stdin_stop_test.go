package v1

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestIOStdinStopModel is the model check of a stop that ends a read of
// standard input (the red acceptance check of batch-12 C3; stdlib.md,
// io 3). 300 seeds; each runs a generated sequence of steps over a pipe the
// test writes as os.Stdin:
//
//   - write: one more line into the pipe;
//   - reader: start a task that reads with Lines (or ReadAll) until it is
//     stopped, keeping what it reads;
//   - stop: stop that task, which must return (a stop that does not end the
//     wait hangs the test, which its timeout reports);
//   - read: with no reader running and a line left, take one line in the
//     root with Lines and a break.
//
// At the end the pipe closes and the root's ReadAll takes the rest. The
// model is the queue of written lines: the lines the readers kept, the
// root's reads and the last ReadAll, in the order they happened, are the
// written lines, each once. A ReadAll that a stop ends keeps nothing and
// puts back what it read. A failing seed prints its steps.
func TestIOStdinStopModel(t *testing.T) {
	oldIn := os.Stdin
	defer func() { os.Stdin = oldIn }()
	before := runtime.NumGoroutine()
	for seed := uint64(0); seed < 300; seed++ {
		if steps, diff := stdinStopRun(seed); diff != "" {
			t.Fatalf("seed %d: %s\nsteps %v", seed, diff, steps)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

func stdinStopRun(seed uint64) (steps []string, diff string) {
	r := rand.New(rand.NewPCG(seed, 31))
	pr, pw, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	defer pr.Close()
	os.Stdin = pr
	root := rt.Root("stdin")
	var (
		mu      sync.Mutex
		got     []string // every line taken, in the order taken
		written []string
		reader  *rt.Handle[struct{}]
		scope   = rt.NewScope(root)
	)
	taken := func(line string) {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
	for range 4 + r.IntN(12) {
		switch k := r.IntN(4); {
		case k == 0 || k == 1:
			line := fmt.Sprintf("line %d", len(written))
			written = append(written, line)
			steps = append(steps, "write")
			if _, err := pw.WriteString(line + "\n"); err != nil {
				panic(err)
			}
		case k == 2 && reader == nil:
			all := r.IntN(3) == 0
			steps = append(steps, map[bool]string{false: "reader Lines", true: "reader ReadAll"}[all])
			reader = rt.Go(&scope, "reader", func(ct *rt.Task) (struct{}, error) {
				if all {
					s, err := IOReadAll(ct)
					if err == nil {
						for line := range strings.Lines(s) {
							taken(strings.TrimSuffix(line, "\n"))
						}
					}
					return struct{}{}, err
				}
				for line := range IOLines(ct) {
					taken(line)
				}
				return struct{}{}, nil
			})
			for range 3 {
				runtime.Gosched()
			}
		case k == 2 || k == 3 && reader != nil: // k == 2: a reader runs
			steps = append(steps, "stop")
			_ = reader.Stop()
			reader = nil
		case k == 3 && count() < len(written):
			steps = append(steps, "read")
			for line := range IOLines(root) {
				taken(line)
				break
			}
		}
	}
	if reader != nil {
		steps = append(steps, "stop")
		_ = reader.Stop()
	}
	pw.Close()
	rest, err := IOReadAll(root)
	if err != nil {
		return steps, fmt.Sprintf("the last ReadAll failed: %v", err)
	}
	for line := range strings.Lines(rest) {
		taken(strings.TrimSuffix(line, "\n"))
	}
	if strings.Join(got, "|") != strings.Join(written, "|") {
		return steps, fmt.Sprintf("taken %q, written %q", got, written)
	}
	return steps, ""
}
