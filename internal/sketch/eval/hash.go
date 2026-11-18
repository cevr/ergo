package eval

import (
	"fmt"
	"hash/fnv"
	"math"
)

// hashValue is the check-time evaluator's hash(k) (CONSTRAINTS C5): FNV-1a
// over a form of v in which == values are one form, so a == b gives
// hashValue(a) == hashValue(b), as the test build's maphash.Comparable
// does. Its numbers differ from the test build's, which are seeded per
// process: no verdict may read a hash beyond its equality.
func hashValue(v any) uint64 {
	h := fnv.New64a()
	switch v := v.(type) {
	case float64:
		if v == 0 {
			v = 0 // -0.0 == 0.0
		}
		fmt.Fprintf(h, "f%x", math.Float64bits(v))
	case float32:
		if v == 0 {
			v = 0
		}
		fmt.Fprintf(h, "f%x", math.Float32bits(v))
	default:
		fmt.Fprintf(h, "%T:%v", v, v)
	}
	return h.Sum64()
}
