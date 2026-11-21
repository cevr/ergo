//go:build (linux && (amd64 || arm64)) || darwin

package v1

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestFSExchangeSwaps checks fs.Exchange's body: the two files trade
// places, and a missing one is NotFound naming it, with nothing moved.
// Mutants (runtime/v1 ledger): a flag that is not RENAME_EXCHANGE (a
// plain rename, or RENAME_NOREPLACE) does not swap; a NotFound that
// always names the first path names the wrong one.
func TestFSExchangeSwaps(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, a, "old")
	write(t, b, "new")
	if err := FSExchange(a, b); err != nil {
		t.Fatalf("FSExchange = %v", err)
	}
	if got := read(t, a) + "," + read(t, b); got != "new,old" {
		t.Fatalf("after FSExchange a,b = %s; want new,old", got)
	}
	missing := filepath.Join(dir, "missing")
	if err := FSExchange(a, missing); err != (NotFound{Path: missing}) {
		t.Fatalf("FSExchange(a, missing) = %#v; want NotFound of %s", err, missing)
	}
	if err := FSExchange(missing, b); err != (NotFound{Path: missing}) {
		t.Fatalf("FSExchange(missing, b) = %#v; want NotFound of %s", err, missing)
	}
	if got := read(t, a) + "," + read(t, b); got != "new,old" {
		t.Fatalf("after a refused FSExchange a,b = %s; want new,old (nothing moved)", got)
	}
}

// TestFSExchangeIsOneStep checks that a reader never sees either path
// missing while the two are exchanged again and again: the exchange is
// one host call, not renames through a third name. An observer stats
// both paths in a loop while the test exchanges them; the loop counts
// work, not time: it ends once the observer has made 20000 stats and
// 2000 exchanges are done. Mutant (runtime/v1 ledger): an exchange done
// by three renames leaves a window in which a path is missing.
func TestFSExchangeIsOneStep(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, a, "x")
	write(t, b, "y")
	var stats, missing atomic.Int64
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			for _, p := range []string{a, b} {
				if _, err := os.Lstat(p); err != nil {
					missing.Add(1)
				}
			}
			stats.Add(1)
		}
	}()
	for n := 0; n < 2000 || stats.Load() < 20000; n++ {
		if err := FSExchange(a, b); err != nil {
			stop.Store(true)
			<-done
			t.Fatalf("FSExchange #%d = %v", n, err)
		}
	}
	stop.Store(true)
	<-done
	if m := missing.Load(); m > 0 {
		t.Fatalf("a reader saw a path missing %d times in %d stats: the exchange is not one step", m, stats.Load())
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
