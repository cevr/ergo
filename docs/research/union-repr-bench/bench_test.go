package unionreprbench

import (
	"runtime"
	"strconv"
	"testing"
)

// Benchmark names carry /form=<Id> last, so `benchstat -col /form` sets
// the forms side by side with form A as the base column.

var (
	sinkInt    int
	sinkInt64  int64
	sinkBool   bool
	sinkStateA StateA
	sinkStateD StateD
	sinkHeldA  HeldA
	sinkHeldD  HeldD
)

// Question 1: one machine transition (step, then apply) per op.
// testing's allocs/op is an integer, so each benchmark also reports the
// exact mean (allocs/transition, B/transition) from runtime.MemStats.

// exactAllocs runs the timed loop and reports the heap allocations and
// bytes per unit (units per op).
func exactAllocs(b *testing.B, units float64, unit string, run func()) {
	b.ReportAllocs()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ResetTimer()
	run()
	b.StopTimer()
	runtime.ReadMemStats(&after)
	n := float64(b.N) * units
	b.ReportMetric(float64(after.Mallocs-before.Mallocs)/n, "allocs/"+unit)
	b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/n, "B/"+unit)
}

func BenchmarkBreaker(b *testing.B) {
	raw := breakerSignals(1024, 7)
	b.Run("form=A", func(b *testing.B) {
		in := signalsA(raw)
		var s StateA = Closed{}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyA(s, stepA(s, in[i&1023]))
			}
		})
		sinkStateA = s
	})
	b.Run("form=D", func(b *testing.B) {
		in := signalsD(raw)
		s := ClosedD(Closed{})
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyD(s, stepD(s, in[i&1023]))
			}
		})
		sinkStateD = s
	})
	b.Run("form=Dx", func(b *testing.B) {
		in := signalsD(raw)
		s := ClosedD(Closed{})
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyD(s, stepDx(s, in[i&1023]))
			}
		})
		sinkStateD = s
	})
	b.Run("form=G", func(b *testing.B) {
		in := signalsC(raw)
		var s StateA = Closed{}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyA(s, stepG(s, in[i&1023]))
			}
		})
		sinkStateA = s
	})
	b.Run("form=M", func(b *testing.B) {
		in := signalsD(raw)
		s := ClosedD(Closed{})
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyM(s, stepM(s, in[i&1023]))
			}
		})
		sinkStateD = s
	})
	b.Run("form=E", func(b *testing.B) {
		in := signalsE(raw)
		s := StateE{stateClosed, Closed{}}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyE(s, stepE(s, in[i&1023]))
			}
		})
		sinkInt = int(s.tag)
	})
	b.Run("form=F", func(b *testing.B) {
		in := signalsF(raw)
		var s StateF = Closed{}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyF(s, stepF(s, in[i&1023]))
			}
		})
		sinkInt = int(s.stateTag())
	})
}

// Counter: a machine whose unions all fit rule G's bound (counter.go).
func BenchmarkCounter(b *testing.B) {
	raw := counterSignals(1024, 7)
	b.Run("form=A", func(b *testing.B) {
		in := counterSignalsA(raw)
		var s CStateA = Idle{}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyCA(s, stepCA(s, in[i&1023]))
			}
		})
		sinkInt = len(textCStateA(s))
	})
	b.Run("form=G", func(b *testing.B) {
		s := CStateC{}
		exactAllocs(b, 1, "transition", func() {
			for i := 0; i < b.N; i++ {
				s = applyCG(s, stepCG(s, raw[i&1023]))
			}
		})
		sinkInt = int(s.v.N)
	})
}

