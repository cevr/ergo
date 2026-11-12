// Command control is the hand-optimized Go control of examples/onebrc, the
// One Billion Row Challenge: the bar the Ergo program must meet
// (TestOneBRCCountsAsHandGo). It reads the same input, keeps the same
// contract (a malformed line and more than 10,000 stations are reported,
// never a panic) and prints the same bytes.
//
//	control FILE [WORKERS]
//
// Method (Hoyt's r9, Yastrebov's Go and Wuerthinger's Java): the file is
// mapped read-only, split at line starts into one chunk per worker, and
// each worker fills its own open-addressing table keyed by the station's
// first 16 bytes, its length and, past 16 bytes, its bytes in the file.
// Station and temperature are found with 8-byte words (SWAR): the ';' by a
// zero-byte test, the temperature and its form in one branch. A line the
// fast path does not accept, and every line within 128 bytes of the end,
// is parsed byte by byte, which is the authority on what is malformed.
// Temperatures are tenths in integers; the tables merge at the end.
package main

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"runtime"
	"slices"
	"strconv"
	"syscall"
)

const (
	tableBits   = 15
	tableSize   = 1 << tableBits
	maxStations = 10000
	maxName     = 100
	// fastMargin is the room the fast path needs after a line start: a
	// station of up to 100 bytes, its ';', and an 8-byte load of the
	// temperature.
	fastMargin = 128
)

// Reasons a line is malformed, in the order the line parser checks them.
const (
	noSeparator = iota
	emptyStation
	longStation
	noNewline
	badTemperature
)

var reasonText = [...]string{
	noSeparator:    "no ';' before the end of the line",
	emptyStation:   "an empty station name",
	longStation:    "a station name longer than 100 bytes",
	noNewline:      "no newline at the end of the input",
	badTemperature: "a temperature that is not -99.9 to 99.9 with one decimal",
}

type slot struct {
	w0, w1   uint64
	start, n int
	min, max int
	sum, cnt int
}

// part is one worker's result: its stations, the first malformed line of
// its chunk (bad >= 0), and whether it saw more than maxStations.
type part struct {
	slots    []slot
	used     []int
	bad      int
	reason   int
	overflow bool
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fail(2, "usage: control FILE [WORKERS]")
	}
	workers := runtime.NumCPU()
	if len(os.Args) == 3 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil || n < 1 {
			fail(2, "control: WORKERS must be a count")
		}
		workers = n
	}
	text := mapFile(os.Args[1])
	out, code := run(text, workers)
	if code != 0 {
		os.Stderr.Write(out)
		os.Exit(code)
	}
	os.Stdout.Write(out)
}

func fail(code int, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}

func mapFile(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		fail(1, "onebrc: "+path+": no such file")
	}
	st, err := f.Stat()
	if err != nil {
		fail(1, "control: "+err.Error())
	}
	if st.Size() == 0 {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		fail(1, "control: "+err.Error())
	}
	return data
}

func run(text []byte, workers int) ([]byte, int) {
	bounds := split(text, workers)
	parts := make([]part, len(bounds)-1)
	done := make(chan struct{}, len(parts))
	for i := range parts {
		go func() {
			parts[i] = scan(text, bounds[i], bounds[i+1])
			done <- struct{}{}
		}()
	}
	for range parts {
		<-done
	}
	overflow := false
	for _, p := range parts {
		if p.bad >= 0 {
			return []byte(fmt.Sprintf("onebrc: malformed line at byte %d: %s\n", p.bad, reasonText[p.reason])), 1
		}
		overflow = overflow || p.overflow
	}
	type stats struct{ min, max, sum, cnt int }
	total := make(map[string]*stats, maxStations)
	for _, p := range parts {
		for _, i := range p.used {
			s := &p.slots[i]
			name := text[s.start : s.start+s.n]
			if t := total[string(name)]; t != nil {
				t.min = min(t.min, s.min)
				t.max = max(t.max, s.max)
				t.sum += s.sum
				t.cnt += s.cnt
			} else {
				total[string(name)] = &stats{s.min, s.max, s.sum, s.cnt}
			}
		}
	}
	if overflow || len(total) > maxStations {
		return []byte("onebrc: more than 10000 stations\n"), 1
	}
	names := make([]string, 0, len(total))
	for name := range total {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]byte, 0, 64*len(names)+4)
	out = append(out, '{')
	for i, name := range names {
		if i > 0 {
			out = append(out, ", "...)
		}
		s := total[name]
		out = append(out, name...)
		out = append(out, '=')
		out = tenths(out, s.min)
		out = append(out, '/')
		out = tenths(out, floorDiv(2*s.sum+s.cnt, 2*s.cnt))
		out = append(out, '/')
		out = tenths(out, s.max)
	}
	out = append(out, "}\n"...)
	return out, 0
}

