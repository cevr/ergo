package check_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestSSEViewIsLent holds std http's EventView to its promise (stdlib.md
// http 14, SSE-VIEW): FeedEach lends each view as a borrow, so a caller
// that keeps it past each's return is refused, at its place and with the
// borrow rule's text, and one that reads it inside each checks clean. The
// reader reuses a view's list of data lines from event to event, so a
// kept view would show a later event's lines. Mutant sse-view-not-lent
// (each takes the view by value, `func(e EventView)`): the kept views
// then check clean, and these rows fail; no law or example writes a
// program the checker must refuse.
func TestSSEViewIsLent(t *testing.T) {
	const head = "import http\n\n"
	cases := []struct {
		name string
		src  string
		want []string // "line:column code message", in order
	}{
		{"read inside each", `func Count(body string) int {
	let mut r = http.NewEventReader()
	let mut n = 0
	http.FeedEach(&r, body, func(e mut http.EventView) {
		n = n + len(http.Data(e)) + len(http.Event(e)) + len(http.ID(e))
	})
	n
}
`, nil},
		{"kept in a slice", `func Keep(body string) int {
	let mut r = http.NewEventReader()
	let mut kept = []http.EventView{}
	http.FeedEach(&r, body, func(e mut http.EventView) {
		kept = kept.Append(e)
	})
	len(kept)
}
`, []string{
			"7:22 S108 cannot use mut http.EventView as http.EventView in argument",
		}},
		{"captured by a task", `import task

func Later(body string) {
	let mut r = http.NewEventReader()
	http.FeedEach(&r, body, func(e mut http.EventView) {
		let t = task.Go() { http.Data(e) }
		t.Stop()
	})
}
`, []string{
			"8:33 S108 e is a borrow (mut T), and this function literal or block may outlive the call that lent it or run on another task; an escaping frame never captures a borrow: copy what it needs into a let before it (types.md, Borrows 1)",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ds := check.CheckSource(head + tc.src)
			var got []string
			for _, d := range ds {
				got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("diagnostics:\n%s\nwant:\n%s", ambientLines(got), ambientLines(tc.want))
			}
		})
	}
}
