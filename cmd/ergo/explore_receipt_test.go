package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestExploreReceiptMatchesPlain generates a module of packages with laws
// and a test that starts tasks, so a search runs many schedules, and runs
// it as `ergo test ./... --explore MODE --receipt DIR`. Each package's
// receipt must be well-formed and equal the receipt of a plain run (the
// reference): each law once, with the model's status. A worker runs the
// tests once per schedule in one process, so a receipt that kept earlier
// runs' laws lists each law once per schedule. It kills
// rcpt-reset-per-run; no other test runs a search with --receipt.
// It builds Go because only the emitted runner, linked with the
// ergoexplore tag and run as search workers, writes the explored receipt.
// The smoke runner owns and bounds every child.
func TestExploreReceiptMatchesPlain(t *testing.T) {
	if testing.Short() {
		t.Skip("explored receipt acceptance runs in the full tier")
	}
	bin := buildTestCLI(t)
	for _, seed := range []uint64{3, 11} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, seed+1))
			mode := []string{"dfs", "pct"}[seed%2]
			schedules := 3 + r.IntN(3)
			root := physicalTempDir(t)
			smokeWrite(t, root, "ergo.mod", "module tree\n")
			n := 2 + r.IntN(2)
			want := map[int][]string{} // package -> "law RuleI_J status"
			var sources strings.Builder
			for i := range n {
				var src strings.Builder
				src.WriteString("import sync\nimport task\n\n")
				for j := range 1 + r.IntN(3) {
					body, status := "x == x", "tested"
					if r.IntN(4) == 0 {
						body, status = "x != x", "refuted"
					}
					fmt.Fprintf(&src, "law Rule%d_%d(x int) { %s }\n\n", i, j, body)
					want[i] = append(want[i], fmt.Sprintf("law Rule%d_%d %s", i, j, status))
				}
				src.WriteString("test Race {\n\tlet c = sync.NewRef(0)\n\tlet a = task.Go() { c.Set(c.Get() + 1) }\n\tlet b = task.Go() { c.Set(c.Get() + 1) }\n\ta.Wait()\n\tb.Wait()\n\tassert c.Get() >= 1\n}\n")
				name := fmt.Sprintf("p%d/p.ergo", i)
				smokeWrite(t, root, name, src.String())
				fmt.Fprintf(&sources, "// %s\n%s\n", name, src.String())
			}
			t.Logf("seed %d, --explore %s --schedules %d\n%sreplay: go test ./cmd/ergo -count=1 -run '^TestExploreReceiptMatchesPlain/%d$'", seed, mode, schedules, sources.String(), seed)
			receipts := func(args ...string) map[int][]byte {
				t.Helper()
				dir := filepath.Join(root, args[0])
				args = append([]string{"test", "./...", "--seed", fmt.Sprint(seed), "--receipt", dir}, args[1:]...)
				out, stderr, code := runSmokeCLI(t, bin, root, "", args...)
				if code > 1 {
					t.Fatalf("seed %d: %v: exit %d\n%s\n%s", seed, args, code, out, stderr)
				}
				got := map[int][]byte{}
				for i := range n {
					data, err := os.ReadFile(filepath.Join(dir, "tree", fmt.Sprintf("p%d.json", i)))
					if err != nil {
						t.Fatalf("seed %d: %v: package p%d: %v\n%s\n%s", seed, args, i, err, out, stderr)
					}
					got[i] = data
				}
				return got
			}
			plain := receipts("plain")
			explored := receipts("explored", "--explore", mode, "--schedules", fmt.Sprint(schedules))
			for i := range n {
				var laws [2][]string
				for k, data := range [][]byte{plain[i], explored[i]} {
					var got struct {
						Laws []struct{ Run, Status string } `json:"laws"`
					}
					if err := json.Unmarshal(data, &got); err != nil {
						t.Fatalf("seed %d: package p%d: a torn receipt: %v\n%s", seed, i, err, data)
					}
					for _, l := range got.Laws {
						laws[k] = append(laws[k], l.Run+" "+l.Status)
					}
				}
				if !slices.Equal(laws[0], want[i]) {
					t.Fatalf("seed %d: package p%d: plain receipt laws %q, want %q\n%s", seed, i, laws[0], want[i], plain[i])
				}
				if string(plain[i]) != string(explored[i]) {
					t.Fatalf("seed %d: package p%d: --explore %s receipt differs from the plain one\nplain:\n%s\nexplored:\n%s", seed, i, mode, plain[i], explored[i])
				}
			}
		})
	}
}
