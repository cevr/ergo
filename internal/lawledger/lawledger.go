// Package lawledger reads the Std laws ledger of docs/spec/stdlib.md and
// runs the laws of one Go test against it (std-align S1a; fu-ledger-runner).
//
// Each row of the ledger names a std law, its statement (the law's source
// text) and its runner, a Go test. A runner calls Run with the laws it
// restates, each with the statement it restates and the check that runs
// it. Run holds that the runner restates exactly the rows that name it,
// each with the row's statement, so a runner named for a law it does not
// run, or a law changed in source and ledger but not in its runner, fails
// the runner. It then runs each law as a subtest named after the law.
// TestStdLawLedger (internal/sketch/std) holds that every runner the
// ledger names calls Run.
package lawledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Doc is the spec file that holds the ledger, from the module root.
const Doc = "docs/spec/stdlib.md"

// Header is the ledger table's header row.
var Header = []string{"Package", "Law", "Statement", "Runner", "State"}

// Row is one row of the Std laws table, its cells unquoted.
type Row struct {
	Pkg, Law, Statement, Runner, State string
	Line                               int // 1-based line in Doc
}

// Key is the row's ledger key: "pkg Law", or "pkg Type.Law" for a member.
func (r Row) Key() string { return r.Pkg + " " + r.Law }

// Parse parses the table of the "## Std laws" section of a stdlib.md text.
// A malformed row is an error that names its line.
func Parse(text string) ([]Row, error) {
	var rows []Row
	var problems []string
	in, header := false, false
	for i, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			in = line == "## Std laws"
			continue
		case !in || !strings.HasPrefix(line, "|"):
			continue
		}
		cells := splitRow(line)
		if !header {
			header = true
			if !slices.Equal(cells, Header) {
				return nil, fmt.Errorf("stdlib.md:%d: the Std laws table header is %q", i+1, cells)
			}
			continue
		}
		if strings.HasPrefix(cells[0], "---") {
			continue
		}
		if len(cells) != 5 {
			problems = append(problems, fmt.Sprintf("stdlib.md:%d: a ledger row has %d cells, want 5: %q", i+1, len(cells), line))
			continue
		}
		rows = append(rows, Row{Pkg: unquote(cells[0]), Law: unquote(cells[1]), Statement: unquote(cells[2]), Runner: unquote(cells[3]), State: cells[4], Line: i + 1})
	}
	if !header {
		return nil, errors.New("stdlib.md has no Std laws table")
	}
	if len(problems) > 0 {
		return rows, errors.New(strings.Join(problems, "\n"))
	}
	return rows, nil
}

// splitRow splits a Markdown table row at each unescaped |, and unescapes
// \| in each cell.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func unquote(cell string) string {
	if len(cell) >= 2 && cell[0] == '`' && cell[len(cell)-1] == '`' {
		return cell[1 : len(cell)-1]
	}
	return cell
}

var spaces = regexp.MustCompile(`\s+`)

// Normalize makes each run of white space in a statement one space, as the
// ledger writes a law's source text.
func Normalize(statement string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(statement, " "))
}

// Read reads the ledger of the module that encloses the working directory.
func Read() ([]Row, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, Doc))
	if err != nil {
		return nil, err
	}
	return Parse(string(data))
}

// moduleRoot is the nearest directory at or above the working directory
// with a go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}

// Law is one law as a runner restates it: the statement it restates (the
// law's source text; white space is free) and the check that runs it.
type Law struct {
	Statement string
	Check     func(t *testing.T)
}

// Run holds that the runner t (a top-level test, by its name) restates
// exactly the ledger rows whose runner it is, each with the row's
// statement, and runs each law's check as a subtest named after its key
// with the space made a dot ("queue.CloseKeepsValues"), in key order. laws
// is keyed by ledger key.
func Run(t *testing.T, laws map[string]Law) {
	t.Helper()
	rows, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	run(t, rows, laws)
}

// runner is the part of *testing.T that run uses.
type runner interface {
	Helper()
	Name() string
	Error(args ...any)
	Run(name string, f func(t *testing.T)) bool
}

// run is Run over rows.
func run(t runner, rows []Row, laws map[string]Law) {
	t.Helper()
	for _, problem := range Compare(t.Name(), rows, laws) {
		t.Error(problem)
	}
	keys := make([]string, 0, len(laws))
	for key := range laws {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		law := laws[key]
		t.Run(strings.Replace(key, " ", ".", 1), func(t *testing.T) {
			t.Logf("law %s: %s", key, Normalize(law.Statement))
			if law.Check != nil {
				law.Check(t)
			}
		})
	}
}

// Compare is the agreement of a runner's laws with the ledger rows that
// name the runner: each problem, in key order; none when they agree. A
// runner no row names is a problem too.
func Compare(runner string, rows []Row, laws map[string]Law) []string {
	var problems []string
	mine := map[string]Row{}
	for _, r := range rows {
		if r.Runner == runner {
			mine[r.Key()] = r
		}
	}
	if len(mine) == 0 {
		problems = append(problems, fmt.Sprintf("no row of the Std laws table names the runner %s", runner))
	}
	keys := make([]string, 0, len(mine)+len(laws))
	for key := range mine {
		keys = append(keys, key)
	}
	for key := range laws {
		if _, ok := mine[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		row, inLedger := mine[key]
		law, restated := laws[key]
		switch {
		case !restated:
			problems = append(problems, fmt.Sprintf("stdlib.md:%d: the ledger runs %s in %s, which does not restate it", row.Line, key, runner))
		case !inLedger:
			problems = append(problems, fmt.Sprintf("%s restates %s, which no row runs in it", runner, key))
		case law.Check == nil:
			problems = append(problems, fmt.Sprintf("%s restates %s with no check", runner, key))
		case Normalize(law.Statement) != Normalize(row.Statement):
			problems = append(problems, fmt.Sprintf("stdlib.md:%d: %s restates %s as\n\t%s\nthe ledger and the source say\n\t%s", row.Line, runner, key, Normalize(law.Statement), Normalize(row.Statement)))
		}
	}
	return problems
}
