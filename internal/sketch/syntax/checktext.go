package syntax

import (
	"strconv"
	"strings"
)

// CheckText is what a check of src can read of it, apart from where its
// tokens sit: TokenText, then each comment's text with the ordinal of the
// token after it (in TokenText's order) and the line breaks between the
// comment's end and that token. The checker reads comments (a binding's
// `// ergo:` lines, which it finds by their line right above a
// declaration), so a comment is part of it; a reformat that keeps every
// comment where it stands against the next token keeps it. The warm
// answer of `ergo check DIR/...` keys a file by it (cmd/ergo,
// checktree.go), and TokenSites gives the positions it leaves out.
func CheckText(src string) string {
	s := scan(src)
	var b strings.Builder
	b.WriteString(TokenText(src))
	starts := tokenStarts(s)
	k := 0
	for _, c := range s.comments {
		for k < len(starts) && starts[k] < int(c.EndPos) {
			k++
		}
		next := len(src)
		if k < len(starts) {
			next = starts[k]
		}
		end := min(int(c.EndPos), next)
		b.WriteString("comment " + strconv.Itoa(k) + " " + strconv.Itoa(strings.Count(src[end:next], "\n")) + " " + strconv.Quote(c.Text) + "\n")
	}
	return b.String()
}

// TokenSites is the one-based line and byte column of each token
// TokenText writes, in its order: a position check reports at a token's
// start is that token's ordinal and stays put across an edit that keeps
// CheckText.
func TokenSites(src string) [][2]int {
	s := scan(src)
	starts := tokenStarts(s)
	out := make([][2]int, len(starts))
	for i, p := range starts {
		line, col := lineColumn(s.lines, p)
		out[i] = [2]int{line, col}
	}
	return out
}

// tokenStarts are the offsets of the tokens TokenText writes: every token
// but semicolons, up to the end or the first lexical error.
func tokenStarts(s *scanner) []int {
	var out []int
	for _, t := range s.toks {
		switch t.tok {
		case EOF, ILLEGAL:
			return out
		case SEMICOLON:
			continue
		}
		out = append(out, int(t.pos))
	}
	return out
}