// One op is the three replays of examples/tidewalk-replay (38 moves).
func BenchmarkTidewalk(b *testing.B) {
	moves := 0
	for _, s := range tideReplays {
		moves += len(s)
	}
	b.Run("form=A", func(b *testing.B) {
		var lists [][]MoveA
		for _, s := range tideReplays {
			lists = append(lists, movesA(s))
		}
		exactAllocs(b, float64(moves), "move", func() {
			for i := 0; i < b.N; i++ {
				for _, ms := range lists {
					g, _ := playA(ms, nil)
					w, _ := walkOfA(g)
					sinkInt += len(w.Left)
				}
			}
		})
	})
	b.Run("form=Dall", func(b *testing.B) {
		var lists [][]MoveB
		for _, s := range tideReplays {
			lists = append(lists, movesB(s))
		}
		exactAllocs(b, float64(moves), "move", func() {
			for i := 0; i < b.N; i++ {
				for _, ms := range lists {
					g, _ := playDall(ms, nil, nil)
					w, _ := walkOfD(g)
					sinkInt += len(w.Left)
				}
			}
		})
	})
	b.Run("form=G", func(b *testing.B) {
		var lists [][]MoveB
		for _, s := range tideReplays {
			lists = append(lists, movesB(s))
		}
		exactAllocs(b, float64(moves), "move", func() {
			for i := 0; i < b.N; i++ {
				for _, ms := range lists {
					g, _ := playG(ms, nil, nil)
					w, _ := walkOfA(g)
					sinkInt += len(w.Left)
				}
			}
		})
	})
}

// Question 2: dispatch time. One op is one call over a seeded input.

func BenchmarkDispatch(b *testing.B) {
	{
		a, d, e, f := inputsU2(rawSingles(1024, 2, 2))
		b.Run("shape=1x2/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU2A(a[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x2/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU2D(d[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x2/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU2E(e[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x2/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU2F(f[i&1023])
			}
			sinkInt = s
		})
	}
	{
		a, d, e, f := inputsU4(rawSingles(1024, 4, 4))
		b.Run("shape=1x4/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU4A(a[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x4/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU4D(d[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x4/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU4E(e[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x4/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU4F(f[i&1023])
			}
			sinkInt = s
		})
	}
	{
		a, d, e, f := inputsU8(rawSingles(1024, 8, 8))
		b.Run("shape=1x8/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU8A(a[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x8/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU8D(d[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x8/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU8E(e[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x8/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU8F(f[i&1023])
			}
			sinkInt = s
		})
	}
	{
		a, d, e, f := inputsU16(rawSingles(1024, 16, 16))
		b.Run("shape=1x16/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16A(a[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16D(d[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16E(e[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16F(f[i&1023])
			}
			sinkInt = s
		})
	}
	{
		in := inputsP3x4(rawPairs(1024, 3, 4, 34))
		b.Run("shape=3x4/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP3x4A(p.aA, p.bA)
			}
			sinkInt = s
		})
		b.Run("shape=3x4/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP3x4D(p.aD, p.bD)
			}
			sinkInt = s
		})
		b.Run("shape=3x4/form=Dx", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP3x4Dx(p.aD, p.bD)
			}
			sinkInt = s
		})
		b.Run("shape=3x4/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP3x4E(p.aE, p.bE)
			}
			sinkInt = s
		})
		b.Run("shape=3x4/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP3x4F(p.aF, p.bF)
			}
			sinkInt = s
		})
	}
	{
		in := inputsP6x6(rawPairs(1024, 6, 6, 66))
		b.Run("shape=6x6/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6A(p.aA, p.bA)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6D(p.aD, p.bD)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=Dx", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6Dx(p.aD, p.bD)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6E(p.aE, p.bE)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6F(p.aF, p.bF)
			}
			sinkInt = s
		})
	}
}

// DispatchCyclic is Dispatch with the variants in a fixed cycle (0, 1, 2,
// …), which a branch predictor learns, against the seeded uniform order of
// Dispatch, which it cannot.
func BenchmarkDispatchCyclic(b *testing.B) {
	cyclic := func(n, v int) []rawValue {
		out := rawSingles(n, v, 99)
		for i := range out {
			out[i].k = i % v
		}
		return out
	}
	{
		a, d, e, f := inputsU16(cyclic(1024, 16))
		b.Run("shape=1x16/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16A(a[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16D(d[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=E", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16E(e[i&1023])
			}
			sinkInt = s
		})
		b.Run("shape=1x16/form=F", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				s += dispatchU16F(f[i&1023])
			}
			sinkInt = s
		})
	}
	{
		raw := rawPairs(1024, 6, 6, 66)
		for i := range raw {
			raw[i][0].k, raw[i][1].k = (i/6)%6, i%6
		}
		in := inputsP6x6(raw)
		b.Run("shape=6x6/form=A", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6A(p.aA, p.bA)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=D", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6D(p.aD, p.bD)
			}
			sinkInt = s
		})
		b.Run("shape=6x6/form=Dx", func(b *testing.B) {
			s := 0
			for i := 0; i < b.N; i++ {
				p := &in[i&1023]
				s += dispatchP6x6Dx(p.aD, p.bD)
			}
			sinkInt = s
		})
	}
}

