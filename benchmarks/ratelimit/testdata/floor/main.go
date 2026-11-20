// Command floor is ratelimitbench's Go-runtime floor: the least work the
// Go toolchain and runtime allow for the same takes with the same
// guarantees (one limiter tasks may share, exact RetryAfter, a late time
// counted as the last grant). Times are int64 nanoseconds, not time.Time;
// a key's state is a pointer into a map read once per take, under one
// mutex; the token bucket is one int64 (GCRA's arrival time); the sliding
// window keeps its grants in a ring of Limit slots, which no grant past
// Limit tokens can overflow. Run as `floor bench KIND N`.
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

type entry struct {
	seen, at int64
	used     int
	head, n  int
	ring     []slot
}

type slot struct{ at, cost int64 }

type limiter struct {
	mu    sync.Mutex
	kind  byte
	limit int64
	width int64
	keys  map[string]*entry
}

// take is one take at now: the remaining tokens, or -1 when refused.
func (l *limiter) take(key string, cost int64, now int64) (int64, int64) {
	if cost < 1 || cost > l.limit {
		return -2, 0
	}
	l.mu.Lock()
	e := l.keys[key]
	if e == nil {
		e = &entry{seen: now, at: now}
		if l.kind == 's' {
			e.ring = make([]slot, l.limit)
		}
		l.keys[key] = e
	}
	if e.seen > now {
		now = e.seen
	}
	switch l.kind {
	case 'w':
		if now-e.at >= l.width {
			e.at, e.used = now, 0
		}
		if int64(e.used)+cost > l.limit {
			wait := l.width - (now - e.at)
			l.mu.Unlock()
			return -1, wait
		}
		e.used += int(cost)
		e.seen = now
		r := l.limit - int64(e.used)
		l.mu.Unlock()
		return r, 0
	case 's':
		used := int64(0)
		for e.n > 0 && now-e.ring[e.head].at >= l.width {
			e.head = (e.head + 1) % len(e.ring)
			e.n--
		}
		for i := 0; i < e.n; i++ {
			used += e.ring[(e.head+i)%len(e.ring)].cost
		}
		if used+cost > l.limit {
			need, freed, i := used+cost-l.limit, int64(0), 0
			for freed < need {
				freed += e.ring[(e.head+i)%len(e.ring)].cost
				i++
			}
			wait := l.width - (now - e.ring[(e.head+i-1)%len(e.ring)].at)
			l.mu.Unlock()
			return -1, wait
		}
		e.ring[(e.head+e.n)%len(e.ring)] = slot{at: now, cost: cost}
		e.n++
		e.seen = now
		l.mu.Unlock()
		return l.limit - used - cost, 0
	default:
		base := e.at
		if now > base {
			base = now
		}
		next := base + cost*l.width
		ahead := next - now
		room := l.limit * l.width
		if ahead > room {
			l.mu.Unlock()
			return -1, ahead - room
		}
		e.seen, e.at = now, next
		l.mu.Unlock()
		return (room - ahead) / l.width, 0
	}
}

func bench(kind string, n int) int {
	l := &limiter{kind: 'b', limit: 10, width: 1e6, keys: map[string]*entry{}}
	switch kind {
	case "window":
		l.kind, l.width = 'w', 1e7
	case "sliding":
		l.kind, l.width = 's', 1e7
	}
	keys := make([]string, 64)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	granted := 0
	for i := 0; i < n; i++ {
		if r, _ := l.take(keys[i%64], 1, int64(i)*1e4); r >= 0 {
			granted++
		}
	}
	return granted
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(bench(os.Args[2], n))
}
