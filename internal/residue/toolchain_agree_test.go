package residue_test

import (
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/toolchain"
	"ergo.local/prototype/toolchain/ir"
)

// TestGoModulesAgreeOnTheToolchain: every Go module of the repository
// builds with the one release ergo is pinned to, the one the IR compiler
// patch is ported to. The root go.mod's toolchain line is
// toolchain.Version, toolchain/ir/lock has a port line for it, and every
// other go.mod the repository tracks (git ls-files, so no list is kept by
// hand) has that same toolchain line and a go line no later than it.
//
// The tests start ergo with ERGO_GO=system (internal/testinputs, the
// gate), and `go test` puts the GOROOT/bin of the toolchain the module's
// go.mod selects first on the test's PATH. A nested module with no
// toolchain line gets the release its go line names, or the go on PATH:
// hosts/mygo's `go 1.27.3` made every test there that builds a program fail
// with "Go go1.27.3 has no port of the IR compiler patch" (main 5f4c5ba24).
// Mutant (testdata/mutants.json): toolchain-mygo-unpinned.
func TestGoModulesAgreeOnTheToolchain(t *testing.T) {
	top, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(top))
	lock, err := ir.Files.ReadFile("lock")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^port ` + regexp.QuoteMeta(toolchain.Version) + ` `).Match(lock) {
		t.Errorf("toolchain/ir/lock has no port of %s, the release ergo is pinned to (toolchain.Version)", toolchain.Version)
	}
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "go.mod", "*/go.mod")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	goLine := regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
	toolLine := regexp.MustCompile(`(?m)^toolchain\s+(\S+)\s*$`)
	seen := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		seen++
		tool := ""
		if m := toolLine.FindSubmatch(data); m != nil {
			tool = string(m[1])
		}
		if tool != toolchain.Version {
			t.Errorf("%s: toolchain %q, want %s (toolchain.Version, the root go.mod's)", name, tool, toolchain.Version)
		}
		if m := goLine.FindSubmatch(data); m != nil && version.Compare("go"+string(m[1]), toolchain.Version) > 0 {
			t.Errorf("%s: go %s is later than %s, the release the IR patch is ported to", name, m[1], toolchain.Version)
		}
	}
	if seen < 2 {
		t.Fatalf("git ls-files found %d go.mod files", seen)
	}
}
