package lower

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// jsonRawSource decodes a message whose id is a json.Raw and gives the
// Raw's text, the message written in a Text and in an HTML Text, and a
// failure's text.
const jsonRawSource = `import json

type msg { ID json.Raw, M string }

func reads(text string) string {
	let m = json.Decode(json.Codec[msg]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case json.Malformed as e:
		return "malformed \(e.Offset)"
	case WireWrongKind as e:
		return "wrong \(e.Path) \(e.Want)"
	case WireMissingField as e:
		return "missing \(e.Path) \(e.Field)"
	}
	let t = json.NewText()
	encode(m, t)
	let h = json.NewHTMLText()
	encode(m, h)
	"\(m.ID.String()) | \(t.Written()) | \(h.Written())"
}
`

// rawValue draws one JSON value spelled with white space drawn between
// its tokens, and strings holding the characters a Text escapes.
func rawValue(r *rand.Rand, depth int) string {
	sp := func() string { return []string{"", " ", "\n\t", " \r\n "}[r.Intn(4)] }
	k := r.Intn(7)
	if depth > 2 && k >= 5 {
		k = r.Intn(5)
	}
	switch k {
	case 0:
		return "null"
	case 1:
		return []string{"true", "false"}[r.Intn(2)]
	case 2:
		return []string{"0", "-12", "3.25e+8", "12345678901234567890123", "1E-2"}[r.Intn(5)]
	case 3:
		return `"` + []string{"", "a", "<b>&", "x\u2028y", `q\"\\\u00e9`, "\u2029"}[r.Intn(6)] + `"`
	case 4:
		return `"` + []string{"tool", "id-7"}[r.Intn(2)] + `"`
	case 5:
		n := r.Intn(4)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = sp() + rawValue(r, depth+1) + sp()
		}
		return "[" + sp() + strings.Join(parts, ",") + "]"
	default:
		n := r.Intn(3)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = sp() + `"k` + strconv.Itoa(i) + `"` + sp() + ":" + sp() + rawValue(r, depth+1) + sp()
		}
		return "{" + sp() + strings.Join(parts, ",") + "}"
	}
}

// rawWant is what reads gives for a message whose id is v: v's text,
// then the message written with U+2028 and U+2029 escaped in v, and in
// HTML also <, > and & (std json's Raw rule, as encoding/json's
// compact with escaping writes a RawMessage).
func rawWant(v string) string {
	line := strings.NewReplacer("\u2028", `\u2028`, "\u2029", `\u2029`)
	html := strings.NewReplacer("\u2028", `\u2028`, "\u2029", `\u2029`, "<", `\u003c`, ">", `\u003e`, "&", `\u0026`)
	return v + ` | {"id":` + line.Replace(v) + `,"m":"x"} | {"id":` + html.Replace(v) + `,"m":"x"}`
}

