package residue_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The rules that hold the specification's text (docs/spec): a rule
// paragraph carries no history, and one form has one name. A file that
// does not hold a rule yet is on the rule's allowlist; the allowlist is
// exact, so it only shrinks: a listed file that holds the rule fails until
// its row goes.

// specHistory matches what belongs to the history of a rule, not to the
// rule: an owner question number, a review round, or the id of the unit or
// stage that built it. Rule ids (R1, G1, I2, Gap 25) and issue links are
// not history.
var specHistory = regexp.MustCompile(`(?i:\bq\d+\b)` +
	`|(?i:\breview \d+\b|\bround[- ]\d+\b|\b(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth) round\b|\bfix-r\d+\b)` +
	`|\b\d{3}[a-z]{1,2}\b|\b(?:check|lower)-v1\b|\bL\d+(?: [A-Z]\d+[a-z]?)?\b|\bD-[A-Z]+\d*(?:-\d+)?\b|\bstage \d+[a-z]?\b|\bbatch \d+\b`)

// specRetired matches a retired name of a form: the language writes record
// (not struct), match (not type switch), let (not var or :=) and operation
// type (not interface or service). Go's own construct keeps its name after
// "Go" or "Go's".
var specRetired = regexp.MustCompile(`(?i)\b(?:structs?|type switch(?:es)?|var|interfaces?|services?)\b|:=`)

// specAssumption matches an assumption that stands for an open owner
// question: it is normative only with a docs/decisions.md entry.
var specAssumption = regexp.MustCompile(`\b(q\d+) assumption\b`)

// specProse calls each normative line of a spec file: the lines outside
// fenced blocks, headings, generated Status lines and Prior art sections,
// with inline code, link targets, comments and inline prior art removed.
func specProse(text string, each func(line int, prose string)) {
	fence, priorArt := false, false
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			priorArt = strings.Contains(strings.ToLower(trimmed), "prior art")
			continue
		}
		if priorArt || strings.HasPrefix(trimmed, "Status") || strings.HasPrefix(trimmed, "<!--") {
			continue
		}
		prose := specCode.ReplaceAllString(line, "")
		prose = specLinkTarget.ReplaceAllString(prose, "]")
		if at := strings.Index(prose, "*Prior art:*"); at >= 0 {
			prose = prose[:at]
		}
		each(i+1, prose)
	}
}

var (
	specCode       = regexp.MustCompile("`[^`]*`")
	specLinkTarget = regexp.MustCompile(`\]\([^)]*\)`)
)

// historyHits lists the history a spec text's rule paragraphs carry.
func historyHits(text string) []string {
	var out []string
	specProse(text, func(line int, prose string) {
		for _, m := range specHistory.FindAllString(prose, -1) {
			out = append(out, fmt.Sprintf("%d: %q", line, m))
		}
	})
	return out
}

// retiredHits lists the retired names a spec text's rule paragraphs use.
func retiredHits(text string) []string {
	var out []string
	specProse(text, func(line int, prose string) {
		for _, at := range specRetired.FindAllStringIndex(prose, -1) {
			before := strings.TrimRight(prose[:at[0]], " ")
			if strings.HasSuffix(before, "Go") || strings.HasSuffix(before, "Go's") {
				continue
			}
			out = append(out, fmt.Sprintf("%d: %q", line, prose[at[0]:at[1]]))
		}
	})
	return out
}

// specRoot is the checkout's root.
func specRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(root))
}

// specFiles reads every docs/spec/*.md file, by base name.
func specFiles(t *testing.T, repo string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repo, "docs/spec/*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("spec files: %v", err)
	}
	out := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(path)] = string(data)
	}
	return out
}

// readSpecAllow reads a `file<TAB>owner` allowlist.
func readSpecAllow(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for i, text := range strings.Split(string(data), "\n") {
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		file, owner, ok := strings.Cut(text, "\t")
		if !ok || file == "" || strings.TrimSpace(owner) == "" {
			t.Errorf("%s:%d: want `file<TAB>owner`, got %q", path, i+1, text)
			continue
		}
		out[file] = owner
	}
	return out
}

// checkSpecRule applies one text rule to every spec file against its exact
// allowlist.
func checkSpecRule(t *testing.T, allowPath, what string, hits func(string) []string) {
	repo := specRoot(t)
	allow := readSpecAllow(t, allowPath)
	for _, p := range specRuleProblems(specFiles(t, repo), allow, allowPath, what, hits) {
		t.Error(p)
	}
}

