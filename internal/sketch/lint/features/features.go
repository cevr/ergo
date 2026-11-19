// Package features fills TN1's feature record (input version 2; the
// record and its text form are specified in modules/tn1/record.ergo) from
// an Ergo file's syntax tree. The record is host-neutral: a TypeScript
// host fills the same fields from its own tree, and one engine reads
// both. Every field comes from the parser, never from the layout of the
// text, so a reformat that keeps the lines keeps the record
// (TestRecordSurvivesWhitespace).
package features

import (
	"fmt"
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Version is the input version the record is.
const Version = 2

// Line kinds, in the order the engine's planes read them.
const (
	Blank = iota
	Code
	CommentOnly
	DocComment
	Directive
)

var lineKindNames = [...]string{"blank", "code", "comment", "doc", "directive"}

// Start bits: the node kinds a line can start.
const (
	StartFunction = 1 << iota
	StartType
	StartBranch
	StartLoop
	StartHandler
	StartReturn
	StartCall
	StartAssignment
	StartTest
)

// Node kinds.
const (
	Function = iota
	Declared
	Handler
	Test
	Module
)

var nodeKindNames = [...]string{"function", "type", "handler", "test", "module"}

// Handler shapes: what one arm of a catch does with the failure.
const (
	Empty         = iota // no statement
	YieldsLiteral        // its value is a literal
	Rethrows             // its value is the failure it caught
	Records              // it calls something and then yields
	Translates           // anything else: another value or failure
)

var shapeNames = [...]string{"empty", "literal", "rethrows", "records", "translates"}

// Line is one source line of the record.
type Line struct {
	Kind         int
	Depth        int
	Starts       int
	CommentWords uint64
	CodeWords    uint64
	CommentText  string
}

// Node is one declaration-sized node of the record.
type Node struct {
	Kind       int
	Offset     int
	Length     int
	Line       int
	Column     int
	NameWords  uint64
	Params     int
	Statements int
	MaxDepth   int
	Handlers   []int
	Shape      uint64
}

// Features is the record of one file.
type Features struct {
	Language string
	Lines    []Line
	Nodes    []Node
}

// Encode is the record's text form (modules/tn1/record.ergo).
func Encode(f Features) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tn1-record %d %s\n", Version, f.Language)
	for _, l := range f.Lines {
		fmt.Fprintf(&b, "L %s %d %x %016x %016x %s\n", lineKindNames[l.Kind], l.Depth, l.Starts, l.CommentWords, l.CodeWords, l.CommentText)
	}
	for _, n := range f.Nodes {
		shapes := "-"
		if len(n.Handlers) > 0 {
			names := make([]string, len(n.Handlers))
			for i, h := range n.Handlers {
				names[i] = shapeNames[h]
			}
			shapes = strings.Join(names, ",")
		}
		fmt.Fprintf(&b, "N %s %d %d %d %d %016x %d %d %d %s %016x\n", nodeKindNames[n.Kind], n.Offset, n.Length, n.Line, n.Column, n.NameWords, n.Params, n.Statements, n.MaxDepth, shapes, n.Shape)
	}
	return b.String()
}

