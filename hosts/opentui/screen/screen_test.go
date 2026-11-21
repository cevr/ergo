//go:build (linux || darwin) && (amd64 || arm64)

package screen

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

var seed = flag.Int64("screen.seed", -1, "replay one seed of the screen properties")

func seeds(t *testing.T, n int) []int64 {
	if *seed >= 0 {
		return []int64{*seed}
	}
	s := make([]int64, n)
	for i := range s {
		s[i] = int64(i + 1)
	}
	return s
}

func headless(t *testing.T, cols, rows int) *Screen {
	t.Helper()
	s, err := Headless(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Quit(); err != nil {
			t.Error(err)
		}
	})
	return s
}

// model is the reference tree: a node's kind, text and children.
type model struct {
	kind     map[int]int
	text     map[int]string
	parent   map[int]int
	children map[int][]int
}

func (m *model) attached(n int) bool {
	for n != 0 {
		p, ok := m.parent[n]
		if !ok || p < 0 {
			return false
		}
		n = p
	}
	return true
}

// texts are the attached text nodes in tree order.
func (m *model) texts(n int, out []int) []int {
	for _, k := range m.children[n] {
		if m.kind[k] == Text {
			out = append(out, k)
		} else {
			out = m.texts(k, out)
		}
	}
	return out
}

func (m *model) unlink(n int) {
	if p := m.parent[n]; p >= 0 {
		m.children[p] = slices.DeleteFunc(m.children[p], func(k int) bool { return k == n })
	}
	m.parent[n] = -1
}

func (m *model) remove(n int) {
	for _, k := range m.children[n] {
		m.remove(k)
	}
	delete(m.children, n)
	delete(m.kind, n)
	delete(m.text, n)
	delete(m.parent, n)
}

// under reports whether a is b or under b.
func (m *model) under(a, b int) bool {
	for a > 0 {
		if a == b {
			return true
		}
		a = m.parent[a]
	}
	return a == b
}

// TestSceneShowsItsModel plays seeded batches of creates, moves (into
// boxes, at seeded indexes), destroys of subtrees and text sets against a
// reference tree. After each Present every attached text is in Rows() at
// its BoxOf, in tree order from the top, every other cell is blank, and
// HitTest at a text's first cell names it. Freed numbers are taken again,
// so a node left behind by a destroy is found. A failing seed prints its
// batch and -screen.seed=N.
// Mutants: a destroy leaves the subtree's children; move ignores the index.
func TestSceneShowsItsModel(t *testing.T) {
	const cols, rows = 30, 40
	for _, sd := range seeds(t, 12) {
		s := headless(t, cols, rows)
		r := rand.New(rand.NewPCG(uint64(sd), 7))
		m := &model{kind: map[int]int{}, text: map[int]string{}, parent: map[int]int{}, children: map[int][]int{}}
		free := func() int {
			for n := 1; ; n++ {
				if _, ok := m.kind[n]; !ok {
					return n
				}
			}
		}
		var log []string
		for step := range 30 {
			for range 1 + r.IntN(6) {
				nodes := slices.Sorted(func(yield func(int) bool) {
					for n := range m.kind {
						if !yield(n) {
							return
						}
					}
				})
				boxes := []int{0}
				for _, n := range nodes {
					if m.kind[n] == Box && m.attached(n) {
						boxes = append(boxes, n)
					}
				}
				switch op := r.IntN(10); {
				case op < 4 && len(m.texts(0, nil)) < rows-2:
					n, kind := free(), r.IntN(2)
					p := boxes[r.IntN(len(boxes))]
					i := r.IntN(len(m.children[p]) + 1)
					m.kind[n], m.parent[n] = kind, -1
					s.Create(n, kind)
					if kind == Text {
						m.text[n] = fmt.Sprintf("t%d.%d", n, step)
						s.SetText(n, m.text[n])
					}
					s.Move(n, p, i)
					m.parent[n] = p
					m.children[p] = slices.Insert(m.children[p], i, n)
					log = append(log, fmt.Sprintf("create %d kind %d at %d[%d]", n, kind, p, i))
				case op < 6 && len(nodes) > 0:
					n := nodes[r.IntN(len(nodes))]
					p := boxes[r.IntN(len(boxes))]
					if m.under(p, n) {
						continue
					}
					m.unlink(n)
					i := r.IntN(len(m.children[p]) + 1)
					s.Move(n, p, i)
					m.parent[n] = p
					m.children[p] = slices.Insert(m.children[p], i, n)
					log = append(log, fmt.Sprintf("move %d to %d[%d]", n, p, i))
				case op < 8 && len(nodes) > 0:
					n := nodes[r.IntN(len(nodes))]
					m.unlink(n)
					m.remove(n)
					s.Destroy(n)
					log = append(log, fmt.Sprintf("destroy %d", n))
				case len(nodes) > 0:
					n := nodes[r.IntN(len(nodes))]
					if m.kind[n] != Text {
						continue
					}
					m.text[n] = fmt.Sprintf("x%d.%d", n, step)
					s.SetText(n, m.text[n])
					log = append(log, fmt.Sprintf("text %d %q", n, m.text[n]))
				}
			}
			if err := s.Present(); err != nil {
				t.Fatalf("seed %d step %d: Present: %v\n%s\nreplay: -screen.seed=%d", sd, step, err, strings.Join(log, "\n"), sd)
			}
			if msg := checkScene(s, m, cols, rows); msg != "" {
				t.Fatalf("seed %d step %d: %s\nbatches:\n%s\nreplay: go test -run TestSceneShowsItsModel -screen.seed=%d", sd, step, msg, strings.Join(log, "\n"), sd)
			}
		}
	}
}

