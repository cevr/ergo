package lower

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
)

// workCopyNames renames std machines' work declarations to a program's
// own: the record, its field, the two builders and outcome's helper.
var workCopyNames = [][2]string{
	{"Work", "Job"}, {"Start", "Begin"}, {"Do", "doJob"}, {"Attempt", "attemptJob"}, {"outcome", "outcomeOf"},
}

// workDecls is the text of std machines' declarations of Work, Do,
// Attempt and outcome, read from the std source (each from its doc
// comment's end to the closing brace at column 0, or the one-line type).
func workDecls(t *testing.T) string {
	files, ok := std.Package("machines")
	if !ok {
		t.Fatal("no std machines")
	}
	var all strings.Builder
	for _, text := range files {
		all.WriteString(text + "\n")
	}
	src := all.String()
	var out []string
	for _, head := range []string{"type Work[", "func Do[", "func Attempt[", "func outcome["} {
		i := strings.Index(src, "\n"+head)
		if i < 0 {
			t.Fatalf("std machines declares no %s", head)
		}
		rest := src[i+1:]
		end := strings.Index(rest, "\n}\n")
		if line := rest[:strings.Index(rest, "\n")]; strings.HasSuffix(line, "}") {
			end = len(line) - 1 // a one-line type
		}
		out = append(out, rest[:end+2])
	}
	return strings.Join(out, "\n\n")
}

// renameWords replaces each whole word from of pairs by its to (from > to
// when back).
func renameWords(s string, back bool) string {
	for _, p := range workCopyNames {
		from, to := p[0], p[1]
		if back {
			from, to = to, from
		}
		s = regexp.MustCompile(`\b`+from+`\b`).ReplaceAllString(s, to)
	}
	return s
}

// workProgram is a machine whose one state owns the work of form, written
// with std machines' names for the work record and its builders, or
// the program's own copy's (workCopyNames).
func workProgram(form string, std bool) string {
	work, do, attempt, start := "Job", "doJob", "attemptJob", "Begin"
	if std {
		work, do, attempt, start = "machines.Work", "machines.Do", "machines.Attempt", "Start"
	}
	var body string
	switch form {
	case "do":
		body = do + `(func() Signal needs Source { Got{N: Source.Count(q.Query)} })`
	case "attempt":
		body = attempt + `(func() int fails Missing needs Source { try Source.Find(q.Query) }, func(n int) Signal { Got{N: n} }, func(failure) {
			let missing Signal = failure
			missing
		})`
	case "listen":
		body = work + `{` + start + `: func(send entry func(e Signal)) needs Source {
			for i in 0..Source.Count(q.Query) {
				send(Got{N: i})
			}
		}}`
	}
	return `import actor
import machines
import time

type Phase = Asking{Query string} | Told{N int}
type Missing {}
type Signal = Got{N int} | Missing

type Source { Count(query string) int, Find(query string) int fails Missing }

type Fixed {}

func Count(f Fixed, query string) int { len(query) }

func Find(f Fixed, query string) int fails Missing {
	if query == "" {
		return Missing
	}
	len(query)
}

func work(s Phase) Option[` + work + `[Signal, Source]] {
	match s {
	case Asking as q:
		Some{` + body + `}
	case Told:
		None
	}
}

func step(s Phase, e Signal) Decision[Phase, int] {
	match e {
	case Got as g:
		Enter[Phase]{Next: Told{N: g.N}}
	case Missing:
		Ignore
	}
}

type Asker machine[Phase, Signal] { initial: start, step: step, work: work }

func start(q string) Phase { Asking{Query: q} }

func main() needs Console {
	with context { Scheduler: time.NewManual(0), Source: Fixed } {
		let a = actor.StartMachine(machines.Start(Asker{}, "abc"), 1, func(n int) {})
		for st in a.Changes() {
			match st {
			case Asking:
			case Told as t:
				Console.Println("\(t.N)")
				break
			}
		}
		a.Stop()
	}
}
`
}

// goDecls is the lowered Go of a program as its top-level declarations,
// sorted, with //line directives dropped: the order in which the
// lowering emits a package's and std's declarations is not the program's.
func goDecls(text string) []string {
	var lines []string
	position := regexp.MustCompile(`/\*line [^*]*\*/`)
	for _, l := range strings.Split(text, "\n") {
		l = position.ReplaceAllString(l, "")
		if !strings.HasPrefix(l, "//") && strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	var decls []string
	var cur []string
	for _, l := range lines {
		if l != "" && l[0] != ' ' && l[0] != '\t' && l[0] != '}' && l[0] != ')' && len(cur) > 0 {
			decls = append(decls, strings.Join(cur, "\n"))
			cur = nil
		}
		cur = append(cur, l)
	}
	decls = append(decls, strings.Join(cur, "\n"))
	// The source tables (#230) name each call and declaration by its Ergo
	// name and line, which the copy changes by design.
	decls = slices.DeleteFunc(decls, func(d string) bool {
		return strings.HasPrefix(d, "var ergoCalls ") || strings.HasPrefix(d, "var ergoDecls ")
	})
	sort.Strings(decls)
	return decls
}

// TestWorkIsAnOrdinaryRecord (STD1b, NP3b; runtime.md, Machines and
// actors 12): std machines' Work, Do, Attempt and outcome, copied into a
// program under other names (workCopyNames), check, and a machine whose
// work uses the copy lowers to the same Go as the machine that uses std's,
// once the copy's names are read back, for one machine per form (Do,
// Attempt, and a listener that writes Start). So the work entry, the child
// rule and the work builders read declarations, never std's names. Mutant
// work-entry-reads-std-name (machineWorkRow accepts only a record named
// Work): the copy is refused.
func TestWorkIsAnOrdinaryRecord(t *testing.T) {
	copied := renameWords(workDecls(t), false)
	for _, form := range []string{"do", "attempt", "listen"} {
		t.Run(form, func(t *testing.T) {
			viaStd := mustLower(t, form+"-std", workProgram(form, true))
			viaCopy := mustLower(t, form+"-copy", workProgram(form, false)+"\n"+copied+"\n")
			a, b := goDecls(goMain(t, viaStd)), goDecls(renameWords(goMain(t, viaCopy), true))
			// Every declaration of the std program is in the copy's; the
			// copy has more only where it declares a builder the std
			// program's lowering leaves out because it calls none.
			inCopy := map[string]bool{}
			for _, d := range b {
				inCopy[d] = true
			}
			for _, d := range a {
				if !inCopy[d] {
					t.Errorf("a declaration of the std program is not in the copy's:\n%s\n\ncopy source:\n%s\n\ncopy's Go:\n%s", d, copied, strings.Join(b, "\n"))
				}
				delete(inCopy, d)
			}
			for d := range inCopy {
				if !strings.HasPrefix(d, "func Do[") && !strings.HasPrefix(d, "func Attempt[") && !strings.HasPrefix(d, "func outcome[") {
					t.Errorf("the copy's Go has a declaration the std program's has not:\n%s", d)
				}
			}
		})
	}
}

// goMain is the main.go a lowering emitted.
func goMain(t *testing.T, o *lowered) string {
	for _, f := range o.files {
		if f.Path == "main.go" {
			return string(f.Data)
		}
	}
	t.Fatalf("%s: no main.go", o.name)
	return ""
}
