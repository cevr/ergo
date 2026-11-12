package v1

import (
	"math/bits"
	"math/rand/v2"
	"sync"
)

// The bodies of std rand (docs/spec/stdlib.md, rand; std-align S2c): the
// state of a seeded PCG provider, and the host's draws.

// RandSeedHi is the second word of every seeded PCG: Seeded(seed) is
// rand.NewPCG(seed, RandSeedHi). It is fixed, so one seed names one stream
// in every build, and a Go program reproduces an Ergo stream with it.
const RandSeedHi = 0x45_72_67_6f_52_61_6e_64 // "ErgoRand"

// Stream is rand.Stream, the state of a rand.PCG provider: a PCG, so each
// draw is the one Go's *rand.Rand over rand.NewPCG(seed, RandSeedHi) makes
// by the method of the same name. It is a pointer, so every copy of a PCG
// shares one stream. The lock keeps the stream whole when tasks on several
// goroutines draw from one provider: without it, two draws at once could
// read the same state and return the same value.
//
// Speed (std-speed-1): the draws call the concrete *rand.PCG, not Go's
// *rand.Rand, whose every draw goes through the rand.Source interface; so
// PCG.Uint64 inlines. uint64n is Go's (*rand.Rand).uint64n, line for line
// (math/rand/v2, go1.27), on 64-bit platforms; a 32-bit platform takes Go's
// *rand.Rand over the same PCG, whose 32-bit path is its own. The model
// check TestSeededRandModel holds every draw equal to Go's.
type Stream struct {
	mu  sync.Mutex
	pcg *rand.PCG
	r   *rand.Rand // over pcg; the 32-bit path of IntN
}

// NewStream is the stream of rand.Seeded(seed).
func NewStream(seed uint64) *Stream {
	p := rand.NewPCG(seed, RandSeedHi)
	return &Stream{pcg: p, r: rand.New(p)}
}

// Uint64 is the next 64 bits of the stream (Go's r.Uint64).
func (s *Stream) Uint64() uint64 {
	s.mu.Lock()
	v := s.pcg.Uint64()
	s.mu.Unlock()
	return v
}

// IntN is the next draw in 0..n-1 (Go's r.IntN). An n of zero or less
// panics, as Go's does: a defect. The check comes before the lock, so the
// panic leaves the lock free.
func (s *Stream) IntN(n int) int {
	if n <= 0 {
		panic("invalid argument to IntN")
	}
	s.mu.Lock()
	var v int
	if bits.UintSize == 64 {
		v = int(s.uint64n(uint64(n)))
	} else {
		v = s.r.IntN(n)
	}
	s.mu.Unlock()
	return v
}

// uint64n is Go's (*rand.Rand).uint64n on 64-bit platforms (Lemire's
// multiply-and-reject), drawing from the PCG directly. s.mu is held.
func (s *Stream) uint64n(n uint64) uint64 {
	if n&(n-1) == 0 { // n is a power of two: mask
		return s.pcg.Uint64() & (n - 1)
	}
	hi, lo := bits.Mul64(s.pcg.Uint64(), n)
	if lo < n {
		thresh := -n % n
		for lo < thresh {
			hi, lo = bits.Mul64(s.pcg.Uint64(), n)
		}
	}
	return hi
}

// Float64 is the next draw in [0, 1) (Go's r.Float64:
// float64(Uint64()<<11>>11) / (1 << 53)).
func (s *Stream) Float64() float64 {
	s.mu.Lock()
	v := float64(s.pcg.Uint64()<<11>>11) / (1 << 53)
	s.mu.Unlock()
	return v
}

// RandHostUint64 is rand.Host's Uint64: Go's global rand.Uint64.
//
//ergo:host
func RandHostUint64() uint64 { return rand.Uint64() }

// RandHostIntN is rand.Host's IntN: Go's global rand.IntN, which panics
// for an n of zero or less.
//
//ergo:host
func RandHostIntN(n int) int { return rand.IntN(n) }

// RandHostFloat64 is rand.Host's Float64: Go's global rand.Float64.
//
//ergo:host
func RandHostFloat64() float64 { return rand.Float64() }