func checkScene(s *Screen, m *model, cols, rows int) string {
	got := s.Rows()
	want := make([][]rune, rows)
	for y := range want {
		want[y] = []rune(strings.Repeat(" ", cols))
	}
	last := -1
	for _, n := range m.texts(0, nil) {
		b := s.BoxOf(n)
		if b.Y <= last {
			return fmt.Sprintf("text %d (%q) is at row %d, not below the text before it (row %d)", n, m.text[n], b.Y, last)
		}
		last = b.Y
		if b.Y >= rows {
			return fmt.Sprintf("text %d is below the screen (%v)", n, b)
		}
		copy(want[b.Y][b.X:], []rune(m.text[n]))
		if h := s.HitTest(b.X, b.Y); h != n {
			return fmt.Sprintf("HitTest at text %d's first cell (%d, %d) is %d", n, b.X, b.Y, h)
		}
	}
	for y := range rows {
		if y >= len(got) || got[y] != string(want[y]) {
			var g, w strings.Builder
			for i := range rows {
				if i < len(got) {
					fmt.Fprintf(&g, "|%s|\n", got[i])
				}
				fmt.Fprintf(&w, "|%s|\n", string(want[i]))
			}
			return fmt.Sprintf("row %d differs from the model\ngot:\n%swant:\n%s", y, g.String(), w.String())
		}
	}
	return ""
}

// event is a key or mouse event and its terminal bytes in one of the
// forms terminals send.
type event struct {
	kind, action, code, mods, x, y int
	bytes                          string
}

func genEvent(r *rand.Rand) event {
	switch r.IntN(6) {
	case 0: // a printable letter or digit, legacy
		c := "abcdefghijklmnopqrstuvwxyz0123456789"[r.IntN(36)]
		return event{kind: Key, action: 1, code: int(c), bytes: string(c)}
	case 1: // Ctrl+letter, legacy (not i, j, m, h: Tab, LF, Enter, Backspace)
		c := "abcdefgklnopqrstuvwxyz"[r.IntN(22)]
		return event{kind: Key, action: 1, code: int(c), mods: 4, bytes: string(c - 'a' + 1)}
	case 2: // an arrow, Home or End, legacy CSI
		k := r.IntN(6)
		codes := []int{57352, 57353, 57351, 57350, 57356, 57357}
		return event{kind: Key, action: 1, code: codes[k], bytes: "\x1b[" + string("ABCDHF"[k])}
	case 3: // CSI u with modifiers (Kitty)
		c := "abcxyz"[r.IntN(6)]
		mods := []int{1, 2, 4, 5}[r.IntN(4)]
		return event{kind: Key, action: 1, code: int(c), mods: mods, bytes: fmt.Sprintf("\x1b[%d;%du", c, mods+1)}
	case 4: // an SGR mouse press or release
		x, y := r.IntN(80), r.IntN(24)
		if r.IntN(2) == 0 {
			return event{kind: Mouse, action: Down, code: 0, x: x, y: y, bytes: fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)}
		}
		return event{kind: Mouse, action: Up, code: 0, x: x, y: y, bytes: fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1)}
	default: // Enter, Tab, Backspace
		k := r.IntN(3)
		return event{kind: Key, action: 1, code: []int{13, 9, 127}[k], bytes: []string{"\r", "\t", "\x7f"}[k]}
	}
}

