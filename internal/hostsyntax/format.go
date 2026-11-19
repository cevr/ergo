// Package hostsyntax is Ergo's formatter (`ergo fmt`) for Go callers: one
// canonical layout with no options (docs/specs/formatting.md). The
// formatter is written in Ergo (compiler/format, linked as
// internal/linked/compiler); this package is its facade. Only the source and
// the text cross: compiler/syntax parses in Ergo and the formatter reads its
// rows. The facade makes no layout decision.
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
//   - Ergo takes gofmt's model (no width, author line breaks kept), and
//     places comments by structure as Gleam, ReScript and Elixir do. It
//     aligns with its own column pass instead of text/tabwriter, because a
//     2026 string literal may hold any byte, including tabwriter's escape
//     byte.
package hostsyntax

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	format "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Identity names this formatter on every surface (MCP formatterVersion,
// the per-file formatterVersion of `ergo fmt --json`). The layout is the
// Go formatter's it replaced (capstone S1c), so the name is kept.
const Identity = "ergo-2026/formatter-2"

// LimitError is the refusal of an output larger than the caller's bound.
type LimitError struct{ Limit int }

func (e *LimitError) Error() string {
	return fmt.Sprintf("formatted source exceeds the %d-byte output limit", e.Limit)
}

// Source formats a 2026 source file. A syntax error is returned as the
// parser's *syntax.Error, and no text.
func Source(src string) (string, error) { return SourceBounded(src, 0) }

// SourceBounded formats like Source with the output bounded to maxBytes
// when it is positive: a larger output is a *LimitError, decided before
// any text is built. A failure returns no text.
func SourceBounded(src string, maxBytes int) (string, error) {
	out, err := format.FormatSource(context.Background(), src, maxBytes)
	if err != nil {
		return "", err
	}
	return outcome(out)
}

// Work is the formatter's work counter on src: the walks of binary
// expressions it computed (capstone S1b's growth guard), or -1 for a
// source that does not parse.
func Work(src string) (int, error) {
	return format.FormatWork(context.Background(), src)
}

// File prints a parsed file in the canonical layout: it formats the file's
// source.
func File(f *syntax.File) string {
	out, _ := FileBounded(f, 0)
	return out
}

// FileBounded prints a parsed file with the output bounded as in
// SourceBounded.
func FileBounded(f *syntax.File, maxBytes int) (string, error) {
	return SourceBounded(f.Src, maxBytes)
}

// outcome reads FormatSource's text: "ok\n" and the formatted text, the
// located syntax failure, or the refused limit.
func outcome(out string) (string, error) {
	head, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return "", fmt.Errorf("hostsyntax: the Ergo formatter answered %q", out)
	}
	if head == "ok" {
		return rest, nil
	}
	if n, found := strings.CutPrefix(head, "limit "); found {
		limit, err := strconv.Atoi(n)
		if err != nil {
			return "", fmt.Errorf("hostsyntax: the Ergo formatter answered %q", head)
		}
		return "", &LimitError{Limit: limit}
	}
	if s, found := strings.CutPrefix(out, "syntax "); found {
		if e := syntaxError(strings.TrimSuffix(s, "\n")); e != nil {
			return "", e
		}
	}
	return "", errors.New("hostsyntax: the Ergo formatter answered " + strconv.Quote(out))
}

// syntaxError reads "<code> <len>:<message> <offset> <length> <line>
// <column>", or returns nil.
func syntaxError(s string) *syntax.Error {
	code, rest, ok := strings.Cut(s, " ")
	if !ok {
		return nil
	}
	n, rest, ok := strings.Cut(rest, ":")
	size, err := strconv.Atoi(n)
	if !ok || err != nil || size < 0 || size > len(rest) {
		return nil
	}
	message, rest := rest[:size], rest[size:]
	fields := strings.Fields(rest)
	if len(fields) != 4 {
		return nil
	}
	var v [4]int
	for i, f := range fields {
		if v[i], err = strconv.Atoi(f); err != nil {
			return nil
		}
	}
	return &syntax.Error{Code: code, Message: message, Offset: v[0], Length: v[1], Line: v[2], Column: v[3]}
}
