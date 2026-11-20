package syntax

import (
	"strconv"
	"strings"
)

// CheckText is what a check of src can read of it, apart from where its
// tokens sit: TokenText, then each gap between tokens that is not all
// space, with the ordinal of the token after it (in TokenText's order),
// the line breaks before and after its first and last byte that is not
// space, and those bytes. The checker reads comments (a binding's
// `// ergo:` lines, which it finds by their line right above a
// declaration), so the bytes between tokens are part of it; a reformat
// that moves no such byte against its tokens' lines keeps it. The warm
// answer of `ergo check DIR/...` keys a file by it (cmd/ergo,
// checktree.go), and TokenSites gives the positions it leaves out.
//
// The tokens are compiler/syntax's scanner's; a gap is opaque bytes:
// this Go reads no comment syntax (the self-host rule: Ergo is lexed only
// by compiler/syntax). It moves to Ergo with the front end.
func CheckText(src string) string {
	toks := scanTokens(src)
	var b strings.Builder
	b.WriteString(TokenText(src))
	for _, g := range tokenGaps(src, toks) {
		text := strings.TrimSpace(g.text)
		if text == "" {
			continue
		}
		lead := strings.Count(g.text[:strings.Index(g.text, text)], "\n")
		trail := strings.Count(g.text[strings.LastIndex(g.text, text)+len(text):], "\n")
		b.WriteString("gap " + strconv.Itoa(g.next) + " " + strconv.Itoa(lead) + " " + strconv.Itoa(trail) + " " + strconv.Quote(text) + "\n")
	}
	return b.String()
}

// MarkerText is the `// ergo:` comments of the parsed file f, each with
// the ordinal of the token after it (in TokenText's order) and the line
// breaks between them: the comments the checker reads (a binding's
// markers, which it finds by their line right above a declaration,
// check/gobinding.go and goinspect.go). TokenText leaves them out, so a
// fingerprint of the file's check holds TokenText and MarkerText. It reads
// the parser's comments, not the bytes: Ergo is lexed only by
// compiler/syntax.
func MarkerText(f *File) string {
	if f == nil {
		return ""
	}
	var starts []int
	var b strings.Builder
	for _, g := range f.Comments {
		for _, c := range g.List {
			if !strings.HasPrefix(c.Text, "// ergo:") {
				continue
			}
			if starts == nil {
				starts = append(tokenStarts(scanTokens(f.Src)), len(f.Src))
			}
			k := 0
			for k < len(starts)-1 && starts[k] < int(c.EndPos) {
				k++
			}
			end := min(int(c.EndPos), starts[k])
			b.WriteString("marker " + strconv.Itoa(k) + " " + strconv.Itoa(strings.Count(f.Src[end:starts[k]], "\n")) + " " + strconv.Quote(c.Text) + "\n")
		}
	}
	return b.String()
}

// TokenSites is the one-based line and byte column of each token
// TokenText writes, in its order: a position check reports at a token's
// start is that token's ordinal and stays put across an edit that keeps
// CheckText.
func TokenSites(src string) [][2]int {
	starts := tokenStarts(scanTokens(src))
	lines := lineStarts(src)
	out := make([][2]int, len(starts))
	for i, p := range starts {
		line, col := lineColumn(lines, p)
		out[i] = [2]int{line, col}
	}
	return out
}

// tokenStarts are the offsets of the tokens TokenText writes: every token
// but semicolons, up to the end or the first lexical error.
func tokenStarts(toks []token) []int {
	var out []int
	for _, t := range toks {
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

// tokenGap is the bytes of src between two tokens that have width (or
// before the first, or after the last), and the ordinal among
// tokenStarts of the token after it (len when none).
type tokenGap struct {
	text string
	next int
}

// tokenGaps are the gaps of src in order, one before each token with
// width and one after the last: those tokens' bytes and the gaps, in
// turn, are src. An inserted semicolon has no width and splits no gap.
// After a failed scan the rest of src is the last gap.
func tokenGaps(src string, toks []token) []tokenGap {
	var out []tokenGap
	at, ordinal := 0, 0
	for _, t := range toks {
		if t.tok == EOF || t.tok == ILLEGAL {
			break
		}
		if t.end == t.pos {
			continue
		}
		out = append(out, tokenGap{text: src[at:int(t.pos)], next: ordinal})
		at = int(t.end)
		if t.tok != SEMICOLON {
			ordinal++
		}
	}
	return append(out, tokenGap{text: src[at:], next: ordinal})
}
