package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// planted is a generator's program: its text and the lines the reference
// predicate says a rule reports.
type planted struct {
	b    strings.Builder
	line int // the line the next written text starts on
	want []int
}

func newPlanted() *planted { return &planted{line: 1} }

// write adds text (one or more whole lines) and, when fires, records the
// line of its first line plus at.
func (p *planted) write(text string, fires bool, at int) {
	if fires {
		p.want = append(p.want, p.line+at)
	}
	p.b.WriteString(text)
	p.b.WriteString("\n")
	p.line += strings.Count(text, "\n") + 1
}

func (p *planted) check(t *testing.T, rule, name string, seed uint64) {
	t.Helper()
	src := p.b.String()
	got := ruleLines(t, rule, src)
	want := slices.Clone(p.want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("seed %d: %s fired on lines %v, the reference says %v\n%s\nreplay: go test ./internal/sketch/lint -run %s -lint.seed=%d", seed, rule, got, want, numbered(src), name, seed)
	}
}

// A Go import of the foreign-where-std generator: the import line, a use,
// and whether std covers it. Std's own strings is the control a spelling
// rule would confuse with Go's.
type goImport struct {
	name, line, use string
	covered         bool
}

var goImports = []goImport{
	{"strings", "import go strings", "strings.ToUpper(\"x\")", true},
	{"strings", "import strings", "strings.ToUpper(\"x\")", false},
	{"utf8", "import go unicode/utf8", "\"\\(utf8.RuneLen(65))\"", true},
	{"hex", "import go encoding/hex", "\"\\(hex.EncodedLen(3))\"", true},
	{"unicode", "import go unicode", "\"\\(unicode.IsUpper(65))\"", false},
	{"path", "import go path", "path.Base(\"a/b\")", false},
	{"html", "import go html", "html.EscapeString(\"x\")", false},
}

// TestForeignWhereStdAgreesWithReference plants Go and std imports and
// checks that foreign-where-std fires on exactly the Go imports std's
// coverage table names, and not on std's strings spelled like Go's.
func TestForeignWhereStdAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xf5))
		p := newPlanted()
		taken := map[string]bool{}
		var uses []string
		for _, i := range r.Perm(len(goImports)) {
			g := goImports[i]
			if taken[g.name] || r.IntN(3) == 0 {
				continue
			}
			taken[g.name] = true
			p.write(g.line, g.covered, 0)
			uses = append(uses, g.use)
		}
		p.write("\nfunc main() needs Console | go.Foreign {", false, 0)
		for _, u := range uses {
			p.write("\tConsole.Println("+u+")", false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "foreign-where-std", "TestForeignWhereStdAgreesWithReference", seed)
	}
}

// A collection kind of the unbounded-collect generator: a function body
// over q (a queue.Queue[int]) and xs (a []int), and the line of the body
// the rule reports, or -1.
type collectKind struct {
	name string
	at   int
	body string
}

var collectKinds = []collectKind{
	{"collect", 0, "slices.Collect(q.Values())"},
	{"sorted", 0, "slices.Sorted(q.Values())"},
	{"collect-let", 1, "let vs = q.Values()\n\tslices.Collect(vs)"},
	{"collect-bounded", -1, "slices.Collect(slices.Values(xs))"},
	// a program function named like the producer, with no Unbounded fact
	{"collect-own", -1, "slices.Collect(Values(xs))"},
	// a program function whose doc states the fact
	{"collect-marked", 0, "slices.Collect(Endless(xs))"},
	// "Unbounded:" inside a paragraph is prose, not the fact
	{"collect-prose", -1, "slices.Collect(Bounded(xs))"},
	{"loop-append", 1, "let mut out = []int{}\n\tfor v in q.Values() {\n\t\tout = out.Append(v)\n\t}\n\tout"},
	{"loop-break", -1, "let mut out = []int{}\n\tfor v in q.Values() {\n\t\tout = out.Append(v)\n\t\tif len(out) > 3 {\n\t\t\tbreak\n\t\t}\n\t}\n\tout"},
	// a break of an inner loop does not leave the outer one
	{"loop-inner-break", 1, "let mut out = []int{}\n\tfor v in q.Values() {\n\t\tout = out.Append(v)\n\t\tfor w in xs {\n\t\t\tif w > v {\n\t\t\t\tbreak\n\t\t\t}\n\t\t}\n\t}\n\tout"},
	{"loop-handle", -1, "let mut n = 0\n\tfor v in q.Values() {\n\t\tn = n + v\n\t}\n\t[]int{n}"},
	{"loop-bounded", -1, "let mut out = []int{}\n\tfor v in slices.Values(xs) {\n\t\tout = out.Append(v)\n\t}\n\tout"},
}

