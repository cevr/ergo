package lower

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
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
import go math
import go encoding/base64
import go hash/crc32
import go time
import go os
import go syscall

// A Go struct whose fields differ by platform binds its common fields
// (GBX5: syscall.SysProcAttr), so os.ProcAttr's Sys maps, and
// os.StartProcess borrows the ProcAttr for the call. go.New(v) sets
// Setpgid at construction from go.Zero's base (GBX5b): the child leads
// its own process group, so a signal 0 to the group of its pid reaches
// it, and the shell exits 3.
func goProcess() string fails go.Error needs go.Foreign {
	let sys = go.New(syscall.SysProcAttr{..go.Zero[syscall.SysProcAttr](), Setpgid: true})
	let mut attr = os.ProcAttr{Dir: "", Env: []string{}, Sys: Some{Value: sys}}
	let p = try os.StartProcess("/bin/sh", []string{"sh", "-c", "kill -s 0 -- -$$ && exit 3"}, &attr)
	match try p.Wait() {
		case Some as state: "\(state.Value.ExitCode())|\(sys.Setpgid)"
		case None: "no state"
	}
}

// time.Duration's methods (GBX4): functions of their receiver, called and
// passed as a value, both Go's method expression.
func goDurations(d duration) string needs go.Foreign {
	let text = time.DurationString
	"\(text(d))|\(time.DurationSeconds(d))|\(time.DurationMilliseconds(d))|\(text(time.DurationRound(d, 1s)))|\(text(time.DurationTruncate(d, 1m)))|\(text(time.DurationAbs(-d)))"
}

