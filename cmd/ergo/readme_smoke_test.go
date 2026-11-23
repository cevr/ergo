package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestReadmeSmoke keeps README.md truthful, as Rust doctests and Go Example
// tests keep documentation executable. The README shows programs, the
// checker's refusals of their mistakes, measured numbers and commands, and
// each is held to the repository:
//
//   - every fence has the one strict style (markdownlint MD046/MD048): a
//     backtick fence at column 0, opened as ```<lang> with lowercase
//     letters and closed by a bare ``` on its own line. Any other line
//     that contains ``` or ~~~ fails, whatever its prefix (indent,
//     blockquote, list item), and so do an unclosed fence and a carriage
//     return anywhere, so the blocks read here are exactly the blocks
//     CommonMark renders;
//   - every go block that elides nothing parses and is canonical under
//     the parser and formatter, and uses no form the specification
//     removed: `uses` (#217), `nil` (types.md) or a prefix catch
//     (rows.md); nor a Close in `scope.OnExit`;
//   - every code block but the commands of "Try it" has a marker comment
//     above it, which GitHub does not render:
//     `<!-- quote FILE [RANGES] -->`: the block is those lines of FILE
//     (all of it with no RANGES), ranges joined by a line "…", a range
//     ending in "<" quoted with its common indent removed;
//     `<!-- shows DIR... -->`: a console block whose `$ ergo …` lines run,
//     one DIR each, in a copy of that site/examples fixture (its
//     `// want` markers stripped; a DIR with over.tsv is a file put over
//     a copy of a repository package), and print exactly the lines below
//     them; a DIR without over.tsv also passes `ergo check --want`;
//     `<!-- receipts -->`: a table whose numbers are the ratios of
//     site/receipts/shown.tsv, read from their receipts, every shown row
//     once;
//     `<!-- case DIR -->`: the lines under the `$` line are the expected
//     output of that recorded case, which the examples runner runs;
//   - each `./bin/ergo` command under "Try it" exits 0 and prints the
//     output its comment names.

var (
	// readmeSmokeFenceLike is any line that holds three backticks or three
	// tildes: CommonMark may read it as a fence inside a container
	// (blockquote, list item) or after an indent.
	readmeSmokeFenceLike = regexp.MustCompile("```|~~~")
	// readmeSmokeOpener is the one admitted opening fence.
	readmeSmokeOpener = regexp.MustCompile("^```([a-z]+)$")
	// readmeSmokeMarker is the marker comment above a block.
	readmeSmokeMarker = regexp.MustCompile(`^<!-- (quote|shows|receipts|case)((?: \S+)*) -->$`)
	// readmeSmokeShBlock is the shell block of "Try it".
	readmeSmokeShBlock = regexp.MustCompile("(?ms)^## Try it\n.*?^```sh\n(.*?)^```$")
	// readmeSmokeCommand is one `./bin/ergo` line, with an optional
	// comment; text after the comment's first ": " is the expected output,
	// one line per " / ".
	readmeSmokeCommand = regexp.MustCompile(`^\./bin/ergo ([^#]*?)\s*(?:#\s*(.*))?$`)
	// readmeSmokeWant is a fixture's `// want` marker, stripped before a
	// fixture is shown or run.
	readmeSmokeWant = regexp.MustCompile(` // want .*$`)
	// readmeSmokeRange is one range of a quote marker.
	readmeSmokeRange = regexp.MustCompile(`^(\d+)(?:-(\d+))?(<?)$`)
	// readmeSmokeRow is a row of the receipts table: its workload and its
	// number.
	readmeSmokeRow = regexp.MustCompile("^\\| `([^`]+)` \\|[^|]*\\| \\*\\*([0-9.]+)\\*\\*")
)

// readmeBlock is one fenced block of the README: its language, its body
// lines and the marker above it.
type readmeBlock struct {
	lang   string
	line   int // 1-based line of the opening fence
	body   []string
	marker []string // the marker's kind and arguments; nil when none
}

