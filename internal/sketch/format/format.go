// Package format is Ergo's formatter (`ergo fmt`): one canonical
// layout with no options, printed from the internal/sketch/syntax AST. It is
// apart from the parser so no checker or lowering code depends on layout
// (docs/specs/formatting.md).
//
// The layout is gofmt's for every construct Go has: tabs indent, spaces
// align (struct fields, trailing comments, the keys of multi-line composite
// literals, context and machine entries, var groups), lines are never wrapped,
// each gap between tokens keeps "line break or not" from the source, runs of
// blank lines collapse to one, and an explicit `;` becomes a line break.
// Tokens are otherwise kept: no import sorting, no parenthesis removal, no
// rewriting. An own-line comment takes the indentation of the block or
// clause body that contains it, never its source column (a deviation from
// gofmt); one between a clause's last statement and the next case belongs to
// the next clause when a blank line separates it from the statement above.
//
// Prior art:
//   - gofmt: one canonical layout and no options; go/parser keeps comments as
//     CommentGroups, go/printer interleaves them by position and aligns with
//     text/tabwriter; it does not wrap lines.
//   - Gleam (gleam format): no configuration; the lexer keeps comments apart
//     and the printer re-inserts them by position into a Wadler-style
//     document with a fixed line width.
//   - ReScript: the printer attaches comments to AST nodes (leading,
//     trailing, inside) in a table before printing, then prints a
//     Wadler-style document with a fixed width.
//   - Elixir (mix format): parses with comments kept beside the AST and
//     reprints with Inspect.Algebra; line_length is configurable (default
//     98); --check-formatted for CI.
//   - MoonBit (moon fmt): one built-in style with no layout options and a
//     --check mode for CI.
//   - Ergo takes gofmt's model (no width, author line breaks kept), as the
//     legacy formatter does, and places comments by structure as Gleam,
//     ReScript and Elixir do. It aligns with its own column pass instead of
//     text/tabwriter, because a 2026 string literal may hold any byte,
//     including tabwriter's escape byte.
package format

import "ergo.local/prototype/internal/sketch/syntax"

// Identity names this formatter on every surface (MCP formatterVersion,
// the per-file formatterVersion of `ergo fmt --json`).
const Identity = "ergo-2026/formatter-2"

// Source formats a 2026 source file. A syntax error is returned as the
// parser's *syntax.Error, and no text.
func Source(src string) (string, error) { return SourceBounded(src, 0) }

// SourceBounded formats like Source with the output bounded to maxBytes
// when it is positive: a larger output is a *LimitError, decided before
// the text is built.
func SourceBounded(src string, maxBytes int) (string, error) {
	file, err := syntax.Parse(src)
	if err != nil {
		return "", err
	}
	return FileBounded(file, maxBytes)
}

// File prints a parsed file in the canonical layout.
func File(f *syntax.File) string {
	out, _ := FileBounded(f, 0)
	return out
}

// FileBounded prints a parsed file with the output bounded as in
// SourceBounded.
func FileBounded(f *syntax.File, maxBytes int) (string, error) {
	p := newPrinter(f)
	p.printFile(f)
	return p.render(maxBytes)
}