const collectPrelude = `import queue
import slices

func Values(xs []int) Seq[int] {
	slices.Values(xs)
}

// Endless gives xs, then waits.
//
// Unbounded: it ends only when its source is closed.
func Endless(xs []int) Seq[int] {
	slices.Values(xs)
}

// Bounded gives xs. It is not what the old notes called
// Unbounded: it ends with xs.
func Bounded(xs []int) Seq[int] {
	slices.Values(xs)
}
`

// TestUnboundedCollectAgreesWithReference plants collections of std's
// unbounded queue.Values, of bounded Seqs, of a program function named
// like it and of a program function whose doc states Unbounded:, and
// checks that unbounded-collect fires exactly on the collections of an
// unbounded producer that never leave early.
func TestUnboundedCollectAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xc0))
		p := newPlanted()
		p.write(collectPrelude, false, 0)
		for i := 0; i < 1+r.IntN(5); i++ {
			k := collectKinds[r.IntN(len(collectKinds))]
			p.write(fmt.Sprintf("\nfunc f%d(q queue.Queue[int], xs []int) []int {", i), false, 0)
			p.write("\t"+k.body, k.at >= 0, k.at)
			p.write("}", false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "unbounded-collect", "TestUnboundedCollectAgreesWithReference", seed)
	}
}

// A lib declaration of the deprecated generator: its text in package lib
// (i is its index), the root's use of it (one line), and whether the
// declaration is deprecated.
type libDecl struct {
	name       string
	deprecated bool
	decl, use  func(i int) string
}

var libDecls = []libDecl{
	{"func-old", true,
		func(i int) string {
			return fmt.Sprintf("// F%d is old.\n//\n// Deprecated: use G%d.\nfunc F%d() int {\n\t%d\n}", i, i, i, i)
		},
		func(i int) string { return fmt.Sprintf("let a%d = lib.F%d()", i, i) }},
	{"func-new", false,
		func(i int) string { return fmt.Sprintf("// F%d is new.\nfunc F%d() int {\n\t%d\n}", i, i, i) },
		func(i int) string { return fmt.Sprintf("let a%d = lib.F%d()", i, i) }},
	// "Deprecated:" inside a paragraph is prose, not the fact
	{"func-prose", false,
		func(i int) string {
			return fmt.Sprintf("// F%d replaced the old form, which the notes call\n// Deprecated: no longer.\nfunc F%d() int {\n\t%d\n}", i, i, i)
		},
		func(i int) string { return fmt.Sprintf("let a%d = lib.F%d()", i, i) }},
	{"type-old", true,
		func(i int) string {
			return fmt.Sprintf("// T%d is old.\n//\n// Deprecated: use U.\ntype T%d { N int }", i, i)
		},
		func(i int) string { return fmt.Sprintf("let a%d = lib.T%d{N: 1}.N", i, i) }},
	{"op-old", true,
		func(i int) string {
			return fmt.Sprintf("type S%d {\n\t// Get is old.\n\t//\n\t// Deprecated: use Fetch.\n\tGet() int,\n\tFetch() int,\n}", i)
		},
		func(i int) string { return fmt.Sprintf("let a%d = useS%d(s%d)", i, i, i) }},
}

