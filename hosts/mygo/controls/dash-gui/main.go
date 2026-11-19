// Dash-gui is the MyGo-direct control of examples/dash-gui (corpus wave
// GP4c): the dashboard's model as cmd/ergo/testdata/dash-gui/control
// writes it in hand Go (a goroutine per sensor source, buffered channels,
// rings over fixed arrays), copied here, and the dashboard's window on
// MyGo's own widgets, each panel's plot a box its Draw paints with MyGo's
// Painter and its chart package (chart.MinMax, chart.LineInto). It is
// what the Ergo window binary (cmd/dash-gui) is measured against.
//
//	dash-gui [-seed S]                           open the window
//	dash-gui -headless [-frames N] [-seed S]     play the scene script on
//	                                             standard input, a frame
//	                                             of ui.Tester at each of
//	                                             the dashboard's frames
//
// Headless, a line goes to the model, not through the widgets: the output
// is the model's, and the window is laid out and painted at each frame.
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	uichart "github.com/egoist/mygo/ui/chart"
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

// window is the dashboard's window on MyGo's widgets, the same as the
// Ergo view's: the frame's number, each panel's line, its pause and
// policy buttons and its plot (a box whose Draw paints the panel with
// MyGo's Painter and its chart package), then the drop counts.
type window struct {
	b     *board
	lines []string
	buf   strings.Builder
	xs    []float32
	ys    []float32
	dx    []float32
	dy    []float32
	path  ui.Path
}

const (
	plotW = 256
	plotH = 64
)

// refresh takes the frame's text (the frame's number and each panel's
// line) and the drop counts from the model, as the Ergo view takes them
// from its state.
func (w *window) refresh() {
	keep := w.b.out
	w.buf.Reset()
	w.b.out = &w.buf
	w.b.show()
	w.b.line("drops")
	w.b.out = keep
	w.lines = strings.Split(strings.TrimSuffix(w.buf.String(), "\n"), "\n")
}

func (w *window) view(c *ui.Context) {
	ui.Column(c).Children(func() {
		ui.Text(c, w.lines[0])
		for i, p := range w.b.panels {
			ui.Column(c).Key(i).Children(func() {
				ui.Text(c, w.lines[1+i])
				ui.Row(c).Children(func() {
					label := "pause"
					if p.mode == paused {
						label = "resume"
					}
					if ui.Button(c, label).Label(fmt.Sprintf("toggle %d", i)).Clicked() {
						w.toggle(i)
					}
					policy := "drop oldest"
					if w.b.newest[i] {
						policy = "drop newest"
					}
					if ui.Button(c, policy).Label(fmt.Sprintf("policy %d", i)).Clicked() {
						w.b.newest[i] = !w.b.newest[i]
						w.refresh()
					}
				})
				ui.Box(c).Label(fmt.Sprintf("panel %d", i)).Size(plotW, plotH).Draw(func(pt *ui.Painter, r ui.Rect) { w.paint(pt, r, p) })
			})
		}
		ui.Text(c, w.lines[len(w.lines)-1])
	})
}

// toggle pauses a running panel and resumes a paused one, as the Ergo
// view's button does.
func (w *window) toggle(i int) {
	if w.b.panels[i].mode == paused {
		w.b.panels[i].mode = live
	} else {
		w.b.panels[i].mode = paused
	}
	w.refresh()
}

var (
	liveInk   = ui.RGB(40, 120, 220)
	pausedInk = ui.RGB(150, 150, 150)
	staleInk  = ui.RGB(220, 80, 40)
)

