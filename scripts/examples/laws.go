package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A domain package states each contract it needs as an operation type with
// laws (AGENTS.md). The runner enforces it for a program of packages: an
// operation type declared outside cmd/ with no member law is a layout
// error. The -lawless flag reports the same check over every example and
// fails nothing.
//
// The check reads the source as text, so the runner keeps to the standard
// library: comments and string literals are blanked, then each top-level
// "type NAME {" or "type NAME[...] {" body is split into its members. A
// member "NAME(" or "NAME[...](" is an operation; "law NAME(" is a law.
// Each "[...]" list is read by bracket depth, so a nested constraint
// ("[T Codec[int]]") does not end it.
// Fields ("Name string", "F func(int) bool") are neither.

// typeName finds the start of a top-level type declaration. typeBody then
// reads its type parameter list by bracket depth, since a constraint can
// hold brackets and parentheses ("type Store[T Codec[int]] {").
var typeName = regexp.MustCompile(`(?m)^type[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)

// memberName finds the name at the start of a member; lawMember finds a law.
var (
	memberName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	lawMember  = regexp.MustCompile(`^law[ \t]+[A-Za-z_]`)
)

// isOperation reports whether a member is "NAME(" or "NAME[...](", the
// type parameter list read by depth.
func isOperation(member string) bool {
	loc := memberName.FindStringIndex(member)
	if loc == nil {
		return false
	}
	i := skipBrackets([]byte(member), loc[1])
	return i < len(member) && member[i] == '('
}

// skipBrackets skips blanks from text[i], then a balanced "[...]" list if
// one starts there (brackets and parentheses nest inside it), then blanks
// again, and returns the index after them. An unbalanced list returns
// len(text).
func skipBrackets(text []byte, i int) int {
	i = skipBlanks(text, i)
	if i >= len(text) || text[i] != '[' {
		return i
	}
	depth := 0
	for ; i < len(text); i++ {
		switch text[i] {
		case '[', '(':
			depth++
		case ']', ')':
			depth--
			if depth == 0 {
				return skipBlanks(text, i+1)
			}
		}
	}
	return len(text)
}

func skipBlanks(text []byte, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}

// typeBody returns the index of the '{' that opens the body of the type
// declaration whose name ends at text[end], or -1 when its body is not a
// brace block (a union "=", a machine, an alias).
func typeBody(text []byte, end int) int {
	i := skipBrackets(text, end)
	if i < len(text) && text[i] == '{' {
		return i
	}
	return -1
}

// lawlessType is an operation type with no member law.
type lawlessType struct {
	file string
	line int
	name string
}

func (l lawlessType) String() string {
	return fmt.Sprintf("%s:%d: operation type %s has no law; a domain package states its contract as an operation type with laws (AGENTS.md)", l.file, l.line, l.name)
}

// lawlessTypes returns the operation types of one source with no member
// law, in source order. file names the source in the result.
func lawlessTypes(file string, source []byte) []lawlessType {
	text := blankCommentsAndStrings(source)
	var found []lawlessType
	for _, m := range typeName.FindAllSubmatchIndex(text, -1) {
		open := typeBody(text, m[1])
		if open < 0 {
			continue
		}
		members := typeMembers(text, open)
		operations, laws := 0, 0
		for _, member := range members {
			switch {
			case lawMember.MatchString(member):
				laws++
			case isOperation(member):
				operations++
			}
		}
		if operations > 0 && laws == 0 {
			line := 1 + strings.Count(string(text[:m[0]]), "\n")
			found = append(found, lawlessType{file: file, line: line, name: string(text[m[2]:m[3]])})
		}
	}
	return found
}

// typeMembers splits the body that opens at text[open] ('{') into its
// members: the text between commas and line ends at the body's own depth,
// trimmed, empty ones left out.
func typeMembers(text []byte, open int) []string {
	var members []string
	depth := 0
	start := open + 1
	add := func(end int) {
		if member := strings.TrimSpace(string(text[start:end])); member != "" {
			members = append(members, member)
		}
	}
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
			if depth == 0 {
				add(i)
				return members
			}
		case ',', '\n':
			if depth == 1 {
				add(i)
				start = i + 1
			}
		}
	}
	add(len(text))
	return members
}

// blankCommentsAndStrings returns a copy of source with each line comment,
// block comment and string, rune or raw string literal replaced by spaces,
// line ends kept, so a brace or a word inside one is not read as code.
func blankCommentsAndStrings(source []byte) []byte {
	out := slices.Clone(source)
	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(source); i++ {
		switch {
		case source[i] == '/' && i+1 < len(source) && source[i+1] == '/':
			end := i
			for end < len(source) && source[end] != '\n' {
				end++
			}
			blank(i, end)
			i = end
		case source[i] == '/' && i+1 < len(source) && source[i+1] == '*':
			end := strings.Index(string(source[i+2:]), "*/")
			stop := len(source)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			blank(i, stop)
			i = stop - 1
		case source[i] == '"' || source[i] == '\'':
			quote := source[i]
			end := i + 1
			for end < len(source) && source[end] != quote && source[end] != '\n' {
				if source[end] == '\\' {
					end++
				}
				end++
			}
			blank(i, end+1)
			i = end
		case source[i] == '`':
			end := i + 1
			for end < len(source) && source[end] != '`' {
				end++
			}
			blank(i, end+1)
			i = end
		}
	}
	return out
}

// moduleLawProblems returns the lawless operation types of a program of
// packages: those declared in its sources outside cmd/, which hold the
// binary and choose providers.
func moduleLawProblems(p *program) []string {
	var problems []string
	commands := filepath.Join(p.dir, "cmd") + string(filepath.Separator)
	for _, source := range p.sources {
		if strings.HasPrefix(source, commands) {
			continue
		}
		content, err := os.ReadFile(source)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		rel, err := filepath.Rel(p.dir, source)
		if err != nil {
			rel = source
		}
		for _, l := range lawlessTypes(filepath.ToSlash(rel), content) {
			problems = append(problems, l.String())
		}
	}
	return problems
}

// reportLawless prints every lawless operation type of every .ergo file
// under examples/, programs or not, and returns 0: a report, not a check.
func reportLawless(root string, stdout io.Writer) int {
	examples := filepath.Join(root, "examples")
	var found []lawlessType
	err := filepath.WalkDir(examples, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".ergo") || !entry.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = append(found, lawlessTypes(filepath.ToSlash(rel), content)...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(stdout, "examples:", err)
		return 2
	}
	for _, l := range found {
		fmt.Fprintln(stdout, l)
	}
	fmt.Fprintf(stdout, "examples: %d operation types with no law (report only)\n", len(found))
	return 0
}
