package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// TestTOMLParseAgreesWithControl is the correctness property of
// examples/tomlparse (a TOML 1.0 parser written in Ergo): the Ergo program
// and the hand Go control (testdata/tomlparse/control) read TOML as two
// references say it reads.
//
//   - toml-test (pinned in the example's testdata, its TOML 1.0 file
//     list): each valid file prints, with `json`, the tagged JSON the
//     suite gives, compared as toml-test compares it (floats as numbers,
//     dates and times as times); each invalid file fails with status 1.
//     The file printed again with `fmt` reads back to the same JSON.
//   - Seeded generated documents: a tree drawn first and written as TOML
//     in a random one of its forms (every string form, integers in each
//     base, dotted keys, inline tables, [headers], [[arrays of tables]],
//     comments, CRLF) must print the tree's tagged JSON byte for byte;
//     the same text with a key defined once more must fail; and the text
//     with one to three bytes changed, which no reference reads, must
//     print the same on both binaries.
//   - The `tomlparse gen` workload at two sizes.
//
// Every run's standard output, standard error and status must be the
// same on both binaries. A failing seed prints the document and its
// replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads its file through the host and ends with os.Exit,
// and the control is Go; no in-process check runs either. Each run is
// bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/tomlparse/
// testdata/mutants.json).
func TestTOMLParseAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusExample(t, "tomlparse")
	control := buildHandControl(t, "tomlparse", work)
	bins := []string{ergoBin, control}
	dir := t.TempDir()
	path := filepath.Join(dir, "p.toml")
	again := filepath.Join(dir, "again.toml")

	roots, err := filepath.Glob(filepath.Join("..", "..", "examples", "tomlparse", "testdata", "toml-test-*"))
	if err != nil || len(roots) != 1 {
		t.Fatalf("toml-test is not in examples/tomlparse/testdata: %v %v", roots, err)
	}
	list, err := os.ReadFile(filepath.Join(roots[0], "files-toml-1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, name := range strings.Fields(string(list)) {
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		files++
		data, err := os.ReadFile(filepath.Join(roots[0], name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(data))
		got := tomlBoth(t, bins, path, name, "json", "")
		if strings.HasPrefix(name, "invalid/") {
			if !strings.HasSuffix(got, "\nstatus 1\n") || !strings.HasPrefix(got, "stdout:\n\nstderr:\ntomlparse: p.toml:") {
				t.Fatalf("%s: toml-test says it is not TOML, but tomlparse printed\n%s", name, clip(got))
			}
			continue
		}
		want, err := os.ReadFile(filepath.Join(roots[0], strings.TrimSuffix(name, ".toml")+".json"))
		if err != nil {
			t.Fatal(err)
		}
		have := tomlStdout(t, name, got)
		if why := tomlSame(t, want, []byte(have)); why != "" {
			t.Fatalf("%s: %s\ntomlparse printed\n%s\ntoml-test wants\n%s", name, why, clip(have), clip(string(want)))
		}
		tomlBoth(t, bins, path, name, "check", "")
		writeFile(t, again, tomlStdout(t, name, tomlBoth(t, bins, path, name, "fmt", "")))
		if back := tomlStdout(t, name+" printed by fmt", tomlBoth(t, bins, again, name+" printed by fmt", "json", "")); back != have {
			t.Fatalf("%s: printed by fmt, it reads as\n%s\nnot as\n%s", name, clip(back), clip(have))
		}
	}
	if files < 700 {
		t.Fatalf("the toml-test list names %d files", files)
	}

	seeds := 200
	first := uint64(1)
	if s := os.Getenv("TOMLPARSE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("TOMLPARSE_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x70e1))
		replay := fmt.Sprintf("replay: TOMLPARSE_SEED=%d go test -count=1 -run '^TestTOMLParseAgreesWithControl$' ./cmd/ergo", seed)
		what := "seed " + strconv.FormatUint(seed, 10)
		tree := tomlGenTable(r, 3)
		w := &tomlWriter{r: r, nl: []string{"\n", "\r\n"}[r.IntN(2)]}
		w.table(tree, nil)
		doc := w.b.String()
		writeFile(t, path, doc)
		got := tomlBoth(t, bins, path, what, "json", replay)
		if want := "stdout:\n" + tomlTagged(tree) + "\n\nstderr:\n\nstatus 0\n"; got != want {
			t.Fatalf("%s: tomlparse json printed\n%s\nthe tree is\n%s\nthe document:\n%s\n%s", what, clip(got), clip(want), clip(doc), replay)
		}
		tomlBoth(t, bins, path, what, "check", replay)
		tomlBoth(t, bins, path, what, "fmt", replay)
		if len(tree.keys) > 0 {
			k := r.IntN(len(tree.keys))
			twice := tomlKey(r, tree.keys[k]) + " = 1" + w.nl + doc
			writeFile(t, path, twice)
			if got := tomlBoth(t, bins, path, what+", a key defined twice", "json", replay); !strings.HasSuffix(got, "\nstatus 1\n") {
				t.Fatalf("%s: with %q defined twice, tomlparse printed\n%s\n%s", what, tree.keys[k], clip(got), replay)
			}
		}
		writeFile(t, path, tomlBreak(r, doc))
		tomlBoth(t, bins, path, what+", with bytes changed", "json", replay)
	}

	for _, n := range []string{"2", "300"} {
		gen := exec.Command(ergoBin, "gen", "-seed", strconv.FormatUint(first, 10), "-n", n)
		var out bytes.Buffer
		gen.Stdout = &out
		if err := proclimit.Run(gen, 1<<30); err != nil {
			t.Fatalf("tomlparse gen: %v", err)
		}
		writeFile(t, path, out.String())
		for _, cmd := range []string{"json", "check"} {
			if got := tomlBoth(t, bins, path, "the workload of "+n+" records", cmd, ""); !strings.HasSuffix(got, "\nstatus 0\n") {
				t.Fatalf("the workload of %s records: tomlparse %s printed\n%s", n, cmd, clip(got))
			}
		}
	}
}