// TestDeprecatedAgreesWithReference writes a module whose package lib
// declares functions, a type and operations with and without a
// Deprecated: paragraph, uses each in lib and in the root, and declares
// root functions spelled like lib's deprecated ones, one of them
// deprecated itself. deprecated fires exactly on the root's uses of lib's
// deprecated declarations: not on prose, not inside the declaring package,
// not on a same-spelled root declaration.
func TestDeprecatedAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(25) {
		r := rand.New(rand.NewPCG(seed, 0xde))
		var lib strings.Builder
		root := newPlanted()
		root.write("import m/lib\n", false, 0)
		var helpers, params []string
		n := 1 + r.IntN(5)
		kinds := make([]libDecl, n)
		for i := range kinds {
			kinds[i] = libDecls[r.IntN(len(libDecls))]
			fmt.Fprintf(&lib, "%s\n\n", kinds[i].decl(i))
			if kinds[i].name == "op-old" {
				fmt.Fprintf(&lib, "func inner%d(s S%d) int {\n\ts.Get()\n}\n\n", i, i)
				helpers = append(helpers, fmt.Sprintf("func useS%d(s lib.S%d) int {", i, i), "\ts.Get()", "}", "")
				params = append(params, fmt.Sprintf("s%d lib.S%d", i, i))
			} else if kinds[i].name != "type-old" {
				fmt.Fprintf(&lib, "func inner%d() int {\n\tF%d()\n}\n\n", i, i)
			}
		}
		// a root function spelled like lib's, deprecated in the root
		root.write("// F0 is the root's own.\n//\n// Deprecated: the root keeps it.\nfunc F0() int {\n\t0\n}\n", false, 0)
		for i, h := range helpers {
			root.write(h, i%4 == 1, 0)
		}
		root.write("func run("+strings.Join(params, ", ")+") int {", false, 0)
		root.write("\tlet z = F0()", false, 0)
		for i, k := range kinds {
			root.write("\t"+k.use(i), k.deprecated && k.name != "op-old", 0)
		}
		sum := []string{"z"}
		for i := range kinds {
			sum = append(sum, fmt.Sprintf("a%d", i))
		}
		root.write("\t"+strings.Join(sum, " + ")+"\n}\n\nfunc main() {\n}", false, 0)
		dir := t.TempDir()
		for name, text := range map[string]string{"ergo.mod": "module m\n", "lib/lib.ergo": lib.String(), "main.ergo": root.b.String()} {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := sketchfront.CheckDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			t.Fatalf("seed %d: the module does not check: %v\nlib:\n%s\nroot:\n%s", seed, a.Diagnostics, numbered(lib.String()), numbered(root.b.String()))
		}
		var got []int
		for _, f := range onlyRule(t, "deprecated").Run(in).Findings {
			if f.File != "main.ergo" {
				t.Fatalf("seed %d: deprecated fired in %s, the declaring package: %+v", seed, f.File, f)
			}
			got = append(got, f.Line)
		}
		if !slices.Equal(got, root.want) {
			t.Fatalf("seed %d: deprecated fired on lines %v, the reference says %v\nlib:\n%s\nroot:\n%s\nreplay: go test ./internal/sketch/lint -run TestDeprecatedAgreesWithReference -lint.seed=%d", seed, got, root.want, numbered(lib.String()), numbered(root.b.String()), seed)
		}
	}
}

// onlyRule is the built-in registry configured with rule alone, at
// warning.
func onlyRule(t *testing.T, rule string) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings[rule] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// A race kind of the race-for-timeout generator: the race's text (at the
// indent of a function body), whether the rule fires, and whether it
// offers a fix.
type raceKind struct {
	name       string
	fires, fix bool
	text       string
}

const (
	raceBody    = "\t\tfunc() int needs Scheduler {\n\t\t\twork()\n\t\t},\n"
	raceSleeper = "\t\tfunc() int needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\t0\n\t\t},\n"
)

