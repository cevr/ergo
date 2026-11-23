// Package tiny is the stock Go reference of the Ergo fixture package
// (compiler/nativeobj/fixture.ergo): compiled by the stock compiler under
// the same import path, it gives the output the producer's archive must
// give. The runner replaces the two constants with the seed's.
package tiny

import (
	"sync/atomic"

	"ergo.local/nativeobj/dep"
)

const (
	K1 = 1111111
	K2 = 2222222
)

var (
	initRuns int
	stop     atomic.Int64
)

func init() { initRuns++ }

func Diff(a int, b int) int { return a - b + K1 }

func Twice(x int) int { return dep.Double(x) + K2 }

func Apply(f func(int) int, x int) int { return f(x) }

func Hold(p *int, f func(int) int) int {
	r := f(3)
	return *p + r
}

func Anchor(f func(int) int, v int) int {
	q := &v
	r := f(v)
	*q = r
	return v
}

func Publish(dst **int, q *int) { *dst = q }

func Spin() int {
	acc, i := 0, 0
	for stop.Load() == 0 {
		acc ^= i
		i++
	}
	return acc
}

func Stop() { stop.Store(1) }

func InitRuns() int { return initRuns }
