// Command control is the hand-written Go reference for benchmarks/machines:
// plusthree's scenarios S1-S4 and S7 as switch machines, the way a Go
// programmer writes them, run for N events.
//
//	control SCENARIO N
//
// It prints the scenario's probe and the heap allocations the events made,
// as the Ergo program p3bench does. S7 is emulated as plusthree's statig and
// smlang drivers emulate it: the exit pushes a cancel effect and the entry a
// schedule effect into a reused buffer, and the host turns the scheduled key
// into the next event. s1actor and s7actor are the floor of a live machine:
// a goroutine that owns the state, asked over an unbuffered channel and
// answering on another, one handoff each way per event.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
)

const chunk = 1024

type light uint8

const (
	inactive light = iota
	active
)

type event uint8

const (
	press event = iota
	inc
	jump
	flip
	due
)

func toggle(s light, e event) light {
	switch s {
	case inactive:
		if e == press {
			return active
		}
	case active:
		if e == press {
			return inactive
		}
	}
	return s
}

type counter struct{ count, limit int }

func count(s counter, e event) counter {
	if e == inc && s.count < s.limit {
		s.count++
	}
	return s
}

// tree is a.a1.a1x or b.b1.b1x: one leaf per level-1 branch.
type tree uint8

const (
	a1x tree = iota
	b1x
)

func jumpStep(s tree, e event) tree {
	if e != jump {
		return s
	}
	switch s {
	case a1x:
		return b1x
	default:
		return a1x
	}
}

type both struct{ r1, r2 uint8 }

func flipStep(s both, e event) both {
	if e != flip {
		return s
	}
	return both{r1: 1 - s.r1, r2: 1 - s.r2}
}

type timerEffect struct {
	schedule bool
	key      uint8
	delayMs  uint32
}

type phase uint8

// s7 is the timer machine with its effect buffer.
type s7 struct {
	at  phase
	buf []timerEffect
}

func (m *s7) fire(key uint8) {
	if key != uint8(m.at) {
		return
	}
	m.buf = m.buf[:0]
	m.buf = append(m.buf, timerEffect{schedule: false, key: uint8(m.at)})
	m.at = 1 - m.at
	m.buf = append(m.buf, timerEffect{schedule: true, key: uint8(m.at), delayMs: 1000})
}

// ask is a request to an actor goroutine: one signal, answered on the
// actor's reply channel with the state after it.
type ask struct{ e event }

// lightActor runs S1 in a goroutine: each request steps the state and
// replies with it, as a live machine answers an Ask.
func lightActor(inbox <-chan ask, reply chan<- light) {
	s := inactive
	for r := range inbox {
		s = toggle(s, r.e)
		reply <- s
	}
	close(reply)
}

// phaseActor runs S7 in a goroutine on a manual clock: each tick moves the
// clock by d; when it reaches the open entry's deadline, the timer fires,
// the state flips and the new entry's timer is armed. It replies with the
// state after each tick, as a time.Manual's Advance returns once the woken
// sleeper has run.
func phaseActor(ticks <-chan int64, reply chan<- phase) {
	var now, deadline int64 = 0, 1000
	at := phase(0)
	for d := range ticks {
		now += d
		if now >= deadline {
			at = 1 - at
			deadline = now + 1000
		}
		reply <- at
	}
	close(reply)
}

func mallocs() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Mallocs
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: control s1|s2|s3|s4|s7|s1actor|s7actor N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil || n < 0 || n%chunk != 0 {
		fmt.Fprintln(os.Stderr, "control: N is not a multiple of 1024")
		os.Exit(2)
	}
	events := make([]event, chunk)
	var probe int
	before := mallocs()
	switch os.Args[1] {
	case "s1":
		for i := range events {
			events[i] = press
		}
		s := inactive
		for i := 0; i < n; i++ {
			s = toggle(s, events[i%chunk])
		}
		probe = int(s)
	case "s2":
		for i := range events {
			events[i] = inc
		}
		s := counter{limit: 1<<53 - 1}
		for i := 0; i < n; i++ {
			s = count(s, events[i%chunk])
		}
		probe = s.count
	case "s3":
		for i := range events {
			events[i] = jump
		}
		s := a1x
		for i := 0; i < n; i++ {
			s = jumpStep(s, events[i%chunk])
		}
		probe = int(s)
	case "s4":
		for i := range events {
			events[i] = flip
		}
		s := both{}
		for i := 0; i < n; i++ {
			s = flipStep(s, events[i%chunk])
		}
		probe = int(s.r1) | int(s.r2)<<1
	case "s7":
		m := &s7{buf: make([]timerEffect, 0, 4)}
		m.buf = append(m.buf, timerEffect{schedule: true, key: 0, delayMs: 1000})
		for i := 0; i < n; i++ {
			// The host's timer for the scheduled key is due: fire it.
			key := m.buf[len(m.buf)-1].key
			m.fire(key)
		}
		probe = int(m.at)
	case "s1actor":
		inbox, reply := make(chan ask), make(chan light)
		go lightActor(inbox, reply)
		var s light
		for i := 0; i < n; i++ {
			inbox <- ask{e: press}
			s = <-reply
		}
		close(inbox)
		probe = int(s)
	case "s7actor":
		ticks, reply := make(chan int64), make(chan phase)
		go phaseActor(ticks, reply)
		var s phase
		for i := 0; i < n; i++ {
			ticks <- 1000
			s = <-reply
		}
		close(ticks)
		probe = int(s)
	default:
		fmt.Fprintln(os.Stderr, "control: no scenario", os.Args[1])
		os.Exit(2)
	}
	after := mallocs()
	fmt.Printf("%s control n=%d probe=%d mallocs=%d\n", os.Args[1], n, probe, after-before)
}
