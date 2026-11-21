//go:build ergogotrace

package v1

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestGoTraceRoot: in a --go-trace build, ERGO_GO_TRACE=FILE writes a Go
// execution trace that names main's child task, and "flight:FILE" writes
// the flight window when main ends in a defect, with ERGO_TRACE's recorder
// still fed (the tee). Run with -tags ergogotrace. Mutant:
// obs-root-untraced (rootFor ignores the Go sink when ERGO_TRACE is unset).
func TestGoTraceRoot(t *testing.T) {
	dir := t.TempDir()
	child := func(def string, fail bool) func(*rt.Task) error {
		return func(root *rt.Task) error {
			s := rt.NewScope(root)
			h := rt.Go(&s, def, func(*rt.Task) (int, error) { return 1, nil })
			_, _ = h.Wait()
			var err error
			s.End(&err)
			if fail {
				panic("broken invariant")
			}
			return err
		}
	}

	full := filepath.Join(dir, "full.trace")
	t.Setenv(GoTraceEnv, full)
	t.Setenv(TraceEnv, "")
	if err := runMain(child("app.fullchild", false)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(full); err != nil || !bytes.Contains(b, []byte("app.fullchild")) {
		t.Fatalf("the full trace (%d bytes, %v) does not name app.fullchild", len(b), err)
	}

	flight := filepath.Join(dir, "flight.trace")
	jsonl := filepath.Join(dir, "ergo.jsonl")
	t.Setenv(GoTraceEnv, "flight:"+flight)
	t.Setenv(TraceEnv, jsonl)
	if err := runMain(child("app.flightchild", true)); err == nil {
		t.Fatal("main ended with no exit; want its defect")
	}
	if b, err := os.ReadFile(flight); err != nil || !bytes.Contains(b, []byte("app.flightchild")) {
		t.Fatalf("the flight window (%d bytes, %v) does not name app.flightchild", len(b), err)
	}
	if b, err := os.ReadFile(jsonl); err != nil || !bytes.Contains(b, []byte("app.flightchild")) {
		t.Fatalf("ERGO_TRACE's file (%d bytes, %v) does not name app.flightchild; want the tee to feed it", len(b), err)
	}
}
