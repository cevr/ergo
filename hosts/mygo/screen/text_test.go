package screen

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/egoist/mygo/drawing/platform"
)

var textSeed = flag.Int64("text.seed", -1, "run TestRangeEditsAgree on this seed only")

// imeModel is the reference a field's document must agree with: its
// text as runes, the selection and the marked range in runes.
type imeModel struct {
	text       []rune
	selA, selB int
	marked     bool
	markA      int
	markB      int
}

func (m *imeModel) committed() string {
	if !m.marked {
		return string(m.text)
	}
	return string(m.text[:m.markA]) + string(m.text[m.markB:])
}

// target is the rune range an edit with no range takes.
func (m *imeModel) target() (int, int) {
	if m.marked {
		return m.markA, m.markB
	}
	return m.selA, m.selB
}

func (m *imeModel) splice(a, b int, s []rune) {
	out := append([]rune{}, m.text[:a]...)
	out = append(out, s...)
	m.text = append(out, m.text[b:]...)
}

// u16 is the UTF-16 offset of rune offset i of rs.
func u16(rs []rune, i int) int { return len(utf16.Encode(rs[:i])) }

// pieces are what the input methods type: one-, two-, three- and
// four-byte runes, the last a surrogate pair in UTF-16.
var pieces = []string{"a", "bc", "é", "日本", "😀", "x😀é", " "}

// TestRangeEditsAgree sends generated input-method edits (insert,
// replace a UTF-16 range, compose and commit or cancel, delete backward
// across a surrogate pair) to a Headless window's text input client, the
// one the surface holds, and checks after each that its text, selection
// and marked range are the reference model's, that a committed edit
// queues one Edited input with the committed text and a composition
// none, and that a program which takes the Edited inputs late, painting
// its value between them, ends with the model's committed text (fast
// typing while a pass runs), and that a value the program rewrote
// replaces the field's text.
func TestRangeEditsAgree(t *testing.T) {
	seeds := []int64{}
	if *textSeed >= 0 {
		seeds = append(seeds, *textSeed)
	} else {
		for s := int64(0); s < 300; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		if log, err := playIME(seed); err != nil {
			t.Fatalf("seed %d: %v\nedits:\n%s\nreplay: go test -run TestRangeEditsAgree -text.seed=%d ./screen", seed, err, strings.Join(log, "\n"), seed)
		}
	}
}

