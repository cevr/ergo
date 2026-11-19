package residue_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// movedAt is the source head F (spec SPLIT §7 step 3): the commit whose
// trees downstream/moved records. The integrator sets it when it fills
// the file at F.
const movedAt = ""

// movedLine is one line of downstream/moved: PATH TREEHASH REPOSITORY.
type movedLine struct {
	path, tree, repository string
}

// parseMoved reads downstream/moved; a blank line and a # line are
// ignored.
func parseMoved(text string) ([]movedLine, error) {
	var lines []movedLine
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("downstream/moved:%d: want PATH TREEHASH REPOSITORY, got %q", i+1, line)
		}
		lines = append(lines, movedLine{path: fields[0], tree: fields[1], repository: fields[2]})
	}
	return lines, nil
}

// frozenProblems checks each moved path of the repository at repo: its
// tree at HEAD is the recorded one, or the path is gone. A changed path
// is a problem that names the repository it moved to at F.
func frozenProblems(repo string, lines []movedLine, at string) []string {
	var problems []string
	for _, l := range lines {
		out, err := exec.Command("git", "--no-optional-locks", "-C", repo, "rev-parse", "--verify", "--quiet", "HEAD:"+l.path).Output()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0:
			continue // gone: unit D1 deleted it
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: git rev-parse: %v", l.path, err))
		case strings.TrimSpace(string(out)) != l.tree:
			problems = append(problems, fmt.Sprintf("%s moved to %s at %s; make this change there", l.path, l.repository, at))
		}
	}
	return problems
}

// TestMovedPathsAreFrozen is the frozen-path guard of the downstream
// check (docs/tooling.md, Downstream check): a path that moved to its own
// repository at F is not edited in ergo after F, so no lane's commit to it
// is lost silently. Before F the file holds no path, and the test says so.
func TestMovedPathsAreFrozen(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "downstream", "moved"))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := parseMoved(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Log("downstream/moved holds no path (before F): nothing is frozen yet")
		return
	}
	if movedAt == "" {
		t.Fatal("downstream/moved holds paths, but movedAt (internal/residue/moved_test.go) names no F: set it to the commit the trees were read at")
	}
	for _, problem := range frozenProblems(root, lines, movedAt) {
		t.Error(problem)
	}
}

// TestMovedPathRule reads a fixture repository whose first commit is its
// F: an unchanged path passes, a gone path passes, and an edited one fails
// with the text that names its repository. It catches
// ds-moved-hash-unchecked (this package's ledger), which the checkout
// alone does not while downstream/moved is empty.
func TestMovedPathRule(t *testing.T) {
	repo := t.TempDir()
	gitIn := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"--no-optional-locks", "-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn("init", "-q")
	write("kept/a.txt", "a\n")
	write("edited/b.txt", "b\n")
	write("gone/c.txt", "c\n")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "F")
	f := gitIn("rev-parse", "HEAD")
	var lines []movedLine
	for _, path := range []string{"kept", "edited", "gone"} {
		lines = append(lines, movedLine{path: path, tree: gitIn("rev-parse", "HEAD:"+path), repository: "example/" + path})
	}
	if problems := frozenProblems(repo, lines, f); len(problems) != 0 {
		t.Fatalf("at F: %q, want none", problems)
	}
	write("edited/b.txt", "b, edited after F\n")
	gitIn("rm", "-q", "-r", "gone")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "after F")
	want := []string{"edited moved to example/edited at " + f + "; make this change there"}
	if problems := frozenProblems(repo, lines, f); strings.Join(problems, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after F: %q, want %q", problems, want)
	}
}
