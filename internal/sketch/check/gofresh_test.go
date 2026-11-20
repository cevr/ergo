package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestGoFreshRefFields: a field read through a fresh Go handle (gofresh.go;
// interop.md, Checking 4) charges nothing, and one through any other
// handle charges go.Foreign at the read (or, in a function literal, at
// the literal's call). Each row's source marks the place that must be
// S102 with «»; a row with no mark checks clean. Mutants
// they alone catch: a handle passed to a function still fresh
// (np4furl-fresh-passed-on), a let mut handle fresh
// (np4furl-fresh-mut), a handle from an Ergo function fresh
// (np4furl-fresh-not-go-call), a handle of a Go call that needs
// go.Foreign fresh (np4furl-fresh-foreign-call), a handle a function
// literal captures fresh (np4furl-fresh-captured).
func TestGoFreshRefFields(t *testing.T) {
	const head = "import go net/url\nimport go os\n\nfunc keep(u go.Ref[url.URL]) string { \"\" }\nfunc mk(raw string) go.Ref[url.URL] fails go.Error { try url.Parse(raw) }\n\n"
	for _, tc := range []struct{ name, src string }{
		{"fresh", `func F(raw string) string {
	let u = url.Parse(raw) catch {
	case go.Error: return ""
	}
	let user = match u.User {
	case Some: true
	case None: false
	}
	"\(u.Scheme)\(u.Host)\(u.Hostname())\(user)\(u.ForceQuery)"
}`},
		{"try", `func F(raw string) string fails go.Error {
	let u = try url.Parse(raw)
	u.Path
}`},
		{"parameter", `func F(u go.Ref[url.URL]) string { «u.Host» }`},
		{"passed-on", `func F(raw string) string fails go.Error {
	let u = try url.Parse(raw)
	let _ = keep(u)
	«u.Host»
}`},
		{"mut", `func F(raw string) string fails go.Error {
	let mut u = try url.Parse(raw)
	«u.Host»
}`},
		{"ergo-call", `func F(raw string) string fails go.Error {
	let u = try mk(raw)
	«u.Host»
}`},
		{"foreign-call", `func F() int fails go.Error {
	let p = try os.FindProcess(1)
	«p.Pid»
}`},
		{"captured", `func F(raw string) string fails go.Error {
	let u = try url.Parse(raw)
	let f = func() string { u.Host }
	«f()»
}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := head + tc.src + "\n"
			mark := strings.Index(src, "«")
			src = strings.NewReplacer("«", "", "»", "").Replace(src)
			_, d := check.CheckSource(src)
			var s102 []check.Diagnostic
			for _, x := range d {
				if x.Code == "S102" {
					s102 = append(s102, x)
					continue
				}
				if x.Code != "S110" { // the shared head's imports
					t.Fatalf("unexpected %s:%d:%d %s", x.Code, x.Line, x.Column, x.Message)
				}
			}
			if mark < 0 {
				if len(s102) != 0 {
					t.Fatalf("want clean, got %s %s", s102[0].Code, s102[0].Message)
				}
				return
			}
			line := strings.Count(src[:mark], "\n") + 1
			col := mark - strings.LastIndex(src[:mark], "\n")
			for _, x := range s102 {
				if x.Line == line && x.Column == col && strings.Contains(x.Message, "requires go.Foreign") {
					return
				}
			}
			t.Fatalf("want S102 at %d:%d (the field read), got %v", line, col, s102)
		})
	}
}
