package residue_test

import (
	"bytes"
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

// The repository's GitHub issues are not a home for anything the
// repository needs: a rule, an owner or a stage is named in words. Two
// shrinking allow lists hold the files that still carry a reference until
// the unit named in each row deletes or rewrites them.
const (
	issueLinksAllow = "testdata/issue-links-allow.txt"
	issueTagsAllow  = "testdata/issue-tags-allow.txt"
)

// issueLink matches a link to an issue of this repository, in URL or
// owner/repo#N form.
var issueLink = regexp.MustCompile(`github\.com/cevr/ergo/issues|\bcevr/ergo#[0-9]`)

// itemTag matches an item tag of the v1 goal's issue: its number and one
// letter, as a rule's label once was. A digit, letter, `;`,
// `#` or `[` before it is another token (an ANSI colour 38;5;196m, a hash).
var itemTag = regexp.MustCompile(`(?:^|[^0-9A-Za-z_;#\[])196[a-z]\b`)

// countTags is the number of item tags in text.
func countTags(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		n += len(itemTag.FindAllStringIndex(line, -1))
	}
	return n
}

// codeTag reports whether a line of non-test Go names an item tag outside
// its comment: a message a user reads, which must say the rule in words.
func codeTag(line string) bool {
	code := line
	if i := strings.Index(line, "//"); i >= 0 {
		code = line[:i]
	}
	return itemTag.MatchString(code)
}

// linkRow is the allow row of a path: an exact file, or a directory when
// the row ends in "/".
func linkRow(allow map[string]string, name string) (string, bool) {
	if _, ok := allow[name]; ok {
		return name, true
	}
	for row := range allow {
		if strings.HasSuffix(row, "/") && strings.HasPrefix(name, row) {
			return row, true
		}
	}
	return "", false
}

// mirroredMessages starts the unit column of a tag row whose messages are
// copied into the generated std/gostd bindings: the message and the
// bindings change together when the std is regenerated, so until then its
// message tags are admitted, counted like comments.
const mirroredMessages = "STD-REGEN"

// judgeIssueRefs holds files (tracked path -> text) to the two allow
// lists: links admits paths, tags maps a path to its exact tag count, and
// mirrored names the tag rows whose messages may still carry a tag.
func judgeIssueRefs(files map[string]string, links map[string]string, tags map[string]int, mirrored map[string]bool) []string {
	var problems []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	usedLinks := map[string]bool{}
	seenTags := map[string]bool{}
	for _, name := range names {
		text := files[name]
		if issueLink.MatchString(text) {
			if row, ok := linkRow(links, name); ok {
				usedLinks[row] = true
			} else {
				for i, line := range strings.Split(text, "\n") {
					if issueLink.MatchString(line) {
						problems = append(problems, fmt.Sprintf("%s:%d links an issue; name what it stood for in words: %s", name, i+1, strings.TrimSpace(line)))
					}
				}
			}
		}
		if strings.Contains("/"+name, "/testdata/") {
			continue
		}
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && !mirrored[name] {
			for i, line := range strings.Split(text, "\n") {
				if codeTag(line) {
					problems = append(problems, fmt.Sprintf("%s:%d: a message names an item tag of the v1 goal; say the rule in words: %s", name, i+1, strings.TrimSpace(line)))
				}
			}
		}
		n := countTags(text)
		want, listed := tags[name]
		switch {
		case listed:
			seenTags[name] = true
			if n != want {
				problems = append(problems, fmt.Sprintf("%s: %s has %d item tags, its row says %d; set the row to %d (0: remove it)", issueTagsAllow, name, n, want, n))
			}
		case n > 0:
			problems = append(problems, fmt.Sprintf("%s has %d item tags of the v1 goal (its number and a letter); name the rule in words", name, n))
		}
	}
	for row := range links {
		if !usedLinks[row] {
			problems = append(problems, fmt.Sprintf("%s: row %s admits no issue link; remove it", issueLinksAllow, row))
		}
	}
	for row := range tags {
		if !seenTags[row] {
			problems = append(problems, fmt.Sprintf("%s: row %s names no tracked file; remove it", issueTagsAllow, row))
		}
	}
	sort.Strings(problems)
	return problems
}

// fixture spells the cases' references, so this file names none itself:
// @L is an issue URL's prefix, @R the owner/repo# form, @T the v1 goal's
// number before an item letter.
var fixture = strings.NewReplacer("@L", "https://github.com/cevr/"+"ergo/issues/", "@R", "cevr/"+"ergo#", "@T", "19"+"6")

