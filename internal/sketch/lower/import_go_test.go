package lower

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/tabwriter"
)

var importGoSeed = flag.Uint64("importgo.seed", 0, "run TestImportGoRuns and TestHostCopyElisionAgrees on this seed alone")

func importGoSeeds(defaults ...uint64) []uint64 {
	if *importGoSeed != 0 {
		return []uint64{*importGoSeed}
	}
	return defaults
}

// importGoProgram is the Ergo half of TestImportGoRuns: one function per Go
// package, each a Level 1 use of import go (#202): a (*S, error) result
// (url.Parse, regexp.Compile), methods on a go.Ref, fields, a nullable
// field (u.User), a method of a Go type over a Map (url.Values.Get), a
// variadic (filepath.Join), a tuple result (strings.CutPrefix), a slice
// result (FindAllString), a Go interface argument (io.Writer) and
// strconv's failures.
const importGoProgram = `import go net/url
import go regexp
import go strconv
import go strings
import go bytes
import go io
import go text/tabwriter
import go path/filepath

func urlParts(raw string) string fails go.Error needs go.Foreign {
	let u = try url.Parse(raw)
	let q = u.Query().Get("q")
	let user = match u.User {
		case Some as ui: ui.Value.Username()
		case None: "-"
	}
	"\(u.Scheme)|\(u.Hostname())|\(u.Port())|\(u.Path)|\(u.RawQuery)|\(q)|\(user)|\(u.String())"
}

func urlLine(raw string) string needs go.Foreign {
	urlParts(raw) catch { case go.Error as e: "err " + e.Text }
}

func reParts(p string, s string) string fails go.Error needs go.Foreign {
	let r = try regexp.Compile(p)
	let all = r.FindAllString(s, -1)
	"\(r.MatchString(s))|\(strings.Join(all, ","))|\(r.ReplaceAllString(s, "<$0>"))"
}

func reLine(p string, s string) string needs go.Foreign {
	reParts(p, s) catch { case go.Error as e: "err " + e.Text }
}

func numParts(s string) string fails go.Error {
	let n = try strconv.Atoi(s)
	"\(n * 2)|\(strconv.FormatInt(int64(n), 36))|\(quoted(s))"
}

func quoted(s string) string {
	strconv.Quote(s)
}

func numLine(s string) string {
	numParts(s) catch { case go.Error as e: "err " + e.Text }
}

func pathLine(parts []string) string {
	let (after, found) = strings.CutPrefix(filepath.Join(parts), "a/")
	"\(after)|\(found)"
}

func table(rows []string) string fails go.Error needs go.Foreign {
	match bytes.NewBufferString("") {
		case Some as b:
			match tabwriter.NewWriter(b.Value, 0, 8, 1, ' ', 0) {
				case Some as w:
					for r in rows {
						let _ = try io.WriteString(w.Value, r)
					}
					try w.Value.Flush()
					strings.ReplaceAll(b.Value.String(), "\n", "/")
				case None: "no writer"
			}
		case None: "no buffer"
	}
}

func tableLine(rows []string) string needs go.Foreign {
	table(rows) catch { case go.Error as e: "err " + e.Text }
}

func main() needs Console | go.Foreign {
`

