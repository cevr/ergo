package lower

import (
	"strings"
	"testing"
)

// TestLawBodyCopies is the smallest repro of the law-copy bug the inbox
// example found (MAPCOPY r2): a law's body had no copies decided, so
// `let before = b` shared b's Map and a write through &b reached before
// (examples/inbox, law ThreadsApart). The law's Go copies b at the bind,
// as a function's does. Mutant: lawDecl decides no copies
// (mapcopy-law-values-off).
func TestLawBodyCopies(t *testing.T) {
	src := "type box { m Map[int, int] }\n\nfunc put(b *box, k int) {\n\tb.m[k] = k\n}\n\n" +
		"law Apart(n int) {\n\tlet mut b = box{m: Map[int, int]{}}\n\tlet before = b\n\tput(&b, n)\n\tlen(before.m) == 0\n}\n\n" +
		"func main() {}\n"
	p := lowerTestBuild(t, "lawcopies", src)
	var all strings.Builder
	for _, f := range p.files {
		if strings.HasSuffix(f.Path, ".go") {
			all.Write(f.Data)
		}
	}
	body := goFuncBody(t, all.String(), "ergoLawApart")
	if !copyOps.MatchString(body) {
		t.Errorf("the law's bind of before copies nothing, so put(&b, n) reaches it:\n%s", body)
	}
}