// TestIssueRefsRules holds the matching on fixed text, so a copy of the
// package without .git (the mutant runner's) judges it too.
func TestIssueRefsRules(t *testing.T) {
	for _, c := range []struct {
		line       string
		link, code bool
		tags       int
	}{
		{"see [#49](@L49)", true, false, 0},
		{"see @R12 for it", true, false, 0},
		{"https://github.com/cevr/ergo/pull/3 and https://github.com/cevr/ergo", false, false, 0},
		{"https://github.com/other/ergo/issues/4", false, false, 0},
		{`refuse("no shadowing (@Tv rule 2)")`, false, true, 1},
		{`x := 1 // stage @Tb, as @Tl says`, false, false, 2},
		{`"\x1b[38;5;196m" and 0x196a and a196b and #@Tc`, false, false, 0},
		{"refusals196u(@Tu)", false, true, 1},
	} {
		c.line = fixture.Replace(c.line)
		if got := issueLink.MatchString(c.line); got != c.link {
			t.Errorf("issueLink(%q) = %v, want %v", c.line, got, c.link)
		}
		if got := codeTag(c.line); got != c.code {
			t.Errorf("codeTag(%q) = %v, want %v", c.line, got, c.code)
		}
		if got := countTags(c.line); got != c.tags {
			t.Errorf("countTags(%q) = %d, want %d", c.line, got, c.tags)
		}
	}
	files := map[string]string{
		"a.md":            "[#1](@L1)",
		"old/b.md":        "@L2",
		"c.go":            "package c\n\nvar m = \"(@Tu)\" // @Tu\n",
		"c_test.go":       "var m = \"(@Tu)\"\n",
		"d.md":            "@Tb and @Tm\n",
		"x/testdata/e.go": "var m = \"@Tu @L3\"\n",
		"f.go":            "package f\n\nvar m = \"(@Tc)\"\n",
	}
	links := map[string]string{"old/": "DEL", "gone.md": "DEL"}
	tags := map[string]int{"c.go": 2, "c_test.go": 1, "d.md": 1, "gone.md": 1, "f.go": 1}
	mirrored := map[string]bool{"f.go": true}
	want := []string{
		"a.md:1 links an issue; name what it stood for in words: [#1](@L1)",
		"c.go:3: a message names an item tag of the v1 goal; say the rule in words: var m = \"(@Tu)\" // @Tu",
		"testdata/issue-links-allow.txt: row gone.md admits no issue link; remove it",
		"testdata/issue-tags-allow.txt: d.md has 2 item tags, its row says 1; set the row to 2 (0: remove it)",
		"testdata/issue-tags-allow.txt: row gone.md names no tracked file; remove it",
		"x/testdata/e.go:1 links an issue; name what it stood for in words: var m = \"@Tu @L3\"",
	}
	for name, text := range files {
		files[name] = fixture.Replace(text)
	}
	for i, w := range want {
		want[i] = fixture.Replace(w)
	}
	got := judgeIssueRefs(files, links, tags, mirrored)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("judgeIssueRefs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestNoIssueRefs reads every tracked text file outside the vendored
// corpora: no file links an issue of this repository, no message of
// non-test Go names an item tag of the v1 goal, and the files that still
// carry a reference are exactly the allow lists' rows, each with the unit
// that removes it.
func TestNoIssueRefs(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("no git checkout (the mutant runner's copy): TestIssueRefsRules judges the rules: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := map[string]string{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasPrefix(name, "conformance/upstream/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue
		}
		files[name] = string(data)
	}
	if len(files) < 1000 {
		t.Fatalf("read %d tracked files; the listing is broken", len(files))
	}
	links := readIssueAllow(t, issueLinksAllow, 2)
	tagRows := readIssueAllow(t, issueTagsAllow, 3)
	tags := map[string]int{}
	mirrored := map[string]bool{}
	for name, rest := range tagRows {
		cols := strings.SplitN(rest, "\t", 2)
		n, err := strconv.Atoi(cols[0])
		if err != nil || n <= 0 {
			t.Errorf("%s: row %s: the count %q is not a positive number", issueTagsAllow, name, rest)
		}
		tags[name] = n
		mirrored[name] = len(cols) == 2 && strings.HasPrefix(cols[1], mirroredMessages)
	}
	for _, p := range judgeIssueRefs(files, links, tags, mirrored) {
		t.Error(p)
	}
}

// readIssueAllow reads a tab-separated allow list of fields columns: the first
// column to the rest of the row.
func readIssueAllow(t *testing.T, path string, fields int) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for i, text := range strings.Split(string(data), "\n") {
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) != fields || cols[0] == "" || strings.TrimSpace(cols[fields-1]) == "" {
			t.Errorf("%s:%d: want %d tab-separated columns, got %q", path, i+1, fields, text)
			continue
		}
		if _, dup := rows[cols[0]]; dup {
			t.Errorf("%s:%d: a second row for %s", path, i+1, cols[0])
		}
		rows[cols[0]] = strings.Join(cols[1:], "\t")
	}
	return rows
}
