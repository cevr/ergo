package lower

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

var goErrorChainSeed = flag.Uint64("goerror.seed", 0, "run TestGoErrorChainsAgree on this seed alone")

// geLeaf is a leaf of a generated go.Error chain: its Ergo expression (a
// go.Error) and the Go error the model makes for it (nil for go.Error{}).
type geLeaf struct {
	ergo  string
	model func() error
}

// geTextErr is the model of a go.Error an Ergo program builds
// (go.Error{Text: s}): a comparable error of its own text.
type geTextErr string

func (e geTextErr) Error() string { return string(e) }

const geMissing = "/nonexistent/loop-probe-x/ig5"

var geLeaves = []geLeaf{
	// import go, Level 1: *strconv.NumError over ErrSyntax and ErrRange.
	{`atoiErr("x")`, func() error { _, err := strconv.Atoi("x"); return err }},
	{`rangeErr("99999999999999999999")`, func() error {
		_, err := strconv.ParseInt("99999999999999999999", 10, 64)
		return err
	}},
	// A C26 user extern (hostFallible) and a Level 2 one (goExtern).
	{`externErr("1x")`, func() error { _, err := strconv.Atoi("1x"); return err }},
	{`urlErr("::bad")`, func() error { _, err := url.Parse("::bad"); return err }},
	// A host error from the file system: *fs.PathError over ENOENT.
	{`readErr("` + geMissing + `")`, func() error { _, err := os.ReadFile(geMissing); return err }},
	// Errors an Ergo program builds, sentinels and the zero go.Error.
	{`go.Error{Text: "t0"}`, func() error { return geTextErr("t0") }},
	{`go.Error{Text: "t1"}`, func() error { return geTextErr("t1") }},
	{`io.EOF`, func() error { return io.EOF }},
	{`strconv.ErrSyntax`, func() error { return strconv.ErrSyntax }},
	{`go.Error{}`, func() error { return nil }},
}

// geChain is a generated chain: errors.Join over leaves (by index into the
// program's leaf lets) and chains.
type geChain struct {
	leaf        int
	left, right *geChain
}

func genGeChain(r *rand.Rand, d, leaves int) *geChain {
	if d <= 0 || r.IntN(3) == 0 {
		return &geChain{leaf: r.IntN(leaves)}
	}
	return &geChain{leaf: -1, left: genGeChain(r, d-1, leaves), right: genGeChain(r, d-1, leaves)}
}

func (c *geChain) ergo() string {
	if c.leaf >= 0 {
		return "l" + strconv.Itoa(c.leaf)
	}
	return "join(" + c.left.ergo() + ", " + c.right.ergo() + ")"
}

func (c *geChain) model(leaves []error) error {
	if c.leaf >= 0 {
		return leaves[c.leaf]
	}
	return errors.Join(c.left.model(leaves), c.right.model(leaves))
}

func geText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// geModelLine is the line the program prints for chain i, computed by Go.
func geModelLine(i int, err error, leaves []error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "c%d %s eof=%v syn=%v rng=%v nex=%v gois=%v t0=%v eq0=%v", i, strconv.Quote(geText(err)),
		errors.Is(err, io.EOF), errors.Is(err, strconv.ErrSyntax), errors.Is(err, strconv.ErrRange),
		errors.Is(err, fs.ErrNotExist), errors.Is(err, strconv.ErrRange), errors.Is(err, geTextErr("t0")), geText(err) == geText(leaves[0]))
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		fmt.Fprintf(&b, "|num=%s:%s", ne.Func, ne.Num)
	} else {
		b.WriteString("|num=-")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		fmt.Fprintf(&b, "|url=%s", ue.Op)
	} else {
		b.WriteString("|url=-")
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		fmt.Fprintf(&b, "|path=%s:%s:%v", pe.Op, pe.Path, errors.Is(pe.Err, fs.ErrNotExist))
	} else {
		b.WriteString("|path=-")
	}
	for j, l := range leaves {
		fmt.Fprintf(&b, " l%d=%v", j, errors.Is(err, l))
	}
	return b.String()
}

