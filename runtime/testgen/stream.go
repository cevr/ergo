// Package testgen owns the deterministic case stream used by both Ergo test
// executables and the compiler's bounded evaluator. It has no compiler imports.
package testgen

import "math/bits"

// Depth is the initial structural generator depth, shared with case plans.
const Depth = 4

// Stream is splitmix64, mixed with the run's full binding label. Start changes
// the size and case world, never the random state. Charge, when set, is called
// before each draw and allocation; a bounded caller may unwind from it.
//
// Every draw is recorded on the case's tape (L10). A shrink replays a case
// from an edited tape (Replay; past its end a draw is 0), and each generator
// maps a zero draw to its simplest value: 0, 0.0, false, an empty string,
// slice or Map, a union's first variant.
type Stream struct {
	s         uint64
	size      int
	world     map[string]any
	tape      []uint64
	replay    []uint64
	replaying bool
	pos       int
	Charge    func(int64)
}

func New(seed uint64, name string) *Stream {
	r := &Stream{s: seed}
	for i := 0; i < len(name); i++ {
		r.s = r.s*31 + uint64(name[i])
	}
	return r
}

func (r *Stream) charge(n int64) {
	if r.Charge != nil {
		r.Charge(n)
	}
}

// Start begins case i: its size is i/4, its world and its tape are empty.
func (r *Stream) Start(i int) {
	r.size = i / 4
	r.world = nil
	r.tape = r.tape[:0]
}

// SetSize serves native context runners that own their case-world boundary.
func (r *Stream) SetSize(n int) { r.size = n }
func (r *Stream) Built(key string, build func() any) any {
	if v, ok := r.world[key]; ok {
		return v
	}
	if r.world == nil {
		r.world = map[string]any{}
	}
	v := build()
	r.world[key] = v
	return v
}

// Record begins a drawn case: an empty tape, draws from the seed.
func (r *Stream) Record() { r.tape, r.replaying = r.tape[:0], false }

// Tape is a copy of the case's recorded draws.
func (r *Stream) Tape() []uint64 { return append([]uint64(nil), r.tape...) }

// Replay begins a case drawn from tape c, with an empty world.
func (r *Stream) Replay(c []uint64) {
	r.replay, r.pos, r.replaying, r.tape, r.world = c, 0, true, r.tape[:0], nil
}

// EndReplay returns the stream to drawing from the seed.
func (r *Stream) EndReplay() { r.replaying = false }

// Next is a draw of any 64 bits; Intn records its draw reduced below n, so
// a shrink lowering a recorded draw lowers the value it gives.
func (r *Stream) Next() uint64 {
	z := r.raw()
	r.tape = append(r.tape, z)
	return z
}

func (r *Stream) raw() uint64 {
	r.charge(1)
	if r.replaying {
		var z uint64
		if r.pos < len(r.replay) {
			z = r.replay[r.pos]
		}
		r.pos++
		return z
	}
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *Stream) Intn(n int) int {
	z := r.raw() % uint64(n)
	r.tape = append(r.tape, z)
	return int(z)
}

func (r *Stream) Bool() bool { return r.Intn(2) == 1 }

// any64 is any int64: draws 0, 1, 2, 3, … give 0, -1, 1, -2, ….
func (r *Stream) any64() int64 {
	z := r.Next()
	return int64(z>>1) ^ -int64(z&1)
}

// small is in -n..n: draws 0, 1, 2, 3, 4, … give 0, -1, 1, -2, 2, …, so a
// smaller draw is a value nearer zero.
func (r *Stream) small(n int) int64 {
	v := r.Intn(2*n + 1)
	if v%2 == 1 {
		return -int64(v+1) / 2
	}
	return int64(v / 2)
}

// edges are the integers where code breaks: zero, one and minus one, and
// the bounds of each Go integer width.
var edges = [...]int64{0, 1, -1, 127, -128, 128, -129, 255, 256, 32767, -32768, 65535, 65536, 2147483647, -2147483648, 4294967295, 4294967296, 9223372036854775807, -9223372036854775808}