// TestJSONRawAgrees: a json.Raw field reads the value's text as written,
// white space inside kept and around it cut, and writes it back as it
// is, with the characters a Text escapes in a string escaped; the
// lowered Go and the evaluator give the same, and both give what the Go
// model rawWant gives. Seeded texts (ERGO_JSON_RAW_SEED replays one).
// It also runs std json's Raw laws under ergo test (fed as a module's
// laws run: RawReadsOneValue, RawRoundTrips, RawIsValue). Mutants:
// raw-skiptext-space (runtime SkipText keeps the white space before the
// value), raw-eval-span (the evaluator's span), raw-escape-dropped
// (WireWrite writes U+2028 as it is).
//
// It builds Go because the Raw read under test is the built Cursor's
// SkipText and the codec functions the lowering makes for json's own
// read and write; the evaluator is the second side compared.
func TestJSONRawAgrees(t *testing.T) {
	needsBuild(t)
	seed := int64(1)
	if s := os.Getenv("ERGO_JSON_RAW_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	replay := fmt.Sprintf("replay: ERGO_JSON_RAW_SEED=%d go test -run TestJSONRawAgrees ./internal/sketch/lower", seed)
	r := rand.New(rand.NewSource(seed))
	var texts, wants []string
	for range 24 {
		v := rawValue(r, 0)
		pad := []string{"", " ", "\n "}
		texts = append(texts, `{"id":`+pad[r.Intn(3)]+v+pad[r.Intn(3)]+`,"m":"x"}`)
		wants = append(wants, rawWant(v))
	}
	texts = append(texts, `{"m":"x"}`, `{"id":,"m":"x"}`)
	wants = append(wants, "missing / id", "")
	var main strings.Builder
	main.WriteString("\nfunc main() needs Console {\n")
	for i, text := range texts {
		fmt.Fprintf(&main, "\tConsole.Println(\"%d \\(reads(%s))\")\n", i, strconv.Quote(text))
	}
	main.WriteString("}\n")
	src := jsonRawSource + main.String()
	o := lowerSource("jsonraw", src, true)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("seed %d: lowering: err %v, panic %v, diags %v, refused %v\n%s", seed, o.err, o.panicked, o.diags, o.refused, replay)
	}
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatalf("seed %d: no build\n%s", seed, replay)
	}
	out, stderr, code := runExe(t, exes[o.name])
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s\n%s", seed, code, firstLines(stderr, 8), replay)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(texts) {
		t.Fatalf("seed %d: %d lines for %d texts:\n%s\n%s", seed, len(lines), len(texts), out, replay)
	}
	got := make([]string, len(lines))
	for i, line := range lines {
		got[i] = strings.TrimPrefix(line, strconv.Itoa(i)+" ")
		if wants[i] == "" {
			if !strings.HasPrefix(got[i], "malformed ") {
				t.Errorf("seed %d: text %q: got %q, want malformed\n%s", seed, texts[i], got[i], replay)
			}
			continue
		}
		if got[i] != wants[i] {
			t.Errorf("seed %d: text %q:\n got %q\nwant %q\n%s", seed, texts[i], got[i], wants[i], replay)
		}
	}
	// The evaluator reads as the lowered Go.
	var tb strings.Builder
	tb.WriteString("\ntest EvalReadsRawAsGo() {\n")
	for i, text := range texts {
		fmt.Fprintf(&tb, "\tassert reads(%s) == %s\n", strconv.Quote(text), strconv.Quote(got[i]))
	}
	tb.WriteString("}\n")
	file, err := syntax.Parse(jsonRawSource + tb.String())
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("seed %d: %v\n%s", seed, diags, replay)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, res := range rep.Results {
		ran = ran || res.Name == "EvalReadsRawAsGo"
		if res.Status != eval.Passed {
			t.Errorf("seed %d: eval %s: %s %v\n%s", seed, res.Name, res.Status, res, replay)
		}
	}
	if !ran {
		t.Errorf("seed %d: eval ran no EvalReadsRawAsGo\n%s", seed, replay)
	}
	// std json's Raw laws run as a module's laws run.
	dir, err := filepath.Abs(filepath.Join(moduleRoot, "internal", "sketch", "std", "json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "test", "-run", "Raw", dir)
	cmd.Dir = t.TempDir()
	lawOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("ergo test -run Raw std json: %v\n%s", err, lawOut)
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range jsonRawLawStatements {
		_, law, _ := strings.Cut(key, " ")
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if !regexp.MustCompile(`(?m)^ok law (json\.)?` + law + `\b`).Match(lawOut) {
				t.Errorf("ergo test std json: law %s did not pass:\n%s", law, lawOut)
			}
		}}
	}
	lawledger.Run(t, laws)
}

// jsonRawLawStatements is each Raw law's ledger statement.
var jsonRawLawStatements = map[string]string{
	"json RawReadsOneValue": `RawReadsOneValue(x Raw) { let v = RawOf(x.text) catch { case _: return true } let r = Reader{c: open("[7," + v.text + ",7]", 0, 0) catch { case _: return false }} let (a, y, b) = decode[(int, Raw, int)](r) catch { case _: return false } a == 7 && y == v && b == 7 }`,
	"json RawRoundTrips":    `RawRoundTrips(d doc, sp int) { rawRoundTrips(d, sp) }`,
	"json RawIsValue":       `RawIsValue(d doc, sp int) { rawIsValue(d, sp) }`,
}
