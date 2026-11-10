package lower

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// level2Program is the Ergo half of TestLevel2AdaptersRun: one user extern
// per Level 2 adapter (docs/spec/interop.md, Level 2 adapters), each over
// a Go function of the standard library whose behaviour the test computes
// with Go itself.
const level2Program = `import go regexp

// Partial: the value Go returns beside an error is kept.
extern go strconv.ParseInt func parseInt(s string, base int, bits int) (int64, Option[go.Error])
extern go strconv.Atoi func atoi(s string) (int, Option[go.Error])
// Option: (T, bool) is Some when Go's bool is true.
extern go strings.CutPrefix func cutPrefix(s string, p string) Option[string]
extern go strings.CutSuffix func cutSuffix(s string, p string) Option[string]
// Non-nil: a pointer that may be nil, kept as a go.Ref.
extern go regexp.MustCompile func mustCompile(p string) go.Ref[regexp.Regexp] needs go.Foreign
// Same, over a slice result outside the C26 domain.
extern go strings.Fields func fields(s string) []string
// Same, over a Go variadic: the lowering spreads the slice.
extern go path/filepath.Join func join(parts []string) string

func errText(e Option[go.Error]) string {
	match e {
		case Some as x: "err " + x.Value.Text
		case None: "ok"
	}
}

func optText(o Option[string]) string {
	match o {
		case Some as x: "some " + x.Value
		case None: "none"
	}
}

func numLine(s string) string {
	let (n, e) = parseInt(s, 10, 64)
	let (m, f) = atoi(s)
	"\(n) \(errText(e)) | \(m) \(errText(f))"
}

func cutLine(s string, p string) string {
	optText(cutPrefix(s, p)) + " | " + optText(cutSuffix(s, p)) + " | " + join([]string{s, p, s})
}

func reLine(p string, s string) string needs go.Foreign {
	let r = mustCompile(p)
	"\(r.MatchString(s)) \(len(fields(s)))"
}

func main() needs Console | go.Foreign {
`

// TestLevel2AdaptersRun is the run half of IG4's acceptance: the admitted
// adapters build, and on generated inputs (numbers in and out of range,
// garbage, prefixes that match or not, patterns and texts) each printed
// line equals Go's own result computed here. It builds Go because the
// subject is the emitted adapter: Go's two results bound and turned into
// an Option, the partial value kept beside the error, the nil check of a
// go.Ref.
func TestLevel2AdaptersRun(t *testing.T) {
	needsBuild(t)
	var src strings.Builder
	src.WriteString(level2Program)
	var want []string
	for _, seed := range importGoSeeds(1, 2) {
		r := rand.New(rand.NewPCG(seed, 0x1e42))
		for range 30 {
			switch r.IntN(3) {
			case 0:
				s := level2Number(r)
				fmt.Fprintf(&src, "\tConsole.Println(numLine(%s))\n", strconv.Quote(s))
				want = append(want, goNumLine2(s))
			case 1:
				s, p := importGoText(r, "ab"), importGoText(r, "ab")
				fmt.Fprintf(&src, "\tConsole.Println(cutLine(%s, %s))\n", strconv.Quote(s), strconv.Quote(p))
				want = append(want, goCutLine(s, p))
			default:
				p := []string{"a+", "^b", "(a|b)*$", `\s`, "ab?"}[r.IntN(5)]
				s := importGoText(r, "ab ")
				fmt.Fprintf(&src, "\tConsole.Println(reLine(%s, %s))\n", strconv.Quote(p), strconv.Quote(s))
				want = append(want, fmt.Sprintf("%v %d", regexp.MustCompile(p).MatchString(s), len(strings.Fields(s))))
			}
		}
	}
	src.WriteString("}\n")
	prog := mustLower(t, "level2_runs", src.String())
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["level2_runs"])
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, firstLines(stderr, 8))
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines for %d cases:\n%s", len(got), len(want), stdout)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d (replay with -importgo.seed=N):\nergo %s\ngo   %s", i, got[i], want[i])
		}
	}
}

// level2Number is a decimal that parses, overflows int64 or is not a
// number.
func level2Number(r *rand.Rand) string {
	digits := make([]byte, 1+r.IntN(22))
	for i := range digits {
		digits[i] = byte('0' + r.IntN(10))
	}
	return []string{"", "-", "+", "x", " "}[r.IntN(5)] + string(digits) + []string{"", "", "z"}[r.IntN(3)]
}

func goNumLine2(s string) string {
	errText := func(err error) string {
		if err != nil {
			return "err " + err.Error()
		}
		return "ok"
	}
	n, e := strconv.ParseInt(s, 10, 64)
	m, f := strconv.Atoi(s)
	return fmt.Sprintf("%d %s | %d %s", n, errText(e), m, errText(f))
}

func goCutLine(s, p string) string {
	opt := func(v string, ok bool) string {
		if ok {
			return "some " + v
		}
		return "none"
	}
	return opt(strings.CutPrefix(s, p)) + " | " + opt(strings.CutSuffix(s, p)) + " | " + filepath.Join(s, p, s)
}

// TestLevel2NonNilDefect: the Non-nil adapter's check, a nil pointer from
// Go (flag.Lookup of a flag no one defined), is a defect naming the
// target and the Ergo call, and the program prints nothing after it.
func TestLevel2NonNilDefect(t *testing.T) {
	needsBuild(t)
	const src = `import go flag
extern go flag.Lookup func lookup(name string) go.Ref[flag.Flag] needs go.Foreign
func main() needs Console | go.Foreign {
	let f = lookup("no-such-flag")
	Console.Println(f.Name)
}
`
	out, err := ergoRun(t, src)
	if err == nil || !strings.Contains(out, "panic: Go returned nil without an error at main.ergo:4:10: flag.Lookup") {
		t.Errorf("err %v; want the nil defect at main.ergo:4:10:\n%s", err, out)
	}
}
