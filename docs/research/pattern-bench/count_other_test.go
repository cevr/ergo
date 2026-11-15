//go:build !linux

package patternbench

import "testing"

// counter counts nothing off Linux: the bench reports time and allocations.
type counter struct{}

func startCount(b *testing.B) *counter { b.ResetTimer(); return &counter{} }

func (c *counter) report(b *testing.B, perOp int) { b.StopTimer() }
