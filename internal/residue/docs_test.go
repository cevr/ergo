package residue_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The tracked documents are a closed set: every tracked Markdown file is a
// row of testdata/docs.txt, and a spec item that source cites exists. Both
// rules read `git ls-files` and file text only.

// docRow is one row of testdata/docs.txt: path-or-glob, budget ("-" for a
// file not yet rewritten) and the file's one job.
type docRow struct {
	path, budget, job string
	glob              *regexp.Regexp // nil for an exact row
}

// docExempt reports whether a tracked path is outside the document set:
// the vendored corpus and every package's testdata, which are inputs.
func docExempt(path string) bool {
	return strings.HasPrefix(path, "conformance/upstream/") || strings.Contains(path, "/testdata/")
}

// docGlob compiles a ledger glob: "*" stays inside one path segment and
// "**/" is any number of segments.
func docGlob(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case g[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// parseDocLedger reads docs.txt; a malformed or repeated row is a problem.
func parseDocLedger(text string) ([]docRow, []string) {
	var rows []docRow
	var problems []string
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] == "" || strings.TrimSpace(f[2]) == "" {
			problems = append(problems, fmt.Sprintf("docs.txt:%d: want `path-or-glob<TAB>budget<TAB>job`, got %q", i+1, line))
			continue
		}
		if n, err := strconv.Atoi(f[1]); f[1] != "-" && (err != nil || n <= 0) {
			problems = append(problems, fmt.Sprintf("docs.txt:%d: budget %q is neither bytes nor -", i+1, f[1]))
			continue
		}
		if seen[f[0]] {
			problems = append(problems, fmt.Sprintf("docs.txt:%d: %s has a second row", i+1, f[0]))
			continue
		}
		seen[f[0]] = true
		row := docRow{path: f[0], budget: f[1], job: f[2]}
		if strings.Contains(f[0], "*") {
			row.glob = docGlob(f[0])
		}
		rows = append(rows, row)
	}
	return rows, problems
}

// docsListedProblems holds the tracked files to the ledger: each tracked
// Markdown file outside the exempt classes matches a row, and each exact
// row names a tracked Markdown file.
func docsListedProblems(tracked []string, rows []docRow) []string {
	var problems []string
	exact := map[string]bool{}
	for _, r := range rows {
		if r.glob == nil {
			exact[r.path] = true
		}
	}
	docs := map[string]bool{}
	for _, f := range tracked {
		if !strings.HasSuffix(f, ".md") || docExempt(f) {
			continue
		}
		docs[f] = true
		if exact[f] {
			continue
		}
		listed := false
		for _, r := range rows {
			if r.glob != nil && r.glob.MatchString(f) {
				listed = true
				break
			}
		}
		if !listed {
			problems = append(problems, fmt.Sprintf("%s: a document with no row in testdata/docs.txt: put a rule in its spec file and a measurement in the request instead", f))
		}
	}
	for _, r := range rows {
		if r.glob == nil && !docs[r.path] {
			problems = append(problems, fmt.Sprintf("testdata/docs.txt: %s is not a tracked document: delete its row", r.path))
		}
	}
	return problems
}

// gitFiles lists the checkout's tracked files.
func gitFiles(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", "--no-optional-locks", "-C", repo, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// TestDocsAreListed: a document is added only with a ledger row, and a
// row goes with its file.
func TestDocsAreListed(t *testing.T) {
	data, err := os.ReadFile("testdata/docs.txt")
	if err != nil {
		t.Fatal(err)
	}
	rows, problems := parseDocLedger(string(data))
	problems = append(problems, docsListedProblems(gitFiles(t, specRoot(t)), rows)...)
	for _, p := range problems {
		t.Error(p)
	}
}

// specCitation matches a citation of a numbered spec item in source:
// "(rows.md, Totality 1)".
var specCitation = regexp.MustCompile(`\b([a-z]+)\.md, ([A-Z][A-Za-z' -]*?) (\d+)\b`)

var specItemLine = regexp.MustCompile(`^(\d+)\. `)

// specItems maps each heading of a spec text, lower-cased, to the numbers
// of the items under it, its subsections' included.
func specItems(text string) map[string]map[string]bool {
	type heading struct {
		level int
		title string
	}
	out := map[string]map[string]bool{}
	var stack []heading
	fence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if level := len(line) - len(strings.TrimLeft(line, "#")); level > 0 && level <= 6 && strings.HasPrefix(line[level:], " ") {
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			title := strings.ToLower(strings.NewReplacer("`", "", "*", "").Replace(strings.TrimSpace(line[level:])))
			stack = append(stack, heading{level, title})
			continue
		}
		if m := specItemLine.FindStringSubmatch(line); m != nil {
			for _, h := range stack {
				if out[h.title] == nil {
					out[h.title] = map[string]bool{}
				}
				out[h.title][m[1]] = true
			}
		}
	}
	return out
}

