package syntax

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// CodeSyntax is the one registered syntax code (docs/spec/diagnostics.md,
// S001). Later syntax codes are S0xx and come from the #79 registry.
const CodeSyntax = "S001"

// Error is a located syntax error. Offset and Length are a byte span into
// the source; Line and Column are 1-based, and Column counts bytes, so a
// caller maps the span to UTF-16 or any other unit from the source itself.
type Error struct {
	Code    string
	Message string
	Offset  int
	Length  int
	Line    int
	Column  int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s %s", e.Line, e.Column, e.Code, e.Message)
}

func newError(src string, lines []int, pos Pos, length int, message string) *Error {
	offset := int(pos)
	if offset > len(src) {
		offset = len(src)
	}
	if offset+length > len(src) {
		length = len(src) - offset
	}
	if length < 0 {
		length = 0
	}
	// A span never ends inside a code point.
	for length > 0 && offset+length < len(src) && !utf8.RuneStart(src[offset+length]) {
		length++
	}
	line, column := lineColumn(lines, offset)
	return &Error{Code: CodeSyntax, Message: message, Offset: offset, Length: length, Line: line, Column: column}
}

// lineColumn returns the 1-based line and byte column of offset.
func lineColumn(lines []int, offset int) (int, int) {
	i := sort.Search(len(lines), func(i int) bool { return lines[i] > offset }) - 1
	if i < 0 {
		i = 0
	}
	return i + 1, offset - lines[i] + 1
}
