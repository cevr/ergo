// Command control is the hand-written Go control of examples/dash-gui
// (corpus wave GP4a): the same dashboard as the Ergo program's play
// command, written as a Go programmer would: a goroutine per sensor
// source, buffered channels for its orders and its samples, rings over
// fixed arrays, and one strings.Builder per line. TestDashGuiAgreesWithControl
// holds it and the Ergo program to one reference model, byte for byte.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	staleAfter = 60
	easeFrames = 30
	capacity   = 16
	burst      = capacity + 8
	span       = 180
	alarm      = 900
	top        = 999
	bins       = 8
	columns    = 8
)

const (
	chart = iota
	spark
	gauge
	strip
	logKind
)

var kinds = [...]int{chart, chart, chart, chart, spark, spark, spark, gauge, gauge, strip, strip, logKind}

func hash(seed, a, b, c int) uint64 {
	x := uint64(seed)*0x9E3779B97F4A7C15 ^ uint64(a)*0xBF58476D1CE4E5B9 ^ uint64(b)*0x94D049BB133111EB ^ uint64(c)*0xD6E8FEB86659FD93
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

func count(seed, source, frame int) int {
	if hash(seed, source, frame/span, 1)%4 == 0 {
		return 0
	}
	h := hash(seed, source, frame, 2) % 100
	if h < 2 {
		return burst
	}
	return int(h % 3)
}

func read(seed, source, frame, k int) int {
	wave := ((frame+source*37)%200 + 200) % 200
	base := wave * 6
	if wave >= 100 {
		base = (200 - wave) * 6
	}
	return base + int(hash(seed, source, frame, 3+k)%400)
}

// ring keeps the last len(buf) values over a fixed buffer.
type ring[T any] struct {
	buf  []T
	head int
	n    int
}

func newRing[T any](n int) *ring[T] { return &ring[T]{buf: make([]T, n)} }

func (r *ring[T]) push(v T) {
	r.buf[(r.head+r.n)%len(r.buf)] = v
	if r.n < len(r.buf) {
		r.n++
	} else {
		r.head = (r.head + 1) % len(r.buf)
	}
}

func (r *ring[T]) items(out []T) []T {
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.head+i)%len(r.buf)])
	}
	return out
}

type entry struct{ frame, value int }

const (
	live = iota
	paused
	stale
)

type panel struct {
	kind, mode      int
	got, seen       int
	samples         *ring[int]
	log             *ring[entry]
	from, to, start int
}

func ease(from, to, k, d int) int {
	if d <= 0 || k >= d {
		return to
	}
	if k <= 0 {
		return from
	}
	rest := d - k
	return from + (to-from)*(d*d*d-rest*rest*rest)/(d*d*d)
}

func (p *panel) take(f int, values []int) {
	switch p.kind {
	case gauge:
		p.from, p.to, p.start = ease(p.from, p.to, f-p.start, easeFrames), values[len(values)-1], f
	case logKind:
		for _, v := range values {
			if v > alarm {
				p.log.push(entry{f, v})
			}
		}
	default:
		for _, v := range values {
			p.samples.push(v)
		}
	}
	p.seen, p.got = f, p.got+len(values)
}

type order struct{ frame, count int }

type report struct{ source, dropped int }

type board struct {
	seed    int
	frame   int
	panels  []*panel
	newest  []bool
	dropped []int
	orders  []chan order
	data    []chan int
	done    chan report
	out     *strings.Builder
	scratch []int
}

func (b *board) serve(i int) {
	for o := range b.orders[i] {
		dropped := 0
		for k := 0; k < o.count; k++ {
			v := read(b.seed, i, o.frame, k)
			switch {
			case len(b.data[i]) < capacity:
				b.data[i] <- v
			case b.newest[i]:
				dropped++
			default:
				<-b.data[i]
				b.data[i] <- v
				dropped++
			}
		}
		b.done <- report{i, dropped}
	}
}

func (b *board) advance() {
	f := b.frame + 1
	b.frame = f
	for i := range b.panels {
		b.orders[i] <- order{f, count(b.seed, i, f)}
	}
	for range b.panels {
		r := <-b.done
		b.dropped[r.source] += r.dropped
	}
	for i, p := range b.panels {
		values := b.scratch[:0]
	drain:
		for {
			select {
			case v := <-b.data[i]:
				values = append(values, v)
			default:
				break drain
			}
		}
		switch p.mode {
		case live:
			if len(values) > 0 {
				p.take(f, values)
			}
			if f-p.seen > staleAfter {
				p.mode = stale
				fmt.Fprintf(b.out, "%d: %d stale\n", f, i)
			}
		case stale:
			if len(values) > 0 {
				p.take(f, values)
				p.mode = live
				fmt.Fprintf(b.out, "%d: %d live\n", f, i)
			}
		}
		b.scratch = values
	}
}

