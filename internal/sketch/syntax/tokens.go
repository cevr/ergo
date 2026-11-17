package syntax

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	linked "ergo.local/prototype/internal/linked/compiler"
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
	for _, t := range scanTokens(src) {
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

// token is one lexical token. lit is the source text of identifiers,
// literals, paths and string segments (carriage returns removed from raw
// strings, as go/scanner does), and ";" or "\n" for an explicit or inserted
// semicolon.
type token struct {
	tok Token
	pos Pos
	end Pos
	lit string
}

// scanTokens is the scan of src by compiler/syntax's scanner (the linked
// dump.Tokens): a failed scan's tokens stop at the failure, with no
// ILLEGAL and no EOF.
func scanTokens(src string) []token {
	words, err := linked.Tokens(context.Background(), src)
	if err != nil {
		panic(fmt.Sprintf("syntax: the linked scanner failed: %v", err))
	}
	return tokensOf(src, words)
}

// tokensOf is the tokens of src that words hold as kind, start and end:
// each with its source text, a raw string's without carriage returns, an
// inserted semicolon's "\n", and none for ILLEGAL and EOF.
func tokensOf(src string, words []uint32) []token {
	n := len(words) / 3
	kinds, starts, ends := make([]uint32, n), make([]uint32, n), make([]uint32, n)
	for j := range n {
		kinds[j], starts[j], ends[j] = words[3*j], words[3*j+1], words[3*j+2]
	}
	return tokensOfColumns(src, kinds, starts, ends)
}

// tokensOfColumns is the tokens of src from their kind, start and end
// columns (syntax.Columns' layout).
func tokensOfColumns(src string, kinds, starts, ends []uint32) []token {
	toks := make([]token, len(kinds))
	for j := range toks {
		k := Token(kinds[j])
		pos, end := Pos(starts[j]), Pos(ends[j])
		var lit string
		switch {
		case k == ILLEGAL || k == EOF:
		case k == SEMICOLON && end == pos:
			lit = "\n"
		case k == STRING && src[pos] == '`':
			lit = strings.ReplaceAll(src[pos:end], "\r", "")
		default:
			lit = src[pos:end]
		}
		toks[j] = token{tok: k, pos: pos, end: end, lit: lit}
	}
	return toks
}
