package check_test

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// hostAuthorityLib is the head of each TestHostAuthority program: helpers
// that need fs.Files and os.Process, and readWith, which reads through a
// host provider it is given.
const hostAuthorityLib = `import fs
import os

func inner(p string) string needs fs.Files {
	let t = fs.Files.ReadFile(p) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "fail"
	}
	t
}

func run() needs Console | fs.Files | os.Process {
	Console.Println(inner("/nonexistent/loop-probe-x"))
	Console.Println("\(len(os.Process.Args()))")
}

func readWith(h fs.Host) string {
	let t = fs.ReadFile(h, "/nonexistent/loop-probe-x") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "fail"
	}
	t
}
`

const (
	fsAuthority = "fs.Host has an unexported field, so only package fs builds it; use fs.NewHost"
	osAuthority = "os.Host has an unexported field, so only package os builds it; use os.NewHost"
	// ambientEntryText is the S106 of a context entry for Ambient
	// (fu-ambient-ctor r7).
	ambientEntryText = "no context binds Ambient: its one provider is the program entry, for a main that lists it (stdlib.md, Host packages 3)"
)

// needsAmbient is the S102 text of a call of NewHost in a site whose row
// does not hold Ambient.
func needsAmbient(site string) string {
	return "this call requires Ambient, which is not in the capability row of " + site + " and no with provides it"
}

