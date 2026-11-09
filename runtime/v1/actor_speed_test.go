package v1

import (
	"runtime"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The actor-speed benchmarks (MU18): Send, Ask, spawn and stop, and the
// bytes of an idle actor, each beside its hand Go control (a goroutine
// that owns an int, a buffered channel of requests, a reply channel the
// asker reuses). The machine is a counter whose step allocates nothing,
// so the numbers are the runner's own cost. One top-level benchmark runs
// every side in turn, so `-count n` interleaves them (only interleaved
// ratios count on a loaded host).

func speedCounter(t *rt.Task, capacity int) *Actor[int, int, int] {
	return MachineStart(Site("speed"), t, releaser{make(chan sleep)}, 0,
		func(_ *rt.Task, s int, e int) (int, error) { return decideStay, nil },
		func(*rt.Task, int, int) (int, error) { return decideIgnore, nil },
		func(int) time.Duration { return ActorNoTimer() }, noWork[int], hasNoWork, runNoWork[int],
		func(d int) int { return d },
		func(s int, d int) int { return s + 1 }, func(*rt.Task, int) {}, capacity)
}

type handReq struct {
	e     int
	reply chan int
}

func handCounter(capacity int) (chan handReq, chan struct{}) {
	in, done := make(chan handReq, capacity), make(chan struct{})
	go func() {
		defer close(done)
		s := 0
		for r := range in {
			s += r.e
			if r.reply != nil {
				r.reply <- s
			}
		}
	}()
	return in, done
}

func speedBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc + m.StackInuse
}

func BenchmarkActorSpeed(b *testing.B) {
	sides := []struct {
		name string
		set  func() func()
	}{
		{"ergo", func() func() { return func() {} }},
		// The reference runner (the inbox and replies before MU18).
		{"ref", func() func() {
			old := actorReference
			actorReference = true
			return func() { actorReference = old }
		}},
	}
	for _, side := range sides {
		b.Run("Send/"+side.name, func(b *testing.B) {
			defer side.set()()
			root := rt.Root("bench")
			a := speedCounter(root, 64)
			b.ReportAllocs()
			for b.Loop() {
				_ = a.Send(root, 1)
			}
			a.State(root)
			_ = a.Stop()
		})
		b.Run("Ask/"+side.name, func(b *testing.B) {
			defer side.set()()
			root := rt.Root("bench")
			a := speedCounter(root, 64)
			b.ReportAllocs()
			for b.Loop() {
				_, _ = a.Ask(root, 1)
			}
			_ = a.Stop()
		})
		b.Run("SpawnStop/"+side.name, func(b *testing.B) {
			defer side.set()()
			root := rt.Root("bench")
			b.ReportAllocs()
			for b.Loop() {
				_ = speedCounter(root, 64).Stop()
			}
		})
		b.Run("Idle/"+side.name, func(b *testing.B) {
			defer side.set()()
			root := rt.Root("bench")
			var as []*Actor[int, int, int]
			before := speedBytes()
			n := 0
			for b.Loop() {
				as = append(as, speedCounter(root, 64))
				n++
			}
			b.ReportMetric(float64(speedBytes()-before)/float64(n), "B/actor")
			for _, a := range as {
				_ = a.Stop()
			}
		})
	}
	b.Run("Send/hand", func(b *testing.B) {
		in, _ := handCounter(64)
		reply := make(chan int, 1)
		b.ReportAllocs()
		for b.Loop() {
			in <- handReq{e: 1}
		}
		in <- handReq{reply: reply}
		<-reply
		close(in)
	})
	b.Run("Ask/hand", func(b *testing.B) {
		in, _ := handCounter(64)
		reply := make(chan int, 1)
		b.ReportAllocs()
		for b.Loop() {
			in <- handReq{e: 1, reply: reply}
			<-reply
		}
		close(in)
	})
	// The hand control with the guarantee Ergo keeps: each wait also wakes
	// on a stop (a select on a done channel, as a ctx.Done() wait does).
	b.Run("Ask/handstop", func(b *testing.B) {
		in, stop := make(chan handReq, 64), make(chan struct{})
		go func() {
			s := 0
			for {
				select {
				case r := <-in:
					s += r.e
					r.reply <- s
				case <-stop:
					return
				}
			}
		}()
		reply := make(chan int, 1)
		b.ReportAllocs()
		for b.Loop() {
			select {
			case in <- handReq{e: 1, reply: reply}:
			case <-stop:
			}
			select {
			case <-reply:
			case <-stop:
			}
		}
		close(stop)
	})
	b.Run("SpawnStop/hand", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			in, done := handCounter(64)
			reply := make(chan int, 1)
			in <- handReq{reply: reply} // started
			<-reply
			close(in)
			<-done
		}
	})
	b.Run("Idle/hand", func(b *testing.B) {
		var hs []chan handReq
		before := speedBytes()
		n := 0
		for b.Loop() {
			h, _ := handCounter(64)
			hs = append(hs, h)
			n++
		}
		b.ReportMetric(float64(speedBytes()-before)/float64(n), "B/actor")
		for _, h := range hs {
			close(h)
		}
	})
}