// readmeBlocks returns every fenced block of a README in the strict fence
// style, and a problem for each line that breaks the style. Inside a
// block, a fence-like line other than the bare closer is a problem too:
// CommonMark might close the block there or keep the line as code, and the
// style removes that question.
func readmeBlocks(readme string) ([]readmeBlock, []string) {
	var blocks []readmeBlock
	var problems []string
	var current readmeBlock
	open := false
	lines := strings.Split(readme, "\n")
	for index, line := range lines {
		if !readmeSmokeFenceLike.MatchString(line) {
			if open {
				current.body = append(current.body, line)
			}
			continue
		}
		switch opener := readmeSmokeOpener.FindStringSubmatch(line); {
		case !open && opener != nil:
			current, open = readmeBlock{lang: opener[1], line: index + 1}, true
			if index > 0 {
				if m := readmeSmokeMarker.FindStringSubmatch(lines[index-1]); m != nil {
					current.marker = append([]string{m[1]}, strings.Fields(m[2])...)
				}
			}
		case open && line == "```":
			blocks = append(blocks, current)
			open = false
		default:
			problems = append(problems, fmt.Sprintf("line %d: non-canonical fence %q: open with ```<lang> and close with a bare ```, both at column 0", index+1, line))
		}
	}
	if open {
		problems = append(problems, fmt.Sprintf("unclosed fence ```%s at the end of the README", current.lang))
	}
	return blocks, problems
}

// readmeGoBlocks returns the body of every go block of a README, and the
// fence problems.
func readmeGoBlocks(readme string) ([]string, []string) {
	blocks, problems := readmeBlocks(readme)
	var bodies []string
	for _, block := range blocks {
		if block.lang == "go" {
			bodies = append(bodies, strings.Join(block.body, "\n")+"\n")
		}
	}
	return bodies, problems
}

// readmeElides reports whether a block quotes parts of a file, joined by
// lines "…"; such a block is no whole source.
func readmeElides(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "…" {
			return true
		}
	}
	return false
}

// readmeProblems reports every fence that breaks the strict style and
// every inadmissible go block of a README.
func readmeProblems(readme string) []string {
	blocks, problems := readmeGoBlocks(readme)
	if index := strings.IndexByte(readme, '\r'); index >= 0 {
		problems = append(problems, fmt.Sprintf("line %d: carriage return: the README uses LF line endings only", strings.Count(readme[:index], "\n")+1))
	}
	if len(blocks) == 0 {
		return append(problems, "README has no go block")
	}
	for index, block := range blocks {
		if readmeElides(block) {
			continue
		}
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
			return fmt.Sprintf("line %d:\n  README  %q\n  source  %q", i+1, a, b)
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

// readmeQuote is the text a quote marker names: the lines of file the
// ranges select, joined by a line "…" (indented as the next range's first
// line), each range ending in "<" without its common indent. No ranges is
// the whole file.
func readmeQuote(file string, ranges []string) (string, error) {
	lines := strings.Split(strings.TrimSuffix(file, "\n"), "\n")
	if len(ranges) == 0 {
		return strings.Join(lines, "\n"), nil
	}
	var out []string
	for i, spec := range strings.Split(strings.Join(ranges, ","), ",") {
		m := readmeSmokeRange.FindStringSubmatch(spec)
		if m == nil {
			return "", fmt.Errorf("range %q is not N, N-M, N< or N-M<", spec)
		}
		from, _ := strconv.Atoi(m[1])
		to := from
		if m[2] != "" {
			to, _ = strconv.Atoi(m[2])
		}
		if from < 1 || to < from || to > len(lines) {
			return "", fmt.Errorf("range %q is outside the file's %d lines", spec, len(lines))
		}
		part := append([]string(nil), lines[from-1:to]...)
		if m[3] == "<" {
			part = dedent(part)
		}
		if i > 0 {
			indent := part[0][:len(part[0])-len(strings.TrimLeft(part[0], " \t"))]
			out = append(out, indent+"…")
		}
		out = append(out, part...)
	}
	return strings.Join(out, "\n"), nil
}

// dedent removes the indent the non-blank lines share.
func dedent(lines []string) []string {
	common := ""
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first || !strings.HasPrefix(indent, common) {
			if first || strings.HasPrefix(common, indent) {
				common = indent
			}
		}
		first = false
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimPrefix(line, common)
	}
	return out
}

// readmeConsole splits a console block into its `$` commands and the
// output each prints.
type readmeRun struct {
	command string // without "$ " and without the comment
	output  string // the lines under it, each ending in a newline
}

func readmeConsole(body []string) []readmeRun {
	var runs []readmeRun
	for _, line := range body {
		if command, ok := strings.CutPrefix(line, "$ "); ok {
			if before, _, found := strings.Cut(command, "#"); found {
				command = before
			}
			runs = append(runs, readmeRun{command: strings.TrimSpace(command)})
			continue
		}
		if len(runs) > 0 {
			runs[len(runs)-1].output += line + "\n"
		}
	}
	return runs
}

// readmeOverlay is a fixture's over.tsv: the repository package it is put
// into, the file it replaces there and the lines of that file it changes.
type readmeOverlay struct {
	pkg, file string
	from, to  int
}

func parseReadmeOverlay(text string) (readmeOverlay, error) {
	var o readmeOverlay
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		key, value, _ := strings.Cut(line, "\t")
		switch key {
		case "package":
			o.pkg = value
		case "file":
			o.file = value
		case "replace":
			from, to, _ := strings.Cut(value, "-")
			o.from, _ = strconv.Atoi(from)
			o.to, _ = strconv.Atoi(to)
		default:
			return o, fmt.Errorf("over.tsv: unknown row %q", line)
		}
	}
	if o.pkg == "" || o.file == "" || o.from < 1 || o.to < o.from {
		return o, fmt.Errorf("over.tsv names no package, file and replace N-M")
	}
	return o, nil
}

