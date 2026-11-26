// Package lawledger reads the runner file of the std laws and runs the
// laws of one Go test against it (docs/spec/stdlib.md, Std laws 3 and 4).
//
// Each line of the runner file names a std law by its key and the Go test
// that runs it, or NoRunner. The law's statement is its source text in
// internal/sketch/std (Statement). A runner calls Run with the laws it
// restates, each with the statement it restates and the check that runs
// it. Run holds that the runner restates exactly the laws whose line names
// it, each with the source's statement, so a runner named for a law it
// does not run, or a law changed in source but not in its runner, fails
// the runner. It then runs each law as a subtest named after the law.
// TestStdLawLedger (internal/sketch/stdlib) holds that every std law has
// one line and that every runner a line names calls Run.
package lawledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/stdlib"
)

// File is the runner file, from the module root: one line per std law,
// `pkg Law<TAB>Runner`, "pkg Type.Law" for a member law; a line that
// starts with "# " is a comment.
const File = "internal/lawledger/testdata/runners.txt"

// NoRunner is the runner of a law that no Go test runs.
const NoRunner = "none"

// Row is one line of the runner file.
type Row struct {
	Pkg, Law, Runner string
	Line             int // 1-based line in File
}

// Key is the row's ledger key: "pkg Law", or "pkg Type.Law" for a member.
func (r Row) Key() string { return r.Pkg + " " + r.Law }

// Parse parses the text of the runner file. A malformed line is an error
// that names its line.
func Parse(text string) ([]Row, error) {
	var rows []Row
	var problems []string
	for i, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		cells := strings.Split(line, "\t")
		if len(cells) != 2 {
			problems = append(problems, fmt.Sprintf("runners.txt:%d: a line is `pkg Law<TAB>Runner`, not %q", i+1, line))
			continue
		}
		pkg, law, ok := strings.Cut(cells[0], " ")
		if !ok || pkg == "" || law == "" || strings.Contains(law, " ") || cells[1] == "" || strings.Contains(cells[1], " ") {
			problems = append(problems, fmt.Sprintf("runners.txt:%d: a line is `pkg Law<TAB>Runner`, not %q", i+1, line))
			continue
		}
		rows = append(rows, Row{Pkg: pkg, Law: law, Runner: cells[1], Line: i + 1})
	}
	if len(problems) > 0 {
		return rows, errors.New(strings.Join(problems, "\n"))
	}
	return rows, nil
}

// Statement is the source text of the law named law in the Ergo source
// src, "Law" for a top-level law and "Type.Law" for a member law of the
// type Type: from the law's name to the brace that closes its body, or its
// second body for a rewrite law (`{ lhs } == { rhs }`), each run of white
// space made one space. It is false when src declares no such law; a law
// the parser derives (a refined type's round trip, a by-name pair's) has
// no source text, and its runner states it through parsed.Run. Brackets
// are counted outside comments and literals, and a string's interpolation
// is read as code.
func Statement(src, law string) (string, bool) {
	owner, name, member := strings.Cut(law, ".")
	if !member {
		owner, name = "", owner
	}
	var stack []string // each open bracket: the type's name for a type's body, else ""
	pending := ""      // the name after a top-level `type`, until its line ends or its body opens
	prev := ""
	start, depth := -1, 0
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			if len(stack) == 0 {
				pending = ""
			}
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == ';':
			i++
			continue
		case c == '/' && strings.HasPrefix(src[i:], "//"):
			if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(src)
			}
			continue
		case c == '/' && strings.HasPrefix(src[i:], "/*"):
			if j := strings.Index(src[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(src)
			}
			continue
		case strings.IndexByte("\"'`", c) >= 0:
			_, i = literal(src, i)
			prev = "literal"
			continue
		case isWord(c):
			n := 1
			for n < len(src)-i && isWord(src[i+n]) {
				n++
			}
			word := src[i : i+n]
			switch {
			case start >= 0:
			case prev == "law" && word == name && (owner == "" && len(stack) == 0 || owner != "" && len(stack) == 1 && stack[0] == owner):
				start, depth = i, len(stack)
			case prev == "type" && len(stack) == 0:
				pending = word
			}
			prev = word
			i += n
			continue
		}
		prev = src[i : i+1]
		i++
		switch c {
		case '(', '[', '{':
			label := ""
			if c == '{' && len(stack) == 0 {
				label, pending = pending, ""
			}
			stack = append(stack, label)
		case ')', ']', '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if start < 0 || c != '}' || len(stack) != depth {
				continue
			}
			// A rewrite law's first body is followed by == and its second.
			rest := strings.TrimLeft(src[i:], " \t\r\n")
			if after, ok := strings.CutPrefix(rest, "=="); ok && strings.HasPrefix(strings.TrimLeft(after, " \t\r\n"), "{") {
				continue
			}
			return Normalize(src[start:i]), true
		}
	}
	return "", false
}

