package residue_test

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mutantNamesAllowlist lists the names after "mutant" in a test comment
// that are no ledger row today: stale names their owners still have to
// repair (rename to the row, add the row, or drop the claim) and the
// hyphenated prose the rule cannot tell from a name. A row is
// `path<TAB>name`.
const mutantNamesAllowlist = "testdata/mutant-names-allow.txt"

// TestTestCommentsNameLedgerRows is the rule AGENTS.md Tests asks of every
// test, read from the source (batch 18 verify M4): a mutant a test's comment
// names is a row of some package's mutant ledger. A name is a word of the
// mutant-name shape (lowercase words and numbers joined by hyphens, with a
// letter somewhere; a trailing `*`
// names every row with that prefix) in a sentence of a test file's comment
// that says mutant or mutants, the comment's lines joined, so a name on
// the line after the word counts. A name that is no row fails unless the
// allowlist lists it, and an allowlist row that names nothing fails, so the
// list only shrinks.
func TestTestCommentsNameLedgerRows(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]bool{}
	var tests []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Name() == "mutants.json" && filepath.Base(filepath.Dir(path)) == "testdata":
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var ledger struct {
				Mutants []struct {
					Name string `json:"name"`
				} `json:"mutants"`
			}
			if err := json.Unmarshal(data, &ledger); err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			for _, m := range ledger.Mutants {
				rows[m.Name] = true
			}
		case strings.HasSuffix(d.Name(), "_test.go") && !strings.Contains(filepath.ToSlash(path), "/testdata/"):
			tests = append(tests, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed, order := readNameAllowlist(t)
	used := map[string]bool{}
	for _, path := range tests {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, ref := range commentMutantNames(string(src)) {
			if isLedgerRow(ref.name, rows) {
				continue
			}
			key := rel + "\t" + ref.name
			if allowed[key] {
				used[key] = true
				continue
			}
			t.Errorf("%s:%d: the comment names mutant %s, which no testdata/mutants.json has; name the ledger row, add it, or drop the claim", rel, ref.line, ref.name)
		}
	}
	for _, row := range order {
		if !used[row] {
			t.Errorf("%s: the row %q names no unknown mutant now; remove it", mutantNamesAllowlist, row)
		}
	}
}

// isLedgerRow reports whether name is a row, or, ending in `*`, the prefix
// of one.
func isLedgerRow(name string, rows map[string]bool) bool {
	if prefix, ok := strings.CutSuffix(name, "*"); ok {
		for row := range rows {
			if strings.HasPrefix(row, prefix) {
				return true
			}
		}
		return false
	}
	return rows[name]
}

// mutantName is a name a comment gives a mutant, at the comment's first
// line.
type mutantName struct {
	name string
	line int
}

var (
	mutantWord  = regexp.MustCompile(`(?i)\bmutants?\b([^.;]*)`)
	mutantShape = regexp.MustCompile("(?:^|[\\s,(`'\"])([a-z0-9]+(?:-[a-z0-9]+)+\\*?)(?:$|[\\s,):`'\"])")
)

// commentMutantNames returns the names of mutants in the comments of a Go
// source: each run of // lines is one text, so a sentence wrapped across
// lines is read whole.
func commentMutantNames(src string) []mutantName {
	var out []mutantName
	var group []string
	start := 0
	flush := func() {
		text := strings.Join(group, " ")
		group = group[:0]
		for _, m := range mutantWord.FindAllStringSubmatch(text, -1) {
			// Overlapping delimiters: scan word by word.
			for _, word := range strings.Fields(m[1]) {
				for _, n := range mutantShape.FindAllStringSubmatch(" "+word+" ", -1) {
					// A name has a letter: a date or a range is none.
					if strings.ContainsFunc(n[1], func(r rune) bool { return 'a' <= r && r <= 'z' }) {
						out = append(out, mutantName{n[1], start})
					}
				}
			}
		}
	}
	for i, line := range strings.Split(src, "\n") {
		text, ok := strings.CutPrefix(strings.TrimSpace(line), "//")
		if !ok {
			if len(group) > 0 {
				flush()
			}
			continue
		}
		if len(group) == 0 {
			start = i + 1
		}
		group = append(group, text)
	}
	if len(group) > 0 {
		flush()
	}
	return out
}

func readNameAllowlist(t *testing.T) (map[string]bool, []string) {
	t.Helper()
	f, err := os.Open(mutantNamesAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allowed := map[string]bool{}
	var order []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "\t") {
			t.Fatalf("%s: row %q is not path<TAB>name", mutantNamesAllowlist, line)
		}
		if !allowed[line] {
			order = append(order, line)
		}
		allowed[line] = true
	}
	return allowed, order
}

// TestMutantNameRule pins the reading of a comment: a name on the line
// after the word, a list, a prefix, and prose that is no name. It catches
// mn-lines-unjoined and mn-prefix-unread (this package's ledger), which
// the checkout alone does not: no comment of the tree needs them today.
func TestMutantNameRule(t *testing.T) {
	src := "package p\n\n// TestA holds. Mutants it catches:\n// aa-one, bb-two* and\n// cc-three. Not a name: dd-four.\nfunc TestA(t *testing.T) {}\n\n// mutant ee-five;\n// ff-six is prose.\nfunc TestB() {}\n"
	var got []string
	for _, n := range commentMutantNames(src) {
		got = append(got, n.name)
	}
	if want := []string{"aa-one", "bb-two*", "cc-three", "ee-five"}; !slices.Equal(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	rows := map[string]bool{"bb-two-x": true}
	if !isLedgerRow("bb-two*", rows) || isLedgerRow("bb-three*", rows) || isLedgerRow("bb-two", rows) {
		t.Fatal("a trailing * names the rows with that prefix, and only it")
	}
}