// specRuleProblems applies one text rule to files against an allowlist.
func specRuleProblems(files, allow map[string]string, allowPath, what string, hits func(string) []string) []string {
	var problems []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	found := map[string][]string{}
	for _, name := range names {
		found[name] = hits(files[name])
		if got := found[name]; len(got) > 0 {
			if _, listed := allow[name]; !listed {
				problems = append(problems, fmt.Sprintf("docs/spec/%s: %s (%d), first %v", name, what, len(got), got[:min(5, len(got))]))
			}
		}
	}
	for name, owner := range allow {
		switch _, ok := files[name]; {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s lists %s, which is not a spec file", allowPath, name))
		case len(found[name]) == 0:
			problems = append(problems, fmt.Sprintf("docs/spec/%s holds the rule now: delete its row from %s (owner %s)", name, allowPath, owner))
		}
	}
	return problems
}

// TestSpecRulesCarryNoHistory: a normative paragraph of the specification
// states the rule only; question numbers, review rounds and unit ids go to
// docs/decisions.md.
func TestSpecRulesCarryNoHistory(t *testing.T) {
	checkSpecRule(t, "testdata/spec-history-allow.txt", "history in a rule paragraph", historyHits)
}

// TestSpecUsesOneNamePerForm: the specification names each form by the
// grammar's word: record, match, let, operation type ("capability" stays
// for the ambient role).
func TestSpecUsesOneNamePerForm(t *testing.T) {
	checkSpecRule(t, "testdata/spec-names-allow.txt", "a retired name", retiredHits)
}

// TestSpecAssumptionsAreDecided: an assumption that stands for an open
// owner question is in docs/decisions.md.
func TestSpecAssumptionsAreDecided(t *testing.T) {
	repo := specRoot(t)
	decisions, err := os.ReadFile(filepath.Join(repo, "docs/decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range specFiles(t, repo) {
		for _, m := range specAssumption.FindAllStringSubmatch(text, -1) {
			if !regexp.MustCompile(`\b` + m[1] + `\b`).Match(decisions) {
				t.Errorf("docs/spec/%s: %q has no docs/decisions.md entry", name, m[0])
			}
		}
	}
}

// TestSpecTextRules pins the two text rules on fixed text.
func TestSpecTextRules(t *testing.T) {
	for _, c := range []struct {
		text             string
		history, retired int
		name             string
	}{
		{"1. **R5.** A scope closes what it owns (q54).", 1, 0, "a question number"},
		{"Checked (check-v1 stage C, 196r; review 9).", 3, 0, "stage, unit and round"},
		{"1. **R5.** See [decisions](../decisions.md#q54-owner).", 0, 0, "a link target"},
		{"A record field; G1 holds; R3 and Gap 25 apply.", 0, 0, "rule ids"},
		{"A struct literal names every field.", 0, 1, "struct in prose"},
		{"A `struct` is refused; a Go struct maps to a record.", 0, 0, "code and Go's construct"},
		{"```go\ntype T struct { x int }\nvar v = T{}\n```\nA record.", 0, 0, "a fenced block"},
		{"## Prior art\n\nGleam's records (q12) and Go's interface.\n## Rules\n\nA service.", 0, 1, "a prior art section"},
		{"1. A rule. *Prior art:* Rust's `Drop` (round 3).", 0, 0, "inline prior art"},
		{"Status: **implemented** (L8 B2).", 0, 0, "a generated status line"},
	} {
		if got := historyHits(c.text); len(got) != c.history {
			t.Errorf("%s: history %v, want %d", c.name, got, c.history)
		}
		if got := retiredHits(c.text); len(got) != c.retired {
			t.Errorf("%s: retired %v, want %d", c.name, got, c.retired)
		}
	}
	// The allowlist is exact over every file: a listed file that holds the
	// rule, and an unlisted one that breaks it, are both problems.
	files := map[string]string{"a.md": "A rule (q1).", "b.md": "A rule.", "c.md": "A rule (q2)."}
	for _, c := range []struct {
		allow    map[string]string
		problems int
		name     string
	}{
		{map[string]string{"a.md": "U", "c.md": "U"}, 0, "the breaking files listed"},
		{map[string]string{"a.md": "U"}, 1, "the last file unlisted"},
		{map[string]string{"a.md": "U", "b.md": "U", "c.md": "U"}, 1, "a clean file listed"},
		{map[string]string{"a.md": "U", "c.md": "U", "d.md": "U"}, 1, "no such file"},
	} {
		if got := specRuleProblems(files, c.allow, "allow.txt", "history", historyHits); len(got) != c.problems {
			t.Errorf("%s: %v, want %d problems", c.name, got, c.problems)
		}
	}
}