// readmeOverlaid reports whether fixture is original with only lines
// from..to replaced, so the shown edit is the only one.
func readmeOverlaid(original, fixture string, o readmeOverlay) error {
	orig := strings.Split(original, "\n")
	fix := strings.Split(fixture, "\n")
	kept := len(orig) - (o.to - o.from + 1)
	if o.to > len(orig) || len(fix) < kept {
		return fmt.Errorf("replace %d-%d does not fit %s", o.from, o.to, o.file)
	}
	inserted := fix[o.from-1 : o.from-1+len(fix)-kept]
	rebuilt := append(append(append([]string(nil), orig[:o.from-1]...), inserted...), orig[o.to:]...)
	if strings.Join(rebuilt, "\n") != fixture {
		return fmt.Errorf("the fixture differs from %s/%s outside lines %d-%d:\n%s", o.pkg, o.file, o.from, o.to, firstLineDiff(fixture, strings.Join(rebuilt, "\n")))
	}
	return nil
}

// readmeReceiptRatio is the ratio of ergo against base for counter in case
// of a bench.v1 receipt.
func readmeReceiptRatio(receipt []byte, kase, counter, base string) (float64, error) {
	var r struct {
		Schema string `json:"schema"`
		Cases  []struct {
			Case        string `json:"case"`
			Comparisons []struct {
				Counter string   `json:"counter"`
				Base    string   `json:"base"`
				Head    string   `json:"head"`
				Ratio   *float64 `json:"ratio"`
			} `json:"comparisons"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(receipt, &r); err != nil {
		return 0, err
	}
	for _, c := range r.Cases {
		if c.Case != kase {
			continue
		}
		for _, p := range c.Comparisons {
			if p.Head == "ergo" && p.Base == base && p.Counter == counter && p.Ratio != nil {
				return *p.Ratio, nil
			}
		}
	}
	return 0, fmt.Errorf("no ratio of ergo against %s for %s %s", base, kase, counter)
}

func TestReadmeSmoke(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	blocks, _ := readmeBlocks(readme)

	t.Run("blocks", func(t *testing.T) {
		t.Parallel()
		for _, problem := range readmeProblems(readme) {
			t.Error(problem)
		}
		for _, block := range blocks {
			if block.marker == nil && block.lang != "sh" {
				t.Errorf("README line %d: a %s block with no marker above it; write <!-- quote FILE RANGES -->, <!-- shows DIR... -->, <!-- receipts --> or <!-- case DIR -->", block.line, block.lang)
			}
		}
	})

	t.Run("quotes", func(t *testing.T) {
		t.Parallel()
		quotes := 0
		for _, block := range blocks {
			if len(block.marker) < 2 || block.marker[0] != "quote" {
				continue
			}
			quotes++
			want, err := readmeQuote(read(t, block.marker[1]), block.marker[2:])
			if err != nil {
				t.Errorf("README line %d: %s: %v", block.line, block.marker[1], err)
				continue
			}
			if got := strings.Join(block.body, "\n"); got != want {
				t.Errorf("README line %d is not a quote of %s %s:\n%s", block.line, block.marker[1], strings.Join(block.marker[2:], " "), firstLineDiff(got, want))
			}
		}
		if quotes == 0 {
			t.Error("README quotes no file")
		}
	})

	t.Run("shows", func(t *testing.T) {
		t.Parallel()
		binary := buildTestCLI(t)
		shown := 0
		for _, block := range blocks {
			if len(block.marker) == 0 || block.marker[0] != "shows" {
				continue
			}
			dirs, runs := block.marker[1:], readmeConsole(block.body)
			if len(dirs) != len(runs) {
				t.Errorf("README line %d: %d fixtures for %d $ lines", block.line, len(dirs), len(runs))
				continue
			}
			for i, run := range runs {
				shown++
				args := strings.Fields(run.command)
				if len(args) < 2 || args[0] != "ergo" {
					t.Errorf("README line %d: %q is no ergo command", block.line, run.command)
					continue
				}
				dir := readmeFixture(t, binary, root, dirs[i])
				stdout, stderr, _ := runSmokeCLI(t, binary, dir, "", args[1:]...)
				if string(stdout) != run.output {
					t.Errorf("README line %d: %s in %s printed\n%s\nREADME shows\n%s\n%s", block.line, run.command, dirs[i], stdout, run.output, stderr)
				}
			}
		}
		if shown == 0 {
			t.Error("README shows no refusal")
		}
	})

	t.Run("receipts", func(t *testing.T) {
		t.Parallel()
		var table []string
		for i, line := range strings.Split(readme, "\n") {
			if line == "<!-- receipts -->" {
				for _, row := range strings.Split(readme, "\n")[i+1:] {
					if !strings.HasPrefix(row, "|") {
						break
					}
					table = append(table, row)
				}
			}
		}
		numbers := map[string]string{}
		for _, row := range table {
			if m := readmeSmokeRow.FindStringSubmatch(row); m != nil {
				numbers[m[1]] = m[2]
			}
		}
		if len(numbers) == 0 {
			t.Fatal("README has no <!-- receipts --> table with a number")
		}
		for i, row := range strings.Split(strings.TrimSpace(read(t, "site/receipts/shown.tsv")), "\n")[1:] {
			cell := strings.Split(row, "\t")
			if len(cell) != 5 {
				t.Errorf("shown.tsv:%d: %d cells", i+2, len(cell))
				continue
			}
			ratio, err := readmeReceiptRatio([]byte(read(t, "site/receipts/"+cell[0]+".json")), cell[1], cell[2], cell[3])
			if err != nil {
				t.Errorf("site/receipts/%s.json: %v", cell[0], err)
				continue
			}
			number, ok := numbers[cell[0]]
			if !ok {
				t.Errorf("README's receipts table has no row %s", cell[0])
				continue
			}
			if want := strconv.FormatFloat(ratio, 'f', 2, 64); number != want {
				t.Errorf("README shows %s for %s; its receipt says %s", number, cell[0], want)
			}
			delete(numbers, cell[0])
		}
		for workload := range numbers {
			t.Errorf("README shows a number for %s, which site/receipts/shown.tsv does not list", workload)
		}
	})

	t.Run("cases", func(t *testing.T) {
		t.Parallel()
		for _, block := range blocks {
			if len(block.marker) != 2 || block.marker[0] != "case" {
				continue
			}
			runs := readmeConsole(block.body)
			if len(runs) != 1 {
				t.Errorf("README line %d: a case block has one $ line, not %d", block.line, len(runs))
				continue
			}
			if want := read(t, block.marker[1]+"/expected.out"); runs[0].output != want {
				t.Errorf("README line %d is not %s/expected.out:\n%s", block.line, block.marker[1], firstLineDiff(runs[0].output, want))
			}
		}
	})

	// Whole-document mutants of the real README. A removed form in a block
	// is refused by the block check; a fence outside the strict style is
	// refused as a fence, before its content could hide anything.
	t.Run("refuses-document-mutants", func(t *testing.T) {
		t.Parallel()
		// Anchors: lookup in the first go block, law and last in the
		// second (the counter).
		const lookup = "(name string) Answer fails NotFound | timeout.Expired needs Resolver | Scheduler {"
		const law = "\tStep(int(count), op) >= 0\n"
		const last = law + "}\n```\n"
		if !strings.Contains(readme, "```go\n") || strings.Count(readme, lookup) != 1 || strings.Count(readme, law) != 1 || strings.Count(readme, last) != 1 {
			t.Fatal("the mutant anchors are not in README.md once each; update them with the first snippets")
		}
		first := func(mutated string) string { return strings.Replace(readme, "```go\n", mutated, 1) }
		uses := func(doc string) string {
			return strings.Replace(doc, lookup, "(name string) Answer fails NotFound | timeout.Expired uses Resolver | Scheduler {", 1)
		}
		const fence = "non-canonical fence"
		for _, mutant := range []struct{ name, doc, want string }{
			{"uses clause", uses(readme), "README go block 1: line 1, column 66: S001"},
			// The comma spelling of the row is refused at its first comma (196n).
			{"comma row", strings.Replace(readme, lookup, "(name string) Answer fails NotFound | timeout.Expired needs Resolver, Scheduler {", 1), "README go block 1: line 1, column 80: S001 a capability row separates its names with |"},
			{"deferred Close", strings.Replace(readme, law, law+"\tscope.OnExit() { count.Close() }\n", 1), "README go block 2: line 21: a resource releases itself"},
			{"indented tilde marker before a go block with uses", readme + "\n    ~~~text\n\n```go\nfunc bad() uses Console {\n\twork()\n}\n```\n", fence},
			{"indented closer inside a go block, uses after it", readme + "\n```go\nfunc okay() {\n\twork()\n}\n    ```\nfunc bad() uses Console {\n\twork()\n}\n```\n", fence},
			{"unclosed tilde go block at the end", readme + "\n~~~go\nfunc okay() {\n\twork()\n}\n", fence},
			{"unclosed backtick go block at the end", readme + "\n```go\nfunc okay() {\n\twork()\n}\n", "unclosed fence ```go"},
			{"opener `go ` with a trailing space", uses(first("```go \n")), fence},
			{"opener indented by one space", uses(first(" ```go\n")), fence},
			{"opener `~~~go`", uses(first("~~~go\n")), fence},
			{"opener `Go`", uses(first("```Go\n")), fence},
			{"opener `go title=fetch.ergo`", uses(first("```go title=fetch.ergo\n")), fence},
			{"opener of four backticks", uses(first("````go\n")), fence},
			{"bare opener with no language", readme + "\n```\nfunc bad() uses Console {}\n```\n", fence},
			{"closer with trailing text", strings.Replace(readme, last, strings.TrimSuffix(last, "\n")+" end\n", 1), fence},
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
		if after, _ := readmeGoBlocks(control); len(after) != len(before)+1 {
			t.Errorf("canonical appended go block not read: %d blocks", len(after))
		}
	})

	// The check refuses the removed forms; each mutant is one a README
	// edit could reintroduce.
	t.Run("refuses-removed-forms", func(t *testing.T) {
		t.Parallel()
		for _, mutant := range []struct{ name, body, want string }{
			{"uses clause", "func f() uses Console {\n\tConsole.Println(\"x\")\n}\n", "S001"},
			{"uses name", "func f() {\n\tlet uses = 1\n\tg(uses)\n}\n", "`uses` was replaced"},
			{"nil comparison", "func f(x Entry) bool {\n\treturn x == nil\n}\n", "no nil"},
			{"nil value", "func f() Entry {\n\treturn nil\n}\n", "no nil"},
			{"prefix catch of a type", "func f() Answer {\n\treturn catch NotFound {\n\t\tAnswer{}\n\t}\n}\n", "S001"},
			{"prefix catch of a call", "func f() Answer {\n\tlet r = catch Resolve(c, 1) {\n\tcase NotFound:\n\t\tAnswer{}\n\t}\n\treturn r\n}\n", "S001"},
			{"not canonical", "func f()   {\n}\n", "not canonical"},
			{"deferred Close", "func f() needs FS {\n\tlet file = FS.Open(\"x\")\n\tscope.OnExit() { file.Close() }\n\tread(file)\n}\n", "deferred Close"},
		} {
			problem := readmeBlockProblem(mutant.body)
			if problem == nil || !strings.Contains(problem.Error(), mutant.want) {
				t.Errorf("%s: got %v, want a refusal naming %q:\n%s", mutant.name, problem, mutant.want, mutant.body)
			}
		}
		for _, control := range []string{
			"func f(x Entry) Answer fails NotFound needs Resolver {\n\treturn Resolver.Resolve(x)\n}\n",
			// Exit cleanup that is not a release stays valid (resources.md, `scope.OnExit`).
			"import queue\nimport scope\n\nfunc f(done queue.Queue[int64]) {\n\tscope.OnExit() { done.Push(1) }\n\twork()\n}\n",
		} {
			if problem := readmeBlockProblem(control); problem != nil {
				t.Errorf("control block refused: %v\n%s", problem, control)
			}
		}
	})

	// The quote, overlay and console readers on fixed text, so a reader
	// that accepts anything is caught without a README edit.
	t.Run("readers", func(t *testing.T) {
		t.Parallel()
		file := "a\n\tb\n\tc\nd\n"
		for _, c := range []struct {
			ranges []string
			want   string
		}{
			{nil, "a\n\tb\n\tc\nd"},
			{[]string{"1,4"}, "a\n…\nd"},
			{[]string{"2-3<"}, "b\nc"},
			{[]string{"1,3"}, "a\n\t…\n\tc"},
		} {
			if got, err := readmeQuote(file, c.ranges); err != nil || got != c.want {
				t.Errorf("readmeQuote(%v) = %q, %v; want %q", c.ranges, got, err, c.want)
			}
		}
		if _, err := readmeQuote(file, []string{"3-9"}); err == nil {
			t.Error("readmeQuote accepted a range past the file's end")
		}
		o := readmeOverlay{pkg: "p", file: "f", from: 2, to: 3}
		if err := readmeOverlaid("a\nb\nc\nd", "a\nX\nd", o); err != nil {
			t.Errorf("an edit inside the replaced lines refused: %v", err)
		}
		if err := readmeOverlaid("a\nb\nc\nd", "Y\nX\nd", o); err == nil {
			t.Error("an edit outside the replaced lines accepted")
		}
		runs := readmeConsole([]string{"$ ergo check x   # note", "out 1", "out 2", "$ ergo test y"})
		if len(runs) != 2 || runs[0].command != "ergo check x" || runs[0].output != "out 1\nout 2\n" || runs[1].output != "" {
			t.Errorf("readmeConsole = %+v", runs)
		}
	})

	t.Run("try-it", func(t *testing.T) {
		t.Parallel()
		block := readmeSmokeShBlock.FindStringSubmatch(readme)
		if block == nil {
			t.Fatal("README has no sh block under \"Try it\"")
		}
		binary := buildTestCLI(t)
		workspace := smokeWorkspace(t, "examples/lookup", "go.mod")
		commands := 0
		for _, line := range strings.Split(block[1], "\n") {
			command := readmeSmokeCommand.FindStringSubmatch(line)
			if command == nil {
				continue
			}
			commands++
			args := strings.Fields(command[1])
			stdout, stderr, code := runSmokeCLI(t, binary, workspace, "", args...)
			if code != 0 {
				t.Errorf("ergo %s: exit %d\n%s%s", command[1], code, stdout, stderr)
				continue
			}
			if _, output, ok := strings.Cut(command[2], ": "); ok {
				if want := strings.ReplaceAll(output, " / ", "\n") + "\n"; string(stdout) != want {
					t.Errorf("ergo %s printed %q, README says %q", command[1], stdout, want)
				}
			}
		}
		if commands == 0 {
			t.Fatal("README runs no ./bin/ergo command under \"Try it\"")
		}
	})
}

// readmeStripWants removes every `// want` marker of a fixture, line by
// line.
func readmeStripWants(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = readmeSmokeWant.ReplaceAllString(line, "")
	}
	return strings.Join(lines, "\n")
}