// Words appends the words of s by the record's word rule: runs of ASCII
// letters, split where a lower-case letter meets an upper-case one,
// lowercased, three letters or more.
func Words(dst []string, s string) []string {
	start := -1
	flush := func(end int) {
		if start >= 0 && end-start >= 3 {
			dst = append(dst, strings.ToLower(s[start:end]))
		}
		start = -1
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter {
			flush(i)
			continue
		}
		if start >= 0 && c <= 'Z' && s[i-1] >= 'a' {
			flush(i)
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(s))
	return dst
}

// WordBit is a word's bit in a sketch: FNV-1a, 32 bits, modulo 64.
func WordBit(w string) uint64 {
	h := uint32(2166136261)
	for i := 0; i < len(w); i++ {
		h ^= uint32(w[i])
		h *= 16777619
	}
	return 1 << (h % 64)
}

// Sketch is the set of the words' bits.
func Sketch(words []string) uint64 {
	var s uint64
	for _, w := range words {
		s |= WordBit(w)
	}
	return s
}

// startWords are the words a keyword adds to its line's code words: the
// neutral name of the node kind it starts, so `func` and TypeScript's
// `function` read alike. A start with no keyword (a call, an assignment)
// adds no word.
var startWords = []struct {
	bit  int
	word string
}{
	{StartFunction, "function"}, {StartType, "type"}, {StartBranch, "branch"}, {StartLoop, "loop"},
	{StartHandler, "handler"}, {StartReturn, "return"}, {StartTest, "test"},
}

// FromErgo is the record of a parsed Ergo file. Its code words are the
// words of the code bytes outside comments and literals (identifiers and
// keywords as written), and each start's neutral word (function, type,
// branch, loop, handler, return, test), so Ergo's func and TypeScript's
// function share a word. Ergo keeps the names of imported packages; the
// TypeScript host drops imported namespaces (Effect, Layer, Schema),
// which would fill the sketch on most lines.
func FromErgo(file *syntax.File) Features {
	src := file.Src
	type span struct{ from, to int }
	var lineStarts []int
	for at := 0; at < len(src); {
		lineStarts = append(lineStarts, at)
		end := strings.IndexByte(src[at:], '\n')
		if end < 0 {
			break
		}
		at += end + 1
	}
	lineOf := func(pos int) int { return file.Line(syntax.Pos(pos)) - 1 }
	lines := make([]Line, len(lineStarts))
	lineEnd := func(i int) int {
		if i+1 < len(lineStarts) {
			return lineStarts[i+1] - 1
		}
		return len(src)
	}

	// Comment bytes, which comments are doc comments and which directives.
	inComment := make([]bool, len(src))
	doc := map[*syntax.Comment]bool{}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if g := docOf(n); g != nil {
			for _, c := range g.List {
				doc[c] = true
			}
		}
		return true
	})
	hasDoc := make([]bool, len(lines))
	hasDirective := make([]bool, len(lines))
	for _, g := range file.Comments {
		for _, c := range g.List {
			for p := int(c.Slash); p < int(c.EndPos) && p < len(src); p++ {
				inComment[p] = true
			}
			first, last := lineOf(int(c.Slash)), lineOf(int(c.EndPos)-1)
			for i := first; i <= last; i++ {
				if doc[c] {
					hasDoc[i] = true
				}
				if directive(c.Text) {
					hasDirective[i] = true
				}
			}
		}
	}

	// Blocks, for depth.
	var blocks []span
	syntax.Inspect(file, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CatchArm, *syntax.ContextBody:
			blocks = append(blocks, span{int(n.Pos()), int(n.End())})
		case *syntax.InterfaceType, *syntax.StructType, *syntax.MachineType, *syntax.CompositeLit:
			blocks = append(blocks, span{int(n.Pos()), int(n.End())})
		}
		return true
	})

	// Starts, and the literals whose bytes are no code words.
	inLiteral := make([]bool, len(src))
	mark := func(pos syntax.Pos, bit int) {
		if pos.IsValid() && int(pos) < len(src) {
			lines[lineOf(int(pos))].Starts |= bit
		}
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if derived(n) {
			return false
		}
		switch n := n.(type) {
		case *syntax.BasicLit:
			for p := int(n.Pos()); p < int(n.End()) && p < len(src); p++ {
				inLiteral[p] = true
			}
		case *syntax.FuncDecl:
			mark(n.Func, StartFunction)
		case *syntax.FuncLit:
			mark(n.Pos(), StartFunction)
		case *syntax.TypeDecl:
			mark(n.Pos(), StartType)
		case *syntax.IfStmt:
			mark(n.If, StartBranch)
		case *syntax.SwitchStmt:
			mark(n.Switch, StartBranch)
		case *syntax.TypeSwitchStmt:
			mark(n.Switch, StartBranch)
		case *syntax.ForStmt, *syntax.RangeStmt:
			mark(n.Pos(), StartLoop)
		case *syntax.CatchExpr:
			mark(n.Catch, StartHandler)
		case *syntax.ReturnStmt:
			mark(n.Pos(), StartReturn)
		case *syntax.CallExpr:
			mark(n.Pos(), StartCall)
		case *syntax.AssignStmt:
			mark(n.Pos(), StartAssignment)
		case *syntax.VarDecl:
			mark(n.Pos(), StartAssignment)
		case *syntax.TestDecl:
			mark(n.Test, StartTest)
		case *syntax.LawDecl:
			mark(n.Law, StartTest)
		}
		return true
	})

	for i := range lines {
		l := &lines[i]
		from, to := lineStarts[i], lineEnd(i)
		var text, codeText []byte
		first := -1
		code := false
		for p := from; p < to; p++ {
			c := src[p]
			switch {
			case inComment[p]:
				if c != '\r' {
					text = append(text, c)
				}
				codeText = append(codeText, ' ')
			case inLiteral[p]:
				codeText = append(codeText, ' ')
			default:
				codeText = append(codeText, c)
			}
			if !inComment[p] && c != ' ' && c != '\t' && c != '\r' {
				code = true
			}
			if first < 0 && c != ' ' && c != '\t' && c != '\r' {
				first = p
			}
		}
		l.CommentText = strings.Trim(string(text), " \t")
		l.CommentWords = Sketch(Words(nil, l.CommentText))
		switch {
		case code:
			l.Kind = Code
		case len(text) == 0:
			l.Kind = Blank
		case hasDoc[i]:
			l.Kind = DocComment
		case hasDirective[i]:
			l.Kind = Directive
		default:
			l.Kind = CommentOnly
		}
		if first >= 0 {
			for _, b := range blocks {
				if b.from < first && first < b.to {
					l.Depth++
				}
			}
		}
		words := Words(nil, string(codeText))
		for _, s := range startWords {
			if l.Starts&s.bit != 0 {
				words = append(words, s.word)
			}
		}
		l.CodeWords = Sketch(words)
	}

	return Features{Language: "ergo", Lines: lines, Nodes: nodesOf(file)}
}

// directive reports a tool's directive comment: // ergo-lint-…, or a Go
// style //name: with no space.
func directive(text string) bool {
	body, ok := strings.CutPrefix(text, "//")
	if !ok {
		return false
	}
	if strings.HasPrefix(strings.TrimLeft(body, " "), "ergo-lint-") {
		return true
	}
	name, _, found := strings.Cut(body, ":")
	if !found || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// docOf is a node's doc comment group: its Doc field, or nil.
func docOf(n syntax.Node) *syntax.CommentGroup {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := v.Elem().FieldByName("Doc")
	if !f.IsValid() {
		return nil
	}
	g, _ := f.Interface().(*syntax.CommentGroup)
	return g
}

// derived reports a node the parser derived from a refined type
// (refine.go): it has no source text of its own.
func derived(n syntax.Node) bool {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return false
	}
	f := v.Elem().FieldByName("Derived")
	return f.IsValid() && f.Kind() == reflect.Pointer && !f.IsNil()
}