// TestHostAuthority is host-authority H1's acceptance check (stdlib.md,
// Host packages): a host provider, os.Host or fs.Host, is made only by its
// package's NewHost, which needs the prelude's Ambient; only the entry
// gives Ambient to main, and no program type implements it. Each case pins the
// exact diagnostics, at the lines of its `// want` markers, or none.
func TestHostAuthority(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		texts []string // the messages, in line and column order
	}{
		{"N1 direct call on fs.Host", `
func Sneak(p string) string {
	let t = fs.ReadFile(fs.Host, p) catch { // want S114
	case fs.NotFound, fs.Denied, fs.Failed:
		return "fail"
	}
	t
}
`, []string{fsAuthority}},
		{"N2 direct call on NewHost with an empty row", `
func Sneak(p string) string {
	let t = fs.ReadFile(fs.NewHost(), p) catch { // want S102
	case fs.NotFound, fs.Denied, fs.Failed:
		return "fail"
	}
	t
}
`, []string{needsAmbient("function Sneak")}},
		{"N3 inline context of fs.Host", `
func Sneak2(p string) string {
	with context { fs.Files: fs.Host } { inner(p) } // want S114
}
`, []string{fsAuthority}},
		{"N4 inline context of NewHost with an empty row", `
func Sneak2(p string) string {
	with context { fs.Files: fs.NewHost() } { inner(p) } // want S102
}
`, []string{needsAmbient("function Sneak2")}},
		{"N5 a forged Ambient", `
type Fake {}

func ambient(_ Fake) {}

func main() needs Console {
	with context { Ambient: Fake } { Console.Println("x") } // want S106
}
`, []string{ambientEntryText}},
		// fu-ambient-ctor r7 (batch-10 r6 B1): a value of Ambient does not
		// become a provider either, in a with or a named context. Before,
		// `with context { Ambient: a }` checked clean.
		{"N5b an Ambient parameter bound in a with", `
func g() int needs Ambient { 1 }

func Rebind(a Ambient) int {
	with context { Ambient: a } { g() } // want S106
}
`, []string{ambientEntryText}},
		{"N5c an Ambient parameter bound in a local context", `
func g() int needs Ambient { 1 }

func Rebind(a Ambient) int {
	let c = context { Ambient: a } // want S106
	with c { g() }
}
`, []string{ambientEntryText}},
		{"N6 a test makes a host", `
test ReadsHost {
	let got = with context { fs.Files: fs.NewHost() } { inner("/nonexistent/loop-probe-x") } // want S102
	assert got == "fail"
}
`, []string{needsAmbient("test ReadsHost")}},
		{"N7 os.Host as a value", `
func Sneak(k string) int {
	let p os.Process = os.Host // want S114
	_ = os.Getenv(os.Host, k) // want S114
	_ = p.Args()
	len(os.Host.Args()) // want S114
}
`, []string{osAuthority, osAuthority, osAuthority}},
		{"N8 a package-level let", `
let H = fs.NewHost() // want S102
`, []string{needsAmbient("the initializer of H")}},
		{"N9 a package-level named context", `
let Live = context { fs.Files: fs.NewHost() } // want S102
`, []string{"context Live requires Ambient while it is built; a named context is closed: its providers require no capability from the with site (README Gap 6)"}},
		{"N10 zero value, record update and literal", `
func Zero() string {
	let h fs.Host // want S114
	readWith(h)
}

func Update(h fs.Host) fs.Host { fs.Host{..h, live: h} } // want S114 S104

func Lit() fs.Host { fs.Host{} } // want S114
`, []string{
			"let h of type fs.Host needs a value: its zero value builds a new value of fs.Host, and " + fsAuthority + " (types.md, Zero values 6)",
			"a record update of fs.Host builds a new value: " + fsAuthority,
			"live is not an exported field of fs.Host (grammar.md, Visibility 2)",
			"a literal of fs.Host builds a new value: " + fsAuthority,
		}},
		{"N11 an unexported helper reaches a main without Ambient", `
func open() fs.Host { fs.NewHost() }

func main() needs Console {
	Console.Println(readWith(open())) // want S102
}
`, []string{needsAmbient("function main")}},
		{"N12 a function value of NewHost", `
func FnValue() string {
	let f = fs.NewHost
	readWith(f()) // want S102
}
`, []string{needsAmbient("function FnValue")}},
		{"N13 mutual recursion carries Ambient", `
func ping(n int) string {
	if n > 0 {
		pong(n - 1)
	} else {
		readWith(fs.NewHost())
	}
}

func pong(n int) string { ping(n) }

func Exp() string { pong(1) } // want S102
`, []string{needsAmbient("function Exp")}},
		{"N14 a defined type over fs.Host", `
type M fs.Host

func Def() M { M{} } // want S114
`, []string{"a literal of M, defined over fs.Host, builds a new value: " + fsAuthority}},
		{"N15 a generic record holding fs.Host at zero", `
type Box[T] { V T }

func Zero() fs.Host {
	let b Box[fs.Host] // want S114
	b.V
}
`, []string{"let b of type Box[fs.Host] needs a value: its zero value builds a new value of fs.Host, and " + fsAuthority + " (types.md, Zero values 6)"}},
		{"P1 main binds both hosts", `
func main() needs Console | Ambient {
	with context { fs.Files: fs.NewHost(), os.Process: os.NewHost() } { run() }
}
`, nil},
		{"P2 a local context in main", `
func main() needs Console | Ambient {
	let live = context { fs.Files: fs.NewHost(), os.Process: os.NewHost() }
	with live { run() }
}
`, nil},
		{"P3 memory providers need no Ambient", `
type Mem { Docs Map[string, string] }

func ReadFile(m Mem, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	match m.Docs.Get(path) {
	case Some[string] as s:
		s.Value
	case None:
		return fs.NotFound{Path: path}
	}
}

func ReadDir(_ Mem, path string) []string fails fs.NotFound | fs.Denied | fs.Failed {
	return fs.NotFound{Path: path}
}

func Stat(_ Mem, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	return fs.NotFound{Path: path}
}

func main() needs Console {
	with context { fs.Files: Mem{Docs: Map[string, string]{"a.md": "alpha"}} } { Console.Println(inner("a.md")) }
}

test ReadsMemory {
	let got = with context { fs.Files: Mem{Docs: Map[string, string]{}} } { inner("a.md") }
	assert got == "fail"
}
`, nil},
		{"P4 a library wraps the host choice and says so", `
func RunLive(f func() needs Console | fs.Files) needs Console | Ambient {
	with context { fs.Files: fs.NewHost() } { f() }
}

func show() needs Console | fs.Files { Console.Println(inner("a.md")) }

func main() needs Console | Ambient { RunLive(show) }
`, nil},
		{"P5 a host provider passed as a parameter", `
func Passed(h fs.Host) string { readWith(h) }

func main() needs Console | Ambient { Console.Println(Passed(fs.NewHost())) }
`, nil},
		{"P6 an exported function that declares Ambient", `
func Open() fs.Host needs Ambient { fs.NewHost() }

func main() needs Console | Ambient { Console.Println(readWith(Open())) }
`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := hostAuthorityLib + c.src
			_, diags := check.CheckSource(src)
			want, have := expected(src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Fatalf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
			sorted := append([]check.Diagnostic(nil), diags...)
			sort.SliceStable(sorted, func(i, j int) bool {
				if sorted[i].Line != sorted[j].Line {
					return sorted[i].Line < sorted[j].Line
				}
				return sorted[i].Column < sorted[j].Column
			})
			if len(sorted) != len(c.texts) {
				t.Fatalf("want %d messages, got\n\t%s", len(c.texts), describe(sorted))
			}
			for i, d := range sorted {
				if d.Message != c.texts[i] {
					t.Errorf("line %d: message\n\t%q\nwant\n\t%q", d.Line, d.Message, c.texts[i])
				}
			}
		})
	}
}

// hostOracleReadsInference is the property oracle's reading of an
// unexported helper's inferred row: true, the rule as written (rows.md,
// Capability rows: an unexported function's row is inferred, and its
// caller is judged). Mutant (h) sets it false.
var hostOracleReadsInference = true

