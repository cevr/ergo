package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workRegisters is a line that runs the work binary (as "$WORK" or
// ${WORK}) with a command that writes the user's list of bases.
var workRegisters = regexp.MustCompile(`"\$\{?WORK\}?"\s+(?:init|new|adopt|adopt-rift|import-rift)\b`)

// TestWorkScriptsKeepTheirRegistry is the guard of SEMWS WK-BASES3: a
// tracked script under scripts/ that runs work init, new or adopt keeps
// work's data in its scratch, by setting both HOME and XDG_DATA_HOME.
// Without them each run appended its scratch base to the real
// ~/.local/share/work/bases, and a caller walked into a deleted one (16
// such bases on the workbox, 2026-11-16).
func TestWorkScriptsKeepTheirRegistry(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "scripts/*.sh")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if !workRegisters.MatchString(text) {
			continue
		}
		for _, v := range []string{"HOME=", "XDG_DATA_HOME="} {
			if !regexp.MustCompile(`(?m)(?:^|[\s;(])` + regexp.QuoteMeta(v)).MatchString(text) {
				t.Errorf("%s runs work init, new or adopt but sets no %s: its scratch base lands in the user's list of bases", name, strings.TrimSuffix(v, "="))
			}
		}
	}
}
