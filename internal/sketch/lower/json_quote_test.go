package lower

import (
	"go/constant"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestJSONKeyFoldAgrees is a seeded property: the constant text that the
// rewrite engine computes for json Text's KeyFolds and KeyFoldsHTML (the
// right side's raw(quote(k, html) + ":"), by eval.Fold with the rewrite
// budget, as rewrite.go computes it) is the text the Go writer's Key body
// writes for k on a Text and on an HTML Text. A key the evaluator cannot
// fold (a byte of invalid UTF-8, whose escape needs the hex extern) must
// say why, and is never folded to other text. Until #162 runs Text's laws,
// this property is what holds quote to the writer. A failing seed prints
// the key and both texts; ERGO_JSON_QUOTE_SEED replays it.
func TestJSONKeyFoldAgrees(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_JSON_QUOTE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	replay := "replay: ERGO_JSON_QUOTE_SEED=" + strconv.FormatInt(seed, 10) + " go test -run TestJSONKeyFoldAgrees ./internal/sketch/lower"
	pkg, err := check.Std.Import("json")
	if err != nil {
		t.Fatal(err)
	}
	folds := map[bool]*check.Law{}
	for _, l := range pkg.Laws {
		if l.Owner != nil && l.Owner.Name == "Text" && (l.Name == "KeyFolds" || l.Name == "KeyFoldsHTML") {
			folds[l.Name == "KeyFoldsHTML"] = l
		}
	}
	if len(folds) != 2 {
		t.Fatalf("json Text has %d of the laws KeyFolds and KeyFoldsHTML", len(folds))
	}
	// The pieces a key draws from: each escape class of the writer, plain
	// text, and a byte of invalid UTF-8.
	pieces := []string{"a", "Z", "_", "0", "é", "日", "\"", "\\", "\n", "\r", "\t", "\x00", "\x1f", "\x7f", "<", ">", "&", "\u2028", "\u2029", "\ufffd", "\U0001F600", "\xff", "\xc3"}
	rng := rand.New(rand.NewSource(seed))
	folded := 0
	for range 400 {
		var b strings.Builder
		for range rng.Intn(6) {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		k := b.String()
		for _, html := range []bool{false, true} {
			law := folds[html]
			x := law.Rewrite.Rhs[0].Args[0].X
			got, why := eval.Fold(law.Pkg, law.Decl, x, map[*check.Var]constant.Value{law.Params[0]: constant.MakeString(k)}, rewriteFoldBudget)
			text := v1.NewJSONText()
			if html {
				text = v1.NewJSONHTMLText()
			}
			text.KeyBody(nil, k)
			want := text.Written()
			switch {
			case why != "":
				if strings.ToValidUTF8(k, "") == k {
					t.Fatalf("seed %d: law %s does not fold the valid key %q: %s\n%s", seed, law.Name, k, why, replay)
				}
			case got.Kind() != constant.String || constant.StringVal(got) != want:
				t.Fatalf("seed %d: law %s folds the key %q to %s; the writer writes %q\n%s", seed, law.Name, k, got.ExactString(), want, replay)
			default:
				folded++
			}
		}
	}
	if folded == 0 {
		t.Fatalf("seed %d: no key folded\n%s", seed, replay)
	}
}