var raceKinds = []raceKind{
	{"sleep-second", true, true, "tasks.Race([]func() int needs Scheduler{\n" + raceBody + raceSleeper + "\t})"},
	{"sleep-first", true, true, "tasks.Race([]func() int needs Scheduler{\n" + raceSleeper + raceBody + "\t})"},
	{"sleep-return", true, true, "tasks.Race([]func() int needs Scheduler{\n" + raceBody + "\t\tfunc() int needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\treturn -1\n\t\t},\n\t})"},
	// a fallback that runs an effect is no plain timeout
	{"fallback-effect", false, false, "tasks.Race([]func() int needs Scheduler{\n" + raceBody + "\t\tfunc() int needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\twork()\n\t\t},\n\t})"},
	// two sleeps: no body to put under a timeout
	{"both-sleep", false, false, "tasks.Race([]func() int needs Scheduler{\n" + raceSleeper + raceSleeper + "\t})"},
	{"three", false, false, "tasks.Race([]func() int needs Scheduler{\n" + raceBody + raceSleeper + raceBody + "\t})"},
	// a program function spelled like tasks.Race
	{"own-race", false, false, "Race([]func() int needs Scheduler{\n" + raceBody + raceSleeper + "\t})"},
	// the race already fails timeout.Expired: a fix's catch would take it
	{"fails-expired", true, false, "tasks.Race([]func() int fails timeout.Expired needs Scheduler{\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\ttry late()\n\t\t},\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\t0\n\t\t},\n\t}) catch {\n\tcase timeout.Expired:\n\t\t-2\n\t}"},
	// the same, in a block that declares the failure, so no catch is on it
	{"fails-expired-declared", true, false, "let f = func() int fails timeout.Expired needs Scheduler { tasks.Race([]func() int fails timeout.Expired needs Scheduler{\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\ttry late()\n\t\t},\n\t\tfunc() int fails timeout.Expired needs Scheduler {\n\t\t\tScheduler.Sleep(2s)\n\t\t\t0\n\t\t},\n\t}) }\n\tf() catch {\n\tcase timeout.Expired:\n\t\t-2\n\t}"},
}

const racePrelude = `import tasks
import timeout

func work() int needs Scheduler {
	Scheduler.Sleep(5s)
	7
}

func late() int fails timeout.Expired {
	timeout.Expired
}

func Race(blocks []func() int needs Scheduler) int needs Scheduler {
	blocks[0]()
}
`

// raceFixes runs race-for-timeout over src and returns the lines it fires
// on and the lines whose finding has a fix.
func raceFixes(t *testing.T, src string) (lines, fixed []int) {
	t.Helper()
	in := lintInput(src)
	if !in.Checked {
		t.Fatalf("the generated program does not check: %v\n%s", sketchfront.Check(src).Diagnostics, numbered(src))
	}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range conf.Run(in).Findings {
		if f.Rule != "race-for-timeout" {
			continue
		}
		lines = append(lines, f.Line)
		if f.Fix != nil {
			fixed = append(fixed, f.Line)
		}
	}
	return lines, fixed
}

// TestRaceForTimeoutAgreesWithReference plants races of every kind and
// checks that race-for-timeout fires, and offers its fix, exactly where
// the kinds say.
func TestRaceForTimeoutAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x7a))
		p := newPlanted()
		p.write(racePrelude, false, 0)
		var fixes []int
		for i := 0; i < 1+r.IntN(4); i++ {
			k := raceKinds[r.IntN(len(raceKinds))]
			p.write(fmt.Sprintf("\nfunc r%d() int needs Scheduler {", i), false, 0)
			if k.fix {
				fixes = append(fixes, p.line)
			}
			p.write("\t"+k.text, k.fires, 0)
			p.write("}", false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		src := p.b.String()
		lines, fixed := raceFixes(t, src)
		if !slices.Equal(lines, p.want) || !slices.Equal(fixed, fixes) {
			t.Fatalf("seed %d: race-for-timeout fired on %v with fixes on %v; the reference says %v and %v\n%s\nreplay: go test ./internal/sketch/lint -run TestRaceForTimeoutAgreesWithReference -lint.seed=%d", seed, lines, fixed, p.want, fixes, numbered(src), seed)
		}
	}
}