// TestImportGoRuns is IG3's acceptance (#202 Level 1): one program over
// net/url, regexp, strconv, strings, path/filepath and text/tabwriter,
// seeds × rounds of generated inputs (URLs with odd escapes, user info and
// ports; patterns that compile or not; numbers that parse or not; table
// rows), each printed line equal to Go's own result computed here.
//
// It builds and runs the program because the subject is the emitted Go:
// the Go imports, the results bound at their Go types, the nil-to-None of a
// nullable field, the spread of a variadic, the tuple of two results and
// the Go interface argument; the checker sees only the Ergo program.
func TestImportGoRuns(t *testing.T) {
	var src strings.Builder
	src.WriteString(importGoProgram)
	var want []string
	for _, seed := range importGoSeeds(1, 2) {
		r := rand.New(rand.NewPCG(seed, 0x1203))
		for range 30 {
			switch r.IntN(5) {
			case 0:
				raw := importGoURL(r)
				fmt.Fprintf(&src, "\tConsole.Println(urlLine(%s))\n", strconv.Quote(raw))
				want = append(want, goURLLine(raw))
			case 1:
				p := []string{"a+", "[0-9]+", "(x|y)*z", "[", "a{2,}", `\d+`, "^h", "(?i)H.", "a(b"}[r.IntN(9)]
				s := importGoText(r, "ahxyz019 H")
				fmt.Fprintf(&src, "\tConsole.Println(reLine(%s, %s))\n", strconv.Quote(p), strconv.Quote(s))
				want = append(want, goRELine(p, s))
			case 2:
				s := []string{"42", "-7", "x", "", "9223372036854775807", "99999999999999999999", " 1", "0x10", "+3"}[r.IntN(9)]
				fmt.Fprintf(&src, "\tConsole.Println(numLine(%s))\n", strconv.Quote(s))
				want = append(want, goNumLine(s))
			case 3:
				parts := make([]string, 1+r.IntN(4))
				for i := range parts {
					parts[i] = []string{"a", "b", "..", "", "/c", "d/"}[r.IntN(6)]
				}
				fmt.Fprintf(&src, "\tConsole.Println(pathLine(%s))\n", importGoStrings(parts))
				after, found := strings.CutPrefix(filepath.Join(parts...), "a/")
				want = append(want, after+"|"+strconv.FormatBool(found))
			default:
				rows := make([]string, 1+r.IntN(3))
				for i := range rows {
					rows[i] = importGoText(r, "ab") + "\t" + importGoText(r, "cde") + "\n"
				}
				fmt.Fprintf(&src, "\tConsole.Println(tableLine(%s))\n", importGoStrings(rows))
				want = append(want, goTableLine(rows))
			}
		}
	}
	src.WriteString("}\n")
	prog := mustLower(t, "import_go_runs", src.String())
	// A call into the reviewed pure tier is transparent (owner q35): the
	// function that makes only such calls takes no environment and does
	// not poll, as hand Go; a foreign call's function does.
	if main := prog.mainGo(); !strings.Contains(main, "func quoted(s string) string {") || !strings.Contains(main, "func urlParts(env *Env, raw string) (string, error) {") {
		t.Errorf("pure-tier transparency: quoted or urlParts has the wrong environment:\n%s", main)
	}
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["import_go_runs"])
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

// importGoURL is a URL that may parse: a scheme, user info, a host with a
// port or an escape, a path with odd escapes, a query.
func importGoURL(r *rand.Rand) string {
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	return pick("http://", "https://", "ftp://", "", "mailto:", "::") +
		pick("", "u@", "u:p@", "%zz@") +
		pick("example.com", "a.b:8080", "[::1]:9", "h%41st", "x:y:z", "") +
		pick("", "/", "/a%20b", "/%zz", "/a b", "/%2F/c", "/é") +
		pick("", "?q=1&q=2", "?q=%41", "?q=a+b", "?%", "#f")
}

// importGoText is a short text over alphabet.
func importGoText(r *rand.Rand, alphabet string) string {
	b := make([]byte, r.IntN(6))
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(b)
}

// importGoStrings is the Ergo literal of a []string.
func importGoStrings(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

func goURLLine(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "err " + err.Error()
	}
	user := "-"
	if u.User != nil {
		user = u.User.Username()
	}
	return strings.Join([]string{u.Scheme, u.Hostname(), u.Port(), u.Path, u.RawQuery, u.Query().Get("q"), user, u.String()}, "|")
}

func goRELine(p, s string) string {
	re, err := regexp.Compile(p)
	if err != nil {
		return "err " + err.Error()
	}
	return strconv.FormatBool(re.MatchString(s)) + "|" + strings.Join(re.FindAllString(s, -1), ",") + "|" + re.ReplaceAllString(s, "<$0>")
}

func goNumLine(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return "err " + err.Error()
	}
	return strconv.Itoa(n*2) + "|" + strconv.FormatInt(int64(n), 36) + "|" + strconv.Quote(s)
}

func goTableLine(rows []string) string {
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 8, 1, ' ', 0)
	for _, r := range rows {
		_, _ = w.Write([]byte(r))
	}
	_ = w.Flush()
	return strings.ReplaceAll(b.String(), "\n", "/")
}

