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
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// The reference and generator of examples/jsonparse's Go test, which
// TP-BUILT replaced by Ergo tests (examples/jsonparse/agree), stay for
// the tests that still use them: TestFlatTreeAgreesWithControl
// (flattree_test.go) and the corpus helpers of mdhtml and tomlparse.

// buildCorpusExample links examples/<name>/cmd/<name> with `ergo build`
// and returns the binary and the emitted module's directory.
func buildCorpusExample(t *testing.T, name string) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/"+name), "examples", name, "cmd", name)
	bin = filepath.Join(t.TempDir(), name)
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildHandControl links testdata/<name>/control in a module with the go
// line of the example's emitted module in work, so one toolchain compiles
// both. Every .go file of the folder is the control's.
func buildHandControl(t *testing.T, name, work string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	files, err := filepath.Glob(filepath.Join("testdata", name, "control", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no control in testdata/%s/control: %v", name, err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module control\n\n"+string(goLine)+"\n")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, filepath.Base(f)), string(src))
	}
	bin := filepath.Join(dir, "control")
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of the control: %v\n%s", err, out.Bytes())
	}
	return bin
}

// runCorpusArgs runs bin with args in the folder of the last argument, which
// it names by its base, and is its stdout, stderr and exit status as one
// text.
func runCorpusArgs(t *testing.T, bin string, args ...string) string {
	t.Helper()
	command := exec.Command(bin, args...)
	command.Dir = filepath.Dir(args[len(args)-1])
	command.Args[len(command.Args)-1] = filepath.Base(args[len(args)-1])
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", bin, err)
		}
		code = exit.ExitCode()
	}
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout.String(), stderr.String(), code)
}

// ---- the reference ----

// jsonValue is the reference's tree: kind is one of n, b, #, s, [, {;
// text is a number's text, a string's bytes or a bool's word.
type jsonValue struct {
	kind  byte
	text  string
	items []*jsonValue
	keys  []string
}

// jsonRef is the reference's reading of a text: its tree, or the offset
// of the first byte that is not JSON (bad), -1 when there is none.
type jsonRef struct {
	src []byte
	v   *jsonValue
	bad int
}