// split cuts text into up to workers chunks that start at line starts.
func split(text []byte, workers int) []int {
	bounds := []int{0}
	for i := 1; i < workers; i++ {
		at := max(len(text)*i/workers, bounds[len(bounds)-1])
		for at < len(text) && at > 0 && text[at-1] != '\n' {
			at++
		}
		if at > bounds[len(bounds)-1] && at < len(text) {
			bounds = append(bounds, at)
		}
	}
	return append(bounds, len(text))
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func tenths(out []byte, v int) []byte {
	if v < 0 {
		out = append(out, '-')
		v = -v
	}
	out = strconv.AppendInt(out, int64(v/10), 10)
	return append(out, '.', byte('0'+v%10))
}

func le64(b []byte, p int) uint64 { return binary.LittleEndian.Uint64(b[p:]) }

// lanes marks the bytes of w equal to the byte that rep repeats: the high
// bit of the lowest marked lane is exact, lanes above it may be marked too.
func lanes(w, rep uint64) uint64 {
	x := w ^ rep
	return (x - 0x0101010101010101) & ^x & 0x8080808080808080
}

func lowMask(n int) uint64 { return 1<<(uint(n)*8) - 1 }

func hash(w0, w1 uint64) int {
	return int(((w0 ^ w1*0xC2B2AE3D27D4EB4F) * 0x9E3779B97F4A7C15) >> (64 - tableBits))
}

// temp reads the temperature that starts the word w, "-?d?d.d\n": its
// tenths, the length of its text before the newline, and whether w holds
// that form.
func temp(w uint64) (int, int, bool) {
	neg := (((w ^ 0x2D) & 0xFF) - 1) >> 63
	dot := bits.TrailingZeros64(^w&0x10101000) >> 3
	n := dot + 2
	dotOK := (w>>(uint(dot)*8))&0xFF == '.'
	nlOK := (w>>(uint(n)*8))&0xFF == '\n'
	d := (w & lowMask(n+1)) ^ 0x1E<<(uint(dot)*8) ^ neg*0x1D ^ 0x3A<<(uint(n)*8)
	l := lowMask(n + 1)
	digitsOK := d&0xF0F0F0F0F0F0F0F0&l == 0x3030303030303030&l &&
		((d&0x0F0F0F0F0F0F0F0F&l)+(0x0606060606060606&l))&0x1010101010101010 == 0
	whole := dot - int(neg)
	ok := dot <= 3 && dotOK && nlOK && digitsOK && whole >= 1 && whole <= 2
	signed := -neg
	digits := ((w & ^(signed & 0xFF)) << (24 - uint(dot)*8)) & 0x0F000F0F00
	abs := ((digits * 0x640a0001) >> 32) & 0x3FF
	return int((abs ^ signed) - signed), n, ok
}

// line parses the line at p byte by byte: the station's length, the
// temperature, the next line's start, and the reason when it is malformed.
func line(text []byte, p int) (n, value, next, reason int) {
	i := p
	for i < len(text) && text[i] != ';' && text[i] != '\n' {
		i++
	}
	if i == len(text) || text[i] == '\n' {
		return 0, 0, 0, noSeparator
	}
	n = i - p
	if n == 0 {
		return 0, 0, 0, emptyStation
	}
	if n > maxName {
		return 0, 0, 0, longStation
	}
	j := i + 1
	for j < len(text) && text[j] != '\n' {
		j++
	}
	if j == len(text) {
		return 0, 0, 0, noNewline
	}
	t := text[i+1 : j]
	neg := len(t) > 0 && t[0] == '-'
	if neg {
		t = t[1:]
	}
	if len(t) < 3 || len(t) > 4 || t[len(t)-2] != '.' {
		return 0, 0, 0, badTemperature
	}
	for k, c := range t {
		if k != len(t)-2 && (c < '0' || c > '9') {
			return 0, 0, 0, badTemperature
		}
	}
	value = int(t[len(t)-1] - '0')
	if len(t) == 4 {
		value += 100*int(t[0]-'0') + 10*int(t[1]-'0')
	} else {
		value += 10 * int(t[0]-'0')
	}
	if neg {
		value = -value
	}
	return n, value, j + 1, -1
}

// words is the first 16 bytes of the station of n bytes at p, as the fast
// path loads them, read byte by byte.
func words(text []byte, p, n int) (w0, w1 uint64) {
	for k := 0; k < n && k < 16; k++ {
		if k < 8 {
			w0 |= uint64(text[p+k]) << (uint(k) * 8)
		} else {
			w1 |= uint64(text[p+k]) << (uint(k-8) * 8)
		}
	}
	return w0, w1
}

func scan(text []byte, p, end int) part {
	slots := make([]slot, tableSize)
	used := make([]int, 0, 1024)
	for p < end {
		var w0, w1 uint64
		var n, value, next int
		fast := false
		if p+fastMargin <= len(text) {
			w0 = le64(text, p)
			semi := lanes(w0, 0x3B3B3B3B3B3B3B3B)
			nl := lanes(w0, 0x0A0A0A0A0A0A0A0A)
			if semi != 0 {
				n = bits.TrailingZeros64(semi) >> 3
				w0 &= lowMask(n)
				fast = nl&lowMask(n) == 0
			} else if nl == 0 {
				w1 = le64(text, p+8)
				semi = lanes(w1, 0x3B3B3B3B3B3B3B3B)
				nl = lanes(w1, 0x0A0A0A0A0A0A0A0A)
				if semi != 0 {
					n = 8 + bits.TrailingZeros64(semi)>>3
					w1 &= lowMask(n - 8)
					fast = nl&lowMask(n-8) == 0
				} else if nl == 0 {
					n = 16
					for n <= maxName && text[p+n] != ';' && text[p+n] != '\n' {
						n++
					}
					fast = text[p+n] == ';'
				}
			}
			if fast && n >= 1 && n <= maxName {
				v, tn, ok := temp(le64(text, p+n+1))
				value, next, fast = v, p+n+tn+2, ok
			} else {
				fast = false
			}
		}
		if !fast {
			var reason int
			n, value, next, reason = line(text, p)
			if reason >= 0 {
				return part{bad: p, reason: reason}
			}
			w0, w1 = words(text, p, n)
		}
		h := hash(w0, w1)
		for {
			s := &slots[h]
			if s.n == n && s.w0 == w0 && s.w1 == w1 && (n <= 16 || string(text[s.start:s.start+n]) == string(text[p:p+n])) {
				s.min = min(s.min, value)
				s.max = max(s.max, value)
				s.sum += value
				s.cnt++
				break
			}
			if s.n == 0 {
				if len(used) == maxStations {
					bad, reason := validate(text, next, end)
					return part{bad: bad, reason: reason, overflow: true}
				}
				*s = slot{w0: w0, w1: w1, start: p, n: n, min: value, max: value, sum: value, cnt: 1}
				used = append(used, h)
				break
			}
			h = (h + 1) & (tableSize - 1)
		}
		p = next
	}
	return part{slots: slots, used: used, bad: -1}
}

// validate parses the lines from p to end: the start of the first
// malformed one and its reason, or -1.
func validate(text []byte, p, end int) (int, int) {
	for p < end {
		_, _, next, reason := line(text, p)
		if reason >= 0 {
			return p, reason
		}
		p = next
	}
	return -1, 0
}