// TestHostCopyElisionAgrees is IG3's copy-elision property (the value rule
// at the Go boundary, q26): generated programs pass slices to Go functions
// that mutate (sort.Ints), retain (bytes.NewBuffer, whose Buffer reads the
// slice later) and only read (strings.Join) them, with the argument fresh,
// live and read after, written after, or dead. The elided lowering and the
// copy-always reference (SetHostCopyAlwaysForTest) print the same lines,
// and both equal the value-semantics model computed here: Go never changes
// what an Ergo binding holds, and an Ergo write never reaches Go. A failing
// seed prints its replay flag and the program.
//
// It builds and runs both programs because the subject is which operand the
// emitted Go copies; only a run shows an alias.
func TestHostCopyElisionAgrees(t *testing.T) {
	for _, seed := range importGoSeeds(1, 2, 3) {
		r := rand.New(rand.NewPCG(seed, 0x51ce))
		var body strings.Builder
		var want []string
		var calls []string
		for k := range 12 {
			line, model := copyElisionCase(r, k)
			body.WriteString(line)
			calls = append(calls, fmt.Sprintf("\tConsole.Println(case%d())\n", k))
			want = append(want, model)
		}
		var src strings.Builder
		for _, pkg := range []string{"sort", "bytes", "strings", "regexp"} {
			if strings.Contains(body.String(), pkg+".") {
				src.WriteString("import go " + pkg + "\n")
			}
		}
		src.WriteString("\n" + body.String() + "func main() needs Console | go.Foreign {\n" + strings.Join(calls, "") + "}\n")
		elided := mustLower(t, fmt.Sprintf("elide_%d", seed), src.String())
		restore := SetHostCopyAlwaysForTest(true)
		always := mustLower(t, fmt.Sprintf("always_%d", seed), src.String())
		restore()
		// The reference copies every operand: more copies than the rule
		// whenever a case hands one over (a dead local or a literal).
		clones := func(o *lowered) int {
			n := 0
			for _, f := range o.files {
				n += bytes.Count(f.Data, []byte(".Clone("))
			}
			return n
		}
		if handsOver := strings.Contains(src.String(), "\"done\"") || strings.Contains(src.String(), "case Some as buf: buf"); handsOver && clones(always) <= clones(elided) {
			t.Fatalf("seed %d: the copy-always lowering copies %d operands, the rule %d", seed, clones(always), clones(elided))
		}
		exes := buildAll(t, []*lowered{elided, always}, true)
		if exes == nil {
			return
		}
		for _, name := range []string{elided.name, always.name} {
			stdout, stderr, code := runExe(t, exes[name])
			if code != 0 {
				t.Fatalf("%s: exit %d, stderr %s\n%s", name, code, firstLines(stderr, 8), src.String())
			}
			got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
			if !slices.Equal(got, want) {
				t.Fatalf("%s (replay with -importgo.seed=%d):\ngot  %q\nwant %q\n%s", name, seed, got, want, src.String())
			}
		}
	}
}

