// Package hostsyntax is the Go facade of the formatter written in Ergo
// (compiler/format, linked as internal/linked/format). Only the source and
// the text cross: compiler/syntax parses in Ergo and the formatter reads its
// rows. The facade makes no layout decision.
package hostsyntax

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/linked/format"
	goformat "ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Source formats src with the formatter written in Ergo. Its signature
// and failures are format.SourceBounded's: a syntax failure is a located
// *syntax.Error, an output above a positive maxBytes is a
// *format.LimitError decided before any text is built, and a failure
// returns no text.
func Source(src string, maxBytes int) (string, error) {
	out, err := format.FormatSource(context.Background(), src, maxBytes)
	if err != nil {
		return "", err
	}
	return outcome(out)
}

// File is Source over a parsed file, format.FileBounded's signature and
// failures: it formats the file's source.
func File(f *syntax.File, maxBytes int) (string, error) { return Source(f.Src, maxBytes) }

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
		return "", &goformat.LimitError{Limit: limit}
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
