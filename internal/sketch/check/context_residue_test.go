package check_test

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// residueAllowlist lists the lines outside history that still say wire.
const residueAllowlist = "testdata/v1/wire-residue.txt"

// residueHistory are the recorded history the rename leaves as written
// (196w decision 6). docs/research is history too, except the inputs code
// parses in the current syntax (residueParsed).
var residueHistory = []string{
	"docs/receipts/",
	"docs/previews/2026-",
	"plans/",
	"prototypes/sketch9/",
	"conformance/upstream/",
	"docs/research/",
}

var residueParsed = regexp.MustCompile(`^docs/research/ef-migrate/[^/]+\.ergo$`)

var residueWord = regexp.MustCompile(`(?i)\bwires?\b`)

// TestContextResidue is 196w acceptance check 1: the construct is spelled
// context, so a tracked line outside history that says wire is the network
// sense (a wire format, the wire key of a codec), grammar.md's old-spelling
// list, an input of the old-syntax translator, or the record of the rename,
// and the allowlist names it. A match no row covers fails, and so does a row
// that covers no match, so the list stays exact.
func TestContextResidue(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	allow, order := readResidueAllowlist(t)

	cmd := exec.Command("git", "-c", "core.quotePath=false", "grep", "-n", "-I", "-w", "-i", "-E", "wires?", "--", ".",
		":(exclude)internal/migrate196/oldsyntax",
		":(exclude)internal/sketch/check/"+residueAllowlist)
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
		if residueIsHistory(path) {
			continue
		}
		if allow[path]["*"] {
			used[path+"\t*"] = true
			continue
		}
		for _, span := range residueWord.FindAllStringIndex(text, -1) {
			covered := false
			for excerpt := range allow[path] {
				if residueCovers(text, excerpt, span) {
					used[path+"\t"+excerpt] = true
					covered = true
				}
			}
			if !covered {
				t.Errorf("%s:%s: %q says wire and no row of %s covers it; spell the construct context, or add the network sense to the allowlist", path, num, text[span[0]:span[1]], residueAllowlist)
			}
		}
	}
	for _, row := range order {
		if !used[row] {
			t.Errorf("%s: the row %q covers no match; remove it", residueAllowlist, row)
		}
	}
}

func residueIsHistory(path string) bool {
	if residueParsed.MatchString(path) {
		return false
	}
	for _, h := range residueHistory {
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

func readResidueAllowlist(t *testing.T) (map[string]map[string]bool, []string) {
	t.Helper()
	f, err := os.Open(residueAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allow := map[string]map[string]bool{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path, excerpt, ok := strings.Cut(line, "\t")
		if !ok || excerpt == "" || (excerpt != "*" && !residueWord.MatchString(excerpt)) {
			t.Fatalf("%s:%d: want path<TAB>excerpt with a match, or path<TAB>*: %q", residueAllowlist, n, line)
		}
		if allow[path] == nil {
			allow[path] = map[string]bool{}
		}
		if allow[path][excerpt] {
			t.Fatalf("%s:%d: duplicate row %q", residueAllowlist, n, line)
		}
		allow[path][excerpt] = true
		order = append(order, path+"\t"+excerpt)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return allow, order
}