// readmeFixture makes the folder a shown command runs in, from the
// site/examples fixture dir: a copy of its files with their `// want`
// markers stripped, or, with over.tsv, a copy of the named package with
// the stripped file put over its original. A fixture without over.tsv
// must also pass `ergo check --want`, so its markers say what it shows.
func readmeFixture(t *testing.T, binary, root, dir string) string {
	t.Helper()
	source := filepath.Join(root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if over, err := os.ReadFile(filepath.Join(source, "over.tsv")); err == nil {
		o, err := parseReadmeOverlay(string(over))
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		fixture, err := os.ReadFile(filepath.Join(source, filepath.Base(o.file)))
		if err != nil {
			t.Fatal(err)
		}
		stripped := readmeStripWants(string(fixture))
		original, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(o.pkg), filepath.FromSlash(o.file)))
		if err != nil {
			t.Fatal(err)
		}
		if err := readmeOverlaid(string(original), stripped, o); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
		workspace := smokeWorkspace(t, o.pkg)
		target := filepath.Join(workspace, filepath.FromSlash(o.pkg))
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(o.file)), []byte(stripped), 0o644); err != nil {
			t.Fatal(err)
		}
		return target
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, root, "", "check", "--want", dir); code != 0 {
		t.Errorf("ergo check --want %s: exit %d\n%s%s", dir, code, stdout, stderr)
	}
	target := t.TempDir()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ergo") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), []byte(readmeStripWants(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
