package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/stdlib"
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
// needs go.Foreign there, and the provider's extra Ambient is refused.
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

// fileRowDecls is a user's file provider over Go's os file calls: each
// operation reads or changes the host's files through a file row.
const fileRowDecls = `import go os
import go io/fs

type Disk {
	Read(path string) []byte fails go.Error,
	Write(path string, data []byte) fails go.Error,
	Count(path string) int fails go.Error,
	Exists(path string) bool,
	Link(path string) string fails go.Error,
	Tidy(path string) fails go.Error,
}

type Real {} implements Disk

func Read(_ Real, path string) []byte fails go.Error needs Ambient {
	try os.ReadFile(path)
}

func Write(_ Real, path string, data []byte) fails go.Error needs Ambient {
	try os.Mkdir("out", fs.FileMode(uint32(0o755)))
	try os.MkdirAll("out/a", fs.FileMode(uint32(0o755)))
	try os.WriteFile(path, data, fs.FileMode(uint32(0o644)))
	try os.Chmod(path, fs.FileMode(uint32(0o600)))
}

func Count(_ Real, path string) int fails go.Error needs Ambient {
	len(try os.ReadDir(path))
}

func Exists(_ Real, path string) bool needs Ambient {
	let _ = os.Stat(path) catch {
	case go.Error:
		return false
	}
	let _ = os.Lstat(path) catch {
	case go.Error:
		return false
	}
	true
}

func Link(_ Real, path string) string fails go.Error needs Ambient {
	try os.Readlink(path)
}

func Tidy(_ Real, path string) fails go.Error needs Ambient {
	try os.Rename(path, path + ".old")
	try os.Remove(path + ".old")
	try os.RemoveAll("out")
}

func count(d Disk) int { d.Count("out") catch { case go.Error: -1 } }
`

// TestStatResultIsValues is HOST-ROW-3's acceptance: os.Stat, os.Lstat
// and os.ReadDir are bound to runtime/v1/hostfs (gobind.ValueResults), so
// their results are structs and a user reads a file's name, size, mode
// and kind, and an entry's name and kind, by field access, with Ambient
// alone: no row vouches for fs.FileInfo's or fs.DirEntry's methods, which
// any Go type may implement. Red at base: the result is fs.FileInfo, which
// has no field Size, and its methods need go.Foreign.
func TestStatResultIsValues(t *testing.T) {
	src := `import go os
import go io/fs

func describe(path string) string needs Ambient {
	let info = os.Stat(path) catch {
	case go.Error:
		return "missing"
	}
	let link = os.Lstat(path) catch {
	case go.Error:
		return "missing"
	}
	let mode fs.FileMode = info.Mode
	"\(info.Name) \(info.Size) \(info.IsDir) \(uint32(mode)) \(link.IsDir)"
}

func names(dir string) string needs Ambient {
	let es = os.ReadDir(dir) catch {
	case go.Error:
		return "unreadable"
	}
	let mut out = ""
	for e in es {
		out = out + e.Name + " \(e.IsDir) \(uint32(e.Type))"
	}
	out
}

func main() needs Console | Ambient {
	Console.Println(describe("a") + names("."))
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ds := check.Check("example.test/statvalues", []*syntax.File{f}, nil); len(ds) != 0 {
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		t.Errorf("%s\ngot:\n%s\nwant no diagnostic", src, strings.Join(got, "\n"))
	}
}

// TestStatValueMethodsNeedNoForeign is HOST-ROW-3b's acceptance: the
// methods of a file's mode and modification time that compute on the
// value alone (fs.FileMode's IsDir, IsRegular, Perm, Type and String;
// time.Time's instant methods) are memory-only rows, so a user reads
// them with Ambient for the stat and nothing more. Red at base: each call
// needs go.Foreign (S102). A zone-reading method (Year) still needs it.
func TestStatValueMethodsNeedNoForeign(t *testing.T) {
	for _, c := range []struct{ expr, want string }{
		{`"\(info.Mode.IsRegular()) \(info.Mode.IsDir()) \(uint32(info.Mode.Perm())) \(uint32(info.Mode.Type())) \(info.Mode.String())"`, ""},
		{`"\(info.ModTime.Unix()) \(info.ModTime.UnixMilli()) \(info.ModTime.UnixNano()) \(info.ModTime.IsZero()) \(info.ModTime.Before(info.ModTime.Add(1s))) \(info.ModTime.After(info.ModTime)) \(info.ModTime.Equal(info.ModTime.UTC())) \(info.ModTime.Compare(info.ModTime)) \(info.ModTime.Sub(info.ModTime))"`, ""},
		{`"\(info.ModTime.Year())"`, "S102"},
	} {
		src := "import go os\n\nfunc describe(path string) string needs Ambient {\n\tlet info = os.Stat(path) catch {\n\tcase go.Error:\n\t\treturn \"missing\"\n\t}\n\t" + c.expr + "\n}\n"
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("example.test/statmethods", []*syntax.File{f}, nil)
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		text := strings.Join(got, "\n")
		if c.want == "" && text != "" || c.want != "" && (len(got) != 1 || !strings.HasPrefix(text, c.want)) {
			t.Errorf("%s\ngot:\n%s\nwant %q", src, text, c.want)
		}
	}
}

// TestFileRowsChargeAmbient is HOST-ROW-2's acceptance: Go's os file
// calls (ReadFile, WriteFile, ReadDir, Stat, Lstat, Mkdir, MkdirAll,
// Remove, RemoveAll, Rename, Readlink, Chmod) are host rows, so a user's
// provider whose operations call them needs Ambient and not go.Foreign,
// exactly as std's fs host provider (NewHost needs Ambient) does; main
// checks with Console and Ambient alone, and without Ambient the build
// site says what it needs. Red at base: each call needs go.Foreign.
//
// Mutant it alone catches: ffi-host-file-row-missing (os.ReadFile has no
// row in gofacts.Hosts, so its binding's needs Ambient is refused).
func TestFileRowsChargeAmbient(t *testing.T) {
	for _, c := range []struct{ main, want string }{
		{"func main() needs Console | Ambient {\n\tConsole.Println(\"\\(count(Real))\")\n}\n", ""},
		{"func main() needs Console {\n\tConsole.Println(\"\\(count(Real))\")\n}\n",
			"building Real as Disk, whose operations read the host, requires Ambient, which is not in the capability row of function main"},
	} {
		src := fileRowDecls + "\n" + c.main
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		_, ds := check.Check("example.test/filerow", []*syntax.File{f}, nil)
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
	current, ok := gofacts.Find(stdlib.Ledger(), key)
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
		{"validated", stdlib.Ledger(), binding, target, ""},
		{"missing", withLine(key, ""), binding, target, target + " is bound as a host call without a reviewed row that acts (the std ledger has no guard line for " + key},
		{"refuted", withLine(key, refuted.Line()), binding, target, target + " is bound as a host call, and its reviewed row does not hold: its guard refuted it"},
		{"unvouched", stdlib.Ledger(), edited, "os.Unsetenv", "os.Unsetenv is bound with needs Ambient and no host line: only a host row of internal/gofacts charges Ambient for a Go call"},
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
