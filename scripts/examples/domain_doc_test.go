package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// quoteMarker introduces a code block of docs/domain.md: the block is the
// declarations it names, quoted from a file of the repository.
var quoteMarker = regexp.MustCompile(`^<!-- quote (\S+) ([A-Za-z0-9_ ]+) -->$`)

// TestDomainDocQuotesResolve: every code block of docs/domain.md is a
// quote of examples/ (a marker names the file and the declarations), and
// it is the text of those declarations now, so the doc cannot drift from
// the worked example it teaches (DK1). Mutant: a quoted line edited in the
// doc and not in the source (dk1-doc-drift).
func TestDomainDocQuotesResolve(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	doc, err := os.ReadFile(filepath.Join(root, "docs", "domain.md"))
	if err != nil {
		t.Fatal(err)
	}
	problems, quotes := domainDocProblems(string(doc), func(path string) (string, error) {
		if !strings.HasPrefix(path, "examples/") {
			return "", fmt.Errorf("a quote comes from examples/, not %s", path)
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		return string(b), err
	})
	for _, p := range problems {
		t.Error(p)
	}
	if quotes == 0 {
		t.Error("docs/domain.md quotes nothing")
	}
}

// domainDocProblems checks each fenced block of doc against the quote its
// marker names, read by read; it returns the problems, by line, and the
// number of quotes.
func domainDocProblems(doc string, read func(path string) (string, error)) ([]string, int) {
	var problems []string
	lines := strings.Split(doc, "\n")
	quotes := 0
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```") {
			continue
		}
		start := i
		end := start + 1
		for end < len(lines) && lines[end] != "```" {
			end++
		}
		i = end
		var m []string
		if start > 0 {
			m = quoteMarker.FindStringSubmatch(lines[start-1])
		}
		if m == nil {
			problems = append(problems, fmt.Sprintf("docs/domain.md:%d: a code block that is no quote; write <!-- quote FILE NAME... --> above it", start+1))
			continue
		}
		quotes++
		src, err := read(m[1])
		if err != nil {
			problems = append(problems, fmt.Sprintf("docs/domain.md:%d: %v", start, err))
			continue
		}
		var parts []string
		for _, name := range strings.Fields(m[2]) {
			d, ok := declarationText(src, name)
			if !ok {
				problems = append(problems, fmt.Sprintf("docs/domain.md:%d: %s declares no %s", start, m[1], name))
				continue
			}
			parts = append(parts, d)
		}
		want := strings.Join(parts, "\n\n")
		if got := strings.Join(lines[start+1:min(end, len(lines))], "\n"); got != want {
			problems = append(problems, fmt.Sprintf("docs/domain.md:%d: the block is not %s %s now; the source says:\n%s", start+1, m[1], m[2], want))
		}
	}
	return problems, quotes
}

// declarationText is the top-level declaration name of src with its doc
// comment: the comment lines just above it, then its first line, and,
// when that line opens a brace, every line up to the closing brace at the
// start of a line.
func declarationText(src, name string) (string, bool) {
	lines := strings.Split(src, "\n")
	head := regexp.MustCompile(`^(func|type|law|let) ` + regexp.QuoteMeta(name) + `\b`)
	for i, l := range lines {
		if !head.MatchString(l) {
			continue
		}
		from := i
		for from > 0 && strings.HasPrefix(lines[from-1], "//") {
			from--
		}
		to := i
		if strings.HasSuffix(l, "{") {
			for to < len(lines) && lines[to] != "}" {
				to++
			}
		}
		if to == len(lines) {
			return "", false
		}
		return strings.Join(lines[from:to+1], "\n"), true
	}
	return "", false
}