// tomlBoth runs cmd on path with each of bins, fails unless both print
// the same, and is what they print.
func tomlBoth(t *testing.T, bins []string, path, what, cmd, replay string) string {
	t.Helper()
	first := runCorpus(t, bins[0], cmd, path)
	if second := runCorpus(t, bins[1], cmd, path); second != first {
		src, _ := os.ReadFile(path)
		t.Fatalf("%s: tomlparse %s printed\n%s\nthe control printed\n%s\nthe document:\n%q\n%s", what, cmd, clip(first), clip(second), clip(string(src)), replay)
	}
	return first
}

// tomlStdout is the standard output of a run that must succeed.
func tomlStdout(t *testing.T, what, run string) string {
	t.Helper()
	out, rest, ok := strings.Cut(strings.TrimPrefix(run, "stdout:\n"), "\nstderr:\n")
	if !ok || rest != "\nstatus 0\n" {
		t.Fatalf("%s: tomlparse failed:\n%s", what, clip(run))
	}
	return out
}

// tomlSame is "" when have is the tagged JSON want, as toml-test compares
// them, and else where they differ.
func tomlSame(t *testing.T, want, have []byte) string {
	t.Helper()
	var w, h any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("toml-test's JSON: %v", err)
	}
	if err := json.Unmarshal(have, &h); err != nil {
		return "the output is not JSON: " + err.Error()
	}
	return tomlSameValue(w, h, "")
}

func tomlSameValue(want, have any, at string) string {
	switch w := want.(type) {
	case []any:
		h, ok := have.([]any)
		if !ok || len(h) != len(w) {
			return at + ": not an array of " + strconv.Itoa(len(w))
		}
		for i := range w {
			if why := tomlSameValue(w[i], h[i], at+"["+strconv.Itoa(i)+"]"); why != "" {
				return why
			}
		}
		return ""
	case map[string]any:
		h, ok := have.(map[string]any)
		if !ok || len(h) != len(w) {
			return at + ": not a table of " + strconv.Itoa(len(w)) + " keys"
		}
		kind, typed := w["type"].(string)
		text, valued := w["value"].(string)
		if typed && valued && len(w) == 2 {
			hk, _ := h["type"].(string)
			ht, _ := h["value"].(string)
			if hk != kind {
				return at + ": a " + hk + ", not a " + kind
			}
			if tomlScalarSame(kind, text, ht) {
				return ""
			}
			return at + ": " + strconv.Quote(ht) + ", not " + strconv.Quote(text)
		}
		for k := range w {
			if why := tomlSameValue(w[k], h[k], at+"."+k); why != "" {
				return why
			}
		}
		return ""
	}
	return at + ": an unexpected JSON value"
}