// copyElisionCase is the Ergo function case<k> and the line the value
// model says it prints.
func copyElisionCase(r *rand.Rand, k int) (string, string) {
	a, b, c := r.IntN(9), r.IntN(9), r.IntN(9)
	xs := fmt.Sprintf("[]int{%d, %d, %d}", a, b, c)
	orig := fmt.Sprintf("%d,%d,%d", a, b, c)
	sorted := []int{a, b, c}
	slices.Sort(sorted)
	head := fmt.Sprintf("func case%d() string needs go.Foreign {\n", k)
	switch r.IntN(7) {
	case 0: // mutate, read after: Ergo's xs is unchanged
		return head + "\tlet xs = " + xs + "\n\tsort.Ints(xs)\n\t\"\\(xs[0]),\\(xs[1]),\\(xs[2])\"\n}\n\n", orig
	case 1: // mutate a let mut, write after, read after
		return head + "\tlet mut xs = " + xs + "\n\tsort.Ints(xs)\n\txs[0] = 9\n\t\"\\(xs[0]),\\(xs[1]),\\(xs[2])\"\n}\n\n", fmt.Sprintf("9,%d,%d", b, c)
	case 2: // mutate a dead local, or a fresh literal: nothing to see
		if r.IntN(2) == 0 {
			return head + "\tlet xs = " + xs + "\n\tsort.Ints(xs)\n\t\"done\"\n}\n\n", "done"
		}
		return head + "\tsort.Ints(" + xs + ")\n\t\"done\"\n}\n\n", "done"
	case 3: // Go retains the slice; Ergo writes it after the call
		bs := []byte{byte('a' + a), byte('a' + b)}
		return head + fmt.Sprintf("\tlet mut bs = []byte{%d, %d}\n", bs[0], bs[1]) +
			"\tmatch bytes.NewBuffer(bs) {\n\t\tcase Some as buf:\n\t\t\tbs[0] = 122\n\t\t\t\"\\(buf.Value.String())|\\(bs[0])\"\n\t\tcase None: \"none\"\n\t}\n}\n\n", string(bs) + "|122"
	case 4: // Go retains a dead slice: handed over, nothing to see
		bs := []byte{byte('a' + a), byte('a' + c)}
		return head + fmt.Sprintf("\tlet bs = []byte{%d, %d}\n", bs[0], bs[1]) +
			"\tmatch bytes.NewBuffer(bs) {\n\t\tcase Some as buf: buf.Value.String()\n\t\tcase None: \"none\"\n\t}\n}\n\n", string(bs)
	case 5: // a Go result that shares Go's state (SubexpNames): a write to it never reaches Go
		return head + "\tmatch regexp.MustCompile(\"(?P<n" + strconv.Itoa(a) + ">x)\") {\n\t\tcase Some as re:\n\t\t\tlet mut names = re.Value.SubexpNames()\n\t\t\tnames[1] = \"z\"\n\t\t\tlet again = re.Value.SubexpNames()\n\t\t\t\"\\(names[1])|\\(again[1])\"\n\t\tcase None: \"none\"\n\t}\n}\n\n", "z|n" + strconv.Itoa(a)
	default: // Go only reads; the slice is read after
		return head + "\tlet ss = []string{\"" + strconv.Itoa(a) + "\", \"" + strconv.Itoa(c) + "\"}\n\tlet j = strings.Join(ss, \"-\")\n\t\"\\(j)|\\(ss[1])\"\n}\n\n", fmt.Sprintf("%d-%d|%d", a, c, c)
	}
}

// TestImportGoDefects: a Go panic in an imported call, and a (*S, error)
// result that is nil with a nil error, are defects whose report names the
// Ergo call site. net/http's ProxyFromEnvironment returns nil, nil when no
// proxy is set (its documentation), which its binding types go.Ref[url.URL]
// fails go.Error, so the call is the defect. It builds and runs because
// the report is the running program's.
func TestImportGoDefects(t *testing.T) {
	panics := mustLower(t, "go_panic", `import go strings

func repeat(s string, n int) string needs go.Foreign {
	strings.Repeat(s, n)
}

func main() needs Console | go.Foreign {
	Console.Println(repeat("x", 2))
	Console.Println(repeat("x", -1))
}
`)
	nilRef := mustLower(t, "go_nil", `import go net/http
import go bufio
import go strings

func proxy(req string) string fails go.Error needs go.Foreign {
	match strings.NewReader(req) {
		case Some as sr:
			match bufio.NewReader(sr.Value) {
				case Some as br:
					let r = try http.ReadRequest(br.Value)
					let p = try http.ProxyFromEnvironment(r)
					p.Host
				case None: "no bufio"
			}
		case None: "no reader"
	}
}

func main() needs Console | go.Foreign {
	Console.Println(proxy("GET http://example.com/ HTTP/1.0\r\n\r\n") catch { case go.Error as e: e.Text })
}
`)
	exes := buildAll(t, []*lowered{panics, nilRef}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["go_panic"])
	if code == 0 || stdout != "xx\n" || !strings.Contains(stderr, "negative Repeat count") || !strings.Contains(stderr, "repeat at /ergo-test/main.ergo:4") {
		t.Errorf("go panic: exit %d, stdout %q, stderr:\n%s", code, stdout, firstLines(stderr, 12))
	}
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	stdout, stderr, code = runExe(t, exes["go_nil"])
	if code == 0 || stdout != "" || !strings.Contains(stderr, "Go returned nil without an error at main.ergo:11:18") || !strings.Contains(stderr, "net/http.ProxyFromEnvironment") {
		t.Errorf("nil go.Ref: exit %d, stdout %q, stderr:\n%s", code, stdout, firstLines(stderr, 12))
	}
}
