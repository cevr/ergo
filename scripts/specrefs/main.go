// Command specrefs lists, and on a renumbering moves, the references to the
// numbered items of a spec section: "interop.md, Lowering 9", "[grammar.md]
// (grammar.md#expressions), Expressions 1 and 6". It reads a reference
// wrapped across lines whole: the lines of one comment (// lines in Go and
// Ergo sources) and of one Markdown paragraph are one text, so "Lowering"
// at the end of a line and "9" at the start of the next is a reference to
// Lowering 9 (batch 18 verify M3, which a line-by-line grep missed).
//
//	go run ./scripts/specrefs DOC SECTION             list every reference
//	go run ./scripts/specrefs -move 9=13,13=9 DOC SECTION
//	                                                  rewrite the numbers
//
// DOC is the spec file's name (interop.md); SECTION its section's title
// (Lowering). It reads the tracked .go, .ergo and .md files of the checkout
// at -root.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("specrefs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "the checkout")
	move := flags.String("move", "", "rewrite item numbers: `FROM=TO,...`, applied at once")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: specrefs [-root DIR] [-move FROM=TO,...] DOC SECTION")
		return 2
	}
	moves := map[string]string{}
	if *move != "" {
		for _, pair := range strings.Split(*move, ",") {
			from, to, ok := strings.Cut(pair, "=")
			if !ok || from == "" || to == "" {
				fmt.Fprintf(stderr, "specrefs: -move %q: want FROM=TO\n", pair)
				return 2
			}
			moves[from] = to
		}
	}
	files, err := tracked(*root)
	if err != nil {
		fmt.Fprintf(stderr, "specrefs: %v\n", err)
		return 2
	}
	pattern := referencePattern(flags.Arg(0), flags.Arg(1))
	for _, file := range files {
		path := filepath.Join(*root, filepath.FromSlash(file))
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "specrefs: %v\n", err)
			return 2
		}
		refs := find(string(data), strings.HasSuffix(file, ".md"), pattern)
		for _, r := range refs {
			fmt.Fprintf(stdout, "%s:%d: %s\n", file, r.line, r.text)
		}
		if len(moves) == 0 || len(refs) == 0 {
			continue
		}
		out, changed := rewrite(string(data), refs, moves)
		if changed {
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				fmt.Fprintf(stderr, "specrefs: %v\n", err)
				return 2
			}
		}
	}
	return 0
}

// tracked lists the checkout's tracked .go, .ergo and .md files.
func tracked(root string) ([]string, error) {
	out, err := exec.Command("git", "--no-optional-locks", "-C", root, "ls-files", "-z", "--", "*.go", "*.ergo", "*.md").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, nil
}

// referencePattern matches a reference to doc's section: the doc's name
// (bare, or a Markdown link to it), a comma, the section's title and its
// numbers ("9", "1 and 6", "9-13"). Group 1 is the numbers.
func referencePattern(doc, section string) *regexp.Regexp {
	d, s := regexp.QuoteMeta(doc), strings.ReplaceAll(regexp.QuoteMeta(section), " ", `\s+`)
	number := `\d+[a-z]?`
	return regexp.MustCompile(`\[?\b` + d + `\]?(?:\([^)\s]*\))?,\s+` + s + `\s+(` + number + `(?:(?:,\s*|\s+and\s+|\s+to\s+|-)` + number + `)*)\b`)
}

// reference is one match: the line of its first byte, its text with the
// line breaks read as spaces, and the file offsets of each of its numbers.
type reference struct {
	line    int
	text    string
	numbers [][2]int
}

// find returns the references in text. A comment's // lines, or in
// Markdown a paragraph's lines, are joined with a space, each joined byte
// keeping its offset in text.
func find(text string, markdown bool, pattern *regexp.Regexp) []reference {
	var refs []reference
	var joined []byte
	var offsets []int
	flush := func() {
		for _, m := range pattern.FindAllSubmatchIndex(joined, -1) {
			r := reference{line: 1 + strings.Count(text[:offsets[m[0]]], "\n"), text: strings.Join(strings.Fields(string(joined[m[0]:m[1]])), " ")}
			for _, n := range regexp.MustCompile(`\d+[a-z]?`).FindAllIndex(joined[m[2]:m[3]], -1) {
				r.numbers = append(r.numbers, [2]int{offsets[m[2]+n[0]], offsets[m[2]+n[1]-1] + 1})
			}
			refs = append(refs, r)
		}
		joined, offsets = joined[:0], offsets[:0]
	}
	at := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		start := at
		at += len(line)
		body := strings.TrimRight(line, "\r\n")
		var content string
		var from int
		if markdown {
			if strings.TrimSpace(body) == "" {
				flush()
				continue
			}
			content, from = body, start
		} else {
			i := strings.Index(body, "//")
			if i < 0 || strings.TrimSpace(body[:i]) != "" {
				flush()
				continue
			}
			content, from = body[i+2:], start+i+2
		}
		if len(joined) > 0 {
			joined = append(joined, ' ')
			offsets = append(offsets, from)
		}
		for k := 0; k < len(content); k++ {
			joined = append(joined, content[k])
			offsets = append(offsets, from+k)
		}
	}
	flush()
	return refs
}

// rewrite replaces each number of refs that moves names, all at once.
func rewrite(text string, refs []reference, moves map[string]string) (string, bool) {
	var b strings.Builder
	last, changed := 0, false
	for _, r := range refs {
		for _, n := range r.numbers {
			to, ok := moves[text[n[0]:n[1]]]
			if !ok {
				continue
			}
			b.WriteString(text[last:n[0]])
			b.WriteString(to)
			last, changed = n[1], true
		}
	}
	b.WriteString(text[last:])
	return b.String(), changed
}
