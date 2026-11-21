package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// riftAllow is the allow list of TestRunScriptsCallNoRift: a row is
// `path<TAB>reason`, one file that measures Rift against work.
const riftAllow = "testdata/rift-allow.txt"

// riftCall is a command line that runs Rift or the workrift wrapper.
var riftCall = regexp.MustCompile("(?:^|[\\s;&|(`\"'])(?:work)?rift\\s+(?:create|remove|list|gc|new|adopt|status|init)\\b")

// useRift is a line of a doc that tells the reader to use Rift.
var useRift = regexp.MustCompile(`(?i)\buse (?:isolated )?rifts?\b`)

// TestRunScriptsCallNoRift is the guard of SEMWS WS9a: the run is off
// Rift. No tracked file under scripts/ runs rift or workrift, and no
// tracked doc (README.md, AGENTS.md, NORTH_STAR.md, docs/) runs them or
// says to use Rift; lanes are workspaces of work (scripts/lanework). A
// row of the allow list admits one file that measures Rift (a receipt and
// the script that counts it); a row that admits nothing fails too. The
// dated history (docs/decisions.md, plans/) is not read: it says what was
// decided then.
func TestRunScriptsCallNoRift(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "scripts", "README.md", "AGENTS.md", "NORTH_STAR.md", "docs/*.md")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	allow := readRiftAllow(t)
	used := map[string]bool{}
	read := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || name == "docs/decisions.md" || strings.Contains("/"+name, "/testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		read++
		doc := strings.HasSuffix(name, ".md")
		for i, line := range strings.Split(string(src), "\n") {
			if !riftCall.MatchString(line) && !(doc && useRift.MatchString(line)) {
				continue
			}
			if _, ok := allow[name]; ok {
				used[name] = true
				continue
			}
			t.Errorf("%s:%d runs or names Rift for the run; lanes are work's (scripts/lanework/lanework.sh): %s", name, i+1, strings.TrimSpace(line))
		}
	}
	if read < 20 {
		t.Fatalf("read %d tracked scripts and docs; the listing is broken", read)
	}
	for name := range allow {
		if !used[name] {
			t.Errorf("%s: row %s admits no Rift line; remove it", riftAllow, name)
		}
	}
}

func readRiftAllow(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(riftAllow)
	if err != nil {
		t.Fatal(err)
	}
	allow := map[string]string{}
	for i, text := range strings.Split(string(data), "\n") {
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 2 || fields[0] == "" || strings.TrimSpace(fields[1]) == "" {
			t.Errorf("%s:%d: want `path<TAB>reason`, got %q", riftAllow, i+1, text)
			continue
		}
		allow[fields[0]] = fields[1]
	}
	return allow
}
