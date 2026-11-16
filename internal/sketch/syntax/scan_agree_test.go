package syntax

// The agreement of compiler/syntax's scanner (the Ergo compiler's, capstone
// S2a) with this one. The Ergo scanner runs in process, linked through the
// IR as internal/linked/syntax (dump.Scan); this file is in package syntax
// so it reads this scanner's tokens with no new export.

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/syntax"
	"ergo.local/prototype/internal/sketch/sketchtest"
)

// agreeInput is one source of an agreement test: a corpus file (seed -1)
// or a seeded one.
type agreeInput struct {
	name string
	src  string
	seed int64
}

// scanList is a scan as both sides print it: the tokens (kind, start,
// end), the comments (start, end), the line count and the failure.
type scanList struct {
	toks     [][3]int
	comments [][2]int
	lines    int
	failed   bool
	offset   int
	length   int
}

// goList is this scanner's scan of src. A failed scan's last two tokens
// are the ILLEGAL and EOF the scanner appends, which the Ergo scanner does
// not write.
func goList(src string) scanList {
	s := scan(src)
	var l scanList
	toks := s.toks
	if s.err != nil {
		toks = toks[:len(toks)-2]
		l.failed, l.offset, l.length = true, s.err.Offset, s.err.Length
	}
	for _, t := range toks {
		l.toks = append(l.toks, [3]int{int(t.tok), int(t.pos), int(t.end)})
	}
	for _, c := range s.comments {
		l.comments = append(l.comments, [2]int{int(c.Slash), int(c.EndPos)})
	}
	l.lines = len(s.lines)
	return l
}

func (l scanList) String() string {
	var b strings.Builder
	for _, t := range l.toks {
		fmt.Fprintf(&b, "T %d(%s) %d %d\n", t[0], Token(t[0]), t[1], t[2])
	}
	for _, c := range l.comments {
		fmt.Fprintf(&b, "C %d %d\n", c[0], c[1])
	}
	fmt.Fprintf(&b, "N %d\n", l.lines)
	if l.failed {
		fmt.Fprintf(&b, "E %s %d %d\n", CodeSyntax, l.offset, l.length)
	} else {
		b.WriteString("OK\n")
	}
	return b.String()
}

// ergoList reads dump.Scan's text ("T kind start end", "C start end",
// "N lines", "E code offset length" or "OK").
func ergoList(dump string) (scanList, error) {
	var l scanList
	for _, line := range strings.Split(strings.TrimSuffix(dump, "\n"), "\n") {
		f := strings.Fields(line)
		n := make([]int, len(f))
		for i := 1; i < len(f); i++ {
			if f[0] == "E" && i == 1 {
				continue
			}
			v, err := strconv.Atoi(f[i])
			if err != nil {
				return l, fmt.Errorf("dump line %q", line)
			}
			n[i] = v
		}
		switch {
		case len(f) == 4 && f[0] == "T":
			l.toks = append(l.toks, [3]int{n[1], n[2], n[3]})
		case len(f) == 3 && f[0] == "C":
			l.comments = append(l.comments, [2]int{n[1], n[2]})
		case len(f) == 2 && f[0] == "N":
			l.lines = n[1]
		case len(f) == 4 && f[0] == "E" && f[1] == CodeSyntax:
			l.failed, l.offset, l.length = true, n[2], n[3]
		case len(f) == 1 && f[0] == "OK":
		default:
			return l, fmt.Errorf("dump line %q", line)
		}
	}
	return l, nil
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

// pieces are the scan generator's alphabet: every token form, its bad
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

func generated(seed int64) string {
	r := rand.New(rand.NewSource(seed))
	n := 1 + r.Intn(40)
	var b strings.Builder
	for range n {
		b.WriteString(pieces[r.Intn(len(pieces))])
		if r.Intn(3) == 0 {
			b.WriteByte(' ')
		}
	}
	return b.String()
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

// pickScan is seed's source: generated for an even seed, a perturbed
// corpus file for an odd one.
func pickScan(seed int64, corpus []agreeInput) string {
	if seed%2 == 0 {
		return generated(seed)
	}
	return perturbed(seed, corpus)
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

// TestScanAgreesWithGo (capstone S2a): on every .ergo file of the
// repository, the positive and superseded corpora, and seeded generated
// and perturbed sources, the Ergo scanner's tokens (kind, start, end),
// comments (start, end), line count and failure (code, offset, length)
// equal this scanner's. A failing seed prints the source, both scans and
// the replay command. It kills the scan-* rows of
// compiler/syntax/testdata/mutants.json that name it.
func TestScanAgreesWithGo(t *testing.T) {
	corpus := corpusInputs(t)
	inputs := agreeSeeds(t, func(s int64) string { return pickScan(s, corpus) })
	if os.Getenv("ERGO_AGREE_SEED") == "" {
		inputs = append(corpus, inputs...)
	}
	failures := 0
	for _, in := range inputs {
		out, err := linked.Scan(context.Background(), in.src)
		if err != nil {
			t.Fatalf("%s: the Ergo scanner failed: %v\nsource %q", in.name, err, in.src)
		}
		got, err := ergoList(out)
		if err != nil {
			t.Fatalf("%s: %v", in.name, err)
		}
		want := goList(in.src)
		if got.String() == want.String() {
			continue
		}
		failures++
		if failures <= 3 {
			t.Errorf("%s: the scans differ\nsource %q\n--- go\n%s--- ergo\n%s%s", in.name, in.src, firstDiff(want.String(), got.String()), firstDiff(got.String(), want.String()), replay(in, "TestScanAgreesWithGo"))
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d inputs differ", failures, len(inputs))
	}
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
