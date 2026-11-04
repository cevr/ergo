package check_test

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// residue is one residue check: a git grep over every tracked path but the
// skipped prefixes, and an allowlist that names each match outside history.
//
// The host's regex engine never sees a pattern (196n round 4). git grep
// hands a pattern to the platform's regcomp, and the engines differ: macOS
// gives `\b` no meaning in an extended pattern, so a GNU-only pattern found
// nothing there. So git grep selects candidate lines by fixed strings only
// (-F, one -e per literal), which every engine reads the same way, and Go's
// regexp, the same on every host, finds the matches in those lines. Each
// line word matches must contain a literal (TestResidueCandidatesCover).
type residue struct {
	allowlist string         // the allowlist file, relative to this package
	literals  []string       // the fixed strings that select candidate lines; ASCII letters only
	fold      bool           // the literals match in any case (-i)
	word      *regexp.Regexp // the matches within a candidate line
	fix       string         // what an uncovered match should become
}

// grepArgs are the git grep flags that select the candidate lines.
func (r residue) grepArgs() []string {
	args := []string{"-F"}
	if r.fold {
		args = append(args, "-i")
	}
	for _, l := range r.literals {
		args = append(args, "-e", l)
	}
	return args
}

// contextResidue is the check of TestContextResidue (196w).
var contextResidue = residue{
	allowlist: residueAllowlist,
	literals:  []string{"wire"},
	fold:      true,
	word:      residueWord,
	fix:       "spell the construct context, or add the network sense to the allowlist",
}

// residueAllowlist lists the lines outside history that still say wire, and
// the skipped path prefixes: the recorded history the rename leaves as
// written (196w decision 6) and the old-syntax parser. docs/research is
// history too, except the inputs code parses in the current syntax
// (residueParsed).
const residueAllowlist = "testdata/v1/wire-residue.txt"

var residueParsed = regexp.MustCompile(`^docs/research/ef-migrate/[^/]+\.ergo$`)

var residueWord = regexp.MustCompile(`(?i)\bwires?\b`)

// TestContextResidue is 196w acceptance check 1: the construct is spelled
// context, so a tracked line outside history that says wire is the network
// sense (a wire format, the wire key of a codec), grammar.md's old-spelling
// list, an input of the old-syntax translator, or the record of the rename,
// and the allowlist names it. A match no row covers fails, and so does a row
// that covers no match, so the list stays exact.
func TestContextResidue(t *testing.T) { contextResidue.run(t) }

// run greps the tracked files and checks every match against the allowlist.
// A match no row covers fails, and so does a row that covers no match.
func (r residue) run(t *testing.T) {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	allow, order, skipped := r.read(t)

	args := append([]string{"-c", "core.quotePath=false", "grep", "-n", "-I"}, r.grepArgs()...)
	args = append(args, "--", ".", ":(exclude)internal/sketch/check/"+r.allowlist)
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("git grep: %v", err)
		}
	}
	used := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		path, rest, ok := strings.Cut(line, ":")
		num, text, ok2 := strings.Cut(rest, ":")
		if !ok || !ok2 {
			t.Fatalf("git grep line %q", line)
		}
		if residueSkipped(path, skipped) {
			continue
		}
		if allow[path]["*"] {
			used[path+"\t*"] = true
			continue
		}
		for _, span := range r.word.FindAllStringIndex(text, -1) {
			covered := false
			for excerpt := range allow[path] {
				if residueCovers(text, excerpt, span) {
					used[path+"\t"+excerpt] = true
					covered = true
				}
			}
			if !covered {
				t.Errorf("%s:%s: %q is not covered by a row of %s; %s", path, num, text[span[0]:span[1]], r.allowlist, r.fix)
			}
		}
	}
	for _, row := range order {
		if !used[row] {
			t.Errorf("%s: the row %q covers no match; remove it", r.allowlist, row)
		}
	}
}

func residueSkipped(path string, skipped []string) bool {
	if residueParsed.MatchString(path) {
		return false
	}
	for _, h := range skipped {
		if strings.HasPrefix(path, h) {
			return true
		}
	}
	return false
}

// residueCovers reports whether an occurrence of excerpt in text contains
// the span.
func residueCovers(text, excerpt string, span []int) bool {
	for from := 0; from <= len(text); {
		i := strings.Index(text[from:], excerpt)
		if i < 0 {
			return false
		}
		at := from + i
		if at <= span[0] && span[1] <= at+len(excerpt) {
			return true
		}
		from = at + 1
	}
	return false
}

// read reads the rows (`path<TAB>excerpt` or `path<TAB>*`) in file order and
// the skipped prefixes (`!prefix`).
func (r residue) read(t *testing.T) (map[string]map[string]bool, []string, []string) {
	t.Helper()
	f, err := os.Open(r.allowlist)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allow := map[string]map[string]bool{}
	var order, skipped []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if prefix, ok := strings.CutPrefix(line, "!"); ok {
			if prefix == "" || strings.ContainsAny(prefix, "\t ") {
				t.Fatalf("%s:%d: want !prefix: %q", r.allowlist, n, line)
			}
			skipped = append(skipped, prefix)
			continue
		}
		path, excerpt, ok := strings.Cut(line, "\t")
		if !ok || excerpt == "" || (excerpt != "*" && !r.word.MatchString(excerpt)) {
			t.Fatalf("%s:%d: want path<TAB>excerpt with a match, or path<TAB>*: %q", r.allowlist, n, line)
		}
		if allow[path] == nil {
			allow[path] = map[string]bool{}
		}
		if allow[path][excerpt] {
			t.Fatalf("%s:%d: duplicate row %q", r.allowlist, n, line)
		}
		allow[path][excerpt] = true
		order = append(order, path+"\t"+excerpt)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(skipped) == 0 {
		t.Fatalf("%s: no !prefix line; the history list is missing", r.allowlist)
	}
	return allow, order, skipped
}