// paint draws panel p in r: a chart or a spark line as a line through
// each column's least and greatest sample (uichart.MinMax), a gauge as a
// bar to its eased value, a heat strip as bins shaded by their share, a
// log as a tick at each entry's value.
func (w *window) paint(pt *ui.Painter, r ui.Rect, p *panel) {
	ink := [...]ui.Color{liveInk, pausedInk, staleInk}[p.mode]
	y := func(v int) float32 { return uichart.Y(r, float32(min(max(v, 0), top))/top) }
	switch p.kind {
	case chart, spark:
		vals := p.samples.items(w.b.scratch[:0])
		w.xs, w.ys = w.xs[:0], w.ys[:0]
		for k, v := range vals {
			w.xs = append(w.xs, float32(k))
			w.ys = append(w.ys, float32(min(max(v, 0), top))/top)
		}
		if len(vals) > 1 {
			for k := range w.xs {
				w.xs[k] /= float32(len(vals) - 1)
			}
		}
		w.dx, w.dy = uichart.MinMax(w.dx[:0], w.dy[:0], w.xs, w.ys, int(r.W))
		if len(w.dx) > 1 {
			pt.StrokePath(uichart.LineInto(w.path.Reset(), r, w.dx, w.dy, uichart.CurveLinear), 1, ink)
		}
	case gauge:
		at := ease(p.from, p.to, w.b.frame-p.start, easeFrames)
		pt.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/3, W: r.W, H: r.H / 3}, pausedInk, 2)
		pt.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/3, W: r.W * float32(min(max(at, 0), top)) / top, H: r.H / 3}, ink, 2)
	case strip:
		var heat [bins]int
		n := 0
		for _, x := range p.samples.items(w.b.scratch[:0]) {
			heat[min(max(x, 0), top)*bins/(top+1)]++
			n++
		}
		for k, h := range heat {
			if n > 0 && h > 0 {
				c := ink
				c.A = uint8(255 * h / n)
				pt.Fill(ui.Rect{X: r.X + r.W*float32(k)/bins, Y: r.Y, W: r.W / bins, H: r.H}, c, 0)
			}
		}
	case logKind:
		for k, e := range p.log.items(nil) {
			x := r.X + r.W*float32(k)/8
			pt.Line(x, y(e.value), x+r.W/16, y(e.value), 2, ink)
		}
	}
}

// scene plays a line of the example's scene script on the window, as the
// Ergo view's host does: frames N moves N frames (each painted), tap
// LABEL presses a panel's button, show prints the window's texts.
func (w *window) scene(line string, out *bufio.Writer, frame func()) {
	words := strings.Split(line, " ")
	switch words[0] {
	case "frames":
		n, err := strconv.Atoi("")
		if len(words) == 2 {
			n, err = strconv.Atoi(words[1])
		}
		if err != nil || n < 0 {
			fmt.Fprintf(out, "? \"%s\": want frames N\n", line)
			return
		}
		for range n {
			w.b.advance()
			out.WriteString(w.b.out.String())
			w.b.out.Reset()
			w.refresh()
			frame()
		}
	case "tap":
		label := strings.TrimPrefix(line, "tap ")
		var i int
		switch {
		case scan(label, "toggle %d", &i) && i >= 0 && i < len(w.b.panels):
			w.toggle(i)
		case scan(label, "policy %d", &i) && i >= 0 && i < len(w.b.panels):
			w.b.newest[i] = !w.b.newest[i]
			w.refresh()
		default:
			fmt.Fprintf(out, "? no node %s\n", label)
		}
	case "show":
		for _, l := range w.lines {
			out.WriteString(l + "\n")
		}
	default:
		fmt.Fprintf(out, "? \"%s\": want frames, tap, show or shot\n", line)
	}
}

// scan is whether s is exactly format with its one number.
func scan(s, format string, n *int) bool {
	if _, err := fmt.Sscanf(s, format, n); err != nil {
		return false
	}
	return fmt.Sprintf(format, *n) == s
}

const usage = "usage: dash-gui [-headless [-frames N]] [-seed S]"

func main() {
	headless, seed, frames := false, 1, 0
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-headless":
			headless = true
		case (args[i] == "-seed" || args[i] == "-frames") && i+1 < len(args):
			n, err := strconv.Atoi(args[i+1])
			if err != nil || args[i] == "-frames" && n < 0 {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(2)
			}
			if args[i] == "-seed" {
				seed = n
			} else {
				frames = n
			}
			i++
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
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
	defer func() {
		for _, o := range b.orders {
			close(o)
		}
	}()
	w := &window{b: b}
	w.refresh()
	if !headless {
		mygo.App.WhenReady(func() {
			mygo.NewWindow(mygo.WindowOptions{Title: "Dashboard", Width: 900, Height: 1400, Content: ui.View(w.view)})
		})
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Headless: each line goes to the model, as the buttons and the
	// display's frames would send it, and ui.Tester lays the window out
	// and paints it at each frame and after each line: the frames a
	// person sees, which this control measures.
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	tt := ui.NewTester(w.view, 900, 1400)
	frame := func() { tt.Frame() }
	w.scene(fmt.Sprintf("frames %d", frames), out, frame)
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, line := range strings.Split(string(input), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") || line == "shot" {
			continue
		}
		w.scene(line, out, frame)
		tt.Frame()
	}
}
