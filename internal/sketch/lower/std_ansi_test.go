package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdAnsiLawsProbe restates the top-level laws of std ansi as Ergo
// property tests (UC2), with the package's law helpers (drawStyle,
// drawColor) written out again.
const stdAnsiLawsProbe = `import ansi
import strings

func lawStripUndoesPaint(seed int, text string) bool {
	let t = ansi.Strip(text)
	let s = drawStyle(seed)
	let painted = with context { ansi.Colors: ansi.Always } { ansi.Paint(s, t) }
	ansi.Strip(painted) == t && ansi.Strip(t) == t
}

func lawNeverPaints(seed int, text string) bool {
	let s = drawStyle(seed)
	with context { ansi.Colors: ansi.Never } {
		let mut b = strings.NewBuilder()
		ansi.WritePaint(&b, s, text)
		ansi.Paint(s, text) == text && b.String() == text
	}
}

func lawPaintIsSGRAndReset(seed int, text string) bool {
	let s = drawStyle(seed)
	with context { ansi.Colors: ansi.Always } {
		let mut b = strings.NewBuilder()
		ansi.WritePaint(&b, s, text)
		let want = if s == ansi.Plain { text } else { ansi.SGR(s) + text + ansi.Reset }
		ansi.Paint(s, text) == want && b.String() == want
	}
}

func lawSGRIsTheTable() bool {
	let p = ansi.Plain
		ansi.SGR(p) == "" &&
		ansi.SGR(ansi.Style{..p, Fg: ansi.Red}) == "\x1b[31m" &&
		ansi.SGR(ansi.Style{..p, Fg: ansi.Basic{N: 9}}) == "\x1b[91m" &&
		ansi.SGR(ansi.Style{..p, Bg: ansi.Blue}) == "\x1b[44m" &&
		ansi.SGR(ansi.Style{..p, Bg: ansi.Basic{N: 15}}) == "\x1b[107m" &&
		ansi.SGR(ansi.Style{..p, Fg: ansi.Indexed{N: 208}}) == "\x1b[38;5;208m" &&
		ansi.SGR(ansi.Style{..p, Bg: ansi.Indexed{N: 300}}) == "\x1b[48;5;255m" &&
		ansi.SGR(ansi.Style{..p, Fg: ansi.RGB{R: 255, G: 128, B: -4}}) == "\x1b[38;2;255;128;0m" &&
		ansi.SGR(ansi.Style{..p, Bold: true}) == "\x1b[1m" &&
		ansi.SGR(ansi.Style{..p, Bold: true, Underline: true, Fg: ansi.Green, Bg: ansi.Black}) == "\x1b[1;4;32;40m" &&
		ansi.SGR(ansi.Style{..p, Dim: true, Italic: true, Reverse: true, Strike: true}) == "\x1b[2;3;7;9m"
}

func lawStripIsTheTable() bool {
		ansi.Strip("a\x1b[1;31mb\x1b[0mc") == "abc" &&
		ansi.Strip("\x1b[2J\x1b[Hx\x1b[10;4H") == "x" &&
		ansi.Strip("\x1b[?25lhi\x1b[?1049h") == "hi" &&
		ansi.Strip("\x1b]0;title\x07t") == "t" &&
		ansi.Strip("\x1b]8;;https://e.org\x1b\\link\x1b]8;;\x1b\\") == "link" &&
		ansi.Strip("\x1b7a\x1b8") == "a" &&
		ansi.Strip("a\x1b[2@b") == "ab" &&
		ansi.Strip("ok\x1b[38;5") == "ok" &&
		ansi.Strip("界\x1b[4mé") == "界é" &&
		ansi.Strip("plain") == "plain"
}

func lawDetectIsTheEnvironment(noColor int, term int, terminal bool) bool {
	let nc = []Option[string]{None, Some[string]{Value: ""}, Some[string]{Value: "1"}}[(noColor%3+3)%3]
	let tm = []Option[string]{None, Some[string]{Value: "xterm-256color"}, Some[string]{Value: "dumb"}}[(term%3+3)%3]
	let off = nc == (Some[string]{Value: "1"}) || tm == (Some[string]{Value: "dumb"}) || !terminal
	ansi.Decide(nc, tm, terminal).On() == !off
}

func drawStyle(seed int) ansi.Style {
	let mut x = (seed%2147483648 + 2147483648) % 2147483648
	let mut bits = []int{}
	for _ in 0..16 {
		x = (x*1103515245 + 12345) % 2147483648
		bits = bits.Append(x / 65536)
	}
	ansi.Style{
		Fg:        drawColor(bits[0], bits[1]),
		Bg:        drawColor(bits[2], bits[3]),
		Bold:      bits[4]%2 == 0,
		Dim:       bits[5]%2 == 0,
		Italic:    bits[6]%2 == 0,
		Underline: bits[7]%2 == 0,
		Reverse:   bits[8]%2 == 0,
		Strike:    bits[9]%2 == 0,
	}
}

func drawColor(k int, n int) ansi.Color {
	let v = n%320 - 20
	match k % 4 {
	case 0:
		ansi.DefaultColor
	case 1:
		ansi.Basic{N: v % 20}
	case 2:
		ansi.Indexed{N: v}
	case _:
		ansi.RGB{R: v, G: 255 - v, B: v / 2}
	}
}

test AnsiStripUndoesPaint(seed int, text string) {
	assert lawStripUndoesPaint(seed, text)
}

test AnsiNeverPaints(seed int, text string) {
	assert lawNeverPaints(seed, text)
}

test AnsiPaintIsSGRAndReset(seed int, text string) {
	assert lawPaintIsSGRAndReset(seed, text)
}

test AnsiSGRIsTheTable() {
	assert lawSGRIsTheTable()
}

test AnsiStripIsTheTable() {
	assert lawStripIsTheTable()
}

test AnsiDetectIsTheEnvironment(noColor int, term int, terminal bool) {
	assert lawDetectIsTheEnvironment(noColor, term, terminal)
}

test AnsiColorsOnIsStable(pick int) {
	let on = with context { ansi.Colors: ansi.Always } { ansi.Colors.On() == ansi.Colors.On() }
	let off = with context { ansi.Colors: ansi.Never } { ansi.Colors.On() == ansi.Colors.On() }
	let d = ansi.Decide(None, None, pick%2 == 0)
	let detected = with context { ansi.Colors: d } { ansi.Colors.On() == ansi.Colors.On() }
	assert on && off && detected
}
`

