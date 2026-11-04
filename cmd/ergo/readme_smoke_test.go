package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestReadmeSmoke keeps README.md truthful, as Rust doctests and Go Example
// tests keep documentation executable. The README opens with a program that
// runs, then presents the decided language (docs/spec), so each claim is held
// to what runs today:
//
//   - the first go block is examples/checkout/main.ergo, byte for byte, so the
//     opening program is a gated example (scripts/examples builds it, runs
//     its case and its tests);
//   - every fence has the one strict style (markdownlint MD046/MD048): a
//     backtick fence at column 0, opened as ```<lang> with lowercase
//     letters and closed by a bare ``` on its own line. Any other line
//     that contains ``` or ~~~ fails, whatever its prefix (indent,
//     blockquote, list item), and so do an unclosed fence and a carriage
//     return anywhere, so the blocks read here are exactly the blocks
//     CommonMark renders;
//   - every go block parses and is canonical under the 2026 parser and
//     formatter, and uses no form the specification removed: `uses`
//     (#217), `nil` (types.md) or a prefix catch (rows.md); nor a Close
//     in `scope.OnExit`, which the README never shows because resources
//     release themselves (resources.md, `scope.OnExit`);
//   - the quoted S102 line is what `ergo diagnostics` prints for
//     readmeSmokeS102Source;
//   - each `./bin/ergo` command under "Working in this repo" exits 0 and
//     prints the output its comment names; a `test` command's comment
//     names the runner's summary, its last line.
//
// TODO(#196): once the 2026 checker lands, also type-check each block
// against declarations of the names it leaves undeclared, so a block that
// drops a failure from a row fails here too.

var (
	// readmeSmokeFenceLike is any line that holds three backticks or three
	// tildes: CommonMark may read it as a fence inside a container
	// (blockquote, list item) or after an indent.
	readmeSmokeFenceLike = regexp.MustCompile("```|~~~")
	// readmeSmokeOpener is the one admitted opening fence.
	readmeSmokeOpener = regexp.MustCompile("^```([a-z]+)$")
	// readmeSmokeDiagnostics is the console block that quotes the S102 line.
	readmeSmokeDiagnostics = regexp.MustCompile("(?ms)^```console\n\\$ ergo diagnostics pay\\.ergo\n(.*?)^```$")
	// readmeSmokeShBlock is the shell block of "Working in this repo".
	readmeSmokeShBlock = regexp.MustCompile("(?ms)^## Working in this repo\n.*?^```sh\n(.*?)^```$")
	// readmeSmokeCommand is one `./bin/ergo` line, with an optional
	// comment; text after the comment's first ": " is the expected output,
	// one line per " / ".
	readmeSmokeCommand = regexp.MustCompile(`^\./bin/ergo ([^#]*?)\s*(?:#\s*(.*))?$`)
)

// readmeSmokeMinBlocks matches the acceptance check of the README unit.
const readmeSmokeMinBlocks = 5

// readmeSmokeOpening is the example program that the README's first go
// block must equal (lower-v1 stage 5, D9).
const readmeSmokeOpening = "examples/checkout/main.ergo"

// readmeSmokeS102Source is the source file behind the README's quoted
// diagnostic: pay calls Gateway, which its needs clause does not list.
const readmeSmokeS102Source = `type OrderNotFound { ID string }
type GatewayDown {}

type Order {
	ID    string,
	Total int64,
}

type Orders {
	Find(id string) Order fails OrderNotFound,
}

type Gateway {
	Charge(order Order) string fails GatewayDown,
}

func pay(id string) string fails OrderNotFound | GatewayDown needs Orders {
	let order = try Orders.Find(id)
	return Gateway.Charge(order)
}

func main() needs Console {
	Console.Println("unused")
}
`

// readmeGoBlocks returns the body of every go block of a README in the
// strict fence style, and a problem for each line that breaks the style.
// Inside a block, a fence-like line other than the bare closer is a
// problem too: CommonMark might close the block there or keep the line as
// code, and the style removes that question.
func readmeGoBlocks(readme string) ([]string, []string) {
	var blocks, problems []string
	var body []string
	lang, open := "", false
	for index, line := range strings.Split(readme, "\n") {
		if !readmeSmokeFenceLike.MatchString(line) {
			if open {
				body = append(body, line)
			}
			continue
		}
		switch opener := readmeSmokeOpener.FindStringSubmatch(line); {
		case !open && opener != nil:
			lang, open, body = opener[1], true, nil
		case open && line == "```":
			if lang == "go" {
				blocks = append(blocks, strings.Join(body, "\n")+"\n")
			}
			open = false
		default:
			problems = append(problems, fmt.Sprintf("line %d: non-canonical fence %q: open with ```<lang> and close with a bare ```, both at column 0", index+1, line))
		}
	}
	if open {
		problems = append(problems, fmt.Sprintf("unclosed fence ```%s at the end of the README", lang))
	}
	return blocks, problems
}

