package rt

import (
	"bytes"
	"runtime"
	"strconv"
	"testing"
	"testing/synctest"
)

// goid is the calling goroutine's id, read from its stack header.
func goid() uint64 {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	buf = bytes.TrimPrefix(buf, []byte("goroutine "))
	n, _ := strconv.ParseUint(string(buf[:bytes.IndexByte(buf, ' ')]), 10, 64)
	return n
}

// TestCarrierRunsChildrenOnOneGoroutine: the successive children StartOn
// starts on one carrier, each waited before the next, run on one
// goroutine, and Close ends it: the bubble fails on a goroutine left
// parked. Mutants carrier-always-goes (every child gets a goroutine) and
// carrier-close-skips-wake (Close leaves the goroutine parked) are killed
// only here.
func TestCarrierRunsChildrenOnOneGoroutine(t *testing.T) {
	if Exploring {
		t.Skip("under the explored scheduler StartOn is Start")
	}
	synctest.Test(t, func(t *testing.T) {
		root := Root("carrier")
		var c Carrier
		var ids []uint64
		for i := range 4 {
			h := StartOn(&c, root, "child", funcBody[uint64](func(*Task) (uint64, error) { return goid(), nil }))
			id, err := h.Wait()
			if err != nil {
				t.Fatalf("child %d: %v", i, err)
			}
			ids = append(ids, id)
		}
		c.Close()
		for i, id := range ids {
			if id != ids[0] {
				t.Fatalf("child %d ran on goroutine %d, child 0 on %d; want one carrier: %v", i, id, ids[0], ids)
			}
		}
	})
}