// hostProvider is one std host package of TestHostAuthorityDomain.
type hostProvider struct {
	pkg, capName string
	direct       []string // a call of a provider function on V, the made value
}

var hostProviders = []hostProvider{
	{"os", "Process", []string{"_ = os.Args(V)"}},
	{"fs", "Files", []string{
		`_ = fs.Stat(V, ".") catch {`,
		"case fs.NotFound, fs.Denied, fs.Failed:",
		"\treturn",
		"}",
	}},
}

// hostLine is one line of a generated program and the codes the oracle
// expects on it.
type hostLine struct {
	text  string
	codes []string
}

// hostMaking is a making form of a host provider: lines before the use,
// and the value V the use names. judge marks the line of a NewHost call.
type hostMaking struct {
	name string
	make func(p hostProvider, judge func(text string) hostLine) (pre []hostLine, v string, vCodes []string)
}

var hostMakings = []hostMaking{
	{"bare", func(p hostProvider, _ func(string) hostLine) ([]hostLine, string, []string) {
		return nil, p.pkg + ".Host", []string{"S114"}
	}},
	{"literal", func(p hostProvider, _ func(string) hostLine) ([]hostLine, string, []string) {
		return nil, p.pkg + ".Host{}", []string{"S114"}
	}},
	{"zero", func(p hostProvider, _ func(string) hostLine) ([]hostLine, string, []string) {
		return []hostLine{{"let z " + p.pkg + ".Host", []string{"S114"}}}, "z", nil
	}},
	{"update", func(p hostProvider, judge func(string) hostLine) ([]hostLine, string, []string) {
		// A record update names a field; the only one is unexported.
		return []hostLine{judge("let h = " + p.pkg + ".NewHost()")}, p.pkg + ".Host{..h, live: h}", []string{"S114", "S104"}
	}},
	{"fnvalue", func(p hostProvider, _ func(string) hostLine) ([]hostLine, string, []string) {
		return []hostLine{{"let mk = " + p.pkg + ".NewHost", nil}}, "mk()", []string{"judge"}
	}},
	{"NewHost", func(p hostProvider, _ func(string) hostLine) ([]hostLine, string, []string) {
		return nil, p.pkg + ".NewHost()", []string{"judge"}
	}},
}

// hostUse is where the made value goes, in a function body.
type hostUse struct {
	name  string
	lines func(p hostProvider) []string // V marks the made value; it is on the first line
	// local: V is a provider of a local context, which is built at its
	// with, so a NewHost in V charges the with on the second line, and it
	// never widens an inferred row (rows.md, Composition laws 9).
	local bool
}

var hostUses = []hostUse{
	{name: "direct", lines: func(p hostProvider) []string { return p.direct }},
	{name: "inline", lines: func(p hostProvider) []string {
		return []string{"with context { " + p.pkg + "." + p.capName + ": V } { need() }"}
	}},
	{name: "local", local: true, lines: func(p hostProvider) []string {
		return []string{"let live = context { " + p.pkg + "." + p.capName + ": V }", "with live { need() }"}
	}},
	{name: "argument", lines: func(p hostProvider) []string { return []string{"take(V)"} }},
}

// hostPkgUses are the package-level uses, which have no site.
var hostPkgUses = []hostUse{
	{name: "pkg-let", lines: func(p hostProvider) []string { return []string{"let H = V"} }},
	{name: "pkg-context", lines: func(p hostProvider) []string {
		return []string{"let Live = context { " + p.pkg + "." + p.capName + ": V }"}
	}},
}

// hostSite is the declaration that holds the use: its head, whether its
// row holds Ambient, and the main that calls it, if any.
type hostSite struct {
	name    string
	head    string
	ambient bool
	helper  bool   // an unexported helper: its row is inferred, its caller judged
	caller  string // main of a helper; "" when the site is main
	mainSrc string // a main the program adds when the site is not main
}

var hostSites = []hostSite{
	{name: "exported-empty", head: "func Site() {", mainSrc: "func main() {}"},
	{name: "exported-ambient", head: "func Site() needs Ambient {", ambient: true, mainSrc: "func main() {}"},
	{name: "helper-main-ambient", head: "func site() {", helper: true, caller: "func main() needs Ambient {", ambient: true},
	{name: "helper-main-empty", head: "func site() {", helper: true, caller: "func main() {"},
	{name: "main-ambient", head: "func main() needs Ambient {", ambient: true},
	{name: "main-empty", head: "func main() {"},
	{name: "test", head: "test Site {", mainSrc: "func main() {}"},
}