func jsonReference(src []byte) jsonRef {
	if jsonFits(src) != "" {
		// The first byte that is not JSON ends the longest prefix that some
		// JSON text starts with; that is, encoding/json reads the prefix and
		// wants more. Such prefixes are closed under prefix, so a binary
		// search finds the longest. (encoding/json's own SyntaxError offset
		// is not used: its meaning changed with Go's json v2.)
		lo, hi := 0, len(src) // src[:lo] fits; src[:hi+1] does not, or hi is len(src)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if jsonFits(src[:mid]) != "bad" {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return jsonRef{src: src, bad: lo}
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v := jsonTokens(dec)
	return jsonRef{src: src, v: v, bad: -1}
}

// jsonFits is "" when encoding/json accepts text, "short" when text is a
// part of a JSON text that ends too soon, and "bad" otherwise.
func jsonFits(text []byte) string {
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

func jsonTokens(dec *json.Decoder) *jsonValue {
	tok, err := dec.Token()
	if err != nil {
		panic(err)
	}
	switch x := tok.(type) {
	case json.Delim:
		v := &jsonValue{kind: byte(x)}
		for dec.More() {
			if x == '{' {
				key, err := dec.Token()
				if err != nil {
					panic(err)
				}
				v.keys = append(v.keys, key.(string))
			}
			v.items = append(v.items, jsonTokens(dec))
		}
		if _, err := dec.Token(); err != nil {
			panic(err)
		}
		return v
	case nil:
		return &jsonValue{kind: 'n', text: "null"}
	case bool:
		return &jsonValue{kind: 'b', text: strconv.FormatBool(x)}
	case json.Number:
		return &jsonValue{kind: '#', text: string(x)}
	case string:
		return &jsonValue{kind: 's', text: x}
	}
	panic(fmt.Sprintf("token %T", tok))
}

// outcome is what jsonparse args FILE must print, as a pattern: a failure's
// reason is the program's own, so only its position is fixed.
func (r jsonRef) outcome(args []string, file string) *regexp.Regexp {
	exact := func(out string) *regexp.Regexp {
		return regexp.MustCompile("^" + regexp.QuoteMeta("stdout:\n"+out+"\nstderr:\n\nstatus 0\n") + "$")
	}
	failure := func(line string) *regexp.Regexp {
		return regexp.MustCompile("^" + regexp.QuoteMeta("stdout:\n\nstderr:\njsonparse: "+line+"\n\nstatus 1\n") + "$")
	}
	if r.bad >= 0 {
		line, start := 1, 0
		for i := 0; i < r.bad; i++ {
			if r.src[i] == '\n' {
				line, start = line+1, i+1
			}
		}
		at := fmt.Sprintf("%s:%d:%d: ", file, line, r.bad-start+1)
		return regexp.MustCompile("^" + regexp.QuoteMeta("stdout:\n\nstderr:\njsonparse: "+at) + "[^\n]+\n\nstatus 1\n$")
	}
	switch args[0] {
	case "min":
		return exact(jsonCompact(r.v) + "\n")
	case "fmt":
		return exact(jsonIndent(r.v, "") + "\n")
	case "check":
		return exact(fmt.Sprintf("values %d\ndepth %d\n", jsonCount(r.v), jsonDepth(r.v)))
	}
	ptr := args[1]
	if ptr != "" && ptr[0] != '/' {
		return failure(ptr + ": a pointer starts with '/'")
	}
	at := r.v
	if ptr != "" {
		for _, raw := range strings.Split(ptr[1:], "/") {
			tok, ok := jsonUnescape(raw)
			if !ok {
				return failure(ptr + ": a '~' is followed by 0 or 1")
			}
			if at == nil {
				continue
			}
			at = jsonStep(at, tok)
		}
	}
	if at == nil {
		return failure(file + ": no value at " + jsonMissing(r.v, ptr))
	}
	return exact(jsonIndent(at, "") + "\n")
}

// jsonMissing is the prefix of ptr up to the first token that names
// nothing in v; ptr's tokens are all well formed.
func jsonMissing(v *jsonValue, ptr string) string {
	prefix := ""
	for _, raw := range strings.Split(ptr[1:], "/") {
		tok, _ := jsonUnescape(raw)
		prefix += "/" + raw
		if v = jsonStep(v, tok); v == nil {
			return prefix
		}
	}
	return prefix
}

func jsonUnescape(raw string) (string, bool) {
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

// jsonStep is the member or element tok names in v (the last member of a
// repeated key), or nil.
func jsonStep(v *jsonValue, tok string) *jsonValue {
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

// jsonQuote is s as jsonparse writes a string: ", \ and the bytes below
// 0x20 and 0x7f escaped (the short escapes where JSON has one), every
// other byte as it is.
func jsonQuote(s string) string {
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

func jsonCompact(v *jsonValue) string {
	switch v.kind {
	case 's':
		return jsonQuote(v.text)
	case '[', '{':
		parts := make([]string, len(v.items))
		for i, x := range v.items {
			if v.kind == '{' {
				parts[i] = jsonQuote(v.keys[i]) + ":"
			}
			parts[i] += jsonCompact(x)
		}
		end := map[byte]string{'[': "]", '{': "}"}[v.kind]
		return string(v.kind) + strings.Join(parts, ",") + end
	}
	return v.text
}

func jsonIndent(v *jsonValue, pad string) string {
	if (v.kind != '[' && v.kind != '{') || len(v.items) == 0 {
		return jsonCompact(v)
	}
	inner := pad + "  "
	parts := make([]string, len(v.items))
	for i, x := range v.items {
		parts[i] = inner
		if v.kind == '{' {
			parts[i] += jsonQuote(v.keys[i]) + ": "
		}
		parts[i] += jsonIndent(x, inner)
	}
	end := map[byte]string{'[': "]", '{': "}"}[v.kind]
	return string(v.kind) + "\n" + strings.Join(parts, ",\n") + "\n" + pad + end
}

// jsonDeep is how deep a generated document nests before the test runs
// only min and check on it: fmt's output grows with the square of the
// depth.
const jsonDeep = 50

func jsonCount(v *jsonValue) int {
	n := 1
	for _, x := range v.items {
		n += jsonCount(x)
	}
	return n
}

func jsonDepth(v *jsonValue) int {
	if v.kind != '[' && v.kind != '{' {
		return 0
	}
	d := 0
	for _, x := range v.items {
		d = max(d, jsonDepth(x))
	}
	return d + 1
}

// ---- the generator ----

// jsonKeys are few, so keys repeat; they hold the bytes a pointer escapes.
var jsonKeys = []string{`a`, `b`, `a/b`, `m~n`, `~1`, ``, `0`, `1`, `é`}

func jsonSpace(r *rand.Rand) string {
	if r.IntN(3) != 0 {
		return ""
	}
	var b strings.Builder
	for range 1 + r.IntN(3) {
		b.WriteByte(" \t\n\r"[r.IntN(4)])
	}
	return b.String()
}

func jsonDigits(r *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + r.IntN(10)))
	}
	return b.String()
}

// jsonNumber is a number in a random one of the grammar's forms.
func jsonNumber(r *rand.Rand) string {
	var b strings.Builder
	if r.IntN(3) == 0 {
		b.WriteByte('-')
	}
	if r.IntN(3) == 0 {
		b.WriteByte('0')
	} else {
		b.WriteByte(byte('1' + r.IntN(9)))
		b.WriteString(jsonDigits(r, r.IntN(25)))
	}
	if r.IntN(3) == 0 {
		b.WriteString("." + jsonDigits(r, 1+r.IntN(5)))
	}
	if r.IntN(3) == 0 {
		b.WriteString([]string{"e", "E"}[r.IntN(2)] + []string{"", "+", "-"}[r.IntN(3)] + jsonDigits(r, 1+r.IntN(4)))
	}
	return b.String()
}

// jsonString is a string literal: plain bytes, every escape, \u escapes
// of every kind of code point (pairs, lone and doubled surrogates), text
// outside ASCII, bytes that are not UTF-8 and DEL.
func jsonString(r *rand.Rand) string {
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

func jsonGenValue(r *rand.Rand, depth int) string {
	n := r.IntN(8)
	if depth <= 0 {
		n = r.IntN(5)
	}
	switch n {
	case 0:
		return []string{"null", "true", "false"}[r.IntN(3)]
	case 1, 2:
		return jsonNumber(r)
	case 3, 4:
		return jsonString(r)
	case 5:
		var parts []string
		for range r.IntN(4) {
			parts = append(parts, jsonSpace(r)+jsonGenValue(r, depth-1)+jsonSpace(r))
		}
		return "[" + jsonSpace(r) + strings.Join(parts, ",") + "]"
	default:
		var parts []string
		for range r.IntN(5) {
			key := jsonQuote(jsonKeys[r.IntN(len(jsonKeys))])
			parts = append(parts, jsonSpace(r)+key+jsonSpace(r)+":"+jsonSpace(r)+jsonGenValue(r, depth-1)+jsonSpace(r))
		}
		return "{" + jsonSpace(r) + strings.Join(parts, ",") + "}"
	}
}

// jsonGenDoc is the document of seed: a value of depth up to 5, and for
// one seed in 25 that value at the bottom of arrays nested to
// encoding/json's limit, one less or (rejected) one more.
func jsonGenDoc(r *rand.Rand, seed uint64) string {
	doc := jsonSpace(r) + jsonGenValue(r, 5) + jsonSpace(r)
	if seed%25 == 0 {
		k := 10000 - 1 - jsonDepth(jsonReference([]byte(doc)).v) + r.IntN(3)
		doc = strings.Repeat("[", k) + doc + strings.Repeat("]", k)
	}
	return doc
}

// jsonPointers are pointers into v: one to a place that exists, one past
// it, and one of a malformed or odd form.
func jsonPointers(r *rand.Rand, v *jsonValue) []string {
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

// jsonBreak changes one to three bytes of src: a byte deleted, put in or
// replaced, from the bytes the grammar cares about.
func jsonBreak(r *rand.Rand, src string) string {
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
