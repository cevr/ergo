package check_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// contextFailsHead declares a constructor that fails with Missing, the
// provider of the rows below (lines 1 to 10; a row's body starts at 11).
const contextFailsHead = `type Missing { Name string }
type Store { Get() int }
type Disk { n int } implements Store
func Open(name string) Disk fails Missing {
	if name == "" {
		return Missing{Name: name}
	}
	Disk{n: 1}
}
func Get(d Disk) int { d.n }
`

// TestContextFailsRow: a named context's header fails row bounds its
// build's failures, and a with that builds a context whose row is not
// empty writes try on that operand (rows.md, Context 4 and With 6;
// CTX-ROWS-2, owner q280). Mutants only it kills (testdata/mutants.json):
// ctxf2-header-ignored, ctxf2-header-unread, ctxf2-with-unmarked-accepted,
// ctxf2-needless-try-accepted, ctxf2-embed-unescaped,
// ctxf2-value-unrefused, ctxf2-literal-unrefused.
func TestContextFailsRow(t *testing.T) {
	const s101 = "S101 context %s can fail with {Missing} while it is built, which its header's fails row does not list: write fails Missing in its header, or catch it in the entry (rows.md, Context 4)"
	cases := []struct {
		name string
		body string   // from line 11 on
		want []string // "line:column code message", in order
	}{
		{"the header lists the entry's failure, and with try passes it on", `let Live = context fails Missing { Store: try Open("db") }
func read() int fails Missing { with try Live { Store.Get() } }
`, nil},
		{"no header: the build lets no failure escape", `let Live = context { Store: try Open("db") }
`, []string{"11:29 " + fmt.Sprintf(s101, "Live")}},
		{"a with without try", `let Live = context fails Missing { Store: try Open("db") }
func read() int fails Missing { with Live { Store.Get() } }
`, []string{"12:38 S105 context Live can fail with {Missing} while it is built, and this with does not handle it: write with try Live (rows.md, With 6)"}},
		{"a try on a context whose build cannot fail", `let Live = context { Store: Disk{n: 1} }
func read() int { with try Live { Store.Get() } }
`, []string{"12:24 S108 try on a context whose build cannot fail: drop the try (rows.md, With 6)"}},
		{"an embedding passes the row into the build that embeds it", `let Live = context fails Missing { Store: try Open("db") }
let Outer = context { Live }
`, []string{"12:23 " + fmt.Sprintf(s101, "Outer")}},
		{"the embedding's header lists it", `let Live = context fails Missing { Store: try Open("db") }
let Outer = context fails Missing { Live }
func read() int fails Missing { with try Outer { Store.Get() } }
`, nil},
		{"a Context value of a context that can fail", `let Live = context fails Missing { Store: try Open("db") }
func read() int {
	let v Context[Store] = Live
	with v { Store.Get() }
}
`, []string{"13:25 S109 a Context value of context Live, which can fail with {Missing} while it is built, is CTX-ROWS-2b's: with try Live builds it in a with"}},
		{"a literal's fails clause stays refused", `func f() {
	with context fails Missing {} {}
}
`, []string{"12:15 S109 an inline or a local context's fails clause is not checked: its entries' failures escape through their own try at the with (rows.md, Context 3 and 9); a fails row is a named context's header (Context 4)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := contextFailsHead + tc.body
			_, ds := check.CheckSource(src)
			var got []string
			for _, d := range ds {
				got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("diagnostics:\n%v\nwant:\n%v\n%s", got, tc.want, src)
			}
		})
	}
}
