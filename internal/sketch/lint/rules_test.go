package lint_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// ruleLines runs the built-in registry with only rule on and returns the
// lines its findings start on.
func ruleLines(t *testing.T, rule, src string) []int {
	t.Helper()
	in := lintInput(src)
	if !in.Checked {
		t.Fatalf("the generated program does not check:\n%s", src)
	}
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings[rule] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := conf.Run(in)
	if !res.Complete {
		t.Fatalf("the run is incomplete: %+v", res.Rules)
	}
	var lines []int
	for _, f := range res.Findings {
		if f.Rule != rule {
			t.Fatalf("unexpected finding %+v", f)
		}
		lines = append(lines, f.Line)
	}
	return lines
}

// An arm kind of the silent-catch generator: its text, whether its body
// leaves no trace (the reference predicate), and what it needs of the
// catch: which error type it names.
type armKind struct {
	name   string
	silent bool
	text   func(t string) string // t is Bad or Other
}

var armKinds = []armKind{
	{"value", true, func(t string) string { return "case " + t + ":\n\t\t0" }},
	{"return", false, func(t string) string { return "case " + t + ":\n\t\treturn 0" }},
	{"reads", false, func(t string) string {
		if t == "Bad" {
			return "case Bad as b:\n\t\tlen(b.Reason)"
		}
		return "case Other as o:\n\t\to.Code"
	}},
	// a pure helper spelled like a logging call: no trace
	{"pure-logname", true, func(t string) string { return "case " + t + ":\n\t\tlogFailure(1)" }},
	// an effectful helper with a neutral name: a trace
	{"effect", false, func(t string) string { return "case " + t + ":\n\t\ttally(1)\n\t\t0" }},
	// a capability operation called directly: a trace
	{"capability", false, func(t string) string { return "case " + t + ":\n\t\tConsole.Println(\"seen\")\n\t\t0" }},
	// a local spelled like a sibling arm's binding, read: still no trace
	{"shadow", true, func(t string) string { return "case " + t + ":\n\t\tlet b = 3\n\t\tlet o = b\n\t\to" }},
	// fails on through try: a trace
	{"try", false, func(t string) string { return "case " + t + ":\n\t\ttry risky(2)" }},
	// a function literal that returns, made and not called: its return is
	// its own frame's, so no trace
	{"literal", true, func(t string) string {
		return "case " + t + ":\n\t\tlet f = func() int { return 1 }\n\t\tlet _ = f\n\t\t0"
	}},
}

const silentCatchPrelude = `type Bad { Reason string }

type Other { Code int }

func risky(n int) int fails Bad | Other {
	if n == 0 {
		return Bad{Reason: "zero"}
	}
	if n == 1 {
		return Other{Code: 1}
	}
	n
}

func logFailure(n int) int {
	n + 1
}

func tally(n int) needs Console {
	Console.Println("tally \(n)")
}
`

// TestSilentCatchAgreesWithReference plants catch arms of every kind,
// alone or beside a case _ or a case err that reads its binding, and
// checks that silent-catch fires on exactly the arms whose kind leaves no
// trace. The controls defeat a spelling rule: a pure helper named like a
// logging call, an effectful helper with a neutral name, and a local
// spelled like a sibling arm's binding.
func TestSilentCatchAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(60) {
		r := rand.New(rand.NewPCG(seed, 0x5c))
		var b strings.Builder
		b.WriteString(silentCatchPrelude)
		var want []int
		line := strings.Count(silentCatchPrelude, "\n") + 1
		emit := func(s string) {
			b.WriteString(s)
			b.WriteString("\n")
			line += strings.Count(s, "\n") + 1
		}
		for f := 0; f < 1+r.IntN(4); f++ {
			emit(fmt.Sprintf("\nfunc f%d(n int) int fails Bad | Other needs Console {", f))
			emit("\tlet r = risky(n) catch {")
			first := armKinds[r.IntN(len(armKinds))]
			if first.silent {
				want = append(want, line)
			}
			emit("\t" + first.text("Bad"))
			switch r.IntN(3) {
			case 0:
				second := armKinds[r.IntN(len(armKinds))]
				if second.silent {
					want = append(want, line)
				}
				emit("\t" + second.text("Other"))
			case 1:
				want = append(want, line)
				emit("\tcase _:\n\t\t0")
			default:
				emit("\tcase err:\n\t\treturn err")
			}
			emit("\t}\n\tr\n}")
		}
		src := b.String()
		got := ruleLines(t, "silent-catch", src)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: silent-catch fired on lines %v, the reference says %v\n%s\nreplay: go test ./internal/sketch/lint -run TestSilentCatchAgreesWithReference -lint.seed=%d", seed, got, want, numbered(src), seed)
		}
	}
}

// A declaration kind of the foreign-error-public generator.
type declKind struct {
	name  string
	fires bool
	text  func(i int) string
}

var declKinds = []declKind{
	{"exported-go", true, func(i int) string { return fmt.Sprintf("func Load%d(n int) int fails go.Error {\n\tn\n}", i) }},
	{"exported-both", true, func(i int) string { return fmt.Sprintf("func Fetch%d(n int) int fails Error | go.Error {\n\tn\n}", i) }},
	// an unexported twin: not public
	{"unexported-go", false, func(i int) string { return fmt.Sprintf("func load%d(n int) int fails go.Error {\n\tn\n}", i) }},
	// the program's own type spelled Error: not Go's
	{"own-error", false, func(i int) string { return fmt.Sprintf("func Parse%d(n int) int fails Error {\n\tn\n}", i) }},
	{"no-row", false, func(i int) string { return fmt.Sprintf("func Count%d(n int) int {\n\tn\n}", i) }},
	// a local spelled go and a parameter typed by the program's Error
	{"shadow", false, func(i int) string {
		return fmt.Sprintf("func Check%d(e Error) int fails Error {\n\tlet goal = len(e.Message)\n\tgoal\n}", i)
	}},
}

// TestForeignErrorPublicAgreesWithReference plants function declarations
// of every kind and checks that foreign-error-public fires on exactly the
// exported functions whose written row holds Go's go.Error, not the
// program's own type spelled Error.
func TestForeignErrorPublicAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xfe))
		var b strings.Builder
		b.WriteString("type Error { Message string }\n")
		line := 2
		var want []int
		for i := 0; i < 1+r.IntN(6); i++ {
			k := declKinds[r.IntN(len(declKinds))]
			b.WriteString("\n")
			line++
			if k.fires {
				want = append(want, line)
			}
			text := k.text(i)
			b.WriteString(text + "\n")
			line += strings.Count(text, "\n") + 1
		}
		b.WriteString("\nfunc main() {\n}\n")
		src := b.String()
		got := ruleLines(t, "foreign-error-public", src)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: foreign-error-public fired on lines %v, the reference says %v\n%s\nreplay: go test ./internal/sketch/lint -run TestForeignErrorPublicAgreesWithReference -lint.seed=%d", seed, got, want, numbered(src), seed)
		}
	}
}

func numbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d  %s\n", i+1, l)
	}
	return b.String()
}