// Go's read-only package variables (GBX3): a go.Ref that is never nil, and
// a slice whose read is a copy, so an Ergo write never reaches Go's.
func goVars() string needs go.Foreign {
	let mut args = os.Args
	args[0] = "x"
	let kept = if os.Args[0] == "x" { "aliased" } else { "kept" }
	"\(base64.StdEncoding.EncodeToString([]byte{104, 105}))|\(base64.RawURLEncoding.EncodeToString([]byte{251, 255}))|\(crc32.Checksum([]byte{104, 105}, crc32.IEEETable))|\(time.Unix(0, 0).In(time.UTC).Year())|\(len(os.Args))|\(kept)"
}

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
// It builds Go because the subject is the emitted Go:
// the Go imports, the results bound at their Go types, the nil-to-None of a
// nullable field, the spread of a variadic, the tuple of two results and
// the Go interface argument; the checker sees only the Ergo program.
func TestImportGoRuns(t *testing.T) {
	needsBuild(t)
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
	// Go constants (typed as the binding says) and an error variable.
	src.WriteString("\tConsole.Println(\"\\(math.MaxInt8 + 1)|\\(io.EOF.Text)|\\(strconv.IntSize)\")\n")
	want = append(want, fmt.Sprintf("%d|%s|%d", math.MaxInt8+1, io.EOF.Error(), strconv.IntSize))
	src.WriteString("\tConsole.Println(goProcess() catch { case go.Error as e: e.Text })\n")
	want = append(want, "3|true")
	src.WriteString("\tConsole.Println(goDurations(90s + 500ms))\n")
	d := 90*time.Second + 500*time.Millisecond
	want = append(want, fmt.Sprintf("%s|%v|%d|%s|%s|%s", d, d.Seconds(), d.Milliseconds(), d.Round(time.Second), d.Truncate(time.Minute), (-d).Abs()))
	src.WriteString("\tConsole.Println(goVars())\n")
	want = append(want, fmt.Sprintf("%s|%s|%d|1970|1|kept", base64.StdEncoding.EncodeToString([]byte("hi")), base64.RawURLEncoding.EncodeToString([]byte{251, 255}), crc32.ChecksumIEEE([]byte("hi"))))
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
// that write them in place (sort.Ints, slices.Sort, slices.Reverse, whose
// value is the slice Go wrote: GBX2), retain (bytes.NewBuffer, whose Buffer reads the
// slice later) and only read (strings.Join) them, with the argument fresh,
// live and read after, written after, or dead. The elided lowering and the
// copy-always reference (SetHostCopyAlwaysForTest) print the same lines,
// and both equal the value-semantics model computed here: Go never changes
// what an Ergo binding holds, and an Ergo write never reaches Go. A failing
// seed prints its replay flag and the program. Mutant it alone catches
// (lower mutants.json): gbx2-lower-written.
//
// It builds Go because the subject is which operand the
// emitted Go copies; only a run shows an alias.
func TestHostCopyElisionAgrees(t *testing.T) {
	needsBuild(t)
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
		for _, pkg := range []string{"sort", "slices", "bytes", "strings", "regexp"} {
			if strings.Contains(body.String(), pkg+".") {
				src.WriteString("import go " + pkg + "\n")
			}
		}
		if strings.Contains(body.String(), "task.Go") {
			src.WriteString("import task\n")
		}
		src.WriteString("\ntype Index { keys []int }\n")
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
		if handsOver := strings.Contains(src.String(), "// handover") || strings.Contains(src.String(), "case Some as buf: buf"); handsOver && clones(always) <= clones(elided) {
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
	hostReadsFalseFact(t)
}

// copyElisionCase is the Ergo function case<k> and the line the value
// model says it prints.
func copyElisionCase(r *rand.Rand, k int) (string, string) {
	a, b, c := r.IntN(9), r.IntN(9), r.IntN(9)
	xs := fmt.Sprintf("[]int{%d, %d, %d}", a, b, c)
	orig := fmt.Sprintf("%d,%d,%d", a, b, c)
	// A Go function that writes its slice in place (GBX2): its value is
	// the slice Go wrote; the operand Ergo holds is unchanged.
	writer, written := "sort.Ints", []int{a, b, c}
	switch r.IntN(3) {
	case 0:
		slices.Sort(written)
	case 1:
		writer = "slices.Sort"
		slices.Sort(written)
	default:
		writer = "slices.Reverse"
		slices.Reverse(written)
	}
	wrote := fmt.Sprintf("%d,%d,%d", written[0], written[1], written[2])
	show := func(v string) string { return "\\(" + v + "[0]),\\(" + v + "[1]),\\(" + v + "[2])" }
	sorted := []int{a, b, c}
	slices.Sort(sorted)
	head := fmt.Sprintf("func case%d() string needs go.Foreign {\n", k)
	// The reads shapes (LF1): sort.SearchInts and bytes.IndexFunc only
	// read their slice (a checked fact), so the run build passes a live
	// slice with no copy where nothing else can reach it.
	q := r.IntN(9)
	at := sort.SearchInts(sorted, q)
	ss := fmt.Sprintf("[]int{%d, %d, %d}", sorted[0], sorted[1], sorted[2])
	switch r.IntN(12) {
	case 7: // a local read after the call
		return head + "\tlet xs = " + ss + fmt.Sprintf("\n\tlet i = sort.SearchInts(xs, %d)\n", q) + "\t\"\\(i)|\\(xs[0]),\\(xs[2])\"\n}\n\n", fmt.Sprintf("%d|%d,%d", at, sorted[0], sorted[2])
	case 8: // a value another binding shares
		return head + "\tlet xs = " + ss + "\n\tlet ys = xs" + fmt.Sprintf("\n\tlet i = sort.SearchInts(ys, %d)\n", q) + "\t\"\\(i)|\\(xs[1])|\\(ys[2])\"\n}\n\n", fmt.Sprintf("%d|%d|%d", at, sorted[1], sorted[2])
	case 9: // a field
		return head + "\tlet ix = Index{keys: " + ss + fmt.Sprintf("}\n\tlet i = sort.SearchInts(ix.keys, %d)\n", q) + "\t\"\\(i)|\\(ix.keys[1])\"\n}\n\n", fmt.Sprintf("%d|%d", at, sorted[1])
	case 10: // a task captures the place
		return head + "\tlet xs = " + ss + fmt.Sprintf("\n\tlet t = task.Go() { sort.SearchInts(xs, %d) }\n", q) + "\tlet i = t.Wait()\n\t\"\\(i)|\\(xs[0])\"\n}\n\n", fmt.Sprintf("%d|%d", at, sorted[0])
	case 11: // a callback that writes the same place: Go scans the slice as it was
		bs := []byte{byte('a' + a), byte('a' + b), byte('a' + c)}
		return head + fmt.Sprintf("\tlet mut bs = []byte{%d, %d, %d}\n", bs[0], bs[1], bs[2]) +
			"\tlet i = bytes.IndexFunc(bs, func(r rune) bool {\n\t\tbs[1] = 122\n\t\tr == 122\n\t})\n\t\"\\(i)|\\(bs[1])\"\n}\n\n", "-1|122"
	}
	switch r.IntN(7) {
	case 0: // Go writes a copy, read after: Ergo's xs is unchanged
		return head + "\tlet xs = " + xs + "\n\tlet ys = " + writer + "(xs)\n\t\"" + show("xs") + "|" + show("ys") + "\"\n}\n\n", orig + "|" + wrote
	case 1: // a let mut, written after: the write reaches neither
		return head + "\tlet mut xs = " + xs + "\n\tlet ys = " + writer + "(xs)\n\txs[0] = 9\n\t\"" + show("xs") + "|" + show("ys") + "\"\n}\n\n", fmt.Sprintf("9,%d,%d|%s", b, c, wrote)
	case 2: // a dead local, or a fresh literal: handed over, written in place
		if r.IntN(2) == 0 {
			return head + "\tlet xs = " + xs + "\n\tlet ys = " + writer + "(xs) // handover\n\t\"" + show("ys") + "\"\n}\n\n", wrote
		}
		return head + "\tlet ys = " + writer + "(" + xs + ") // handover\n\t\"" + show("ys") + "\"\n}\n\n", wrote
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

// TestImportGoDefects: a Go panic in an imported call is a defect whose
// report names the Ergo call site. net/http's ProxyFromEnvironment returns
// nil, nil when no proxy is set (its documentation): its binding is
// Option[go.Ref[url.URL]] fails go.Error, since ergo bind go cannot prove
// it non-nil (GOBIND-NIL), so the program reads None, where the old
// go.Ref binding was a defect. It builds Go because the report and the
// None are the running program's.
func TestImportGoDefects(t *testing.T) {
	needsBuild(t)
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
					match try http.ProxyFromEnvironment(r) {
						case Some as p: p.Value.Host
						case None: "no proxy"
					}
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
	if code != 0 || stdout != "no proxy\n" {
		t.Errorf("nil, nil from Go: exit %d, stdout %q, want \"no proxy\"; stderr:\n%s", code, stdout, firstLines(stderr, 12))
	}
}

// TestImportGoHeaderWriteReachesGo is G2's acceptance (H4, H5): Go's own
// Map (http.ResponseWriter.Header's result, http.Request's Header field
// through a go.Ref) that only Go calls take goes on to them with no copy,
// directly or through a let, so each Set reaches Go; the promised fact of
// the lowered Go is that no copy wraps it in handle or stamp. A let that
// the program also reads as an Ergo value (kept) is a copy, which Go's
// later write does not reach. It builds Go because the subject is what
// Go's recorder and request hold after the emitted calls; the checker sees
// only Ergo.
func TestImportGoHeaderWriteReachesGo(t *testing.T) {
	needsBuild(t)
	prog := mustLower(t, "go_header", `import go net/http
import go net/http/httptest
import go strings

func handle(w http.ResponseWriter) needs go.Foreign {
	w.Header().Set("Content-Type", "application/json")
	let h = w.Header()
	h.Set("X-Bound", "bound")
	w.WriteHeader(201)
}

func stamp(r go.Ref[http.Request]) needs go.Foreign {
	r.Header.Set("X-Field", "field")
}

func kept(w http.ResponseWriter) int needs go.Foreign {
	let h = w.Header()
	let before = len(h)
	w.Header().Set("X-Later", "later")
	len(h) - before
}

func main() needs Console | go.Foreign {
	match httptest.NewRecorder() {
	case Some as rec:
		handle(rec.Value)
		let h = rec.Value.Header()
		Console.Println(h.Get("Content-Type") + " " + h.Get("X-Bound") + " \(kept(rec.Value))")
	case None:
	}
	match strings.NewReader("") {
	case Some as body:
		match httptest.NewRequest("GET", "/", body.Value) {
		case Some as r:
			stamp(r.Value)
			Console.Println(r.Value.Header.Get("X-Field"))
		case None:
		}
	case None:
	}
}
`)
	for _, name := range []string{"handle", "stamp"} {
		if body := goFunc(t, prog.mainGo(), name); strings.Contains(body, "Clone") {
			t.Errorf("a copy wraps Go's Header in %s:\n%s", name, body)
		}
	}
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["go_header"])
	if code != 0 || stdout != "application/json bound 0\nfield\n" {
		t.Errorf("the recorder's and the request's headers: exit %d, stdout %q, stderr:\n%s", code, stdout, firstLines(stderr, 12))
	}
}

// TestPureGoCallCopiesALiveLet is the smallest repro of fu-elide1 (case
// elide_1 of TestHostCopyElisionAgrees, -importgo.seed=1, red since V7
// d302050c8): sort.Ints is in the reviewed pure Go tier, so the value walk
// filed its call as a pure std body (a V7 hand-over candidate) and never
// as a Go call, and its live let went to Go uncopied: Go sorted Ergo's xs
// in place. The one promised fact (interop.md Lowering, the value rule at
// the Go boundary, q26): the operand is copied. Mutant
// values-go-call-filed-as-pure (the pure std branch before the Go call
// branch in valueWalk.walk) passes it uncopied.
func TestPureGoCallCopiesALiveLet(t *testing.T) {
	prog := mustLower(t, "pure_go_live", `import go sort

func sorted() string needs go.Foreign {
	let xs = []int{8, 2, 5}
	let _ = sort.Ints(xs)
	"\(xs[0]),\(xs[1]),\(xs[2])"
}

func main() needs Console | go.Foreign {
	Console.Println(sorted())
}
`)
	body := goFunc(t, prog.mainGo(), "sorted")
	call := regexp.MustCompile(`sort\.Ints\(([^\n]*)\)`).FindStringSubmatch(body)
	operand := ""
	if call != nil {
		operand = call[1]
		// sort.Ints writes its slice (GBX2): Go gets the temporary that
		// holds the value rule's operand, and the call's value is it.
		if m := regexp.MustCompile(`\b` + regexp.QuoteMeta(operand) + ` := ([^\n]*)`).FindStringSubmatch(body); m != nil {
			operand = m[1]
		}
	}
	if !strings.Contains(operand, "Clone(") {
		t.Errorf("sort.Ints takes the live let xs uncopied:\n%s", body)
	}
}
