//go:build unix

package source

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// TestSourceAgreesWithErgo holds the source admission written in Ergo
// (compiler/source, linked as internal/linked/compiler through
// compiler/api/source.ergo) to ReadRegularFile, the reference until the
// S5 swap, on real nodes: regular files of seeded sizes, a FIFO and a
// folder, under bounds below, at, just past and far past each size, 0
// (no bound) and negative. The Ergo side is the pure decision; this test
// is its host, stating what the node is and what it holds. A failing case
// prints its seed and the replay command.
//
// Mutants it alone catches: internal/selfhostcheck/testdata/mutants.json,
// killer internal/source:TestSourceAgreesWithErgo.
func TestSourceAgreesWithErgo(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SELFHOST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	replay := "replay: ERGO_SELFHOST_SEED=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestSourceAgreesWithErgo$' ./internal/source"
	rng := rand.New(rand.NewPCG(seed, 0x5ce))
	ctx := context.Background()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.ergo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "folder.ergo")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]int{}
	for c := 0; c < 60; c++ {
		size := rng.IntN(64)
		content := strings.Repeat("é", size/2) + strings.Repeat("x", size%2)
		file := filepath.Join(dir, "f"+strconv.Itoa(c)+".ergo")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n := len(content)
		for _, limit := range []int{-1 - rng.IntN(3), 0, max(n-1, 1), n, n + 1, n + 1 + rng.IntN(100), 1 + rng.IntN(n+1)} {
			for _, node := range []struct {
				path    string
				regular bool
			}{{file, true}, {fifo, false}, {folder, false}} {
				data, err := ReadRegularFile(node.path, limit)
				want := "ok\n" + string(data)
				if err != nil {
					text := err.Error()
					if errors.Is(err, ErrTooLarge) {
						text = "source exceeds configured limit"
					}
					want = "fail " + text + "\n"
				}
				got, lerr := linked.SourceAdmit(ctx, node.regular, limit, content)
				if lerr != nil {
					t.Fatal(lerr)
				}
				if got != want {
					t.Fatalf("seed %d case %d: %s (regular %v, %d bytes) under limit %d: Ergo %q, Go %q\n%s", seed, c, node.path, node.regular, n, limit, got, want, replay)
				}
				outcomes[strings.SplitN(want, "\n", 2)[0]]++
			}
		}
	}
	for _, o := range []string{"ok", "fail source must be a regular file", "fail source size limit must not be negative", "fail source exceeds configured limit"} {
		if outcomes[o] == 0 {
			t.Errorf("seed %d: no case ended %q: the outcomes reached are %v\n%s", seed, o, outcomes, replay)
		}
	}
}
