package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A module of modules/ (MK0) runs the fmt and test legs and nothing else:
// `-modules` runs it alone, a run of the tags runs it beside the examples,
// a failing law fails it, and a layout error stops the run with status 2.
func TestModulesLeg(t *testing.T) {
	t.Parallel()
	module := func(f *fixture, source string, extra map[string]string) {
		f.write("modules/hello/ergo.mod", "module example.com/hello\n")
		f.write("modules/hello/go.mod", "module example.com/hello\n\ngo 1.27\n")
		f.write("modules/hello/README.md", "# hello\n")
		f.write("modules/hello/hello/hello.ergo", source)
		for name, text := range extra {
			f.write("modules/hello/"+name, text)
		}
	}
	t.Run("alone", func(t *testing.T) {
		f := newFixture(t)
		f.good()
		module(f, "func f() {}\n", nil)
		code, out := f.run("-modules")
		if code != 0 || !strings.Contains(out, "ok modules/hello: test hello (") || strings.Contains(out, "good") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		calls := readCalls(t, f)
		if want := "test " + filepath.Join(f.root, "modules/hello/hello"); !slices.Contains(calls, want) {
			t.Errorf("no call %q in\n%q", want, calls)
		}
		for _, call := range calls {
			if strings.HasPrefix(call, "build ") {
				t.Errorf("a module is built: %q", call)
			}
		}
	})
	t.Run("beside the examples", func(t *testing.T) {
		f := newFixture(t)
		f.good()
		module(f, "func f() {}\n", nil)
		code, out := f.run()
		if code != 0 || !strings.Contains(out, "ok modules/hello: test hello (") || !strings.Contains(out, "ok good/basic (") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("a failing law", func(t *testing.T) {
		f := newFixture(t)
		module(f, "func f() {}\n// TESTFAIL\n", nil)
		code, out := f.run("-modules", "hello")
		if code != 1 || !strings.Contains(out, "FAIL modules/hello: test hello") {
			t.Fatalf("exit %d, want 1\n%s", code, out)
		}
	})
	for _, c := range []struct {
		extra map[string]string
		want  string
	}{
		{map[string]string{"cmd/hello/main.ergo": "func main() {}\n"}, "modules/hello: a module is a library: it has no cmd/"},
		{map[string]string{"go.mod": "module example.com/other\n"}, "modules/hello: ergo.mod names the module example.com/hello and go.mod example.com/other"},
		{map[string]string{"notes.txt": "x\n"}, "modules/hello: unexpected notes.txt"},
	} {
		f := newFixture(t)
		module(f, "func f() {}\n", c.extra)
		code, out := f.run("-modules")
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d, want 2 and %q\n%s", c.extra, code, c.want, out)
		}
	}
}