// readmeProblems reports every fence that breaks the strict style and
// every inadmissible go block of a README.
func readmeProblems(readme string) []string {
	blocks, problems := readmeGoBlocks(readme)
	if index := strings.IndexByte(readme, '\r'); index >= 0 {
		problems = append(problems, fmt.Sprintf("line %d: carriage return: the README uses LF line endings only", strings.Count(readme[:index], "\n")+1))
	}
	if len(blocks) < readmeSmokeMinBlocks {
		return append(problems, fmt.Sprintf("README has %d go blocks, want at least %d", len(blocks), readmeSmokeMinBlocks))
	}
	for index, block := range blocks {
		if problem := readmeBlockProblem(block); problem != nil {
			problems = append(problems, fmt.Sprintf("README go block %d: %v", index+1, problem))
		}
	}
	return problems
}

// readmeBlockProblem reports why one README go block is not admissible
// source of the decided language, or nil when it is.
func readmeBlockProblem(source string) error {
	file, err := syntax.Parse(source)
	if err != nil {
		// S001 covers `uses` as a clause, since it is not a keyword, and a
		// prefix catch, which no production admits.
		if located, ok := err.(*syntax.Error); ok {
			return fmt.Errorf("line %d, column %d: %s %s", located.Line, located.Column, located.Code, located.Message)
		}
		return err
	}
	var refused []string
	lineOf := func(pos syntax.Pos) int {
		return strings.Count(source[:int(pos)], "\n") + 1
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if onExit, ok := n.(*syntax.CallExpr); ok && isScopeOnExit(onExit) {
			// Ordinary exit cleanup is valid Ergo; a Close on exit is what
			// the README promises the reader never writes.
			syntax.Inspect(onExit.Trailing, func(m syntax.Node) bool {
				if call, ok := m.(*syntax.CallExpr); ok {
					if selector, ok := call.Fun.(*syntax.SelectorExpr); ok && selector.Sel.Name == "Close" {
						refused = append(refused, fmt.Sprintf("line %d: a resource releases itself; the README shows no deferred Close (resources.md)", lineOf(onExit.Pos())))
					}
				}
				return true
			})
			return true
		}
		ident, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		line := lineOf(ident.NamePos)
		switch ident.Name {
		case "nil":
			refused = append(refused, fmt.Sprintf("line %d: Ergo has no nil (docs/spec/types.md)", line))
		case "uses":
			refused = append(refused, fmt.Sprintf("line %d: `uses` was replaced by `needs` and `provides` (#217)", line))
		}
		return true
	})
	if len(refused) > 0 {
		return fmt.Errorf("%s", strings.Join(refused, "; "))
	}
	if formatted := format.File(file); formatted != source {
		return fmt.Errorf("not canonical; ergo fmt prints:\n%s", formatted)
	}
	return nil
}

// firstLineDiff names the first line where got and want differ.
func firstLineDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\n  README  %q\n  example %q", i+1, a, b)
		}
	}
	return ""
}

// isScopeOnExit reports whether call is `scope.OnExit() { … }`, the
// cleanup a scope runs when it exits (resources.md).
func isScopeOnExit(call *syntax.CallExpr) bool {
	selector, ok := call.Fun.(*syntax.SelectorExpr)
	if !ok || selector.Sel.Name != "OnExit" || call.Trailing == nil {
		return false
	}
	scope, ok := selector.X.(*syntax.Ident)
	return ok && scope.Name == "scope"
}

