package syntax

import (
	"strings"
	"testing"
)

// TestTokenGapsAreTheSource: the tokens with width and the gaps between
// them, in turn, are each source byte for byte, for every .ergo file of the
// repository and both corpora (std, examples and compiler among them), so
// no byte of a file is outside CheckText but where its tokens sit.
// Mutant: ws0-gap-trims-space.
func TestTokenGapsAreTheSource(t *testing.T) {
	for _, in := range corpusInputs(t) {
		toks := scanTokens(in.src)
		gaps := tokenGaps(in.src, toks)
		var got strings.Builder
		got.WriteString(gaps[0].text)
		i := 1
		for _, tk := range toks {
			if tk.tok == EOF || tk.tok == ILLEGAL {
				break
			}
			if tk.end == tk.pos {
				continue
			}
			got.WriteString(in.src[tk.pos:tk.end])
			got.WriteString(gaps[i].text)
			i++
		}
		if i != len(gaps) || got.String() != in.src {
			t.Fatalf("%s: the tokens and gaps are not the source (%d gaps, %d used)", in.name, len(gaps), i)
		}
	}
}
