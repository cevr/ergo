package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdLayoutLawsProbe restates each law of std ui/layout (ledger runner
// TestStdLayoutLawsRun) as a property test named by layoutLawTestName,
// the member laws of Measure over Monospace, with the laws' helpers
// restated over the package's exported operations.
const stdLayoutLawsProbe = `import arena
import math
import strings
import ui/layout

let m = layout.Monospace

let inf = float32(math.Inf(1))

func fmin(a float32, b float32) float32 { if a < b { a } else { b } }

func fmax(a float32, b float32) float32 { if a > b { a } else { b } }

func near(a float32, b float32) bool { a - b <= 0.01 && b - a <= 0.01 }

func textStyle(text string, size float32) layout.Style {
	let mut s = layout.Default()
	s.Content = layout.Text{Value: text, Size: size}
	s
}

test LayoutMeasureMinFitsMax(text string, size int) {
	let w = m.Widths(text, float32(size%64 + 64))
	assert w.Min <= w.Max
}

test LayoutMeasureWiderIsNoTaller(text string, size int, a int, b int) {
	let s = float32(size%64 + 64)
	let narrow = float32(a%400 + 400)
	let wide = narrow + float32(b%400 + 400)
	assert m.Height(text, s, wide) <= m.Height(text, s, narrow)
}

test LayoutMeasureAtMaxOneLinePerParagraph(text string, size int) {
	let s = float32(size%64 + 64)
	let one = m.Height("x", s, inf)
	assert m.Height(text, s, m.Widths(text, s).Max) <= one * float32(len(strings.Split(text, "\n")))
}

func textTakesItsMeasure(word string, copies int, size int, width int) bool {
	let text = strings.Repeat(word+" ", (copies%12+12)%12+1)
	let s = float32(size%32 + 32)
	let w = float32(width%500 + 500)
	let mut t = layout.New()
	let root = layout.Add(&t, layout.Default())
	let stretched = layout.Add(&t, textStyle(text, s))
	let mut halfStyle = textStyle(text, s)
	halfStyle.Width = layout.Px{Value: w / 2}
	let half = layout.Add(&t, halfStyle)
	let mut rowStyle = layout.Default()
	rowStyle.Direction = layout.Row
	rowStyle.Items = layout.Start
	let row = layout.Add(&t, rowStyle)
	let fitted = layout.Add(&t, textStyle(text, s))
	let ws = m.Widths(text, s)
	let fit = fmax(ws.Min, fmin(ws.Max, w))
	with context { layout.Measure: m } {
		layout.Append(&t, root, stretched) catch {
		case arena.Stale:
			return false
		}
		layout.Append(&t, root, half) catch {
		case arena.Stale:
			return false
		}
		layout.Append(&t, root, row) catch {
		case arena.Stale:
			return false
		}
		layout.Append(&t, row, fitted) catch {
		case arena.Stale:
			return false
		}
		layout.Layout(&t, root, w, inf) catch {
		case arena.Stale:
			return false
		}
	}
	let a = layout.BoxOf(&t, stretched) catch {
	case arena.Stale:
		return false
	}
	let b = layout.BoxOf(&t, fitted) catch {
	case arena.Stale:
		return false
	}
	let c = layout.BoxOf(&t, half) catch {
	case arena.Stale:
		return false
	}
	a.W == w && a.H == m.Height(text, s, w) && c.W == w / 2 && c.H == m.Height(text, s, w / 2) && near(b.W, fit) && near(b.H, m.Height(text, s, b.W))
}

test LayoutTextTakesItsMeasure(word string, copies int, size int, width int) { assert textTakesItsMeasure(word, copies, size, width) }

func measuresOnceAtAWidth(text string, width int) bool {
	let w = float32(width%500 + 500)
	let mut t = layout.New()
	let root = layout.Add(&t, layout.Default())
	let a = layout.Add(&t, textStyle(text, 16))
	let b = layout.Add(&t, textStyle(text, 16))
	with context { layout.Measure: m } {
		layout.Append(&t, root, a) catch {
		case arena.Stale:
			return false
		}
		layout.Append(&t, root, b) catch {
		case arena.Stale:
			return false
		}
		layout.Layout(&t, root, w, inf) catch {
		case arena.Stale:
			return false
		}
		layout.SetStyle(&t, root, layout.Default()) catch {
		case arena.Stale:
			return false
		}
		layout.Layout(&t, root, w, inf) catch {
		case arena.Stale:
			return false
		}
	}
	layout.Measured(t) == 1
}

test LayoutMeasuresOnceAtAWidth(text string, width int) { assert measuresOnceAtAWidth(text, width) }

func heightFollowsTheWidth(word string, copies int, width int) bool {
	let text = strings.Repeat(word+" ", (copies%12+12)%12+1)
	let w = float32(width%500 + 500)
	let mut t = layout.New()
	let root = layout.Add(&t, layout.Default())
	let a = layout.Add(&t, textStyle(text, 16))
	with context { layout.Measure: m } {
		layout.Append(&t, root, a) catch {
		case arena.Stale:
			return false
		}
		layout.Layout(&t, root, w, inf) catch {
		case arena.Stale:
			return false
		}
		layout.Layout(&t, root, w / 2, inf) catch {
		case arena.Stale:
			return false
		}
	}
	let b = layout.BoxOf(&t, a) catch {
	case arena.Stale:
		return false
	}
	b.W == w / 2 && b.H == m.Height(text, 16, w / 2)
}

test LayoutHeightFollowsTheWidth(word string, copies int, width int) { assert heightFollowsTheWidth(word, copies, width) }
`

// stdLayoutLawStatements is the statement of each ui/layout law, as the
// ledger writes it, by ledger key.
var stdLayoutLawStatements = map[string]string{
	"ui/layout Measure.MinFitsMax":               `MinFitsMax(text string, size int) { let w = Widths(text, float32(size%64+64)) w.Min <= w.Max }`,
	"ui/layout Measure.WiderIsNoTaller":          `WiderIsNoTaller(text string, size int, a int, b int) { let s = float32(size%64 + 64) let narrow = float32(a%400 + 400) let wide = narrow + float32(b%400+400) Height(text, s, wide) <= Height(text, s, narrow) }`,
	"ui/layout Measure.AtMaxOneLinePerParagraph": `AtMaxOneLinePerParagraph(text string, size int) { let s = float32(size%64 + 64) let one = Height("x", s, inf) Height(text, s, Widths(text, s).Max) <= one*float32(len(strings.Split(text, "\n"))) }`,
	"ui/layout TextTakesItsMeasure":              `TextTakesItsMeasure(word string, copies int, size int, width int) { let text = strings.Repeat(word+" ", (copies%12+12)%12+1) let s = float32(size%32 + 32) let w = float32(width%500 + 500) let mut t = New() let root = Add(&t, Default()) let stretched = Add(&t, textStyle(text, s)) let mut halfStyle = textStyle(text, s) halfStyle.Width = Px{Value: w / 2} let half = Add(&t, halfStyle) let mut rowStyle = Default() rowStyle.Direction = Row rowStyle.Items = Start let row = Add(&t, rowStyle) let fitted = Add(&t, textStyle(text, s)) let ws = lawMeasure.Widths(text, s) let fit = fmax(ws.Min, fmin(ws.Max, w)) with context { Measure: lawMeasure } { Append(&t, root, stretched) catch { case arena.Stale: return false } Append(&t, root, half) catch { case arena.Stale: return false } Append(&t, root, row) catch { case arena.Stale: return false } Append(&t, row, fitted) catch { case arena.Stale: return false } Layout(&t, root, w, inf) catch { case arena.Stale: return false } } let a = BoxOf(&t, stretched) catch { case arena.Stale: return false } let b = BoxOf(&t, fitted) catch { case arena.Stale: return false } let c = BoxOf(&t, half) catch { case arena.Stale: return false } a.W == w && a.H == lawMeasure.Height(text, s, w) && c.W == w/2 && c.H == lawMeasure.Height(text, s, w/2) && near(b.W, fit) && near(b.H, lawMeasure.Height(text, s, b.W)) }`,
	"ui/layout HeightFollowsTheWidth":            `HeightFollowsTheWidth(word string, copies int, width int) { let text = strings.Repeat(word+" ", (copies%12+12)%12+1) let w = float32(width%500 + 500) let mut t = New() let root = Add(&t, Default()) let a = Add(&t, textStyle(text, 16)) with context { Measure: lawMeasure } { Append(&t, root, a) catch { case arena.Stale: return false } Layout(&t, root, w, inf) catch { case arena.Stale: return false } Layout(&t, root, w/2, inf) catch { case arena.Stale: return false } } let b = BoxOf(&t, a) catch { case arena.Stale: return false } b.W == w/2 && b.H == lawMeasure.Height(text, 16, w/2) }`,
	"ui/layout MeasuresOnceAtAWidth":             `MeasuresOnceAtAWidth(text string, width int) { let w = float32(width%500 + 500) let mut t = New() let root = Add(&t, Default()) let a = Add(&t, textStyle(text, 16)) let b = Add(&t, textStyle(text, 16)) with context { Measure: lawMeasure } { Append(&t, root, a) catch { case arena.Stale: return false } Append(&t, root, b) catch { case arena.Stale: return false } Layout(&t, root, w, inf) catch { case arena.Stale: return false } SetStyle(&t, root, Default()) catch { case arena.Stale: return false } Layout(&t, root, w, inf) catch { case arena.Stale: return false } } Measured(t) == 1 }`,
}

// layoutLawTestName is the probe's test of a ledger key: the last element
// of the package path, capitalized, then the law ("ui/layout
// Measure.MinFitsMax" is LayoutMeasureMinFitsMax).
func layoutLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdLayoutLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's ui/layout laws, each
// with the row's statement, and that each passed. The flexbox itself is
// held to MyGo's by TestLayoutMatchesMyGo in hosts/mygo.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdLayoutLawsRun(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "stdlayout", stdLayoutLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdlayout"], "--seed", "1")
	if code != 0 {
		t.Errorf("the ui/layout law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdLayoutLawStatements, layoutLawTestName, stdLayoutLawsProbe, stdout))
}