// citationKey is a dangling citation's allowlist key: the citing file and
// the citation, so a moved line keeps its row.
func citationKey(file, cite string) string { return file + "\t" + cite }

// danglingCitations lists, by key, the citations in sources (path to text)
// of an item that specs (name without ".md" to text) does not hold. A
// citation names its section by the heading or by the heading's first
// words ("Zero values" for "Zero values and definite initialisation"),
// with an optional trailing "rule" or "rules".
func danglingCitations(sources, specs map[string]string) map[string]bool {
	items := map[string]map[string]map[string]bool{}
	out := map[string]bool{}
	for file, text := range sources {
		for _, m := range specCitation.FindAllStringSubmatch(text, -1) {
			spec, ok := specs[m[1]]
			if !ok {
				continue
			}
			if items[m[1]] == nil {
				items[m[1]] = specItems(spec)
			}
			want := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(m[2]), " rules"), " rule")
			found := false
			for title, nums := range items[m[1]] {
				if (title == want || strings.HasPrefix(title, want+" ") || strings.HasPrefix(title, want+",")) && nums[m[3]] {
					found = true
					break
				}
			}
			if !found {
				out[citationKey(file, m[0])] = true
			}
		}
	}
	return out
}

// citationProblems holds the dangling citations to an exact allowlist
// (key to owner): an unlisted dangling citation and a listed one that
// resolves (or is gone) are both problems, so the list only shrinks.
func citationProblems(dangling map[string]bool, allow map[string]string) []string {
	var problems []string
	for key := range dangling {
		if _, ok := allow[key]; !ok {
			file, cite, _ := strings.Cut(key, "\t")
			problems = append(problems, fmt.Sprintf("%s: %q names no item of docs/spec: cite the item that states the rule", file, cite))
		}
	}
	for key, owner := range allow {
		if !dangling[key] {
			problems = append(problems, fmt.Sprintf("testdata/spec-citations-allow.txt: %q resolves or is gone: delete its row (owner %s)", key, owner))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestSpecCitationsResolve: a spec item that a diagnostic or a comment
// cites exists, so a spec rewrite that drops a cited item, and a new
// citation of no item, are red.
func TestSpecCitationsResolve(t *testing.T) {
	repo := specRoot(t)
	sources, specs := map[string]string{}, map[string]string{}
	for _, f := range gitFiles(t, repo) {
		isSpec := strings.HasPrefix(f, "docs/spec/") && strings.HasSuffix(f, ".md") && !strings.Contains(f[len("docs/spec/"):], "/")
		isSource := (strings.HasSuffix(f, ".go") || strings.HasSuffix(f, ".ergo")) && !docExempt(f)
		if !isSpec && !isSource {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			t.Fatal(err)
		}
		if isSpec {
			specs[strings.TrimSuffix(f[len("docs/spec/"):], ".md")] = string(data)
		} else if strings.Contains(string(data), ".md, ") {
			sources[f] = string(data)
		}
	}
	allow := map[string]string{}
	data, err := os.ReadFile("testdata/spec-citations-allow.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || strings.TrimSpace(f[2]) == "" {
			t.Errorf("spec-citations-allow.txt:%d: want `file<TAB>citation<TAB>owner`, got %q", i+1, line)
			continue
		}
		allow[citationKey(f[0], f[1])] = f[2]
	}
	for _, p := range citationProblems(danglingCitations(sources, specs), allow) {
		t.Error(p)
	}
}

// TestDocsListedRules pins the ledger rule on fixed text, so the mutant
// runner's copy of the checkout (no .git) judges it.
func TestDocsListedRules(t *testing.T) {
	ledger := "# a comment\n" +
		"README.md\t-\twhat it is\n" +
		"docs/spec/rows.md\t40000\trows\n" +
		"examples/**/README.md\t-\tone example\n" +
		"hosts/*/README.md\t-\tone host\n"
	rows, problems := parseDocLedger(ledger)
	if len(problems) != 0 || len(rows) != 4 {
		t.Fatalf("parse: %d rows, problems %v", len(rows), problems)
	}
	for _, c := range []struct {
		tracked  []string
		problems int
		name     string
	}{
		{[]string{"README.md", "docs/spec/rows.md", "go.mod"}, 0, "every document listed"},
		{[]string{"README.md", "docs/spec/rows.md", "docs/notes.md"}, 1, "a new document"},
		{[]string{"README.md"}, 1, "an exact row with no file"},
		{[]string{"README.md", "docs/spec/rows.md", "examples/README.md", "examples/a/b/README.md"}, 0, "a ** glob at any depth"},
		{[]string{"README.md", "docs/spec/rows.md", "hosts/go/README.md", "hosts/go/x/README.md"}, 1, "a * glob stays in one segment"},
		{[]string{"README.md", "docs/spec/rows.md", "x/testdata/a.md", "conformance/upstream/k/README.md"}, 0, "exempt inputs"},
		{[]string{"README.md", "docs/spec/rows.md", "docs/specx/rows.md"}, 1, "a glob's literal text is literal"},
	} {
		if got := docsListedProblems(c.tracked, rows); len(got) != c.problems {
			t.Errorf("%s: %v, want %d problems", c.name, got, c.problems)
		}
	}
	for _, c := range []struct {
		ledger string
		name   string
	}{
		{"README.md\twhat it is\n", "two fields"},
		{"README.md\tlots\twhat it is\n", "a budget that is not bytes"},
		{"README.md\t-\tit\nREADME.md\t-\tagain\n", "a second row"},
		{"README.md\t-\t \n", "no job"},
	} {
		if _, problems := parseDocLedger(c.ledger); len(problems) != 1 {
			t.Errorf("%s: problems %v, want 1", c.name, problems)
		}
	}
}

// TestSpecCitationRules pins the citation rule on fixed text.
func TestSpecCitationRules(t *testing.T) {
	specs := map[string]string{
		"rows": "# Rows\n\n## Capability rows\n\n1. One.\n2. Two.\n\n" +
			"## Zero values and definite initialisation\n\n1. Zero.\n\n```\n7. not an item\n```\n\n" +
			"## Totality\n\n### Tail results\n\n3. Tail.\n",
	}
	for _, c := range []struct {
		source string
		dangle int
		name   string
	}{
		{"// (rows.md, Capability rows 2)", 0, "an item of its section"},
		{"// (rows.md, Capability rows 3)", 1, "an item past the section's end"},
		{"// (rows.md, Zero values 1)", 0, "a heading's first words"},
		{"// (rows.md, Zero val 1)", 1, "a prefix that is not whole words"},
		{"// (rows.md, Zero values and definite initialisation 7)", 1, "a number in a fenced block"},
		{"// (rows.md, Totality 3)", 0, "an item of a subsection"},
		{"// (rows.md, Capability rows rule 1)", 0, "a trailing rule"},
		{"// (rows.md, Entry 4)", 1, "no such section"},
		{"// (notes.md, Entry 4)", 0, "not a spec file"},
	} {
		if got := danglingCitations(map[string]string{"a.go": c.source}, specs); len(got) != c.dangle {
			t.Errorf("%s: %v, want %d dangling", c.name, got, c.dangle)
		}
	}
	dangling := map[string]bool{citationKey("a.go", "rows.md, Entry 4"): true, citationKey("b.go", "rows.md, Entry 4"): true}
	for _, c := range []struct {
		allow    map[string]string
		problems int
		name     string
	}{
		{map[string]string{citationKey("a.go", "rows.md, Entry 4"): "U", citationKey("b.go", "rows.md, Entry 4"): "U"}, 0, "every dangling citation listed"},
		{map[string]string{citationKey("a.go", "rows.md, Entry 4"): "U"}, 1, "a new dangling citation"},
		{map[string]string{citationKey("a.go", "rows.md, Entry 4"): "U", citationKey("b.go", "rows.md, Entry 4"): "U", citationKey("c.go", "rows.md, Entry 4"): "U"}, 1, "a row that no longer dangles"},
	} {
		if got := citationProblems(dangling, c.allow); len(got) != c.problems {
			t.Errorf("%s: %v, want %d problems", c.name, got, c.problems)
		}
	}
}
