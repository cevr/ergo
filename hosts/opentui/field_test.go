package opentui

import (
	"encoding/base64"
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

var fieldSeed = flag.Uint64("field.seed", 0, "run TestFieldFollowsTheLineEditor on this seed only")

// fieldReplacements are cmd/fieldplay's Replacements: F1 to F8 set the
// field to one of them.
var fieldReplacements = []string{"", "a", "hello world", "日本 語", "é e\u0301 x", "foo-bar  baz", "  lead", "end  "}

// lineEditor is the reference: the field's text and its caret, a byte
// index at a grapheme's start. A grapheme is a rune and the combining
// marks after it; a Han, Hiragana, Katakana or Hangul rune is two cells
// wide, any other one.
type lineEditor struct {
	text  string
	caret int
}

// graphemes are the byte offsets where text's graphemes start, and its
// length.
func graphemes(text string) []int {
	var at []int
	for i, r := range text {
		if i == 0 || !unicode.Is(unicode.Mn, r) {
			at = append(at, i)
		}
	}
	return append(at, len(text))
}

func cells(text string) int {
	n := 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			n += 2
		default:
			n++
		}
	}
	return n
}

// prev and next are the grapheme starts before and after the caret.
func (e *lineEditor) prev() int {
	p := 0
	for _, g := range graphemes(e.text) {
		if g >= e.caret {
			break
		}
		p = g
	}
	return p
}

func (e *lineEditor) next() int {
	for _, g := range graphemes(e.text) {
		if g > e.caret {
			return g
		}
	}
	return len(e.text)
}

// atCell is the grapheme start at or before cell col, the end past it.
func (e *lineEditor) atCell(col int) int {
	at := 0
	for _, g := range graphemes(e.text) {
		if cells(e.text[:g]) > col {
			break
		}
		at = g
	}
	return at
}

// wordFrom is where a word motion from byte i goes: over spaces, then
// over what is not a space.
func wordFrom(text string, i int, back bool) int {
	space := unicode.IsSpace
	if back {
		for i > 0 {
			r, n := utf8.DecodeLastRuneInString(text[:i])
			if !space(r) {
				break
			}
			i -= n
		}
		for i > 0 {
			r, n := utf8.DecodeLastRuneInString(text[:i])
			if space(r) {
				break
			}
			i -= n
		}
		return i
	}
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if !space(r) {
			break
		}
		i += n
	}
	for i < len(text) {
		r, n := utf8.DecodeRuneInString(text[i:])
		if space(r) {
			break
		}
		i += n
	}
	return i
}

func (e *lineEditor) insert(s string) {
	e.text = e.text[:e.caret] + s + e.text[e.caret:]
	e.caret += len(s)
}

func (e *lineEditor) cut(from, to int) {
	e.text = e.text[:from] + e.text[to:]
	e.caret = from
}

// fieldStep is one step of a script: the bytes the terminal sends, a name
// for a failing seed's listing, and what it does to the reference.
type fieldStep struct {
	bytes, name string
	apply       func(e *lineEditor)
}

// typables are what a step types: ASCII, a space, punctuation, a
// precomposed é, an e and a combining acute, wide runes.
var typables = []string{"a", "b", "x", " ", "-", ".", "é", "e\u0301", "日", "本"}

// genField is a seeded script: Tab (the field takes the focus, its caret
// at the end), then 10 to 49 steps of typing, editing keys, pastes, clicks
// and program Sets.
func genField(r *rand.Rand) []fieldStep {
	steps := []fieldStep{{"\t", "Tab", func(e *lineEditor) { e.caret = len(e.text) }}}
	key := func(b, name string, f func(e *lineEditor)) fieldStep { return fieldStep{b, name, f} }
	ed := &lineEditor{}
	for range 10 + r.IntN(40) {
		var s fieldStep
		switch k := r.IntN(17); {
		case k < 4 && cells(ed.text) < 28:
			t := typables[r.IntN(len(typables))]
			s = key(t, "type "+strconv.Quote(t), func(e *lineEditor) { e.insert(t) })
		case k == 4:
			s = key("\x7f", "Backspace", func(e *lineEditor) { e.cut(e.prev(), e.caret) })
		case k == 5:
			s = key("\x1b[3~", "Delete", func(e *lineEditor) { n := e.next(); e.text = e.text[:e.caret] + e.text[n:] })
		case k == 6:
			s = key("\x1b[D", "ArrowLeft", func(e *lineEditor) { e.caret = e.prev() })
		case k == 7:
			s = key("\x1b[C", "ArrowRight", func(e *lineEditor) { e.caret = e.next() })
		case k == 8:
			s = pick(r,
				key("\x1b[H", "Home", func(e *lineEditor) { e.caret = 0 }),
				key("\x01", "Ctrl+A", func(e *lineEditor) { e.caret = 0 }))
		case k == 9:
			s = pick(r,
				key("\x1b[F", "End", func(e *lineEditor) { e.caret = len(e.text) }),
				key("\x05", "Ctrl+E", func(e *lineEditor) { e.caret = len(e.text) }))
		case k == 10:
			s = key("\x17", "Ctrl+W", func(e *lineEditor) { e.cut(wordFrom(e.text, e.caret, true), e.caret) })
		case k == 11:
			s = pick(r,
				key("\x0b", "Ctrl+K", func(e *lineEditor) { e.text = e.text[:e.caret] }),
				key("\x15", "Ctrl+U", func(e *lineEditor) { e.cut(0, e.caret) }))
		case k == 12:
			s = pick(r,
				key("\x1b[1;3D", "Alt+ArrowLeft", func(e *lineEditor) { e.caret = wordFrom(e.text, e.caret, true) }),
				key("\x1b[1;3C", "Alt+ArrowRight", func(e *lineEditor) { e.caret = wordFrom(e.text, e.caret, false) }))
		case k == 13 && cells(ed.text) < 20:
			p := pick(r, "p q", "one\ntwo", "\n日\n", "a\n")
			s = key("\x1b[200~"+p+"\x1b[201~", "paste "+strconv.Quote(p), func(e *lineEditor) { e.insert(strings.ReplaceAll(p, "\n", " ")) })
		case k == 14:
			col := r.IntN(34)
			s = key(fmt.Sprintf("\x1b[<0;%d;1M\x1b[<0;%d;1m", col+1, col+1), fmt.Sprintf("click %d", col), func(e *lineEditor) { e.caret = e.atCell(col) })
		case k == 15:
			i := r.IntN(len(fieldReplacements))
			fkeys := []string{"\x1bOP", "\x1bOQ", "\x1bOR", "\x1bOS", "\x1b[15~", "\x1b[17~", "\x1b[18~", "\x1b[19~"}
			s = key(fkeys[i], fmt.Sprintf("F%d (Set %q)", i+1, fieldReplacements[i]), func(e *lineEditor) {
				// The program's text replaces the field's; the caret keeps
				// its cell where the new text reaches it.
				col := cells(e.text[:e.caret])
				e.text = fieldReplacements[i]
				e.caret = e.atCell(col)
			})
		default:
			s = key("\x1b[A", "ArrowUp (the view's)", func(e *lineEditor) {})
		}
		s.apply(ed)
		steps = append(steps, s)
	}
	return steps
}