// tomlScalarSame compares a float as a number and a date or time as a
// time, as toml-test does, and any other scalar as text.
func tomlScalarSame(kind, want, have string) bool {
	switch kind {
	case "float":
		parse := func(s string) float64 {
			switch strings.TrimPrefix(s, "+") {
			case "nan", "-nan":
				return math.NaN()
			case "inf":
				return math.Inf(1)
			case "-inf":
				return math.Inf(-1)
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return math.NaN() + 1
			}
			return f
		}
		a, b := parse(want), parse(have)
		return a == b || (math.IsNaN(a) && math.IsNaN(b) && !strings.Contains(have, "N"))
	case "datetime", "datetime-local", "date-local", "time-local":
		layout := map[string]string{
			"datetime":       time.RFC3339Nano,
			"datetime-local": "2006-01-02T15:04:05.999999999",
			"date-local":     "2006-01-02",
			"time-local":     "15:04:05.999999999",
		}[kind]
		norm := strings.NewReplacer(" ", "T", "t", "T", "z", "Z")
		a, err1 := time.Parse(layout, norm.Replace(want))
		b, err2 := time.Parse(layout, norm.Replace(have))
		return err1 == nil && err2 == nil && a.Equal(b)
	}
	return want == have
}

// ---- the generated documents ----

// tomlTree is a table of the generator: its keys in order, and each
// value a scalar (kind and the text the tagged JSON gives it), an array,
// a table or an array of tables.
type tomlTree struct {
	kind  string // string, integer, float, bool, a date kind; "array", "table", "tables"
	text  string // a scalar's tagged text; for a float or a moment, the form written
	items []*tomlTree
	keys  []string
}

