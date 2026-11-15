package lower

import (
	"strings"
	"testing"
)

// strconvAppendSource is a writer that holds its buffer, as a codec's
// provider does, and appends numbers to it with std strconv's appenders;
// and two appends to a buffer the caller holds.
const strconvAppendSource = `import strconv

type text { out []byte }

func put(t *text, n int64, f float64) {
	t.out = strconv.AppendInt(t.out, n, 10)
	t.out = strconv.AppendUint(t.out, uint64(n), 16)
	t.out = strconv.AppendFloat(t.out, f, 'g', -1)
}

func two(p []byte) ([]byte, []byte) {
	let a = strconv.AppendFloat(p, 2.5, 'g', -1)
	let b = strconv.AppendInt(p, 1, 10)
	(a, b)
}

func main() {
	let mut t = text{out: []byte{}}
	put(&t, 42, 1.5)
	let _ = two(t.out)
}
`

// std strconv's AppendInt, AppendUint and AppendFloat are appenders
// (goBodyAppends), with the prelude Append's value rule (V3), so any
// package, not only std json, appends a number to the buffer it holds:
// `t.out = strconv.AppendFloat(t.out, …)` runs on t.out itself, with no
// copy, and an append to a buffer that is read again takes it clipped, so
// a second append to it never writes the first one's bytes. The promised
// facts are the operand's form, so this reads the lowered Go. Mutants
// (testdata/mutants.json): nappend-float-not-appender (the body written
// so that it is no appender), nappend-std-operand-unclipped (stdCall
// skips the value rule).
func TestStrconvAppendersWriteInPlace(t *testing.T) {
	t.Parallel()
	text := mustLower(t, "strconvappend", strconvAppendSource).mainGo()
	put, two := goFuncText(text, "put"), goFuncText(text, "two")
	for _, body := range []string{"StrconvAppendInt(t.out,", "StrconvAppendUint(t.out,", "StrconvAppendFloat(t.out,"} {
		if !strings.Contains(put, body) {
			t.Errorf("put appends with no %s in place:\n%s", body, put)
		}
	}
	if !strings.Contains(two, "StrconvAppendFloat(slices.Clip(p),") {
		t.Errorf("an append to p, the caller's buffer and read again, does not clip it:\n%s", two)
	}
}

// goFuncText is the Go text of the function or method name in text, to
// the next top-level declaration.
func goFuncText(text, name string) string {
	i := strings.Index(text, ") "+name+"(")
	if j := strings.Index(text, "\nfunc "+name+"("); j >= 0 && (i < 0 || j < i) {
		i = j
	}
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}