// TestFieldFollowsTheLineEditor: a focused field (cmd/fieldplay, the
// terminal driver on a headless screen) follows a reference line editor
// over seeded scripts of typed runes (wide and combining ones too), the
// editing keys of routing rule 3, pastes, clicks and program Sets: after
// each step the program's text (the last ui.Type) is the reference's, the
// field's row shows it (the placeholder when it is empty), and the
// terminal's cursor is on the field's row at the caret's cell.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it. A failing seed
// prints the steps up to the first that differs and the replay command.
func TestFieldFollowsTheLineEditor(t *testing.T) {
	bin, err := build("fieldplay", "./cmd/fieldplay")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{}
	if *fieldSeed != 0 {
		seeds = append(seeds, *fieldSeed)
	} else {
		for s := uint64(1); s <= 30; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, total := 0, 0
	for _, seed := range seeds {
		steps := genField(rand.New(rand.NewPCG(seed, 0x6669656c64)))
		var in strings.Builder
		for _, s := range steps {
			in.WriteString("send " + base64.StdEncoding.EncodeToString([]byte(s.bytes)) + "\n")
		}
		out, err := play(bin, in.String())
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		ed := &lineEditor{}
		var why string
		for i, s := range steps {
			s.apply(ed)
			if i >= len(lines) {
				why = fmt.Sprintf("no output for step %d (%v)", i+1, err)
				break
			}
			if why = fieldDiffers(ed, lines[i]); why != "" {
				why = fmt.Sprintf("step %d (%s): %s", i+1, s.name, why)
				break
			}
			total++
		}
		if why == "" && err != nil {
			why = fmt.Sprintf("fieldplay: %v\n%s", err, out)
		}
		if why != "" {
			failed++
			if failed <= 3 {
				var names []string
				for _, s := range steps {
					names = append(names, s.name)
				}
				t.Errorf("seed %d: %s\nsteps: %s\nreplay: go test -run TestFieldFollowsTheLineEditor -field.seed=%d", seed, why, strings.Join(names, ", "), seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d steps follow the reference", len(seeds), total)
}

// fieldDiffers is why fieldplay's line differs from the reference, "" when
// it does not.
func fieldDiffers(e *lineEditor, line string) string {
	parts := strings.Split(line, "\t")
	if len(parts) != 4 {
		return "fieldplay printed " + strconv.Quote(line)
	}
	text, err1 := strconv.Unquote(parts[0])
	row, err2 := strconv.Unquote(parts[1])
	if err1 != nil || err2 != nil {
		return "fieldplay printed " + strconv.Quote(line)
	}
	var cx, cy, bx, by, bw, bh int
	var shown bool
	fmt.Sscanf(parts[2], "%d %d %t", &cx, &cy, &shown)
	fmt.Sscanf(parts[3], "%d %d %d %d", &bx, &by, &bw, &bh)
	if text != e.text {
		return fmt.Sprintf("the program's text is %q, the reference's %q", text, e.text)
	}
	want := e.text
	if want == "" {
		want = "type here"
	}
	if bx != 0 || bh != 1 {
		return fmt.Sprintf("the field's box is %d,%d %dx%d, not one row from column 0", bx, by, bw, bh)
	}
	if strings.TrimRight(row, " ") != strings.TrimRight(want, " ") {
		return fmt.Sprintf("the field's row shows %q, not %q", strings.TrimRight(row, " "), want)
	}
	if c := cells(e.text[:e.caret]); !shown || cx != bx+c || cy != by {
		return fmt.Sprintf("the cursor is at %d,%d (shown %t), not at the caret's cell %d,%d (text %q, caret after %q)", cx, cy, shown, bx+c, by, e.text, e.text[:e.caret])
	}
	return ""
}