var tomlKeys = []string{"a", "b", "key-1", "under_score", "007", "with space", "dot.ted", "", "é", `q"\`, "tab\t", "it's"}

var tomlFloatForms = []string{"1.5", "-0.25", "1e3", "6.02e+23", "1_000.5", "inf", "-inf", "nan", "+inf", "3.0E-2", "+1.0", "0.0", "-0.0", "1e-7", "12_3.4_5e1_0"}

var tomlMomentForms = [][3]string{
	{"datetime", "1979-05-27T07:32:00Z", "1979-05-27T07:32:00Z"},
	{"datetime", "1979-05-27 07:32:00z", "1979-05-27T07:32:00Z"},
	{"datetime", "1979-05-27t00:32:00.999999-07:00", "1979-05-27T00:32:00.999999-07:00"},
	{"datetime-local", "2000-02-29T23:59:59.5", "2000-02-29T23:59:59.5"},
	{"date-local", "2024-12-31", "2024-12-31"},
	{"time-local", "00:00:00", "00:00:00"},
	{"time-local", "23:59:59.123", "23:59:59.123"},
}

var tomlTextPieces = []string{"plain", " ", "é", "☃", "😀", "\n", "\t", "\"", "'", "\\", "\x01", "\x7f", "''", `"""`, "#", "\r"}

func tomlGenScalar(r *rand.Rand) *tomlTree {
	switch r.IntN(5) {
	case 0:
		var b strings.Builder
		for range r.IntN(5) {
			b.WriteString(tomlTextPieces[r.IntN(len(tomlTextPieces))])
		}
		return &tomlTree{kind: "string", text: b.String()}
	case 1:
		n := []int64{0, 1, -1, 42, math.MaxInt64, math.MinInt64, 255, 1 << 40}[r.IntN(8)]
		if r.IntN(2) == 0 {
			n = r.Int64N(1<<62) - 1<<61
		}
		return &tomlTree{kind: "integer", text: strconv.FormatInt(n, 10)}
	case 2:
		return &tomlTree{kind: "float", text: tomlFloatForms[r.IntN(len(tomlFloatForms))]}
	case 3:
		return &tomlTree{kind: "bool", text: strconv.FormatBool(r.IntN(2) == 0)}
	}
	m := tomlMomentForms[r.IntN(len(tomlMomentForms))]
	return &tomlTree{kind: m[0], text: m[1]}
}

func tomlGenValue(r *rand.Rand, depth int) *tomlTree {
	n := r.IntN(10)
	if depth <= 0 {
		n = r.IntN(6)
	}
	switch {
	case n < 6:
		return tomlGenScalar(r)
	case n < 8:
		v := &tomlTree{kind: "array"}
		for range r.IntN(4) {
			v.items = append(v.items, tomlGenValue(r, depth-1))
		}
		return v
	case n == 8:
		v := &tomlTree{kind: "tables"}
		for range 1 + r.IntN(3) {
			v.items = append(v.items, tomlGenTable(r, depth-1))
		}
		return v
	}
	return tomlGenTable(r, depth-1)
}

func tomlGenTable(r *rand.Rand, depth int) *tomlTree {
	t := &tomlTree{kind: "table"}
	for range r.IntN(5) {
		k := tomlKeys[r.IntN(len(tomlKeys))]
		if !slices.Contains(t.keys, k) {
			t.keys = append(t.keys, k)
			t.items = append(t.items, tomlGenValue(r, depth))
		}
	}
	return t
}

// tomlTagged is the tagged JSON of v, keys in byte order, as tomlparse
// prints it.
func tomlTagged(v *tomlTree) string {
	var b strings.Builder
	switch v.kind {
	case "table":
		order := slices.Clone(v.keys)
		slices.Sort(order)
		b.WriteByte('{')
		for i, k := range order {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(tomlQuote(k) + ":" + tomlTagged(v.items[slices.Index(v.keys, k)]))
		}
		b.WriteByte('}')
	case "array", "tables":
		b.WriteByte('[')
		for i, x := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(tomlTagged(x))
		}
		b.WriteByte(']')
	default:
		text := v.text
		switch v.kind {
		case "float":
			text = tomlFloatTagged(v.text)
		case "datetime", "datetime-local", "date-local", "time-local":
			for _, m := range tomlMomentForms {
				if m[1] == v.text {
					text = m[2]
				}
			}
		}
		b.WriteString(`{"type":"` + v.kind + `","value":` + tomlQuote(text) + "}")
	}
	return b.String()
}

// tomlFloatTagged is the tagged text of the float form s: inf, -inf, nan,
// or Go's shortest form of its value.
func tomlFloatTagged(s string) string {
	s = strings.ReplaceAll(s, "_", "")
	switch strings.TrimPrefix(s, "+") {
	case "inf":
		return "inf"
	case "-inf":
		return "-inf"
	case "nan":
		return "nan"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		panic(err)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// tomlQuote is s as a JSON string with ", \ and the control bytes
// escaped, every other byte as it is: tomlparse's quoting.
func tomlQuote(s string) string { return jsonQuote(s) }

// tomlWriter writes a tree as TOML in random forms.
type tomlWriter struct {
	r  *rand.Rand
	b  strings.Builder
	nl string
}

func (w *tomlWriter) line(s string) {
	w.b.WriteString(s)
	switch w.r.IntN(6) {
	case 0:
		w.b.WriteString(" \t# a comment ' \" é")
	case 1:
		w.b.WriteString("   ")
	}
	w.b.WriteString(w.nl)
	if w.r.IntN(8) == 0 {
		w.b.WriteString(w.nl + "# a line of comment" + w.nl)
	}
}

// tomlBare says whether k can be a bare key.
func tomlBare(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// tomlLiteralOK says whether s can be a one-line literal string.
func tomlLiteralOK(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\'' || (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// tomlKey is k as a key: bare when it can be and the draw says so, else a
// basic or a literal string.
func tomlKey(r *rand.Rand, k string) string {
	if tomlBare(k) && r.IntN(4) != 0 {
		return k
	}
	if tomlLiteralOK(k) && r.IntN(2) == 0 {
		return "'" + k + "'"
	}
	return tomlBasic(r, k)
}

// tomlBasic is s as a basic string, some bytes and runes as escapes.
func tomlBasic(r *rand.Rand, s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"' || c == '\\' || c < 0x20 || c == 0x7f:
			if r.IntN(2) == 0 {
				fmt.Fprintf(&b, `\u%04X`, c)
			} else {
				q := jsonQuote(string(c))
				b.WriteString(q[1 : len(q)-1])
			}
		case c >= 0x80 && r.IntN(3) == 0:
			fmt.Fprintf(&b, `\U%08x`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlString is s in a random one of the string forms that can hold it.
func (w *tomlWriter) tomlString(s string) string {
	r := w.r
	switch r.IntN(4) {
	case 1:
		if tomlLiteralOK(s) {
			return "'" + s + "'"
		}
	case 2:
		// A multi-line basic string: newlines as they are, every quote
		// and backslash escaped, a line-ending backslash now and then.
		var b strings.Builder
		b.WriteString(`"""` + w.nl)
		for _, c := range s {
			// A backslash that ends a line takes the blanks and newlines
			// after it, so it stands only before a rune that is none.
			if c != ' ' && c != '\t' && c != '\n' && r.IntN(6) == 0 {
				b.WriteString("\\ " + w.nl + "   ")
			}
			switch {
			case c == '\n':
				if w.nl == "\r\n" {
					return tomlBasic(r, s) // the text would read \r\n back
				}
				b.WriteString(w.nl)
			case c == '"' || c == '\\' || (c < 0x20 && c != '\t') || c == 0x7f:
				fmt.Fprintf(&b, `\u%04x`, c)
			default:
				b.WriteRune(c)
			}
		}
		b.WriteString(`"""`)
		return b.String()
	case 3:
		if !strings.Contains(s, "'") && !strings.Contains(s, "\r") && tomlLiteralOK(strings.ReplaceAll(s, "\n", "")) && w.nl == "\n" {
			return "'''\n" + s + "'''"
		}
	}
	return tomlBasic(r, s)
}

func (w *tomlWriter) integer(text string) string {
	n, _ := strconv.ParseInt(text, 10, 64)
	r := w.r
	if n >= 0 && r.IntN(3) == 0 {
		k := r.IntN(3)
		digits := strconv.FormatInt(n, []int{16, 8, 2}[k])
		if r.IntN(2) == 0 {
			digits = strings.ToUpper(digits)
		}
		return "0" + string("xob"[k]) + tomlUnderscores(r, digits)
	}
	digits := strings.TrimPrefix(text, "-")
	sign := ""
	if n < 0 {
		sign = "-"
	} else if r.IntN(4) == 0 {
		sign = "+"
	}
	return sign + tomlUnderscores(r, digits)
}

// tomlUnderscores puts an underscore between some pairs of digits.
func tomlUnderscores(r *rand.Rand, digits string) string {
	var b strings.Builder
	for i := 0; i < len(digits); i++ {
		if i > 0 && r.IntN(4) == 0 {
			b.WriteByte('_')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// value is v on one line (an array may span lines).
func (w *tomlWriter) value(v *tomlTree) string {
	switch v.kind {
	case "string":
		return w.tomlString(v.text)
	case "integer":
		return w.integer(v.text)
	case "array", "tables":
		var b strings.Builder
		b.WriteByte('[')
		for i, x := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			if w.r.IntN(3) == 0 {
				b.WriteString(" # in an array" + w.nl)
			}
			b.WriteString(" " + w.value(x))
		}
		if len(v.items) > 0 && w.r.IntN(3) == 0 {
			b.WriteString("," + w.nl)
		}
		b.WriteByte(']')
		return b.String()
	case "table":
		var parts []string
		for i, k := range v.keys {
			parts = append(parts, w.dotted(tomlKey(w.r, k), v.items[i])...)
		}
		if len(parts) == 0 {
			return "{}"
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	return v.text
}

// dotted is the key/value pairs that give v to the key k: one pair, or a
// pair for each value inside a table v, with dotted keys. An empty table
// and an array of tables take one pair.
func (w *tomlWriter) dotted(k string, v *tomlTree) []string {
	if v.kind != "table" || len(v.keys) == 0 || w.r.IntN(2) == 0 {
		return []string{k + " = " + w.value(v)}
	}
	var out []string
	for i, sub := range v.keys {
		for _, p := range w.dotted(tomlKey(w.r, sub), v.items[i]) {
			out = append(out, k+[]string{".", " . "}[w.r.IntN(2)]+p)
		}
	}
	return out
}

// table writes the members of t, whose header path is path: its plain
// members and some of its tables as key/value pairs, then the rest of its
// tables as [path.k] and its arrays of tables as [[path.k]] sections.
func (w *tomlWriter) table(t *tomlTree, path []string) {
	var later []int
	for i, k := range t.keys {
		v := t.items[i]
		if (v.kind == "table" || v.kind == "tables") && w.r.IntN(3) != 0 {
			later = append(later, i)
			continue
		}
		for _, p := range w.dotted(tomlKey(w.r, k), v) {
			w.line(p)
		}
	}
	for _, i := range later {
		inner := append(slices.Clone(path), tomlKey(w.r, t.keys[i]))
		v := t.items[i]
		if v.kind == "table" {
			w.line(w.nl + "[" + strings.Join(inner, ".") + "]")
			w.table(v, inner)
			continue
		}
		for _, x := range v.items {
			w.line(w.nl + "[[ " + strings.Join(inner, " . ") + " ]]")
			w.table(x, inner)
		}
	}
}

// tomlBreak changes one to three bytes of src: a byte deleted, put in or
// replaced, from the bytes the grammar cares about.
func tomlBreak(r *rand.Rand, src string) string {
	b := []byte(src)
	marks := []byte("[]{}\"'=.,#\n\r \\_0x+-:Ttz5e\x01\x80")
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