// TestGoErrorChainsAgree is the acceptance of the native go.Error (IG5):
// go.Error keeps Go's error, so go.Is and go.As read its chain as Go's
// errors.Is and errors.As do, and Text is err.Error(). Each seed draws
// leaves and chains: errors.Join trees (import go errors, whose []go.Error
// argument crosses as Go's own errors) over leaves made by Go through
// import go (strconv's *NumError over ErrSyntax and ErrRange,
// os.ReadFile's *fs.PathError over ENOENT), a C26 user extern and a Level
// 2 one (*url.Error), sentinel variables of bindings (io.EOF,
// strconv.ErrSyntax), and go.Error values the program builds, the zero one
// included (errors.Join drops it, as Go drops nil). For each chain the
// program prints its Text, go.Is against the sentinels, an Ergo-built
// target and every leaf, == against a leaf (Text), and go.As into
// go.Ref[strconv.NumError], go.Ref[url.Error] and go.Ref[fs.PathError]
// with their fields (PathError's Err field is a go.Error read from Go).
// The model builds the same chains in Go. The program is lowered lazily
// and with the eager reference (SetGoErrorEagerForTest); both print the
// model. -goerror.seed=N replays one seed.
//
// It builds Go because the native error lives only in the emitted
// program: which Go error a call returns and what Go's errors package
// finds in it. A typed nil and custom Unwrap types need a Go package of
// the test's own, which import go admits from IG7 on; the runtime's
// TestGoErrorChainsModel covers them in process.
//
// Mutants: a go.Error argument passed to Go as the go.Error wrapper, not
// its native error (Join hides the leaves); a sentinel variable made from
// its text (go.Is of io.EOF fails); a Go error field not made a go.Error
// (build); the update or literal of go.Error not GoErrorText (build).
func TestGoErrorChainsAgree(t *testing.T) {
	var src strings.Builder
	src.WriteString(`import go strconv
import go errors
import go io
import go io/fs
import go os
import go net/url

extern go strconv.Atoi func atoiX(s string) int fails go.Error
extern go net/url.Parse func parseURL(s string) go.Ref[url.URL] fails go.Error needs go.Foreign

func atoiErr(s string) go.Error {
	let _ = strconv.Atoi(s) catch { case go.Error as e: return e }
	go.Error{}
}

func rangeErr(s string) go.Error {
	let _ = strconv.ParseInt(s, 10, 64) catch { case go.Error as e: return e }
	go.Error{}
}

func externErr(s string) go.Error {
	let _ = atoiX(s) catch { case go.Error as e: return e }
	go.Error{}
}

func urlErr(s string) go.Error needs go.Foreign {
	let _ = parseURL(s) catch { case go.Error as e: return e }
	go.Error{}
}

func readErr(p string) go.Error needs go.Foreign {
	let _ = os.ReadFile(p) catch { case go.Error as e: return e }
	go.Error{}
}

func join(a go.Error, b go.Error) go.Error {
	errors.Join([]go.Error{a, b}) catch { case go.Error as e: return e }
	go.Error{}
}

func show(i int, c go.Error, l0 go.Error) string needs go.Foreign {
	let num = match go.As[go.Ref[strconv.NumError]](c) {
		case Some as v: "num=" + v.Value.Func + ":" + v.Value.Num
		case None: "num=-"
	}
	let u = match go.As[go.Ref[url.Error]](c) {
		case Some as v: "url=" + v.Value.Op
		case None: "url=-"
	}
	let p = match go.As[go.Ref[fs.PathError]](c) {
		case Some as v: "path=\(v.Value.Op):\(v.Value.Path):\(go.Is(v.Value.Err, fs.ErrNotExist))"
		case None: "path=-"
	}
	"c\(i) \(strconv.Quote(c.Text)) eof=\(go.Is(c, io.EOF)) syn=\(go.Is(c, strconv.ErrSyntax)) rng=\(go.Is(c, strconv.ErrRange)) nex=\(go.Is(c, fs.ErrNotExist)) gois=\(errors.Is(c, strconv.ErrRange)) t0=\(go.Is(c, go.Error{Text: "t0"})) eq0=\(c == l0)|" + num + "|" + u + "|" + p
}
`)
	seeds := []uint64{1, 2, 3, 4}
	if *goErrorChainSeed != 0 {
		seeds = []uint64{*goErrorChainSeed}
	}
	var want []string
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x16e5))
		fmt.Fprintf(&src, "\nfunc seed%d() needs Console | go.Foreign {\n", seed)
		n := 3 + r.IntN(3)
		var leaves []error
		for j := range n {
			l := geLeaves[r.IntN(len(geLeaves))]
			fmt.Fprintf(&src, "\tlet l%d = %s\n", j, l.ergo)
			leaves = append(leaves, l.model())
		}
		for i := range 6 {
			c := genGeChain(r, 3, n)
			fmt.Fprintf(&src, "\tlet c%d = %s\n\tConsole.Println(show(%d, c%d, l0)", i, c.ergo(), i, i)
			for j := range n {
				fmt.Fprintf(&src, ` + " l%d=\(go.Is(c%d, l%d))"`, j, i, j)
			}
			src.WriteString(")\n")
			want = append(want, geModelLine(i, c.model(leaves), leaves))
		}
		src.WriteString("}\n")
	}
	// Fixed cases: == compares Text, not Go's identity (two *NumError of
	// one text); an update and a literal build a go.Error of their text.
	src.WriteString(`
func fixed() string {
	let a = atoiErr("x")
	let b = go.Error{..a, Text: "u"}
	"eq=\(a == atoiErr("x")) ne=\(a != atoiErr("y")) upd=\(b.Text) is=\(go.Is(b, go.Error{Text: "u"})) lit=\(go.Error{Text: "v"} == go.Error{Text: "v"})"
}
`)
	want = append(want, "eq=true ne=true upd=u is=true lit=true")
	src.WriteString("\nfunc main() needs Console | go.Foreign {\n")
	for _, seed := range seeds {
		fmt.Fprintf(&src, "\tseed%d()\n", seed)
	}
	src.WriteString("\tConsole.Println(fixed())\n}\n")
	lazy := mustLower(t, "goerror_lazy", src.String())
	restore := SetGoErrorEagerForTest(true)
	eager := mustLower(t, "goerror_eager", src.String())
	restore()
	if strings.Contains(lazy.mainGo(), "Eager") || !strings.Contains(eager.mainGo(), "GoErrorEager") {
		t.Fatalf("the lazy lowering names the eager form, or the reference does not")
	}
	exes := buildAll(t, []*lowered{lazy, eager}, true)
	if exes == nil {
		return
	}
	for _, prog := range []string{"goerror_lazy", "goerror_eager"} {
		stdout, stderr, code := runExe(t, exes[prog])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s", prog, code, firstLines(stderr, 8))
		}
		got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(got) != len(want) {
			t.Fatalf("%s: %d lines for %d chains:\n%s\nsource:\n%s", prog, len(got), len(want), stdout, src.String())
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s line %d (replay with -goerror.seed=N):\nergo %s\ngo   %s", prog, i, got[i], want[i])
			}
		}
	}
	if t.Failed() {
		t.Logf("source:\n%s", src.String())
	}
}
