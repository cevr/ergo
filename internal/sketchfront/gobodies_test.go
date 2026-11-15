package sketchfront

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestModuleGoBodies (STD-NOPRIV NP4d; docs/spec/interop.md, Trusted Go
// 3): a trusted module's package binds a body of the module's own Go
// package that takes hidden arguments (the call's site; the caller's task,
// on which it waits) and states its waiting policy, and the lowering calls
// it with them, as it calls a runtime/v1 body: the promised form is the
// hidden argument first, before the Ergo one. An untrusted module is
// refused with the line to write, and a body that states no policy with
// the mark to write. Mutants: module-door-untrusted (moduleDoor skips the
// trust check), binding-policy-default-nowait (a body with no mark is
// read as one that does not wait), module-hidden-unqualified (v1's hidden
// types are read unqualified only, so v1.Site is an Ergo parameter).
func TestModuleGoBodies(t *testing.T) {
	const goSrc = `package gohost

import (
	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

//ergo:wait none
func Mark(at v1.Site, s string) string { return s + string(at) }

//ergo:wait caller
func Poll(t *rt.Task, n int) int {
	t.Check()
	return n + 1
}

func Unmarked(t *rt.Task) int { return 0 }
`
	files := map[string]string{
		"go.mod":           "module example.com/app\n\ngo 1.27\n",
		"gohost/gohost.go": goSrc,
		"main.ergo": "import strconv\n\n" +
			"extern go example.com/app/gohost.Mark func mark(s string) string\n" +
			"extern go example.com/app/gohost.Poll func poll(n int) int\n\n" +
			"func main() needs Console {\n\tConsole.Println(mark(\"here\"))\n\tConsole.Println(strconv.Itoa(poll(41)))\n}\n",
	}
	for _, c := range []struct{ mod, extra, want string }{
		{"module app\ntrust app\n", "", ""},
		{"module app\n", "", "main.ergo S108 3:1 extern go example.com/app/gohost.Mark binds a body of the module's own Go package, which a package binds only when the root module trusts its Go; write the line `trust app` in the root module's ergo.mod"},
		{"module app\ntrust app\n", "extern go example.com/app/gohost.Unmarked func unmarked() int\n", "main.ergo S108 3:1 extern go example.com/app/gohost.Unmarked: Unmarked states no //ergo:wait KIND"},
	} {
		files["ergo.mod"] = c.mod
		main := files["main.ergo"]
		files["main.ergo"] = strings.Replace(main, "\n\n", "\n\n"+c.extra, 1)
		a, err := CheckDir(writeTree(t, files))
		files["main.ergo"] = main
		if err != nil {
			t.Fatal(err)
		}
		got := diagnosticLines(a)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("ergo.mod %q:\n%s\nwant %q", c.mod, got, c.want)
			continue
		}
		if c.want != "" {
			continue
		}
		out, err := lower.Lower(a.program.unit())
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, f := range out {
			text.Write(f.Data)
		}
		for _, call := range []string{"gohost.Mark(", "gohost.Poll("} {
			i := strings.Index(text.String(), call)
			if i < 0 {
				t.Fatalf("the lowered Go calls no %s)\n%s", call, text.String())
			}
			if rest := text.String()[i+len(call):]; strings.HasPrefix(rest, `"here"`) || strings.HasPrefix(rest, "41") {
				t.Errorf("%s) is called with no hidden argument first: %.80s", call, rest)
			}
		}
	}
}