// StdStatement is the statement of the std law key as its source in
// internal/sketch/std states it, and whether the source has its text.
func StdStatement(key string) (string, bool) {
	pkg, law, _ := strings.Cut(key, " ")
	var files map[string]string
	if pkg == stdlib.PreludePath {
		files = stdlib.Prelude()
	} else if files, _ = stdlib.Package(pkg); files == nil {
		return "", false
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if s, ok := Statement(files[name], law); ok {
			return s, true
		}
	}
	return "", false
}

var spaces = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s+`) })

// Normalize makes each run of white space in a statement one space, as the
// ledger writes a law's source text.
func Normalize(statement string) string {
	return strings.TrimSpace(spaces().ReplaceAllString(statement, " "))
}

// Same reports whether a and b state the same law: they are the same
// tokens, so they differ at most in the white space the formatter adds or
// drops, and a reformatted law is not a changed one. A token is a word
// (letters, digits and _), a string, rune or raw literal as written, an
// operator of Ergo's scanner (the longest of operators, below, that
// starts there) or any other one character. White space only separates
// tokens: a - -b is not a --b, but a!=-b is a != -b.
func Same(a, b string) bool { return slices.Equal(tokens(a), tokens(b)) }

// operators are the scanner's operators of more than one character
// (internal/sketch/syntax/token.go), the longest first.
var operators = []string{
	"&^=", "<<=", ">>=", "...",
	"!=", "%=", "&&", "&=", "&^", "*=", "++", "+=", "--", "-=", "..", "/=", ":=",
	"<-", "<<", "<=", "==", ">=", ">>", "^=", "|=", "||",
}

// tokens is s as Same reads it.
func tokens(s string) []string {
	out, _ := scan(s, 0, false)
	return out
}

// scan is the tokens of s from i. A semicolon is a line break, so white
// space. In an interpolation (inner), it ends after the ) that closes it,
// and gives the index after that.
func scan(s string, i int, inner bool) ([]string, int) {
	var out []string
	depth := 0
	for i < len(s) {
		c := s[i]
		n := 1
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';':
			i++
			continue
		case c == '"' || c == '\'' || c == '`':
			var lit []string
			lit, i = literal(s, i)
			out = append(out, lit...)
			continue
		case isWord(c):
			for n < len(s)-i && isWord(s[i+n]) {
				n++
			}
		default:
			if inner && c == ')' && depth == 0 {
				return out, i + 1
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
			}
			for _, op := range operators {
				if strings.HasPrefix(s[i:], op) {
					n = len(op)
					break
				}
			}
		}
		out = append(out, s[i:i+n])
		i += n
	}
	return out, i
}

// literal is the tokens of the literal that opens at s[i], and the index
// after it: its text with each run of white space made one space, as the
// ledger writes a statement, split at each interpolation \( … ) of a
// string, whose code is tokens (the formatter formats it).
func literal(s string, i int) ([]string, int) {
	q := s[i]
	var out []string
	var b strings.Builder
	b.WriteByte(q)
	i++
	space := false
	for i < len(s) {
		c := s[i]
		switch {
		case q == '"' && c == '\\' && i+1 < len(s) && s[i+1] == '(':
			code, next := scan(s, i+2, true)
			out = append(append(append(out, b.String(), "\\("), code...), ")")
			b.Reset()
			i, space = next, false
			continue
		case c == '\\' && q != '`' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i, space = i+2, false
			continue
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if !space {
				b.WriteByte(' ')
			}
			i, space = i+1, true
			continue
		}
		b.WriteByte(c)
		i, space = i+1, false
		if c == q {
			break
		}
	}
	return append(out, b.String()), i
}

