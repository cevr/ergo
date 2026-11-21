// Command control is the hand Go control of TPROBE's built kind: the least
// Go that gives TestJSONParseAgreesWithControl's verdicts on the same
// subject (the linked examples/jsonparse binary, the file jsonparse beside
// this command, which the form links with `ergo build` first). It runs the
// binary on every JSONTestSuite file, 200 seeded documents (each again
// with bytes changed), and the `gen` workload at two sizes, for min, check,
// fmt and get, over the pointer tree and the flat tree (-flat), and
// compares each run with a reference on encoding/json, as the test does.
// The document lives beside the command, as p.json. It prints the first
// disagreement as a FAIL line (exit 1), or "ok N
// runs" (exit 0). Run it from the repository root.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var (
	bin  string
	path string
	runs int
)

func main() {
	self, err := os.Executable()
	if err != nil {
		fail("%v", err)
	}
	bin = filepath.Join(filepath.Dir(self), "jsonparse")
	path = filepath.Join(filepath.Dir(self), "p.json")

	suite, _ := filepath.Glob(filepath.Join("examples", "jsonparse", "agree", "deep", "JSONTestSuite-*", "test_parsing", "*.json"))
	if len(suite) == 0 {
		fail("JSONTestSuite is not in examples/jsonparse/agree/deep")
	}
	for _, file := range suite {
		data, err := os.ReadFile(file)
		if err != nil {
			fail("%v", err)
		}
		name := filepath.Base(file)
		ref := reference(data)
		if (strings.HasPrefix(name, "y_") && ref.bad >= 0) || (strings.HasPrefix(name, "n_") && ref.bad < 0) {
			fail("%s: encoding/json disagrees with the suite's verdict", name)
		}
		write(string(data))
		for _, cmd := range []string{"min", "check"} {
			agree(ref, name, []string{cmd})
		}
	}

	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x150))
		doc := genDoc(r, seed)
		what := "seed " + strconv.FormatUint(seed, 10)
		ref := reference([]byte(doc))
		write(doc)
		if ref.bad >= 0 {
			if !strings.HasPrefix(doc, "[[[") {
				fail("%s: the generator wrote a text encoding/json rejects at %d", what, ref.bad)
			}
			agree(ref, what+", nested too deep", []string{"min"})
			continue
		}
		for _, cmd := range []string{"min", "check"} {
			agree(ref, what, []string{cmd})
		}
		if depth(ref.v) > deep {
			continue
		}
		agree(ref, what, []string{"fmt"})
		for _, ptr := range pointers(r, ref.v) {
			agree(ref, what, []string{"get", ptr})
		}
		broken := breakBytes(r, doc)
		bref := reference([]byte(broken))
		write(broken)
		agree(bref, what+", with bytes changed", []string{"min"})
	}

	for _, n := range []string{"2", "300"} {
		var out bytes.Buffer
		gen := exec.Command(bin, "gen", "-seed", "1", "-n", n)
		gen.Stdout = &out
		if err := gen.Run(); err != nil {
			fail("jsonparse gen: %v", err)
		}
		ref := reference(out.Bytes())
		if ref.bad >= 0 {
			fail("the workload of %s records is not JSON at byte %d", n, ref.bad)
		}
		write(out.String())
		for _, cmd := range []string{"min", "check"} {
			agree(ref, "the workload of "+n+" records", []string{cmd})
		}
	}
	fmt.Printf("ok %d runs\n", runs)
}

func fail(format string, args ...any) {
	fmt.Printf("FAIL "+format+"\n", args...)
	os.Exit(1)
}

func write(text string) {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		fail("%v", err)
	}
}

// agree runs args on the document over both trees and fails unless each
// prints what ref says, and both print the same.
func agree(ref jsonRef, what string, args []string) {
	want := ref.outcome(args, filepath.Base(path))
	flat := append([]string{args[0], "-flat"}, args[1:]...)
	var first string
	for i, run := range [][]string{args, flat} {
		got := runBin(run)
		if !want.match(got) || (i > 0 && got != first) {
			fail("%s: jsonparse %s printed %q, the reference wants %s", what, strings.Join(run, " "), got, want)
		}
		first = got
	}
}

