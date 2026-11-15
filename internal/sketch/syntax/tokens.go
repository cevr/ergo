package syntax

import (
	"strconv"
	"strings"
)

// TokenText is src as its tokens: each token's kind and literal, one per
// line, with no comment, no space and no position. A semicolon is written
// once however many end a line, and none before a closing `)`, `]` or `}`,
// so a declaration reflowed over more or fewer lines keeps its text. Two
// sources with the same TokenText parse to the same tree. The law ledger's
// digest reads it (lower, LawDigest): an edit that changes no token
// leaves a declaration's digest as it was. A source with a lexical error
// ends its text at the error.
func TokenText(src string) string {
	var b strings.Builder
	semi := false
	for _, t := range scan(src).toks {
		switch t.tok {
		case EOF, ILLEGAL:
			return b.String()
		case SEMICOLON:
			semi = b.Len() > 0
			continue
		case RPAREN, RBRACK, RBRACE:
			semi = false
		}
		if semi {
			b.WriteString(";\n")
			semi = false
		}
		b.WriteString(t.tok.String())
		if t.lit != "" {
			b.WriteString(" " + strconv.Quote(t.lit))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
