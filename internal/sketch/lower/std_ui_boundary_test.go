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
		"S101 24:10 {NotFound | ui.Pending} escapes function Spelled, whose error row {} does not list it",
		"S101 29:2 {NotFound | ui.Pending} escapes function InColumn, whose error row {} does not list it",
		"S108 38:24 cannot use func(*ui.Frame[int], State) fails NotFound | ui.Pending as func(*ui.Frame[int], State) in argument: its error row {NotFound | ui.Pending} is wider than {}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// uiNeedsSource holds the three ways to get a view's needs wrong (H8b,
// stdlib.md, UI 5): an exported view that reads a service its row does
// not list, a call of ui.Render with no with around it, and a needing
// view given to a host entry that was not updated (show, whose view needs
// nothing); runIt, ui.Run with a needing view, and the Render in the with
// check clean.
const uiNeedsSource = `import ui
import actor

type Store {
	Name(id int) string,
}

type Mem {}

func Name(_ Mem, id int) string { "m\(id)" }

func Unlisted(f *ui.Frame[int], s int) {
	ui.Text(f, Store.Name(s))
}

func Listed(f *ui.Frame[int], s int) needs Store {
	ui.Load(f, ui.ByInt(s), func() int needs Store { len(Store.Name(s)) })
	ui.Text(f, Store.Name(s))
}

func show(view func(f *ui.Frame[int], s int)) {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, view, 0)
}

func runIt[D, F](h ui.Host[int], a actor.Actor[int, int, D, F]) fails ui.Closed | actor.Stopped needs Store {
	try ui.Run(h, a, Listed)
}

func main() {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, Listed, 1)
	with context { Store: Mem } {
		let _ = ui.Render(&f, Listed, 1)
		show(Listed)
	}
}
`

// TestUIViewNeeds: a view's needs are checked as any function's (Q16):
// S102 at the read a row does not list and at a call with no with, S108
// at a host entry whose view type has no needs row. Diagnostic rows:
// their text and position are the spec (H8b). Mutants: ui.Render's or
// ui.Run's view type loses its needs row (ui-render-view-no-needs,
// ui-run-view-no-needs).
func TestUIViewNeeds(t *testing.T) {
	_, ds := checkProgram(t, []pkgSource{{"/ergo-test/uineeds/main.ergo", uiNeedsSource}})
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%s %d:%d %s", d.Code, d.Line, d.Column, d.Message))
	}
	want := []string{
		"S102 13:13 this call requires Store, which is not in the capability row of function Unlisted and no with provides it",
		"S102 32:10 this call requires Store, which is not in the capability row of function main and no with provides it",
		"S108 35:8 cannot use func(*ui.Frame[int], int) needs Store as func(*ui.Frame[int], int) in argument: its capability row {Store} is wider than {}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
