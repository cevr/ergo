// Command control is ratelimitbench's hand Go twin
// (benchmarks/ratelimit/cmd/ratelimitbench): the same takes over the same
// keys, as a person writing Go would keep a limiter per key: a map under
// a mutex, each key's state with time.Time, the token bucket by GCRA (as
// x/time/rate's arithmetic reduces to for whole tokens), the fixed window
// as a start and a count, and the sliding window as a log of grants that
// drops its expired head. Run as `control bench KIND N`.
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type slot struct {
	at   time.Time
	cost int
}

type entry struct {
	seen, at time.Time
	used     int
	log      []slot
}

type limiter struct {
	mu    sync.Mutex
	kind  string
	limit int
	every time.Duration
	keys  map[string]*entry
}

// take is one take at now: the remaining tokens, or -1 when refused.
func (l *limiter) take(key string, cost int, now time.Time) (int, time.Duration) {
	if cost < 1 || cost > l.limit {
		return -2, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.keys[key]
	if !ok {
		e = &entry{seen: now, at: now}
		l.keys[key] = e
	}
	if e.seen.After(now) {
		now = e.seen
	}
	switch l.kind {
	case "window":
		start, used := e.at, e.used
		if now.Sub(e.at) >= l.every {
			start, used = now, 0
		}
		if used+cost > l.limit {
			return -1, l.every - now.Sub(start)
		}
		e.seen, e.at, e.used = now, start, used+cost
		return l.limit - e.used, 0
	case "sliding":
		first := 0
		for first < len(e.log) && now.Sub(e.log[first].at) >= l.every {
			first++
		}
		used := 0
		for _, s := range e.log[first:] {
			used += s.cost
		}
		if used+cost > l.limit {
			need, freed, i := used+cost-l.limit, 0, first
			for freed < need {
				freed += e.log[i].cost
				i++
			}
			return -1, l.every - now.Sub(e.log[i-1].at)
		}
		e.log = append(e.log[first:], slot{at: now, cost: cost})
		e.seen = now
		return l.limit - used - cost, 0
	default:
		base := e.at
		if now.After(base) {
			base = now
		}
		next := base.Add(time.Duration(cost) * l.every)
		ahead := next.Sub(now)
		room := time.Duration(l.limit) * l.every
		if ahead > room {
			return -1, ahead - room
		}
		e.seen, e.at = now, next
		return int((room - ahead) / l.every), 0
	}
}

func bench(kind string, n int) int {
	l := &limiter{kind: kind, limit: 10, every: time.Millisecond, keys: map[string]*entry{}}
	if kind == "window" || kind == "sliding" {
		l.every = 10 * time.Millisecond
	}
	keys := make([]string, 64)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	start := time.Unix(0, 0)
	granted := 0
	for i := 0; i < n; i++ {
		if r, _ := l.take(keys[i%64], 1, start.Add(time.Duration(i)*10*time.Microsecond)); r >= 0 {
			granted++
		}
	}
	return granted
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(bench(os.Args[2], n))
}
