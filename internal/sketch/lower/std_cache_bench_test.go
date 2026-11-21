package lower

import "testing"

// BenchmarkStdCache is the cache's hand-Go control (std-spike C1; plan §5
// C1): 4,096 Gets on a fresh cache of 1,024 entries over 2,048 keys drawn
// so that about two thirds hit, against a map under a mutex, a recency
// list (container/list) and a hand singleflight (a map of calls in flight,
// each with a channel the waiters read), the shape of
// hashicorp/golang-lru's simplelru beside x/sync/singleflight. Both read
// a manual clock: the Ergo side time.Manual, the Go side an int64 the
// caller sets. Expected: within a small factor of hand Go; reported
// either way.
func BenchmarkStdCache(b *testing.B) {
	const src = `import cache
import time

func Gets(keys []int) int {
	let m = time.NewManual(0)
	let c = cache.New[int, int](cache.Config{Capacity: 1024, Life: 0s, Idle: 0s}) catch {
	case cache.BadConfig:
		return 0
	}
	with context { time.Clock: m } {
		let mut s = 0
		for k in keys {
			s = s + cache.Get(c, k, func(key int) int { key * 2 })
		}
		s
	}
}

func main() needs Console {
	Console.Println("\(Gets([]int{1, 2, 1}))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"container/list"
	"sync"
	"testing"
	"time"
)

var keys = func() []int {
	xs := make([]int, 4096)
	for i := range xs {
		// Two thirds of the gets go to 512 hot keys; the rest spread over 2,048.
		if i%3 != 0 {
			xs[i] = (i * 7919) % 512
		} else {
			xs[i] = (i * 104729) % 2048
		}
	}
	return xs
}()

var sink int

func BenchmarkCacheErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Gets", "keys") + `
	}
}

type entry struct {
	key, value int
	born, used int64
}

type call struct {
	done  chan struct{}
	value int
}

// handCache is the Go control: a map into a recency list under a mutex,
// and the calls in flight by key.
type handCache struct {
	mu       sync.Mutex
	capacity int
	life     time.Duration
	items    map[int]*list.Element
	order    *list.List
	flight   map[int]*call
	now      *int64
	hits     int
	misses   int
}

func newHand(capacity int, now *int64) *handCache {
	return &handCache{capacity: capacity, items: map[int]*list.Element{}, order: list.New(), flight: map[int]*call{}, now: now}
}

func (c *handCache) get(k int, lookup func(int) int) int {
	for {
		c.mu.Lock()
		now := *c.now
		if el, ok := c.items[k]; ok {
			e := el.Value.(*entry)
			if c.life == 0 || time.Duration(now-e.born) < c.life {
				e.used = now
				c.order.MoveToBack(el)
				c.hits++
				c.mu.Unlock()
				return e.value
			}
			c.order.Remove(el)
			delete(c.items, k)
		}
		if f, ok := c.flight[k]; ok {
			c.mu.Unlock()
			<-f.done
			continue
		}
		f := &call{done: make(chan struct{})}
		c.flight[k] = f
		c.misses++
		c.mu.Unlock()
		v := lookup(k)
		c.mu.Lock()
		delete(c.flight, k)
		c.items[k] = c.order.PushBack(&entry{key: k, value: v, born: *c.now, used: *c.now})
		for len(c.items) > c.capacity {
			old := c.order.Front()
			c.order.Remove(old)
			delete(c.items, old.Value.(*entry).key)
		}
		c.mu.Unlock()
		close(f.done)
		return v
	}
}

func handGets(keys []int) int {
	var now int64
	c := newHand(1024, &now)
	s := 0
	for _, k := range keys {
		s += c.get(k, func(key int) int { return key * 2 })
	}
	return s
}

func BenchmarkCacheGo(b *testing.B) {
	for range b.N {
		sink = handGets(keys)
	}
}

func TestCacheAgrees(t *testing.T) {
	if got, want := ` + emittedCall(mainGo, "Gets", "keys") + `, handGets(keys); got != want {
		t.Fatalf("Ergo %d, hand Go %d", got, want)
	}
}
`
	}
	emittedBench(b, "std_cache", src, gen, "CacheErgo", "CacheGo")
}
