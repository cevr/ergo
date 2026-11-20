package syntax

// The inputs of the decoder property (decode_view_test.go) and of the
// parse laws (roundtrip_test.go): every .ergo file of the repository, the
// positive and superseded corpora, and seeded sources, generated
// (parse_gen_test.go) or perturbed corpus files.

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// agreeInput is one source of an agreement test: a corpus file (seed -1)
// or a seeded one.
type agreeInput struct {
	name string
	src  string
	seed int64
}

// corpusInputs are every .ergo file of the repository, the positive
// inputs and the superseded corpus (which holds the files that do not
// parse).
func corpusInputs(t *testing.T) []agreeInput {
	var out []agreeInput
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "tmp") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".ergo") {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out = append(out, agreeInput{name: p, src: string(b), seed: -1})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pos, err := sketchtest.Positive()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range pos {
		out = append(out, agreeInput{name: "positive:" + in.Name, src: in.Src, seed: -1})
	}
	_, sup, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range sup {
		out = append(out, agreeInput{name: "superseded:" + in.Name, src: in.Src, seed: -1})
	}
	return out
}

// pieces are the perturbation's alphabet: every token form, its bad
// forms, and the bytes that change a context.
var pieces = []string{
	"x", "_y", "héllo", "名前", "a1", "for", "fortune", "go", "goto", "import", "extern", "import go", "extern go",
	"net/http", "a.b/c", "x-y", "a//b", "a/", "/a", "~x", "a+b", "\xff", "\xe2\x82", "é", "€",
	"0", "42", "1_000", "1__0", "_1", "1_", "0x1F", "0xFF_FF", "0x", "0x_1", "0b102", "0o78", "0X1p3", "0x1.5",
	"1.5", "1.", ".5", "1e9", "2.5e-3", "1e", "1..n", "500ms", "3h", "2s", "1i", "5xs", "1.5s", "1E+2",
	"\"a\"", "\"a\\\"b\"", "\"\\(x)\"", "\"a \\(f(1)) b \\(y) c\"", "\"\\(\"", "\"unterminated", "\"a\\\nb\"",
	"`raw`", "`raw\r\nx`", "`open", "'a'", "'\\n'", "'ab'", "''", "'\\x41'", "'é'", "'\\u00e9'", "'", "'\\'",
	"'\\U0001F600'", "'\\U00110000'", "'\\ud800'", "'\\uDFFF'", "'\\uE000'", "'\\377'", "'\\400'", "'\\08'", "'\\xff'",
	"'\\xg1'", "'\\a'", "'\\''", "'\\\"'", "'\xff'", "'\xe2'", "'\\q'", "'\\u12'", "'éa'", "'\\\\'", "'\\x4'",
	"// line", "//", "/* b */", "/* multi\nline */", "/* open", "/**/", "// x\r",
	"(", ")", "[", "]", "{", "}", ",", ".", "..", "...", ";", ":", ":=", "=", "==", "!", "!=",
	"<", "<<", "<<=", "<-", "<=", ">", ">>", ">>=", ">=", "&", "&&", "&^", "&^=", "&=", "|", "||", "|=",
	"+", "++", "+=", "-", "--", "-=", "*", "*=", "/", "/=", "%", "%=", "^", "^=", "@", "#", "$", "?", "\\",
	" ", " ", "\t", "\n", "\n", "\r\n", "\r", "return", "break", "continue", "takes", "case", "match", "skip",
}

// perturbed is a corpus file with a few bytes changed, inserted or
// removed.
func perturbed(seed int64, corpus []agreeInput) string {
	r := rand.New(rand.NewSource(seed))
	src := []byte(corpus[r.Intn(len(corpus))].src)
	if len(src) > 3000 {
		at := r.Intn(len(src) - 2000)
		src = src[at : at+2000]
	}
	for range 1 + r.Intn(4) {
		if len(src) == 0 {
			break
		}
		at := r.Intn(len(src))
		p := pieces[r.Intn(len(pieces))]
		switch r.Intn(3) {
		case 0:
			src = append(src[:at:at], append([]byte(p), src[at:]...)...)
		case 1:
			src = append(src[:at:at], src[at+1:]...)
		default:
			src[at] = p[0]
		}
	}
	return string(src)
}

// agreeSeeds are the seeded inputs of an agreement test: ERGO_AGREE_SEED
// replays one seed, ERGO_AGREE_SEEDS sets the count (default 2000).
func agreeSeeds(t *testing.T, pick func(int64) string) []agreeInput {
	if v := os.Getenv("ERGO_AGREE_SEED"); v != "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_AGREE_SEED=%q", v)
		}
		return []agreeInput{{name: "seed", src: pick(s), seed: s}}
	}
	seeds := 2000
	if v := os.Getenv("ERGO_AGREE_SEEDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("ERGO_AGREE_SEEDS=%q", v)
		}
		seeds = n
	}
	var out []agreeInput
	for i := range seeds {
		out = append(out, agreeInput{name: fmt.Sprintf("seed-%d", i), src: pick(int64(i)), seed: int64(i)})
	}
	return out
}

// replay is the command that reruns a seeded input alone, or "".
func replay(in agreeInput, test string) string {
	if in.seed < 0 {
		return ""
	}
	return fmt.Sprintf("\nreplay: ERGO_AGREE_SEED=%d go test -count=1 -run '^%s$' ./internal/sketch/syntax", in.seed, test)
}

// firstDiff is a's lines from a few before the first line b lacks.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	i := 0
	for i < len(al) && i < len(bl) && al[i] == bl[i] {
		i++
	}
	lo, hi := max(0, i-3), min(len(al), i+4)
	return strings.Join(al[lo:hi], "\n") + "\n"
}
