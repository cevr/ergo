# Counters and flags shared by tasks

```go
import atomic
import tasks

test CountFinished {
	let count = atomic.NewInt64(0)
	let closed = atomic.NewBool(false)
	let set = tasks.NewSet(tasks.FailFast)
	for _ in 0..4 {
		try tasks.Go(set, func() { _ = count.Add(1) })
	}
	set.Wait()
	closed.Store(true)
	assert count.Load() == 4 && closed.Load()
}
```

An Add returns the new count, a Swap the old value. CompareAndSwap stores only when the old value matches. Handles are shared; a copy sees the same cell. These are Go's sequentially consistent typed atomics, without unsafe code in Ergo's bodies. For a compound update of an arbitrary value, use sync.Ref instead.
