package v1

import (
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The Go runners of std rand (std-align S2c; docs/spec/stdlib.md, rand).

// TestSeededRandModel is the model check of rand.Seeded (the red
// acceptance check of S2c). For 1,000 seeds, a generated mix of 64 calls
// of Uint64, IntN(n) and Float64 on NewStream(seed) equals the same calls
// on Go's rand.New(rand.NewPCG(seed, RandSeedHi)), the reference model;
// and a second source of the same seed, drawn in step, agrees with both
// (SeedReplays).
func TestSeededRandModel(t *testing.T) {
	gen := rand.New(rand.NewPCG(3, 4))
	for seed := range uint64(1000) {
		src := NewStream(seed)
		twin := NewStream(seed)
		model := rand.New(rand.NewPCG(seed, RandSeedHi))
		for call := range 64 {
			switch gen.IntN(3) {
			case 0:
				got, again, want := src.Uint64(), twin.Uint64(), model.Uint64()
				if got != want || again != want {
					t.Fatalf("seed %d call %d: Uint64 %d, twin %d, Go %d", seed, call, got, again, want)
				}
			case 1:
				n := 1 + gen.IntN(1000)
				if gen.IntN(4) == 0 {
					n = 1 + int(gen.Int64N(1<<62))
				}
				got, again, want := src.IntN(n), twin.IntN(n), model.IntN(n)
				if got != want || again != want {
					t.Fatalf("seed %d call %d: IntN(%d) %d, twin %d, Go %d", seed, call, n, got, again, want)
				}
			default:
				got, again, want := src.Float64(), twin.Float64(), model.Float64()
				if got != want || again != want {
					t.Fatalf("seed %d call %d: Float64 %v, twin %v, Go %v", seed, call, got, again, want)
				}
			}
		}
	}
}

// TestRandLaws runs the member laws over the Go bodies: IntNInRange for
// each n in 1..1,000 and 1,000 seeded larger n, on a Stream and on the
// host; Float64InUnit 10,000 times on each.
func TestRandLaws(t *testing.T) {
	gen := rand.New(rand.NewPCG(5, 6))
	ns := make([]int, 0, 2000)
	for n := 1; n <= 1000; n++ {
		ns = append(ns, n)
	}
	for range 1000 {
		ns = append(ns, 1001+int(gen.Int64N(1<<62)))
	}
	ns = append(ns, 1<<62, int(^uint(0)>>1))
	src := NewStream(9)
	for _, n := range ns {
		if x := src.IntN(n); x < 0 || x >= n {
			t.Fatalf("Stream.IntNInRange: IntN(%d) = %d", n, x)
		}
		if x := RandHostIntN(n); x < 0 || x >= n {
			t.Fatalf("Random.IntNInRange on the host: IntN(%d) = %d", n, x)
		}
	}
	for range 10_000 {
		if f := src.Float64(); f < 0 || f >= 1 {
			t.Fatalf("Float64InUnit: Stream.Float64() = %v", f)
		}
		if f := RandHostFloat64(); f < 0 || f >= 1 {
			t.Fatalf("Float64InUnit: host Float64() = %v", f)
		}
	}
}

// TestRandIntNNotPositiveIsADefect: IntN of zero or less panics on a
// Stream and on the host, as Go's does, and a Stream's panic leaves its
// lock free, so the stream goes on.
func TestRandIntNNotPositiveIsADefect(t *testing.T) {
	src := NewStream(1)
	for _, n := range []int{0, -1, -1 << 63} {
		for name, draw := range map[string]func(int) int{"Stream": src.IntN, "host": RandHostIntN} {
			func() {
				defer func() {
					p := recover()
					if s, ok := p.(string); !ok || !strings.Contains(s, "invalid argument to IntN") {
						t.Errorf("%s IntN(%d): panic %v, want Go's invalid argument to IntN", name, n, p)
					}
				}()
				draw(n)
			}()
		}
	}
	want := rand.New(rand.NewPCG(1, RandSeedHi)).Uint64()
	if got := src.Uint64(); got != want {
		t.Errorf("after the defects the stream is at %d, want its first value %d", got, want)
	}
}

// TestStreamSharedByTasks: goroutines that draw from one Stream at once
// take each value of the stream exactly once, so the draws, sorted, are
// the first values of Go's stream, sorted. Run with -race, it also shows
// the lock covers the state.
func TestStreamSharedByTasks(t *testing.T) {
	const workers, each = 8, 2000
	src := NewStream(11)
	got := make([][]uint64, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				got[w] = append(got[w], src.Uint64())
			}
		}()
	}
	wg.Wait()
	all := slices.Concat(got...)
	model := rand.New(rand.NewPCG(11, RandSeedHi))
	want := make([]uint64, workers*each)
	for i := range want {
		want[i] = model.Uint64()
	}
	slices.Sort(all)
	slices.Sort(want)
	if !slices.Equal(all, want) {
		t.Fatalf("the shared draws are not the stream's first %d values", len(want))
	}
}
