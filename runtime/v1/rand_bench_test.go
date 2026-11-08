package v1

import (
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// Benchmarks of std rand against hand-written Go (std-align S2c; plan
// section 4, Benchmark). The ergo side is the shape of the Go the lowering
// emits for Random.IntN(n) bound to rand.Seeded(seed) and to
// rand.NewHost(): benchRandom is the program's Random interface, benchPCG
// the program's PCG record, and benchSeededRandom and benchHostRandom the
// operations conversions of PCG and Host.

type benchRandom interface{ IntN(t *rt.Task, n int) int }

type benchPCG struct{ src *Stream }

func (p benchPCG) IntN(env *benchEnv, n int) int {
	env.task.Check()
	return p.src.IntN(n)
}

type benchSeededRandom struct{ v benchPCG }

func (a *benchSeededRandom) IntN(t *rt.Task, n int) int { return a.v.IntN(&benchEnv{task: t}, n) }

type benchRandHost struct{}

func (benchRandHost) IntN(env *benchEnv, n int) int {
	env.task.Check()
	return RandHostIntN(n)
}

type benchHostRandom struct{ v benchRandHost }

func (a *benchHostRandom) IntN(t *rt.Task, n int) int { return a.v.IntN(&benchEnv{task: t}, n) }

// benchRandSink keeps each draw alive; an int, so no draw is boxed.
var benchRandSink int

// BenchmarkRandIntN: IntN(1000) through Seeded against r.IntN on a
// *rand.Rand over PCG, and through NewHost against Go's global rand.IntN.
func BenchmarkRandIntN(b *testing.B) {
	task := rt.Root("bench")
	var seeded benchRandom = &benchSeededRandom{v: benchPCG{src: NewStream(7)}}
	var host benchRandom = &benchHostRandom{}
	r := rand.New(rand.NewPCG(7, RandSeedHi))
	b.Run("ergo-seeded", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchRandSink = seeded.IntN(task, 1000)
		}
	})
	b.Run("go-pcg", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchRandSink = r.IntN(1000)
		}
	})
	b.Run("ergo-host", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchRandSink = host.IntN(task, 1000)
		}
	})
	b.Run("go-global", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchRandSink = rand.IntN(1000)
		}
	})
}
