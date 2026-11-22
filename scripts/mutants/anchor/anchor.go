// Package anchor locates a mutant row's edit in its file (MN0
// DECL-ANCHOR). A row names the text it replaces (from) and, optionally,
// the declaration that holds it (decl). With a declaration, from must occur
// exactly once inside that declaration's span, so an edit elsewhere in the
// file cannot break the row (anchor drift); without one, from must occur
// exactly once in the file. It is the one place an edit is located: the
// mutant runner, the gate's backtest and internal/residue's ledger check
// call Locate, and the runner's -anchor-decls migration calls Enclosing.
//
// A declaration is named within its file:
//   - Go: Name for a func, type, var or const (a group by its first name),
//     Recv.Name for a method (the receiver's type name, without a pointer or
//     type parameters). go/parser finds them.
//   - Ergo: Name for a func, type, let, context or extern, Name#law for a
//     law, Name#test for a test, and Recv.Name for a method. Formatted
//     Ergo source starts each top-level declaration at the start of a line
//     with its keyword, so the spans are found by lines (ergoDecls), which
//     keeps the Ergo parser out of the runner's and the gate's closures;
//     internal/residue's TestErgoDeclSpansMatchTheParser holds them to the
//     parser on every file a row anchors in.
//
// A declaration's span runs from its doc comment (the comment lines right
// above it) to its end.
package anchor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strings"
)

// Decl is one top-level declaration of a file: its name and the byte span
// [Start, End) of its doc comment and its text.
type Decl struct {
	Name       string
	Start, End int
}

// Decls answers the top-level declarations of a .go or .ergo file, in
// order; an error for any other file, or Go source that does not parse.
func Decls(file string, src []byte) ([]Decl, error) {
	switch path.Ext(file) {
	case ".go":
		return goDecls(file, src)
	case ".ergo":
		return ErgoDecls(src), nil
	}
	return nil, fmt.Errorf("%s: no declarations in a %s file", file, path.Ext(file))
}

// Locate answers the byte offset of the one occurrence of from in src: in
// the file when decl is "", else in the span of the file's one declaration
// named decl.
func Locate(file string, src []byte, decl, from string) (int, error) {
	if from == "" {
		return 0, fmt.Errorf("the row has no from")
	}
	if decl == "" {
		if n := strings.Count(string(src), from); n != 1 {
			return 0, fmt.Errorf("its from occurs %d times in %s", n, file)
		}
		return strings.Index(string(src), from), nil
	}
	decls, err := Decls(file, src)
	if err != nil {
		return 0, err
	}
	var found []Decl
	for _, d := range decls {
		if d.Name == decl {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return 0, fmt.Errorf("%s has %d declarations named %s", file, len(found), decl)
	}
	d := found[0]
	text := string(src[d.Start:d.End])
	if n := strings.Count(text, from); n != 1 {
		return 0, fmt.Errorf("its from occurs %d times in %s of %s", n, decl, file)
	}
	return d.Start + strings.Index(text, from), nil
}

// Apply answers src with the row's edit made at Locate's offset.
func Apply(file string, src []byte, decl, from, to string) ([]byte, error) {
	at, err := Locate(file, src, decl, from)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(src)-len(from)+len(to))
	out = append(out, src[:at]...)
	out = append(out, to...)
	return append(out, src[at+len(from):]...), nil
}

// Enclosing answers the name of the one declaration whose span holds
// [at, at+n), and whether there is one with a name unique in the file (the
// migration's test: a text outside every declaration, across two, or in a
// declaration whose name repeats keeps the file-wide rule).
func Enclosing(file string, src []byte, at, n int) (string, bool) {
	decls, err := Decls(file, src)
	if err != nil {
		return "", false
	}
	count := map[string]int{}
	for _, d := range decls {
		count[d.Name]++
	}
	for _, d := range decls {
		if d.Start <= at && at+n <= d.End {
			return d.Name, d.Name != "" && count[d.Name] == 1
		}
	}
	return "", false
}