// runBin runs the binary on the document, named by its base in its own
// folder, and is its stdout, stderr and exit status as one text.
func runBin(args []string) string {
	runs++
	cmd := exec.Command(bin, append(slices.Clone(args), filepath.Base(path))...)
	cmd.Dir = filepath.Dir(path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			fail("jsonparse: %v", err)
		}
		code = exit.ExitCode()
	}
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout.String(), stderr.String(), code)
}

// ---- the reference (cmd/ergo/jsonparse_test.go's) ----

type value struct {
	kind  byte
	text  string
	items []*value
	keys  []string
}

type jsonRef struct {
	src []byte
	v   *value
	bad int
}

func reference(src []byte) jsonRef {
	if fits(src) != "" {
		lo, hi := 0, len(src)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(src[:mid]) != "bad" {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return jsonRef{src: src, bad: lo}
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	return jsonRef{src: src, v: tokens(dec), bad: -1}
}

func fits(text []byte) string {
	var raw json.RawMessage
	err := json.Unmarshal(text, &raw)
	var syntax *json.SyntaxError
	switch {
	case err == nil:
		return ""
	case !errors.As(err, &syntax):
		panic(err)
	case syntax.Error() == "unexpected end of JSON input":
		return "short"
	}
	return "bad"
}

func tokens(dec *json.Decoder) *value {
	tok, err := dec.Token()
	if err != nil {
		panic(err)
	}
	switch x := tok.(type) {
	case json.Delim:
		v := &value{kind: byte(x)}
		for dec.More() {
			if x == '{' {
				key, err := dec.Token()
				if err != nil {
					panic(err)
				}
				v.keys = append(v.keys, key.(string))
			}
			v.items = append(v.items, tokens(dec))
		}
		if _, err := dec.Token(); err != nil {
			panic(err)
		}
		return v
	case nil:
		return &value{kind: 'n', text: "null"}
	case bool:
		return &value{kind: 'b', text: strconv.FormatBool(x)}
	case json.Number:
		return &value{kind: '#', text: string(x)}
	case string:
		return &value{kind: 's', text: x}
	}
	panic(fmt.Sprintf("token %T", tok))
}

// expected is what a run must print, compared byte for byte (a regexp
// reads an invalid UTF-8 byte as U+FFFD and cannot tell the two apart):
// the whole text, or with reason a prefix, then one line of the
// program's own reason and the failure's end.
type expected struct {
	text   string
	reason bool
}

func (e expected) match(got string) bool {
	if !e.reason {
		return got == e.text
	}
	rest, ok := strings.CutPrefix(got, e.text)
	why, ok2 := strings.CutSuffix(rest, "\n\nstatus 1\n")
	return ok && ok2 && why != "" && !strings.Contains(why, "\n")
}

func (e expected) String() string { return fmt.Sprintf("%q (reason %v)", e.text, e.reason) }

func (r jsonRef) outcome(args []string, file string) expected {
	exact := func(out string) expected {
		return expected{text: "stdout:\n" + out + "\nstderr:\n\nstatus 0\n"}
	}
	failure := func(line string) expected {
		return expected{text: "stdout:\n\nstderr:\njsonparse: " + line + "\n\nstatus 1\n"}
	}
	if r.bad >= 0 {
		line, start := 1, 0
		for i := 0; i < r.bad; i++ {
			if r.src[i] == '\n' {
				line, start = line+1, i+1
			}
		}
		at := fmt.Sprintf("%s:%d:%d: ", file, line, r.bad-start+1)
		return expected{text: "stdout:\n\nstderr:\njsonparse: " + at, reason: true}
	}
	switch args[0] {
	case "min":
		return exact(compact(r.v) + "\n")
	case "fmt":
		return exact(indent(r.v, "") + "\n")
	case "check":
		return exact(fmt.Sprintf("values %d\ndepth %d\n", count(r.v), depth(r.v)))
	}
	ptr := args[1]
	if ptr != "" && ptr[0] != '/' {
		return failure(ptr + ": a pointer starts with '/'")
	}
	at := r.v
	if ptr != "" {
		for _, raw := range strings.Split(ptr[1:], "/") {
			tok, ok := unescape(raw)
			if !ok {
				return failure(ptr + ": a '~' is followed by 0 or 1")
			}
			if at == nil {
				continue
			}
			at = step(at, tok)
		}
	}
	if at == nil {
		return failure(file + ": no value at " + missing(r.v, ptr))
	}
	return exact(indent(at, "") + "\n")
}

func missing(v *value, ptr string) string {
	prefix := ""
	for _, raw := range strings.Split(ptr[1:], "/") {
		tok, _ := unescape(raw)
		prefix += "/" + raw
		if v = step(v, tok); v == nil {
			return prefix
		}
	}
	return prefix
}

func unescape(raw string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '~' {
			b.WriteByte(raw[i])
			continue
		}
		if i+1 >= len(raw) || (raw[i+1] != '0' && raw[i+1] != '1') {
			return "", false
		}
		b.WriteByte("~/"[raw[i+1]-'0'])
		i++
	}
	return b.String(), true
}

func step(v *value, tok string) *value {
	switch v.kind {
	case '{':
		for k := len(v.keys) - 1; k >= 0; k-- {
			if v.keys[k] == tok {
				return v.items[k]
			}
		}
	case '[':
		if tok == "0" || (tok != "" && tok[0] != '0' && len(tok) < 10 && strings.Trim(tok, "0123456789") == "") {
			if i, _ := strconv.Atoi(tok); i < len(v.items) {
				return v.items[i]
			}
		}
	}
	return nil
}

func quote(s string) string {
	short := map[byte]string{'"': `\"`, '\\': `\\`, '\n': `\n`, '\t': `\t`, '\r': `\r`, '\b': `\b`, '\f': `\f`}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if e, ok := short[c]; ok {
			b.WriteString(e)
		} else if c < 0x20 || c == 0x7f {
			fmt.Fprintf(&b, `\u%04x`, c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func compact(v *value) string {
	switch v.kind {
	case 's':
		return quote(v.text)
	case '[', '{':
		parts := make([]string, len(v.items))
		for i, x := range v.items {
			if v.kind == '{' {
				parts[i] = quote(v.keys[i]) + ":"
			}
			parts[i] += compact(x)
		}
		end := map[byte]string{'[': "]", '{': "}"}[v.kind]
		return string(v.kind) + strings.Join(parts, ",") + end
	}
	return v.text
}

func indent(v *value, pad string) string {
	if (v.kind != '[' && v.kind != '{') || len(v.items) == 0 {
		return compact(v)
	}
	inner := pad + "  "
	parts := make([]string, len(v.items))
	for i, x := range v.items {
		parts[i] = inner
		if v.kind == '{' {
			parts[i] += quote(v.keys[i]) + ": "
		}
		parts[i] += indent(x, inner)
	}
	end := map[byte]string{'[': "]", '{': "}"}[v.kind]
	return string(v.kind) + "\n" + strings.Join(parts, ",\n") + "\n" + pad + end
}

const deep = 50

func count(v *value) int {
	n := 1
	for _, x := range v.items {
		n += count(x)
	}
	return n
}

func depth(v *value) int {
	if v.kind != '[' && v.kind != '{' {
		return 0
	}
	d := 0
	for _, x := range v.items {
		d = max(d, depth(x))
	}
	return d + 1
}

// ---- the generator (cmd/ergo/jsonparse_test.go's) ----

var keys = []string{`a`, `b`, `a/b`, `m~n`, `~1`, ``, `0`, `1`, `é`}

func space(r *rand.Rand) string {
	if r.IntN(3) != 0 {
		return ""
	}
	var b strings.Builder
	for range 1 + r.IntN(3) {
		b.WriteByte(" \t\n\r"[r.IntN(4)])
	}
	return b.String()
}

func digits(r *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + r.IntN(10)))
	}
	return b.String()
}

func number(r *rand.Rand) string {
	var b strings.Builder
	if r.IntN(3) == 0 {
		b.WriteByte('-')
	}
	if r.IntN(3) == 0 {
		b.WriteByte('0')
	} else {
		b.WriteByte(byte('1' + r.IntN(9)))
		b.WriteString(digits(r, r.IntN(25)))
	}
	if r.IntN(3) == 0 {
		b.WriteString("." + digits(r, 1+r.IntN(5)))
	}
	if r.IntN(3) == 0 {
		b.WriteString([]string{"e", "E"}[r.IntN(2)] + []string{"", "+", "-"}[r.IntN(3)] + digits(r, 1+r.IntN(4)))
	}
	return b.String()
}

func str(r *rand.Rand) string {
	pieces := []func() string{
		func() string { return "plain" },
		func() string { return []string{`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`}[r.IntN(8)] },
		func() string { return fmt.Sprintf(`\u%04x`, r.IntN(0x80)) },
		func() string { return fmt.Sprintf(`\u%04X`, r.IntN(0x10000)) },
		func() string { return fmt.Sprintf(`\u%04x\u%04x`, 0xD800+r.IntN(0x400), 0xDC00+r.IntN(0x400)) },
		func() string { return fmt.Sprintf(`\u%04x`, 0xD800+r.IntN(0x800)) },
		func() string { return fmt.Sprintf(`\u%04x\u%04x`, 0xD800+r.IntN(0x400), 0xD800+r.IntN(0x400)) },
		func() string { return []string{"é", "☃", "😀", "\u2028"}[r.IntN(4)] },
		func() string { return []string{"\x80", "\xff", "\xe2\x98", "\xed\xa0\x80", "\x7f"}[r.IntN(5)] },
		func() string { return " ~/" },
	}
	var b strings.Builder
	b.WriteByte('"')
	for range r.IntN(5) {
		b.WriteString(pieces[r.IntN(len(pieces))]())
	}
	b.WriteByte('"')
	return b.String()
}

func genValue(r *rand.Rand, d int) string {
	n := r.IntN(8)
	if d <= 0 {
		n = r.IntN(5)
	}
	switch n {
	case 0:
		return []string{"null", "true", "false"}[r.IntN(3)]
	case 1, 2:
		return number(r)
	case 3, 4:
		return str(r)
	case 5:
		var parts []string
		for range r.IntN(4) {
			parts = append(parts, space(r)+genValue(r, d-1)+space(r))
		}
		return "[" + space(r) + strings.Join(parts, ",") + "]"
	default:
		var parts []string
		for range r.IntN(5) {
			key := quote(keys[r.IntN(len(keys))])
			parts = append(parts, space(r)+key+space(r)+":"+space(r)+genValue(r, d-1)+space(r))
		}
		return "{" + space(r) + strings.Join(parts, ",") + "}"
	}
}

func genDoc(r *rand.Rand, seed uint64) string {
	doc := space(r) + genValue(r, 5) + space(r)
	if seed%25 == 0 {
		k := 10000 - 1 - depth(reference([]byte(doc)).v) + r.IntN(3)
		doc = strings.Repeat("[", k) + doc + strings.Repeat("]", k)
	}
	return doc
}

func pointers(r *rand.Rand, v *value) []string {
	esc := strings.NewReplacer("~", "~0", "/", "~1")
	ptr := ""
	for at := v; (at.kind == '[' || at.kind == '{') && len(at.items) > 0 && r.IntN(4) != 0; {
		i := r.IntN(len(at.items))
		if at.kind == '[' {
			ptr += "/" + strconv.Itoa(i)
		} else {
			ptr += "/" + esc.Replace(at.keys[i])
			for k := range at.keys {
				if at.keys[k] == at.keys[i] {
					i = max(i, k)
				}
			}
		}
		at = at.items[i]
	}
	odd := []string{"x", "/a~2", "/~", "/01", "/-", "/99", "/a/b", "/", "//"}
	return []string{ptr, ptr + "/" + []string{"0", "a", "nope", "1"}[r.IntN(4)], odd[r.IntN(len(odd))]}
}

func breakBytes(r *rand.Rand, src string) string {
	b := []byte(src)
	marks := []byte("{}[]\",:\\u019eE.+-tfn \n\x80\x01")
	for range 1 + r.IntN(3) {
		at := r.IntN(len(b) + 1)
		switch r.IntN(3) {
		case 0:
			if at < len(b) {
				b = append(b[:at], b[at+1:]...)
			}
		case 1:
			b = append(b[:at], append([]byte{marks[r.IntN(len(marks))]}, b[at:]...)...)
		default:
			if at < len(b) {
				b[at] = marks[r.IntN(len(marks))]
			}
		}
	}
	return string(b)
}