// TestInputRoundTrips encodes seeded keys and mouse events as terminal
// bytes (legacy, CSI u and SGR mouse forms), feeds them in chunks cut at
// seeded places (inside escape sequences too), and reads back exactly the
// events. Mutant: a chunk boundary drops a partial escape.
func TestInputRoundTrips(t *testing.T) {
	s := headless(t, 80, 24)
	for _, sd := range seeds(t, 40) {
		r := rand.New(rand.NewPCG(uint64(sd), 11))
		var evs []event
		var all []byte
		for range 1 + r.IntN(30) {
			e := genEvent(r)
			evs = append(evs, e)
			all = append(all, e.bytes...)
		}
		var cuts []int
		for b := all; len(b) > 0; {
			n := 1 + r.IntN(min(len(b), 7))
			cuts = append(cuts, n)
			s.Feed(string(b[:n]))
			b = b[n:]
		}
		for i, e := range evs {
			in, ok := s.Poll()
			want := Input{Kind: e.kind, Action: e.action, Code: e.code, Mods: e.mods, X: e.x, Y: e.y}
			got := in
			got.BaseCode, got.Text = 0, ""
			if !ok || got != want {
				t.Fatalf("seed %d: event %d: %+v (%v), want %+v from %q\nbytes %q cut %v\nreplay: -screen.seed=%d", sd, i, in, ok, want, e.bytes, all, cuts, sd)
			}
		}
		if in, ok := s.Poll(); ok {
			t.Fatalf("seed %d: an extra input %+v\nbytes %q cut %v", sd, in, all, cuts)
		}
	}
}

// TestResizeRelaysOut: after Resize, a full-width box's BoxOf has the new
// width, Rows() the new size, and a Resize Input follows.
// Mutant: resize not passed to the session.
func TestResizeRelaysOut(t *testing.T) {
	s := headless(t, 20, 5)
	s.Create(1, Box)
	s.Move(1, 0, 0)
	s.Create(2, Text)
	s.SetText(2, "edge")
	s.Move(2, 1, 0)
	if err := s.Present(); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{33, 9}, {12, 3}, {40, 12}} {
		if err := s.Resize(size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		if err := s.Present(); err != nil {
			t.Fatal(err)
		}
		if b := s.BoxOf(1); b.Width != size[0] {
			t.Errorf("after Resize(%d, %d) the full-width box is %+v", size[0], size[1], b)
		}
		rows := s.Rows()
		if len(rows) != size[1] || len([]rune(rows[0])) != size[0] {
			t.Errorf("after Resize(%d, %d) Rows() is %d rows of %d", size[0], size[1], len(rows), len([]rune(rows[0])))
		}
		in, ok := s.Poll()
		if !ok || in.Kind != Resize || in.Width != size[0] || in.Height != size[1] {
			t.Errorf("after Resize(%d, %d): input %+v %v", size[0], size[1], in, ok)
		}
	}
}

// TestOwnerThreadOnly: scene commands, Present and reads from eight
// goroutines at once all succeed (none returns OT_WRONG_THREAD).
// Mutant: a call made off the owner thread.
func TestOwnerThreadOnly(t *testing.T) {
	s := headless(t, 40, 20)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := range 8 {
		wg.Go(func() {
			for i := range 10 {
				n := 1 + g*10 + i
				s.Create(n, Text)
				s.SetText(n, fmt.Sprint(n))
				if err := s.Present(); err != nil {
					errs <- err
					return
				}
				_ = s.Rows()
				_ = s.HitTest(0, 0)
				s.Destroy(n)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := s.Present(); err != nil {
		t.Error(err)
	}
}

// TestClosedRestoresTheTerminal opens a terminal screen on a pipe pair (no
// TTY): the bytes written end with the sequences that leave the alternate
// screen and show the cursor, also when the caller panics between Open
// and Close. Mutant: close skips the output drain.
func TestClosedRestoresTheTerminal(t *testing.T) {
	for _, panics := range []bool{false, true} {
		inR, inW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		read := make(chan struct{})
		go func() {
			io.Copy(&out, outR)
			close(read)
		}()
		func() {
			defer func() {
				if v := recover(); v != nil && v != "boom" {
					panic(v)
				}
			}()
			s, err := OpenOn(inR, outW, 40, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Quit()
			s.Create(1, Text)
			s.SetText(1, "hello")
			s.Move(1, 0, 0)
			if err := s.Present(); err != nil {
				t.Fatal(err)
			}
			if panics {
				panic("boom")
			}
		}()
		outW.Close()
		inW.Close()
		<-read
		inR.Close()
		b := out.Bytes()
		if !bytes.Contains(b, []byte("hello")) {
			t.Errorf("panics %v: the frame was not written: %q", panics, b)
		}
		leave, show := bytes.LastIndex(b, []byte("\x1b[?1049l")), bytes.LastIndex(b, []byte("\x1b[?25h"))
		enter := bytes.LastIndex(b, []byte("\x1b[?1049h"))
		if leave < 0 || show < 0 || leave < enter || bytes.LastIndex(b, []byte("hello")) > leave {
			t.Errorf("panics %v: the output does not end by leaving the alternate screen and showing the cursor: %q", panics, b[max(0, len(b)-200):])
		}
	}
}