// Enum: form B against form A for an all-empty union, the Move of
// tidewalk (5 variants): one target(p, m) per op over seeded moves.

//go:noinline
func enumTargetA(p Pos, m MoveA) Pos { return targetA(p, m) }

//go:noinline
func enumTargetB(p Pos, m MoveB) Pos { return targetB(p, m) }

func BenchmarkEnum(b *testing.B) {
	s := randomMoves(1024, 5)
	a, bb := movesA(s), movesB(s)
	b.Run("shape=1x5/form=A", func(b *testing.B) {
		p := Pos{}
		for i := 0; i < b.N; i++ {
			p = enumTargetA(p, a[i&1023])
		}
		sinkInt = p.X + p.Y
	})
	b.Run("shape=1x5/form=B", func(b *testing.B) {
		p := Pos{}
		for i := 0; i < b.N; i++ {
			p = enumTargetB(p, bb[i&1023])
		}
		sinkInt = p.X + p.Y
	})
}

// Question 4: GC. A slice of 1,000,000 union values stays live.
// GCCycle: one op is one full runtime.GC() with the slice live (the mark
// cost of the slice). Churn: one op replaces one element by the next value,
// as a machine replaces its state; the metrics are GC cycles per million
// ops and GC pause per op.

const gcN = 1_000_000

func BenchmarkGCCycle(b *testing.B) {
	raw := rawSingles(gcN, 2, 400)
	cycle := func(b *testing.B, keep func() any) {
		live := keep()
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			runtime.GC()
		}
		b.StopTimer()
		runtime.KeepAlive(live)
		// After ResetTimer, which deletes reported metrics.
		b.ReportMetric(float64(ms.HeapObjects), "heap-objects")
		b.ReportMetric(float64(ms.HeapAlloc), "heap-bytes")
	}
	b.Run("payload=NP/form=A", func(b *testing.B) { cycle(b, func() any { return buildNPA(raw) }) })
	b.Run("payload=NP/form=D", func(b *testing.B) { cycle(b, func() any { return buildNPD(raw) }) })
	b.Run("payload=NP/form=E", func(b *testing.B) { cycle(b, func() any { return buildNPE(raw) }) })
	b.Run("payload=WP/form=A", func(b *testing.B) { cycle(b, func() any { return buildWPA(raw) }) })
	b.Run("payload=WP/form=D", func(b *testing.B) { cycle(b, func() any { return buildWPD(raw) }) })
	b.Run("payload=WP/form=E", func(b *testing.B) { cycle(b, func() any { return buildWPE(raw) }) })
}

func BenchmarkGCChurn(b *testing.B) {
	raw := rawSingles(gcN, 2, 400)
	churn := func(b *testing.B, step func(i int)) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			step(i % gcN)
		}
		b.StopTimer()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.NumGC-before.NumGC)*1e6/float64(b.N), "gc/Mop")
		b.ReportMetric(float64(after.PauseTotalNs-before.PauseTotalNs)/float64(b.N), "pause-ns/op")
	}
	b.Run("payload=NP/form=A", func(b *testing.B) { xs := buildNPA(raw); churn(b, func(i int) { xs[i] = nextNPA(xs[i]) }) })
	b.Run("payload=NP/form=D", func(b *testing.B) { xs := buildNPD(raw); churn(b, func(i int) { xs[i] = nextNPD(xs[i]) }) })
	b.Run("payload=NP/form=E", func(b *testing.B) { xs := buildNPE(raw); churn(b, func(i int) { xs[i] = nextNPE(xs[i]) }) })
	b.Run("payload=WP/form=A", func(b *testing.B) { xs := buildWPA(raw); churn(b, func(i int) { xs[i] = nextWPA(xs[i]) }) })
	b.Run("payload=WP/form=D", func(b *testing.B) { xs := buildWPD(raw); churn(b, func(i int) { xs[i] = nextWPD(xs[i]) }) })
	b.Run("payload=WP/form=E", func(b *testing.B) { xs := buildWPE(raw); churn(b, func(i int) { xs[i] = nextWPE(xs[i]) }) })
}

// Question 5 (CopyPass, CopyNext) is in generated_test.go, one pair per
// size of internal/gensrc copyWords.

