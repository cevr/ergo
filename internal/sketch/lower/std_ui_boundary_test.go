package lower

import (
	"fmt"
	"strings"
	"testing"
)

// uiForgotSource holds the three ways to forget a boundary around a read
// that can stop (stdlib.md, UI, Boundaries): an exported view whose row is
// spelled empty, a read in a container's block of such a view, and an
// unexported view, whose row is inferred, given to ui.Render, which takes
// a view that cannot fail.
const uiForgotSource = `import ui

type NotFound { ID int }
type User { Name string }
type State { User ui.Async[User, NotFound] }

func await(a ui.Async[User, NotFound]) User fails NotFound | ui.Pending {
	match a {
	case ui.Loading:
		return ui.Pending
	case ui.Ready as r:
		return r.Value
	case ui.Failed as e:
		return e.Error
	}
}

func bare(f *ui.Frame[int], s State) {
	let u = try await(s.User)
	ui.Text(f, u.Name)
}

func Spelled(f *ui.Frame[int], s State) {
	let u = try await(s.User)
	ui.Text(f, u.Name)
}

func InColumn(f *ui.Frame[int], s State) {
	ui.Column(f, ui.Plain()) {
		let u = try await(s.User)
		ui.Text(f, u.Name)
	}
}

func main() {
	let mut f = ui.NewFrame[int]()
	let s = State{User: ui.Loading}
	let _ = ui.Render(&f, bare, s)
	let _ = ui.Render(&f, Spelled, s)
	let _ = ui.Render(&f, InColumn, s)
}
`

// TestUIForgottenBoundary: a forgotten boundary is a compile error that
// names ui.Pending and the failure, at the read (S101) or at the call of
// ui.Render (S108). Diagnostic rows: their text and position are the spec
// (H8a). Mutant: ui.Render's view type gains a failure row parameter (the
// third row goes; ui-render-view-fails).
func TestUIForgottenBoundary(t *testing.T) {
	_, ds := checkProgram(t, []pkgSource{{"/ergo-test/uiforgot/main.ergo", uiForgotSource}})
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%s %d:%d %s", d.Code, d.Line, d.Column, d.Message))
	}
	want := []string{
		"S101 24:10 {ui.Pending | NotFound} escapes function Spelled, whose error row {} does not list it",
		"S101 29:2 {ui.Pending | NotFound} escapes function InColumn, whose error row {} does not list it",
		"S108 38:24 cannot use func(*ui.Frame[int], State) fails ui.Pending | NotFound as func(*ui.Frame[int], State) in argument: its error row {ui.Pending | NotFound} is wider than {}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