func goDecls(file string, src []byte) ([]Decl, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var out []Decl
	for _, d := range f.Decls {
		var name string
		var doc *ast.CommentGroup
		switch d := d.(type) {
		case *ast.FuncDecl:
			name, doc = d.Name.Name, d.Doc
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = recvName(d.Recv.List[0].Type) + "." + name
			}
		case *ast.GenDecl:
			doc = d.Doc
			if len(d.Specs) > 0 {
				switch s := d.Specs[0].(type) {
				case *ast.TypeSpec:
					name = s.Name.Name
				case *ast.ValueSpec:
					name = s.Names[0].Name
				}
			}
		}
		start := off(d.Pos())
		if doc != nil {
			start = off(doc.Pos())
		}
		out = append(out, Decl{Name: name, Start: start, End: off(d.End())})
	}
	return out, nil
}

// recvName is a Go receiver's type name: T of T, *T, T[A] and *T[A, B].
func recvName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// ErgoDecls answers the top-level declarations of formatted Ergo source by
// lines: a declaration starts at a line that begins with a keyword (its
// doc comment, the comment lines right above it, starts its span) and ends
// at the end of its last non-blank line before the next declaration's span.
// An import is a span with no name: a text in it is outside every named
// declaration.
func ErgoDecls(src []byte) []Decl {
	text := string(src)
	var lines []int // the start offset of each line
	for i := 0; i < len(text); {
		lines = append(lines, i)
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
	}
	line := func(k int) string {
		end := len(text)
		if k+1 < len(lines) {
			end = lines[k+1] - 1
		}
		return text[lines[k]:end]
	}
	var out []Decl
	for k := range lines {
		name, ok := ergoDeclName(line(k))
		if !ok {
			continue
		}
		start := k
		for start > 0 && strings.HasPrefix(line(start-1), "//") {
			start--
		}
		if n := len(out); n > 0 {
			out[n-1].End = trimEnd(text, out[n-1].Start, lines[start])
		}
		out = append(out, Decl{Name: name, Start: lines[start], End: len(text)})
	}
	if n := len(out); n > 0 {
		out[n-1].End = trimEnd(text, out[n-1].Start, len(text))
	}
	return out
}

// trimEnd is end moved back past trailing blank lines, to the end of the
// last line of text at or after start.
func trimEnd(text string, start, end int) int {
	for end > start && (text[end-1] == '\n' || text[end-1] == ' ' || text[end-1] == '\t' || text[end-1] == '\r') {
		end--
	}
	return end
}

// ergoDeclName is the name of the declaration a line starts, and whether it
// starts one.
func ergoDeclName(l string) (string, bool) {
	kw, rest, ok := strings.Cut(l, " ")
	if !ok {
		return "", false
	}
	switch kw {
	case "import":
		return "", true
	case "extern":
		// extern go PATH func ... | extern go PATH type ...: the
		// declaration after the Go path.
		for _, k := range []string{" func ", " type ", " let "} {
			if i := strings.Index(rest, k); i >= 0 {
				return ergoDeclName(rest[i+1:])
			}
		}
		return "", true
	case "func":
		recv := ""
		if strings.HasPrefix(rest, "(") {
			i := strings.IndexByte(rest, ')')
			if i < 0 {
				return "", true
			}
			fields := strings.Fields(strings.Trim(rest[1:i], " "))
			if len(fields) > 0 {
				recv = strings.TrimLeft(fields[len(fields)-1], "*")
				if j := strings.IndexByte(recv, '['); j >= 0 {
					recv = recv[:j]
				}
				recv += "."
			}
			rest = strings.TrimLeft(rest[i+1:], " ")
		}
		return recv + ident(rest), true
	case "type", "let", "context":
		if kw == "let" {
			rest = strings.TrimPrefix(rest, "mut ")
		}
		return ident(rest), true
	case "law":
		return ident(rest) + "#law", true
	case "test":
		return ident(rest) + "#test", true
	}
	return "", false
}

// ident is the identifier s starts with.
func ident(s string) string {
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return s[:i]
		}
	}
	return s
}