// stdAnsiLawStatements is the statement of each law the probe restates, by
// ledger key; lawledger.Run holds it equal to the ledger.
var stdAnsiLawStatements = map[string]string{
	"ansi StripUndoesPaint":       `StripUndoesPaint(seed int, text string) { let t = Strip(text) let s = drawStyle(seed) let painted = with context { Colors: Always } { Paint(s, t) } Strip(painted) == t && Strip(t) == t }`,
	"ansi NeverPaints":            `NeverPaints(seed int, text string) { let s = drawStyle(seed) with context { Colors: Never } { let mut b = strings.NewBuilder() WritePaint(&b, s, text) Paint(s, text) == text && b.String() == text } }`,
	"ansi PaintIsSGRAndReset":     `PaintIsSGRAndReset(seed int, text string) { let s = drawStyle(seed) with context { Colors: Always } { let mut b = strings.NewBuilder() WritePaint(&b, s, text) let want = if s == Plain { text } else { SGR(s) + text + Reset } Paint(s, text) == want && b.String() == want } }`,
	"ansi SGRIsTheTable":          `SGRIsTheTable() { let p = Plain SGR(p) == "" && SGR(Style{..p, Fg: Red}) == "\x1b[31m" && SGR(Style{..p, Fg: Basic{N: 9}}) == "\x1b[91m" && SGR(Style{..p, Bg: Blue}) == "\x1b[44m" && SGR(Style{..p, Bg: Basic{N: 15}}) == "\x1b[107m" && SGR(Style{..p, Fg: Indexed{N: 208}}) == "\x1b[38;5;208m" && SGR(Style{..p, Bg: Indexed{N: 300}}) == "\x1b[48;5;255m" && SGR(Style{..p, Fg: RGB{R: 255, G: 128, B: -4}}) == "\x1b[38;2;255;128;0m" && SGR(Style{..p, Bold: true}) == "\x1b[1m" && SGR(Style{..p, Bold: true, Underline: true, Fg: Green, Bg: Black}) == "\x1b[1;4;32;40m" && SGR(Style{..p, Dim: true, Italic: true, Reverse: true, Strike: true}) == "\x1b[2;3;7;9m" }`,
	"ansi StripIsTheTable":        `StripIsTheTable() { Strip("a\x1b[1;31mb\x1b[0mc") == "abc" && Strip("\x1b[2J\x1b[Hx\x1b[10;4H") == "x" && Strip("\x1b[?25lhi\x1b[?1049h") == "hi" && Strip("\x1b]0;title\x07t") == "t" && Strip("\x1b]8;;https://e.org\x1b\\link\x1b]8;;\x1b\\") == "link" && Strip("\x1b7a\x1b8") == "a" && Strip("a\x1b[2@b") == "ab" && Strip("ok\x1b[38;5") == "ok" && Strip("界\x1b[4mé") == "界é" && Strip("plain") == "plain" }`,
	"ansi DetectIsTheEnvironment": `DetectIsTheEnvironment(noColor int, term int, terminal bool) { let nc = []Option[string]{None, Some[string]{Value: ""}, Some[string]{Value: "1"}}[(noColor%3+3)%3] let tm = []Option[string]{None, Some[string]{Value: "xterm-256color"}, Some[string]{Value: "dumb"}}[(term%3+3)%3] let off = nc == (Some[string]{Value: "1"}) || tm == (Some[string]{Value: "dumb"}) || !terminal Decide(nc, tm, terminal).On() == !off }`,
	"ansi Colors.OnIsStable":      `OnIsStable() { On() == On() }`,
}

// TestStdAnsiLawsRun runs the ansi probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdAnsiLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdansi", stdAnsiLawsProbe, stdAnsiLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
