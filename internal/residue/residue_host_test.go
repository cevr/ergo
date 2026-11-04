package residue_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// residues are the residue checks of this package.
var residues = map[string]residue{"TestContextResidue": contextResidue, "TestRowResidue": rowResidue}

var residueLiteral = regexp.MustCompile(`^[A-Za-z]+$`)

// TestResidueGrepIsLiteral is the guard of 196n round 4: a residue check
// hands git grep fixed strings only. git grep passes a pattern to the
// platform's regcomp, and the engines differ (glibc reads `\b` as a word
// boundary in an extended pattern; macOS, without REG_ENHANCED, reads it
// as the letter b), so a pattern may select different lines on each host.
// A literal of ASCII letters under -F reads the same everywhere.
func TestResidueGrepIsLiteral(t *testing.T) {
	for name, r := range residues {
		args := r.grepArgs()
		if len(args) == 0 || args[0] != "-F" {
			t.Errorf("%s: git grep %q does not select by fixed strings (-F)", name, args)
			continue
		}
		literals := 0
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-i":
			case "-e":
				i++
				if i == len(args) || !residueLiteral.MatchString(args[i]) {
					t.Errorf("%s: git grep %q: every -e takes a literal of ASCII letters", name, args)
				}
				literals++
			default:
				t.Errorf("%s: git grep %q: the flag %q may read differently on another host", name, args, args[i])
			}
		}
		if literals == 0 {
			t.Errorf("%s: git grep %q has no literal", name, args)
		}
	}
}

// selects reports whether git grep with r's literals selects line: the
// fixed-string rule, case-folded when r.fold.
func (r residue) selects(line string) bool {
	if r.fold {
		line = strings.ToLower(line)
	}
	for _, l := range r.literals {
		if r.fold {
			l = strings.ToLower(l)
		}
		if strings.Contains(line, l) {
			return true
		}
	}
	return false
}

// TestResidueCandidatesCover: the literals select every line word matches
// in, so the fixed-string selection loses no match. The lines are every
// excerpt of each allowlist (each holds a match) and the spellings below.
func TestResidueCandidatesCover(t *testing.T) {
	samples := map[string][]string{
		"TestContextResidue": {"wire", "Wire", "WIRES", "a wire format", "with(wire{ A = a })"},
		"TestRowResidue":     {"needs A, B", "provides A, B", "needs (A, B)", "x needs pkg.A ,B", "provides\tA,\tB"},
	}
	for name, r := range residues {
		lines := samples[name]
		data, err := os.ReadFile(r.allowlist)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range strings.Split(string(data), "\n") {
			if _, excerpt, ok := strings.Cut(row, "\t"); ok && !strings.HasPrefix(row, "#") && excerpt != "*" {
				lines = append(lines, excerpt)
			}
		}
		matched := 0
		for _, line := range lines {
			if !r.word.MatchString(line) {
				continue
			}
			matched++
			if !r.selects(line) {
				t.Errorf("%s: %q holds a match, and no literal of %q selects it", name, line, r.literals)
			}
		}
		if matched == 0 {
			t.Errorf("%s: no sample line holds a match; the samples are wrong", name)
		}
	}
}

// TestResidueGrepMissesNothing runs the check both ways on this host: Go
// reads every tracked text file and finds each line word matches in, and
// git grep must have selected each of them. A host whose git grep reads the
// selection differently fails here, whatever its regex engine.
func TestResidueGrepMissesNothing(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "ls-files", "-z")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for name, r := range residues {
		args := append([]string{"-c", "core.quotePath=false", "grep", "-n", "-I"}, r.grepArgs()...)
		cmd := exec.Command("git", append(args, "--", ".")...)
		cmd.Dir = repo
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
				t.Fatalf("git grep: %v", err)
			}
		}
		selected := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			path, rest, ok := strings.Cut(line, ":")
			num, _, ok2 := strings.Cut(rest, ":")
			if ok && ok2 {
				selected[path+":"+num] = true
			}
		}
		found := 0
		for _, path := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
			// git grep reads a symlink's target path, not the file it
			// points to, so only regular files are read here.
			if info, err := os.Lstat(filepath.Join(repo, path)); err != nil || !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(repo, path))
			if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
				continue // binary: git grep -I skips it
			}
			for i, line := range strings.Split(string(data), "\n") {
				if r.word.MatchString(line) {
					found++
					if key := path + ":" + strconv.Itoa(i+1); !selected[key] {
						t.Errorf("%s: %s holds a match, and git grep %q did not select it", name, key, r.grepArgs())
					}
				}
			}
		}
		if found == 0 {
			t.Errorf("%s: Go found no match in the tracked files; the check reads nothing", name)
		}
	}
}