// isWord reports a byte of a word: a letter, a digit, _ or a byte of a
// multi-byte rune.
func isWord(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// Read reads the runner file of the module that encloses the working
// directory.
func Read() ([]Row, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(File)))
	if err != nil {
		return nil, err
	}
	return Parse(string(data))
}

// moduleRoot is the nearest directory at or above the working directory
// with a go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}

// Law is one law as a runner restates it: the statement it restates (the
// law's source text; white space is free) and the check that runs it.
type Law struct {
	Statement string
	Check     func(t *testing.T)
}

// Run holds that the runner t (a top-level test, by its name) restates
// exactly the laws whose line in the runner file names it, each with the
// statement of its std source (Statement), and runs each law's check as a
// subtest named after its key with the space made a dot
// ("queue.CloseKeepsValues"), in key order. laws is keyed by ledger key. A
// law with no source text, one the parser derives, fails the runner: its
// runner calls parsed.Run.
func Run(t *testing.T, laws map[string]Law) {
	t.Helper()
	rows, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	run(t, rows, StdStatement, laws)
}

// RunWith is Run with the statements of the laws given by key, as the
// parser states them (parsed.Run).
func RunWith(t *testing.T, statements map[string]string, laws map[string]Law) {
	t.Helper()
	rows, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	run(t, rows, func(key string) (string, bool) {
		s, ok := statements[key]
		return s, ok
	}, laws)
}

// runner is the part of *testing.T that run uses.
type runner interface {
	Helper()
	Name() string
	Error(args ...any)
	Run(name string, f func(t *testing.T)) bool
}

// run is Run over rows, with each law's statement from statement.
func run(t runner, rows []Row, statement func(key string) (string, bool), laws map[string]Law) {
	t.Helper()
	statements := map[string]string{}
	for _, r := range rows {
		if r.Runner != t.Name() {
			continue
		}
		if s, ok := statement(r.Key()); ok {
			statements[r.Key()] = s
		}
	}
	for _, problem := range Compare(t.Name(), rows, statements, laws) {
		t.Error(problem)
	}
	keys := make([]string, 0, len(laws))
	for key := range laws {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		law := laws[key]
		t.Run(strings.Replace(key, " ", ".", 1), func(t *testing.T) {
			t.Logf("law %s: %s", key, Normalize(law.Statement))
			if law.Check != nil {
				law.Check(t)
			}
		})
	}
}

// Compare is the agreement of a runner's laws with the lines of the runner
// file that name the runner, given the source's statement of each by key:
// each problem, in key order; none when they agree. A runner no line names
// is a problem too.
func Compare(runner string, rows []Row, statements map[string]string, laws map[string]Law) []string {
	var problems []string
	mine := map[string]Row{}
	for _, r := range rows {
		if r.Runner == runner {
			mine[r.Key()] = r
		}
	}
	if len(mine) == 0 {
		problems = append(problems, fmt.Sprintf("no line of %s names the runner %s", File, runner))
	}
	keys := make([]string, 0, len(mine)+len(laws))
	for key := range mine {
		keys = append(keys, key)
	}
	for key := range laws {
		if _, ok := mine[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		row, listed := mine[key]
		law, restated := laws[key]
		statement, stated := statements[key]
		switch {
		case !restated:
			problems = append(problems, fmt.Sprintf("runners.txt:%d: %s is listed to run in %s, which does not restate it", row.Line, key, runner))
		case !listed:
			problems = append(problems, fmt.Sprintf("%s restates %s, which no line of %s lists for it", runner, key, File))
		case law.Check == nil:
			problems = append(problems, fmt.Sprintf("%s restates %s with no check", runner, key))
		case !stated:
			problems = append(problems, fmt.Sprintf("runners.txt:%d: std source has no text for %s, a law the parser derives: %s runs it through parsed.Run", row.Line, key, runner))
		case !Same(law.Statement, statement):
			problems = append(problems, fmt.Sprintf("runners.txt:%d: %s restates %s as\n\t%s\nthe source says\n\t%s", row.Line, runner, key, Normalize(law.Statement), statement))
		}
	}
	return problems
}