// Question 6: narrow to a 64-byte variant and pass its record on.

func BenchmarkNarrow(b *testing.B) {
	a, d, ds := n64Inputs(rawSingles(1024, 2, 64))
	b.Run("form=A", func(b *testing.B) {
		var s int64
		for i := 0; i < b.N; i++ {
			s += narrowA(a[i&1023])
		}
		sinkInt64 = s
	})
	b.Run("form=D", func(b *testing.B) {
		var s int64
		for i := 0; i < b.N; i++ {
			s += narrowD(d[i&1023])
		}
		sinkInt64 = s
	})
	b.Run("form=Ds", func(b *testing.B) {
		var s int64
		for i := 0; i < b.N; i++ {
			s += narrowDs(ds[i&1023])
		}
		sinkInt64 = s
	})
}

// Question 7: the cost of == (its correctness is TestEqualityAgrees).

//go:noinline
func eqStateA(x, y StateA) bool { return x == y }

//go:noinline
func eqStateD(x, y StateD) bool { return x == y }

func BenchmarkEqual(b *testing.B) {
	closeds, opens, _ := breakerDomain()
	var sa []StateA
	var sd []StateD
	for i := 0; i < 1024; i++ {
		if i%2 == 0 {
			c := closeds[i%len(closeds)]
			sa, sd = append(sa, c), append(sd, ClosedD(c))
		} else {
			o := Open{"svc-" + strconv.Itoa(i%3)}
			sa, sd = append(sa, o), append(sd, OpenD(o))
		}
	}
	_ = opens
	b.Run("form=A", func(b *testing.B) {
		n := 0
		for i := 0; i < b.N; i++ {
			if eqStateA(sa[i&1023], sa[(i*7)&1023]) {
				n++
			}
		}
		sinkInt = n
	})
	b.Run("form=D", func(b *testing.B) {
		n := 0
		for i := 0; i < b.N; i++ {
			if eqStateD(sd[i&1023], sd[(i*7)&1023]) {
				n++
			}
		}
		sinkInt = n
	})
}

// Question 8: clone a union value whose variant holds a []int of 16.

func BenchmarkClone(b *testing.B) {
	a, d := heldBag(16)
	b.Run("form=A", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sinkHeldA = cloneA(a)
		}
	})
	b.Run("form=D", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sinkHeldD = cloneD(d)
		}
	})
}

// Question 10: Option[T] (X1). One op is one lookup and its match.

func BenchmarkOptionGet(b *testing.B) {
	for _, small := range []bool{false, true} {
		m, keys := optionMapInt(small)
		val := "large"
		if small {
			val = "small"
		}
		b.Run("val="+val+"/form=A", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetIntA(m, keys, b.N) }) })
		b.Run("val="+val+"/form=C", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetIntC(m, keys, b.N) }) })
		b.Run("val="+val+"/form=Go", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetIntGo(m, keys, b.N) }) })
	}
	ms, keys := optionMapString()
	b.Run("val=string/form=A", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetStringA(ms, keys, b.N) }) })
	b.Run("val=string/form=C", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetStringC(ms, keys, b.N) }) })
	b.Run("val=string/form=Go", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumGetStringGo(ms, keys, b.N) }) })
}

// The Option crosses a call that is not inlined, so its box cannot stay on
// the stack.
func BenchmarkOptionLookup(b *testing.B) {
	m, keys := optionMapInt(false)
	b.Run("form=A", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumLookupA(m, keys, b.N) }) })
	b.Run("form=C", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumLookupC(m, keys, b.N) }) })
	b.Run("form=Go", func(b *testing.B) { exactAllocs(b, 1, "lookup", func() { sinkInt = sumLookupGo(m, keys, b.N) }) })
}

func BenchmarkCut(b *testing.B) {
	in := cutInputs()
	b.Run("form=A", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutA(in, b.N) }) })
	b.Run("form=C", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutC(in, b.N) }) })
	b.Run("form=Go", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutGo(in, b.N) }) })
}

func BenchmarkCutOut(b *testing.B) {
	in := cutInputs()
	b.Run("form=A", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutOutA(in, b.N) }) })
	b.Run("form=C", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutOutC(in, b.N) }) })
	b.Run("form=Go", func(b *testing.B) { exactAllocs(b, 1, "cut", func() { sinkInt = sumCutOutGo(in, b.N) }) })
}
