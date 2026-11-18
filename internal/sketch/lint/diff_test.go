package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// everyNode is a test rule that reports every top-level declaration and
// every local let, so a run has findings of one line and of many.
var everyNode = &lint.Rule{
	Name: "every-node", Code: "L900", Doc: "every declaration and let", Instead: "look", Default: lint.Warning,
	Check: func(p *lint.Pass) {
		for _, d := range p.File.Decls {
			p.Report(d.Pos(), d.End(), "declaration")
		}
		syntax.Inspect(p.File, func(n syntax.Node) bool {
			if s, ok := n.(*syntax.DeclStmt); ok {
				p.Report(s.Pos(), s.End(), "let")
			}
			return true
		})
	},
}

// genLine is one line of a generated program: its text, and whether the
// edit added it (the model's truth; git never sees the flag).
type genLine struct {
	text  string
	added bool
}

// diffCase generates a program of functions whose lines are all distinct
// but the closing braces, then edits it: lines inserted inside a
// function, a let's value replaced, a deletable let removed, a one-line
// function appended. Each changed line holds a number no other line
// holds, so git's minimal diff of the two texts is the edit itself.
func diffCase(r *rand.Rand) (old string, edited []genLine) {
	next := 1000
	fresh := func() int { next++; return next }
	var lines []genLine
	for f := 0; f < 1+r.IntN(4); f++ {
		lines = append(lines, genLine{text: fmt.Sprintf("func f%d(x int) int {", f)})
		n := 1 + r.IntN(4)
		for i := 0; i < n; i++ {
			prev := "x"
			if i > 0 {
				prev = fmt.Sprintf("a%d_%d", f, i-1)
			}
			lines = append(lines, genLine{text: fmt.Sprintf("\tlet a%d_%d = %s + %d", f, i, prev, fresh())})
			if r.IntN(3) == 0 {
				lines = append(lines, genLine{text: fmt.Sprintf("\tlet _ = x + %d", fresh())})
			}
		}
		lines = append(lines, genLine{text: fmt.Sprintf("\ta%d_%d", f, n-1)}, genLine{text: "}"}, genLine{text: ""})
	}
	lines = append(lines, genLine{text: "func main() {"}, genLine{text: "}"})
	var oldText []string
	for _, l := range lines {
		oldText = append(oldText, l.text)
	}
	for e := r.IntN(5); e > 0; e-- {
		i := r.IntN(len(lines))
		t := lines[i].text
		switch r.IntN(4) {
		case 0: // insert a let after a line inside a function
			if strings.HasPrefix(t, "\tlet ") || strings.HasPrefix(t, "func f") {
				lines = slices.Insert(lines, i+1, genLine{text: fmt.Sprintf("\tlet _ = x + %d", fresh()), added: true})
			}
		case 1: // replace a let's value
			if strings.HasPrefix(t, "\tlet ") {
				head, _, _ := strings.Cut(t, " + ")
				lines[i] = genLine{text: fmt.Sprintf("%s + %d", head, fresh()), added: true}
			}
		case 2: // delete a let nothing reads
			if strings.HasPrefix(t, "\tlet _ ") {
				lines = slices.Delete(lines, i, i+1)
			}
		case 3: // append a one-line function
			n := fresh()
			lines = append(lines, genLine{text: fmt.Sprintf("func g%d() int { %d }", n, n), added: true})
		}
	}
	return strings.Join(oldText, "\n") + "\n", lines
}

// TestDiffScopeIsTheLaw is the diff law of `ergo lint --diff` (LINT-HYGIENE
// §[R2] 2) as a seeded property over generated edits: the findings of the
// scoped run are exactly the findings of the full run whose span, from its
// first line to its last, meets a line the edit added. The added lines
// come from git's own diff of the two texts (-U0 --no-prefix, the form
// the command reads), so the property also holds ParseDiff to git's
// hunk headers: a count of one written as no count, and a count of zero
// for a deletion. It runs git because git's output is the input under
// test. Mutants: lhp-diff-count-default, lhp-diff-zero-count,
// lhp-diff-start-line-only.
func TestDiffScopeIsTheLaw(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is needed: %v", err)
	}
	reg, err := lint.NewRegistry([]*lint.Rule{everyNode}, []lint.Preset{{Name: "base", Rules: map[string]lint.Setting{"every-node": {}}}}, "base")
	if err != nil {
		t.Fatal(err)
	}
	conf, err := reg.Configure(lint.Config{Version: lint.ConfigVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for seed := uint64(1); seed <= 100; seed++ {
		old, edited := diffCase(rand.New(rand.NewPCG(seed, 3)))
		var newText []string
		for _, l := range edited {
			newText = append(newText, l.text)
		}
		src := strings.Join(newText, "\n") + "\n"
		in := lintInput(src)
		if !in.Checked {
			t.Fatalf("seed %d: the edited program does not check:\n%s", seed, src)
		}
		full := conf.Run(in)
		added := lint.ParseDiff(gitDiff(t, old, src))
		got := full.InDiff(in, lint.Added{"": added["main.ergo"]})
		var want []lint.Finding
		for _, f := range full.Findings {
			first := strings.Count(src[:f.Offset], "\n") + 1
			last := first + strings.Count(src[f.Offset:f.Offset+f.Length], "\n")
			if strings.HasSuffix(src[f.Offset:f.Offset+f.Length], "\n") {
				last--
			}
			for line := first; line <= last; line++ {
				if edited[line-1].added {
					want = append(want, f)
					break
				}
			}
		}
		key := func(fs []lint.Finding) string {
			var out []string
			for _, f := range fs {
				out = append(out, fmt.Sprintf("%d+%d", f.Offset, f.Length))
			}
			return strings.Join(out, " ")
		}
		if key(got.Findings) != key(want) {
			t.Fatalf("seed %d: --diff kept %s, want %s\nadded %v\nold:\n%s\nnew:\n%s\nreplay: go test ./internal/sketch/lint -run TestDiffScopeIsTheLaw", seed, key(got.Findings), key(want), added, old, src)
		}
	}
}

// gitDiff writes old and new as old/main.ergo and main.ergo and returns
// git's diff of the two in the form the command reads (-U0, no prefix);
// the new side is named main.ergo. It compares files, not a commit: a
// commit costs a second or more on a loaded host and tests nothing more.
func gitDiff(t *testing.T, old, new string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old", "main.ergo"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(new), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "--no-optional-locks", "-c", "core.quotePath=false", "diff", "--no-index", "-U0", "--no-color", "--no-ext-diff", "--no-prefix", "old/main.ergo", "main.ergo")
	cmd.Dir = dir
	out, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); err != nil && (!ok || exit.ExitCode() != 1) {
		t.Fatalf("git diff --no-index: %v", err) // exit 1 is "the files differ"
	}
	return string(out)
}