// TestHostAuthorityDomain is the property of host-authority H1 over the
// rule's whole domain as written: provider {os, fs} × making form {bare
// name, T{}, zero let, record update, function value, NewHost()} × use
// {direct call, inline context, local context, argument} × site
// {exported function with an empty row, exported function with Ambient,
// unexported helper called from a main with and without Ambient, main
// with and without Ambient, test}, plus the package-level uses {let,
// named context}, which have no site. The oracle: any form but NewHost
// is S114 at the form (a record update adds the S104 of its hidden
// field); a NewHost call is S102 at the call unless the row of its site
// holds Ambient; an unexported helper's row is inferred, so its caller in
// main is judged instead; a local context's provider charges its with,
// and that need never widens an inferred row (rows.md, Composition laws
// 9); a package-level use is always S102. Code and line of every
// diagnostic are compared.
func TestHostAuthorityDomain(t *testing.T) {
	n := 0
	for _, p := range hostProviders {
		for _, m := range hostMakings {
			for _, u := range hostUses {
				for _, s := range hostSites {
					n++
					src, want := hostProgram(p, m, u, &s)
					judgeHostProgram(t, p.pkg+"/"+m.name+"/"+u.name+"/"+s.name, src, want)
				}
			}
			for _, u := range hostPkgUses {
				n++
				src, want := hostProgram(p, m, u, nil)
				judgeHostProgram(t, p.pkg+"/"+m.name+"/"+u.name, src, want)
			}
		}
	}
	if want := len(hostProviders) * len(hostMakings) * (len(hostUses)*len(hostSites) + len(hostPkgUses)); n != want || n < 300 {
		t.Fatalf("%d programs, want %d (at least 300)", n, want)
	}
}

// hostProgram writes the program of one point of the domain and the
// oracle's expected code@line list. s is nil for a package-level use.
func hostProgram(p hostProvider, m hostMaking, u hostUse, s *hostSite) (string, []string) {
	var lines []hostLine
	add := func(l hostLine) { lines = append(lines, l) }
	for _, text := range []string{
		"import " + p.pkg,
		"",
		"func take(_ " + p.pkg + ".Host) {}",
		"",
		"func need() needs " + p.pkg + "." + p.capName + " {}",
		"",
	} {
		add(hostLine{text: text})
	}
	// judge is the verdict of a NewHost call: S102 at the call unless the
	// row of its site holds Ambient. A helper's row is inferred, so the
	// call charges the helper and its caller in main is judged instead.
	callerCharged := false
	judge := func(text string) hostLine {
		switch {
		case s == nil:
			return hostLine{text: text, codes: []string{"S102"}}
		case s.helper && hostOracleReadsInference:
			callerCharged = true
			return hostLine{text: text}
		case s.helper || !s.ambient:
			return hostLine{text: text, codes: []string{"S102"}}
		}
		return hostLine{text: text}
	}
	indent := "\t"
	if s == nil {
		indent = ""
	} else {
		add(hostLine{text: s.head})
	}
	pre, v, vCodes := m.make(p, judge)
	for _, l := range pre {
		l.text = indent + l.text
		add(l)
	}
	charge := 0 // the use line a NewHost in V charges
	if u.local {
		charge = 1
	}
	for i, text := range u.lines(p) {
		l := hostLine{text: indent + strings.ReplaceAll(text, "V", v)}
		for _, c := range vCodes {
			switch {
			case c != "judge" && i == 0:
				l.codes = append(l.codes, c)
			case c == "judge" && i == charge && u.local && s.helper:
				// A local context's need never widens an inferred row.
				l.codes = append(l.codes, "S102")
			case c == "judge" && i == charge:
				l.codes = append(l.codes, judge("").codes...)
			}
		}
		add(l)
	}
	if s != nil {
		add(hostLine{text: "}"})
		add(hostLine{text: ""})
		switch {
		case s.helper:
			add(hostLine{text: s.caller})
			call := hostLine{text: "\tsite()"}
			if callerCharged && !s.ambient {
				call.codes = []string{"S102"}
			}
			add(call)
			add(hostLine{text: "}"})
		case s.mainSrc != "":
			add(hostLine{text: s.mainSrc})
		}
	}
	var b strings.Builder
	var want []string
	for i, l := range lines {
		b.WriteString(l.text + "\n")
		for _, c := range l.codes {
			want = append(want, c+"@"+strconv.Itoa(i+1))
		}
	}
	sort.Strings(want)
	return b.String(), want
}

func judgeHostProgram(t *testing.T, name, src string, want []string) {
	t.Helper()
	_, diags := check.CheckSource(src)
	have := got(diags)
	if strings.Join(want, " ") != strings.Join(have, " ") {
		t.Errorf("%s: want %v, got %v\n\t%s\n%s", name, want, have, describe(diags), src)
	}
}
