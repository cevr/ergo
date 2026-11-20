package eval

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"
)

// hashValue is the check-time evaluator's hash(k) (CONSTRAINTS C5): FNV-1a
// over a form of v in which == values are one form, so a == b gives
// hashValue(a) == hashValue(b), as the test build's maphash.Comparable
// does. A value with parts (a record, a tuple, an array) is hashed by its
// canonical key text (mapkeys.go writeCanon), which reads every part and
// writes -0 as 0. Its numbers differ from the test build's, which are
// seeded per process: no verdict may read a hash beyond its equality.
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
		if basicValue(v) {
			fmt.Fprintf(h, "%T:%v", v, v)
			break
		}
		var b strings.Builder
		writeCanon(&b, v)
		h.Write([]byte(b.String()))
	}
	return h.Sum64()
}