// I64 is biased to small values and edges: 40% in -10..10, 15% an edge,
// 15% in -999999..999999 and 30% any 64 bits; a narrower type wraps it.
func (r *Stream) I64() int64 {
	switch k := r.Intn(20); {
	case k < 8:
		return r.small(10)
	case k < 11:
		return edges[r.Intn(len(edges))]
	case k < 14:
		return r.small(999999)
	}
	return r.any64()
}

// Integer is I64 narrowed to bits (0: the target int), recorded as sign and
// magnitude (an unsigned value has only magnitude). Distribution choices are
// not replay choices: switching from an edge to a small-value branch must not
// strand a shrink at 127. For a fixed sign, lowering magnitude moves toward
// zero monotonically, including MinInt64 and MaxUint64.
func (r *Stream) Integer(width uint, signed bool) uint64 {
	if width == 0 {
		width = bits.UintSize
	}
	mask := ^uint64(0) >> (64 - width)
	if r.replaying {
		negative := signed && r.Bool()
		magnitude := r.Next()
		limit := mask
		if signed {
			limit = mask >> 1
			if negative {
				limit++
			}
		}
		// Deleting draws can move a wider integer into this position.
		// Clamp rather than wrap: shrinking stays monotone in magnitude.
		if magnitude > limit {
			magnitude = limit
		}
		if negative {
			return -magnitude
		}
		return magnitude
	}
	start := len(r.tape)
	n := uint64(r.I64()) & mask
	r.tape = r.tape[:start]
	magnitude := n
	if signed {
		sign := uint64(0)
		if n&(uint64(1)<<(width-1)) != 0 {
			sign, magnitude = 1, -n&mask
		}
		r.tape = append(r.tape, sign)
	}
	r.tape = append(r.tape, magnitude)
	return n
}

// F64 is always finite: a small half-integer half the time.
func (r *Stream) F64() float64 {
	if !r.Bool() {
		return float64(r.small(10)) / 2
	}
	return float64(r.any64()) / (1 << 32)
}

var runes = []rune("abz AZ09_-é世\n")

func (r *Stream) Str() string {
	n := r.Intn(6)
	if r.Bool() {
		n = r.Intn(r.size + 1)
	}
	r.charge(int64(n) * 4)
	b := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		b = r.unit(b)
	}
	return string(b)
}
func (r *Stream) unit(b []byte) []byte {
	switch r.Intn(16) {
	case 8:
		return append(b, '"')
	case 9:
		return append(b, '\\')
	case 10:
		return append(b, byte(r.Intn(0x20)))
	case 11:
		return append(b, 0x7f)
	case 12:
		k := r.Intn(0x42)
		if k < 0x40 {
			return append(b, byte(0x80+k))
		}
		return append(b, byte(0xFE+k-0x40))
	case 13:
		return append(b, string(rune(0x10000+r.Intn(0x100000)))...)
	case 14:
		return append(b, string(rune(0x80+r.Intn(0xD800-0x80)))...)
	case 15:
		return append(b, byte(0x20+r.Intn(0x5F)))
	}
	return append(b, string(runes[r.Intn(len(runes))])...)
}
func (r *Stream) In(spans [][2]int64) int64 {
	s := spans[r.Intn(len(spans))]
	switch r.Intn(4) {
	case 0:
		return s[0]
	case 1:
		return s[1]
	}
	w := uint64(s[1]) - uint64(s[0]) + 1
	if w == 0 {
		return int64(r.Next())
	}
	return s[0] + int64(r.Next()%w)
}
func (r *Stream) StrLen(n int) string {
	r.charge(int64(n) + 4)
	b := make([]byte, 0, n+4)
	for len(b) < n {
		b = r.unit(b)
	}
	return string(b[:n])
}
func (r *Stream) Limit(d int) int {
	n := r.size
	for k := d; k < Depth && n > 0; k++ {
		n /= 2
	}
	return n
}