func playIME(seed int64) ([]string, error) {
	r := rand.New(rand.NewPCG(uint64(seed), 7))
	w, err := Headless(200, 100)
	if err != nil {
		return nil, err
	}
	start := pieces[r.IntN(len(pieces))]
	m := &imeModel{text: []rune(start)}
	m.selA, m.selB = len(m.text), len(m.text)
	value := start
	known := start
	var queued []Input
	taken := 0
	paint := func() {
		w.Begin()
		w.Editing(value, "text", taken, 0, 0, 1, 10)
		w.Present()
	}
	paint()
	c := w.fake.TextInputState().Client
	if c == nil {
		return nil, fmt.Errorf("the surface holds no text input client")
	}
	log := []string{fmt.Sprintf("start %q", start)}
	for step := 0; step < 40; step++ {
		var commits bool
		switch k := r.IntN(9); {
		case k <= 1:
			s := pieces[r.IntN(len(pieces))]
			log = append(log, fmt.Sprintf("insert %q", s))
			c.ReplaceText(nil, s)
			a, b := m.target()
			m.splice(a, b, []rune(s))
			m.selA, m.selB, m.marked = a+len([]rune(s)), a+len([]rune(s)), false
			commits = true
		case k == 2:
			a := r.IntN(len(m.text) + 1)
			b := a + r.IntN(len(m.text)-a+1)
			s := pieces[r.IntN(len(pieces))]
			rg := platform.TextRange{Start: u16(m.text, a), End: u16(m.text, b)}
			log = append(log, fmt.Sprintf("replace %d..%d %q", rg.Start, rg.End, s))
			c.ReplaceText(&rg, s)
			m.splice(a, b, []rune(s))
			m.selA, m.selB, m.marked = a+len([]rune(s)), a+len([]rune(s)), false
			commits = true
		case k <= 4:
			s := []rune(pieces[r.IntN(len(pieces))])
			caret := r.IntN(len(s) + 1)
			a, b := m.target()
			var rg *platform.TextRange
			if !m.marked && r.IntN(3) == 0 {
				a = r.IntN(len(m.text) + 1)
				b = a + r.IntN(len(m.text)-a+1)
				rg = &platform.TextRange{Start: u16(m.text, a), End: u16(m.text, b)}
			}
			cu := u16(s, caret)
			log = append(log, fmt.Sprintf("compose %q caret %d over %v", string(s), cu, rg))
			c.SetMarkedText(rg, string(s), platform.TextRange{Start: cu, End: cu})
			m.splice(a, b, s)
			m.selA, m.selB = a+caret, a+caret
			m.marked, m.markA, m.markB = len(s) > 0, a, a+len(s)
			// An empty composition ends one: what it composed over goes.
			commits = !m.marked && m.committed() != known
		case k == 5:
			log = append(log, "commit")
			c.UnmarkText()
			commits = m.marked
			m.marked = false
		case k == 6:
			// Delete backward, as Android's deleteSurroundingText: the
			// selection, else the rune before the caret.
			if m.marked {
				log = append(log, "cancel")
				c.SetMarkedText(nil, "", platform.TextRange{})
				c.UnmarkText()
				m.splice(m.markA, m.markB, nil)
				m.selA, m.selB, m.marked = m.markA, m.markA, false
				commits = m.committed() != known
				break
			}
			a, b := m.selA, m.selB
			if a == b {
				if a == 0 {
					continue
				}
				a = b - 1
			}
			rg := platform.TextRange{Start: u16(m.text, a), End: u16(m.text, b)}
			log = append(log, fmt.Sprintf("delete %d..%d", rg.Start, rg.End))
			c.ReplaceText(&rg, "")
			m.splice(a, b, nil)
			m.selA, m.selB = a, a
			commits = true
		case k == 8:
			// The program takes every Edited input and rewrites its value
			// (clears it, or refuses the last edit): the field shows that,
			// the caret at its end, and any composition ends.
			if len(queued) > 0 {
				taken, queued = queued[len(queued)-1].Edit, nil
			}
			value = pieces[r.IntN(len(pieces))]
			if r.IntN(2) == 0 {
				value = ""
			}
			log = append(log, fmt.Sprintf("rewrite %q", value))
			paint()
			if value != known {
				m.text, m.marked = []rune(value), false
				m.selA, m.selB = len(m.text), len(m.text)
				known = value
			}
		default:
			// The program takes some of the Edited inputs and paints.
			n := r.IntN(len(queued) + 1)
			if n > 0 {
				value, taken, queued = queued[n-1].Text, queued[n-1].Edit, queued[n:]
			}
			log = append(log, fmt.Sprintf("paint %q", value))
			paint()
		}
		got := 0
		for in, ok := w.Poll(); ok; in, ok = w.Poll() {
			if in.Kind == Edited {
				got++
				queued = append(queued, in)
			}
		}
		if want := map[bool]int{true: 1, false: 0}[commits]; got != want {
			return log, fmt.Errorf("%d Edited inputs, want %d", got, want)
		}
		if commits && queued[len(queued)-1].Text != m.committed() {
			return log, fmt.Errorf("Edited %q, want %q", queued[len(queued)-1].Text, m.committed())
		}
		if commits {
			known = m.committed()
		}
		text, _ := c.TextForRange(platform.TextRange{Start: 0, End: 1 << 30})
		if text != string(m.text) {
			return log, fmt.Errorf("text %q, want %q", text, string(m.text))
		}
		sel := c.Selection().Range
		if want := (platform.TextRange{Start: u16(m.text, m.selA), End: u16(m.text, m.selB)}); sel != want {
			return log, fmt.Errorf("selection %v, want %v", sel, want)
		}
		mark, marked := c.MarkedRange()
		if marked != m.marked || marked && mark != (platform.TextRange{Start: u16(m.text, m.markA), End: u16(m.text, m.markB)}) {
			return log, fmt.Errorf("marked %v %v, want %v %d..%d", marked, mark, m.marked, m.markA, m.markB)
		}
	}
	// The input method commits what it composes; the program takes the
	// rest of the Edited inputs.
	log = append(log, "commit")
	c.UnmarkText()
	m.marked = false
	for in, ok := w.Poll(); ok; in, ok = w.Poll() {
		if in.Kind == Edited {
			queued = append(queued, in)
		}
	}
	if len(queued) > 0 {
		value, taken = queued[len(queued)-1].Text, queued[len(queued)-1].Edit
	}
	paint()
	if value != m.committed() {
		return log, fmt.Errorf("the program's value is %q, want %q", value, m.committed())
	}
	if text, _ := c.TextForRange(platform.TextRange{Start: 0, End: 1 << 30}); text != string(m.text) {
		return log, fmt.Errorf("after the last paint the text is %q, want %q", text, string(m.text))
	}
	return log, nil
}
