package irspike

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRCompactSliceLoopsAgree runs the compact source shapes and the same
// IR lowering with the forms disabled. The mutable end is snapshotted,
// continue and break preserve their targets, a nonzero start still runs,
// and the new allocation has exactly the old capacity.
//
// It builds Go because range is interpreted by Go's frontend and the result
// of a loop whose body changes the end is not decided by type checking.
// Mutant: compact-count-wrong-end (the native operand is not the upper
// bound), compact-make-cap-lost (make uses zero instead of the caller's
// capacity).
func TestIRCompactSliceLoopsAgree(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		rng := rand.New(rand.NewPCG(uint64(seed), 0))
		limit, cap := rng.IntN(8)+5, rng.IntN(8)+1
		text := fmt.Sprintf(`extern go cap func room[T](xs []T) int

func walk(n int, size int) int {
	let mut end = n
	let xs = MakeSlice[int](size)
	let mut sum = 0
	for i in 0..end {
		end = end - 1
		if i == 1 { continue }
		if i == n - 1 { break }
		sum = sum + i
	}
	for i in 1..n { sum = sum + i }
	for _ in 0..size { sum = sum + 1 }
	sum + room(xs)
}

func main() needs Console {
	Console.Println("\(walk(%d, %d)) \(walk(-3, 0))")
}`, limit, cap)
		dir := t.TempDir()
		p := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(p)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		var got [2]outcome
		for i, mode := range []string{"", "off"} {
			s := filepath.Join(dir, fmt.Sprintf("mode%d", i))
			restore := lower.SetIRCompactGoForTest(mode)
			_, err := Emit(u, IR, s)
			restore()
			if err != nil {
				t.Fatalf("seed %d: emit %q: %v\n%s", seed, mode, err, text)
			}
			bin := filepath.Join(dir, fmt.Sprintf("mode%d.bin", i))
			if _, err := Build(ir, IR, s, bin, BuildOptions{}); err != nil {
				t.Fatalf("seed %d: build %q: %v\n%s", seed, mode, err, text)
			}
			got[i] = run(t, bin)
		}
		if got[0] != got[1] || got[1].exit != 0 {
			t.Fatalf("seed %d: compact %+v, reference %+v\n%s\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRCompactSliceLoopsAgree$' ./internal/experimental/irspike",
				seed, got[0], got[1], text, seed)
		}
	}
}