func TestReadmeSmoke(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)

	t.Run("blocks", func(t *testing.T) {
		t.Parallel()
		for _, problem := range readmeProblems(readme) {
			t.Error(problem)
		}
	})

	// The opening program is the checkout example, so the gate's examples
	// step runs what the README shows first. An edit of one and not the
	// other fails here.
	t.Run("opening-is-the-checkout-example", func(t *testing.T) {
		t.Parallel()
		example, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(readmeSmokeOpening)))
		if err != nil {
			t.Fatal(err)
		}
		blocks, _ := readmeGoBlocks(readme)
		if len(blocks) == 0 {
			t.Fatal("README has no go block")
		}
		if blocks[0] != string(example) {
			t.Errorf("README's first go block is not %s byte for byte:\n%s", readmeSmokeOpening, firstLineDiff(blocks[0], string(example)))
		}
	})

	// Whole-document mutants of the real README. A removed form in a block
	// is refused by the block check; a fence outside the strict style is
	// refused as a fence, before its content could hide anything.
	t.Run("refuses-document-mutants", func(t *testing.T) {
		t.Parallel()
		// Anchors in the opening program (the first go block).
		const charge = "timeout.Expired needs Payments | Scheduler {"
		const entry = "func main() needs Console {\n"
		const last = "\tassert with Declining { purchase(100) } == \"declined\"\n}\n```\n"
		if !strings.Contains(readme, "```go\n") || strings.Count(readme, charge) != 1 || strings.Count(readme, entry) != 1 || strings.Count(readme, last) != 1 {
			t.Fatal("the mutant anchors are not in README.md once each; update them with the first snippet")
		}
		first := func(mutated string) string { return strings.Replace(readme, "```go\n", mutated, 1) }
		uses := func(doc string) string {
			return strings.Replace(doc, charge, "timeout.Expired uses Payments | Scheduler {", 1)
		}
		const fence = "non-canonical fence"
		for _, mutant := range []struct{ name, doc, want string }{
			{"uses clause", uses(readme), "README go block 1: line 12, column 70: S001"},
			// The comma spelling of the row is refused at its first comma (196n).
			{"comma row", strings.Replace(readme, charge, "timeout.Expired needs Payments, Scheduler {", 1), "README go block 1: line 12, column 84: S001 a capability row separates its names with |"},
			{"deferred Close", strings.Replace(readme, entry, entry+"\tscope.OnExit() { receipt.Close() }\n", 1), "README go block 1: line 45: a resource releases itself"},
			// The reviewer's round-2 cases (round-2-receipts).
			{"indented tilde marker before a go block with uses", readme + "\n    ~~~text\n\n```go\nfunc bad() uses Console {\n\twork()\n}\n```\n", fence},
			{"indented closer inside a go block, uses after it", readme + "\n```go\nfunc okay() {\n\twork()\n}\n    ```\nfunc bad() uses Console {\n\twork()\n}\n```\n", fence},
			{"unclosed tilde go block at the end", readme + "\n~~~go\nfunc okay() {\n\twork()\n}\n", fence},
			{"unclosed backtick go block at the end", readme + "\n```go\nfunc okay() {\n\twork()\n}\n", "unclosed fence ```go"},
			// Opener and closer spellings outside the style.
			{"opener `go ` with a trailing space", uses(first("```go \n")), fence},
			{"opener indented by one space", uses(first(" ```go\n")), fence},
			{"opener `~~~go`", uses(first("~~~go\n")), fence},
			{"opener `Go`", uses(first("```Go\n")), fence},
			{"opener `go title=pay.ergo`", uses(first("```go title=pay.ergo\n")), fence},
			{"opener of four backticks", uses(first("````go\n")), fence},
			{"bare opener with no language", readme + "\n```\nfunc bad() uses Console {}\n```\n", fence},
			{"closer with trailing text", strings.Replace(readme, last, strings.TrimSuffix(last, "\n")+" end\n", 1), fence},
			// The reviewer's round-3 cases (round-3-findings, T1).
			{"quoted fence", readme + "\n> ```go\n> func bad() uses Console {\n> \twork()\n> }\n> ```\n", fence},
			{"nested quoted tilde fence", readme + "\n>> ~~~go\n>> func bad() uses Console {\n>> \twork()\n>> }\n>> ~~~\n", fence},
			{"list item holding a quoted fence", readme + "\n- > ```go\n  > func bad() uses Console {\n  > \twork()\n  > }\n  > ```\n", fence},
			{"CR line endings", strings.ReplaceAll(readme, "\n", "\r\n"), "carriage return"},
			{"closer of four backticks", strings.Replace(readme, last, strings.TrimSuffix(last, "```\n")+"````\n", 1), fence},
		} {
			problems := strings.Join(readmeProblems(mutant.doc), "\n")
			if !strings.Contains(problems, mutant.want) {
				t.Errorf("%s: got %q, want a refusal naming %q", mutant.name, problems, mutant.want)
			}
		}
		// Controls: a canonical block appended at the end is read and passes.
		control := readme + "\n```go\nfunc okay() {\n\twork()\n}\n```\n"
		if problems := readmeProblems(control); len(problems) > 0 {
			t.Errorf("canonical appended go block refused: %q", problems)
		}
		before, _ := readmeGoBlocks(readme)
		if blocks, _ := readmeGoBlocks(control); len(blocks) != len(before)+1 {
			t.Errorf("canonical appended go block not read: %d blocks", len(blocks))
		}
	})

	// The check refuses the removed forms; each mutant is one a README
	// edit could reintroduce.
	t.Run("refuses-removed-forms", func(t *testing.T) {
		t.Parallel()
		for _, mutant := range []struct{ name, body, want string }{
			{"uses clause", "func f() uses Console {\n\tConsole.Println(\"x\")\n}\n", "S001"},
			{"uses name", "func f() {\n\tlet uses = 1\n\tg(uses)\n}\n", "`uses` was replaced"},
			{"nil comparison", "func f(x Order) bool {\n\treturn x == nil\n}\n", "no nil"},
			{"nil value", "func f() Order {\n\treturn nil\n}\n", "no nil"},
			{"prefix catch of a type", "func f() Receipt {\n\treturn catch Declined {\n\t\tReceipt{}\n\t}\n}\n", "S001"},
			{"prefix catch of a call", "func f() Receipt {\n\tlet r = catch Charge(c, 1) {\n\tcase Declined:\n\t\tReceipt{}\n\t}\n\treturn r\n}\n", "S001"},
			{"not canonical", "func f()   {\n}\n", "not canonical"},
			{"deferred Close", "func f() needs FS {\n\tlet file = FS.Open(\"x\")\n\tscope.OnExit() { file.Close() }\n\tread(file)\n}\n", "deferred Close"},
		} {
			problem := readmeBlockProblem(mutant.body)
			if problem == nil || !strings.Contains(problem.Error(), mutant.want) {
				t.Errorf("%s: got %v, want a refusal naming %q:\n%s", mutant.name, problem, mutant.want, mutant.body)
			}
		}
		for _, control := range []string{
			"func f(x Order) Receipt fails Declined needs Payments {\n\treturn Payments.Charge(x)\n}\n",
			// Exit cleanup that is not a release stays valid (resources.md, `scope.OnExit`).
			"import queue\nimport scope\n\nfunc f(done queue.Queue[int64]) {\n\tscope.OnExit() { done.Push(1) }\n\twork()\n}\n",
		} {
			if problem := readmeBlockProblem(control); problem != nil {
				t.Errorf("control block refused: %v\n%s", problem, control)
			}
		}
	})

	t.Run("diagnostic", func(t *testing.T) {
		t.Parallel()
		quoted := readmeSmokeDiagnostics.FindStringSubmatch(readme)
		if quoted == nil {
			t.Fatal("README has no `$ ergo diagnostics pay.ergo` console block")
		}
		binary := buildTestCLI(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pay.ergo"), []byte(readmeSmokeS102Source), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, dir, "", "diagnostics", "pay.ergo")
		if code == 0 {
			t.Fatalf("ergo diagnostics pay.ergo exited 0\n%s", stdout)
		}
		if string(stdout) != quoted[1] {
			t.Fatalf("ergo diagnostics pay.ergo printed %q, README quotes %q\n%s", stdout, quoted[1], stderr)
		}
	})

	t.Run("working-in-this-repo", func(t *testing.T) {
		t.Parallel()
		block := readmeSmokeShBlock.FindStringSubmatch(readme)
		if block == nil {
			t.Fatal("README has no sh block under \"Working in this repo\"")
		}
		binary := buildTestCLI(t)
		workspace := smokeWorkspace(t, "examples/editions/sketch", readmeSmokeOpening, "go.mod")
		commands := 0
		verbs := map[string]bool{}
		for _, line := range strings.Split(block[1], "\n") {
			command := readmeSmokeCommand.FindStringSubmatch(line)
			if command == nil {
				continue
			}
			commands++
			args := strings.Fields(command[1])
			verbs[args[0]] = true
			stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, workspace, "", args...)
			if code != 0 {
				t.Errorf("ergo %s: exit %d\n%s%s", command[1], code, stdout, stderr)
				continue
			}
			if _, output, ok := strings.Cut(command[2], ": "); ok {
				got := string(stdout)
				if args[0] == "test" {
					// The runner prints a line per test, then its summary;
					// the README quotes the summary.
					got = got[strings.LastIndex(strings.TrimSuffix(got, "\n"), "\n")+1:]
				}
				if want := strings.ReplaceAll(output, " / ", "\n") + "\n"; got != want {
					t.Errorf("ergo %s printed %q, README says %q", command[1], stdout, want)
				}
			}
		}
		if commands == 0 {
			t.Fatal("README runs no ./bin/ergo command under \"Working in this repo\"")
		}
		// The README shows a v1 program both run and tested.
		for _, verb := range []string{"run", "test"} {
			if !verbs[verb] {
				t.Errorf("README shows no ./bin/ergo %s command under \"Working in this repo\"", verb)
			}
		}
	})
}
