package v1

import (
	"math/rand/v2"
	"sync"
)

// The bodies of std rand (docs/spec/stdlib.md, rand; std-align S2c): the
// state of a seeded PCG provider, and the host's draws.

// RandSeedHi is the second word of every seeded PCG: Seeded(seed) is
// rand.NewPCG(seed, RandSeedHi). It is fixed, so one seed names one stream
// in every build, and a Go program reproduces an Ergo stream with it.
const RandSeedHi = 0x45_72_67_6f_52_61_6e_64 // "ErgoRand"

// Stream is rand.Stream, the state of a rand.PCG provider: Go's *rand.Rand
// over a PCG, so each draw is the one Go's method of the same name makes.
// It is a pointer, so every copy of a PCG shares one stream. The lock
// keeps the stream whole when tasks on several goroutines draw from one
// provider: without it, two draws at once could read the same state and
// return the same value.
type Stream struct {
	mu sync.Mutex
	r  *rand.Rand
}

// NewStream is the stream of rand.Seeded(seed).
func NewStream(seed uint64) *Stream {
	return &Stream{r: rand.New(rand.NewPCG(seed, RandSeedHi))}
}

// Uint64 is the next 64 bits of the stream (Go's r.Uint64).
func (s *Stream) Uint64() uint64 {
	s.mu.Lock()
	v := s.r.Uint64()
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
	v := s.r.IntN(n)
	s.mu.Unlock()
	return v
}

// Float64 is the next draw in [0, 1) (Go's r.Float64).
func (s *Stream) Float64() float64 {
	s.mu.Lock()
	v := s.r.Float64()
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