func downsample(xs []int, width int) string {
	cols := min(len(xs), width)
	var s strings.Builder
	s.WriteByte('[')
	for c := 0; c < cols; c++ {
		from, to := c*len(xs)/cols, (c+1)*len(xs)/cols
		lo, hi := xs[from], xs[from]
		for _, x := range xs[from+1 : to] {
			lo, hi = min(lo, x), max(hi, x)
		}
		if c > 0 {
			s.WriteByte(' ')
		}
		fmt.Fprintf(&s, "%d-%d", lo, hi)
	}
	s.WriteByte(']')
	return s.String()
}

func (b *board) show() {
	fmt.Fprintf(b.out, "frame %d\n", b.frame)
	for i, p := range b.panels {
		fmt.Fprintf(b.out, "%d %s got=%d seen=%d ", i, [...]string{"live", "paused", "stale"}[p.mode], p.got, p.seen)
		switch p.kind {
		case chart:
			xs := p.samples.items(nil)
			fmt.Fprintf(b.out, "chart n=%d %s\n", len(xs), downsample(xs, columns))
		case spark:
			fmt.Fprintf(b.out, "spark %s\n", downsample(p.samples.items(nil), columns))
		case gauge:
			fmt.Fprintf(b.out, "gauge at=%d to=%d\n", ease(p.from, p.to, b.frame-p.start, easeFrames), p.to)
		case strip:
			var heat [bins]int
			for _, x := range p.samples.items(nil) {
				heat[min(max(x, 0), top)*bins/(top+1)]++
			}
			fmt.Fprintf(b.out, "strip %v\n", heat)
		case logKind:
			var words []string
			for _, e := range p.log.items(nil) {
				words = append(words, fmt.Sprintf("%d:%d", e.frame, e.value))
			}
			b.out.WriteString("log [" + strings.Join(words, " ") + "]\n")
		}
	}
}

func number(line, word string) (int, string) {
	n, err := strconv.Atoi(word)
	if err != nil || n < 0 || strings.HasPrefix(word, "+") {
		return 0, "a number from 0"
	}
	return n, ""
}

func (b *board) line(line string) {
	// The program quotes the line as it is, with no escapes.
	bad := func(want string) { b.out.WriteString("? \"" + line + "\": want " + want + "\n") }
	words := strings.Split(line, " ")
	args := len(words) - 1
	switch words[0] {
	case "show", "drops":
		if args != 0 {
			bad("no argument")
			return
		}
		if words[0] == "show" {
			b.show()
			return
		}
		b.out.WriteString("dropped")
		for _, d := range b.dropped {
			fmt.Fprintf(b.out, " %d", d)
		}
		b.out.WriteByte('\n')
	case "run", "pause", "resume":
		if args != 1 {
			bad("one number")
			return
		}
		n, want := number(line, words[1])
		if want != "" {
			bad(want)
			return
		}
		switch words[0] {
		case "run":
			for range n {
				b.advance()
			}
		case "pause":
			if n >= len(b.panels) || b.panels[n].mode == paused {
				b.out.WriteString("? \"" + line + "\": refused\n")
				return
			}
			b.panels[n].mode = paused
		default:
			if n >= len(b.panels) || b.panels[n].mode != paused {
				b.out.WriteString("? \"" + line + "\": refused\n")
				return
			}
			b.panels[n].mode = live
		}
	case "policy":
		if args != 2 {
			bad("a panel and oldest or newest")
			return
		}
		n, want := number(line, words[1])
		if want != "" {
			bad(want)
			return
		}
		if words[2] != "oldest" && words[2] != "newest" {
			bad("oldest or newest")
			return
		}
		if n >= len(b.panels) {
			fmt.Fprintf(b.out, "? \"%s\": no panel %d\n", line, n)
			return
		}
		b.newest[n] = words[2] == "newest"
	default:
		bad("run, pause, resume, policy, show or drops")
	}
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "play" || os.Args[2] != "-seed" {
		fmt.Fprintln(os.Stderr, "usage: control play -seed S")
		os.Exit(2)
	}
	seed, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: control play -seed S")
		os.Exit(2)
	}
	b := &board{seed: seed, done: make(chan report, len(kinds)), out: &strings.Builder{}}
	for i, k := range kinds {
		p := &panel{kind: k}
		switch k {
		case chart:
			p.samples = newRing[int](10000)
		case spark:
			p.samples = newRing[int](64)
		case strip:
			p.samples = newRing[int](256)
		case logKind:
			p.log = newRing[entry](8)
		}
		b.panels = append(b.panels, p)
		b.newest = append(b.newest, false)
		b.dropped = append(b.dropped, 0)
		b.orders = append(b.orders, make(chan order, 1))
		b.data = append(b.data, make(chan int, capacity))
		go b.serve(i)
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lines := strings.Split(string(in), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.line(line)
		os.Stdout.WriteString(b.out.String())
		b.out.Reset()
	}
	for _, o := range b.orders {
		close(o)
	}
}
