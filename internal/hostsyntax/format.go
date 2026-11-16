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

// Source formats src with the formatter written in Ergo (compiler/format,
// linked as internal/linked/format): syntax.Parse parses it, the tree
// crosses as its encoding, and the Ergo formatter lays it out. Its
// signature and failures are format.SourceBounded's: a syntax failure is
// the parser's *syntax.Error, an output above a positive maxBytes is a
// *format.LimitError decided before any text is built, and a failure
// returns no text.
func Source(src string, maxBytes int) (string, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return "", err
	}
	return File(f, maxBytes)
}

// File is Source over a parsed file: format.FileBounded's signature and
// failures.
func File(f *syntax.File, maxBytes int) (string, error) {
	out, err := format.FormatTree(context.Background(), encode(f), maxBytes)
	if err != nil {
		return "", err
	}
	return outcome(out)
}

// outcome reads FormatTree's text: "ok\n" and the formatted text, or the
// failure. A refused limit is the Go formatter's *format.LimitError; an
// invalid tree or an encoded syntax failure of a parsed file is a defect
// of the bridge, which the Go formatter has no failure for.
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
	return "", errors.New("hostsyntax: the Ergo formatter refused the parsed file: " + head)
}
