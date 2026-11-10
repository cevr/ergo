// Package testgen owns the deterministic case stream used by both Ergo test
// executables and the compiler's bounded evaluator. It has no compiler imports.
package testgen

// Depth is the initial structural generator depth, shared with case plans.
const Depth = 4

// Stream is splitmix64, mixed with the run's full binding label. Start changes
// the size and case world, never the random state. Charge, when set, is called
// before each draw and allocation; a bounded caller may unwind from it.
type Stream struct {
	s      uint64
	size   int
	world  map[string]any
	Charge func(int64)
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
func (r *Stream) Start(i int) { r.size = i / 4; r.world = nil }

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
func (r *Stream) Next() uint64 {
	r.charge(1)
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}
func (r *Stream) Intn(n int) int { return int(r.Next() % uint64(n)) }
func (r *Stream) Bool() bool     { return r.Next()&1 == 0 }

var edges = [...]int64{0, 1, -1, 127, -128, 128, -129, 255, 256, 32767, -32768, 65535, 65536, 2147483647, -2147483648, 4294967295, 4294967296, 9223372036854775807, -9223372036854775808}

func (r *Stream) I64() int64 {
	switch k := r.Intn(20); {
	case k < 8:
		return int64(r.Intn(21)) - 10
	case k < 11:
		return edges[r.Intn(len(edges))]
	case k < 14:
		return int64(r.Intn(1999999)) - 999999
	}
	return int64(r.Next())
}
func (r *Stream) F64() float64 {
	if r.Bool() {
		return float64(r.Intn(21)-10) / 2
	}
	return float64(int64(r.Next())) / (1 << 32)
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
