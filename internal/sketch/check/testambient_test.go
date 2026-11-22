package check_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestTestAmbientDiagnostics holds the refusals of the test's ambient
// (testambient.go rules 1 to 3; rows.md, Test's ambient), each at its place and
// with its text, and that a context whose header is exactly `needs
// TestAmbient` checks clean where a test builds it. Each row kills a
// mutant of testambient.go that no law or example catches (the examples
// only build such a context where it is allowed): rule 1's header read
// (a wider row admitted), rule 2's entry refusal, rule 3's with site, its
// embedding and its Context value.
func TestTestAmbientDiagnostics(t *testing.T) {
	const head = "import fs\n\nlet onDisk = context needs TestAmbient { fs.Files: fs.NewTestHost() }\n\n"
	const withOutside = "context onDisk needs TestAmbient; only the test runner provides it: with it in a test, or let a law run under it (rows.md, Test's ambient)"
	cases := []struct {
		name string
		src  string
		want []string // "line:column code message", in order
	}{
		{"a with in a test and one in its function literal", `test Reads {
	with onDisk {
		let _ = fs.Files.ReadFile("a") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			""
		}
	}
	let f = func() {
		with onDisk {}
	}
	f()
}
`, nil},
		{"a with outside a test", `func outside() {
	with onDisk {}
}
`, []string{"6:7 S102 " + withOutside}},
		{"no header", `let bare = context { fs.Files: fs.NewTestHost() }
`, []string{"5:32 S102 context bare requires TestAmbient while it is built; a named context is closed: its providers require no capability from the with site (README Gap 6)"}},
		{"a wider header", `let wider = context needs TestAmbient | fs.Writes { fs.Files: fs.NewTestHost() }
`, []string{
			"5:21 S109 a context's needs clause waits for context header checking (rows.md Context 8); it is not checked yet, but for needs TestAmbient alone (rows.md, Test's ambient)",
			"5:63 S102 context wider requires TestAmbient while it is built; a named context is closed: its providers require no capability from the with site (README Gap 6)",
		}},
		{"an embedding", `let embeds = context { onDisk }
`, []string{"5:24 S108 context onDisk needs TestAmbient and is not embedded: its folder lives for one with of it (rows.md, Test's ambient)"}},
		{"a Context value in a test", `test Value {
	let v Context[fs.Files] = onDisk
	with v {}
}
`, []string{"6:28 S108 a Context value cannot hold context onDisk, which needs TestAmbient: its folder lives for one with of it; bind it with with in a test (rows.md, Test's ambient)"}},
		{"an entry of TestAmbient", `type Fake {}

func folder(_ Fake) string { "/" }

test Forged {
	with context { TestAmbient: Fake } {}
}
`, []string{"10:30 S106 no context binds TestAmbient: its one provider is the test runner, for a named context whose header says needs TestAmbient (rows.md, Test's ambient)"}},
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

func ambientLines(xs []string) string {
	out := ""
	for _, x := range xs {
		out += "\t" + x + "\n"
	}
	return out
}
