package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
)

// hostRowDecls is a user's provider package in one file: Real's
// operation reads the environment through Go's os.Getenv, a host row.
const hostRowDecls = `import go os

type Env { Get(key string) string }

type Real {} implements Env

func Get(_ Real, key string) string needs Ambient { os.Getenv(key) }

func show(e Env) string { e.Get("HOME") }
`

// TestHostRowChargesAmbient is HOST-ROW's acceptance (std-priv NP4f-6;
// interop.md, Purity and authority): Go's os.Getenv is a host row, so a
// call of it needs Ambient and not go.Foreign, and a user's provider whose
// operation calls it captures Ambient where it is built, as std's own
// would: main checks with Console and Ambient alone, and without Ambient
// the build site says what it needs. Red at base: Get's call of os.Getenv
// needs go.Foreign there, and the provider's extra Ambient is refused (#37).
//
// Mutants it alone catches: ffi-host-row-not-written (ergo bind go writes
// go.Foreign for a host row), ffi-capture-ambient-refused (a provider's
// Ambient is not captured) and ffi-capture-ambient-uncharged (the build
// site is not charged it).
func TestHostRowChargesAmbient(t *testing.T) {
	for _, c := range []struct{ main, want string }{
		{"func main() needs Console | Ambient {\n\tConsole.Println(show(Real) + with context { Env: Real } { Env.Get(\"PWD\") })\n}\n", ""},
		{"func main() needs Console {\n\tConsole.Println(show(Real))\n}\n",
			"S102 12:23 building Real as Env, whose operations read the host, requires Ambient, which is not in the capability row of function main"},
	} {
		src := hostRowDecls + "\n" + c.main
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("example.test/hostrow", []*syntax.File{f}, nil)
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		text := strings.Join(got, "\n")
		if c.want == "" && text != "" || !strings.Contains(text, c.want) || c.want != "" && len(got) != 1 {
			t.Errorf("%s\ngot:\n%s\nwant %q", src, text, c.want)
		}
	}
}

// TestHostRowsActOnlyValidated is HOST-ROW's admission of a host row: the
// binding of os, whose Getenv is bound with needs Ambient and its line,
// checks clean exactly when the std ledger holds the row's guard line
// current and validated; a missing line and a refuted one are diagnostics at the
// binding that say which; and a binding that writes needs Ambient for a Go
// function with no host line (a hand edit) is refused.
//
// Mutants it alone catches: ffi-host-unguarded-acts (a host row acts with
// no guard line), ffi-host-ambient-unvouched (a binding's needs Ambient
// with no host line is admitted).
func TestHostRowsActOnlyValidated(t *testing.T) {
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	target := "os.Getenv"
	key := gofacts.HostKey(target)
	current, ok := gofacts.Find(std.Ledger(), key)
	if !ok || current.Verdict != "validated" {
		t.Fatalf("the shipped ledger has no validated line for %s: %+v", key, current)
	}
	refuted := current
	refuted.Verdict = "refuted"
	binding, ok := gostd.Binding("os")
	line := "// ergo:effect ambient (vouched: "
	if !ok || !strings.Contains(binding, line) {
		t.Fatal("the binding of os has no host line")
	}
	// The hand edit: os.Unsetenv, which has no host row, bound with needs
	// Ambient.
	const unset = "extern go os.Unsetenv func Unsetenv(key string) fails go.Error needs go.Foreign"
	if !strings.Contains(binding, unset) {
		t.Fatalf("the binding of os has no line %q", unset)
	}
	edited := strings.Replace(binding, unset, strings.Replace(unset, "go.Foreign", "Ambient", 1), 1)
	for _, c := range []struct {
		name, ledger, binding, target, want string
	}{
		{"validated", std.Ledger(), binding, target, ""},
		{"missing", withLine(key, ""), binding, target, target + " is bound as a host call without a reviewed row that acts (the std ledger has no guard line for " + key},
		{"refuted", withLine(key, refuted.Line()), binding, target, target + " is bound as a host call, and its reviewed row does not hold: its guard refuted it"},
		{"unvouched", std.Ledger(), edited, "os.Unsetenv", "os.Unsetenv is bound with needs Ambient and no host line: only a host row of internal/gofacts charges Ambient for a Go call"},
	} {
		ledger, src := c.ledger, c.binding
		check.FactLedger = func() string { return ledger }
		set := check.NewGoBindings(func(path string) (string, bool) {
			if path == "os" {
				return src, true
			}
			return gostd.Binding(path)
		})
		if !set.ResolveAll("os") {
			t.Fatal("no binding of os")
		}
		var got []string
		for _, d := range set.Diagnostics() {
			if strings.Contains(d, c.target+" ") {
				got = append(got, d)
			}
		}
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("%s: the binding of %s has diagnostics: %q", c.name, c.target, got)
		case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
			t.Errorf("%s: the binding of %s gives %q, want one diagnostic with %q", c.name, c.target, got, c.want)
		}
	}
}
