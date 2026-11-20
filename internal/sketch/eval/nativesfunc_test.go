package eval

import (
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestFuncNativesMatchGo holds the evaluator's natives of the Go bodies std
// strings and utf8 keep (SQ5) to Go's own functions: TrimFunc and
// FieldsFunc, which call an Ergo function rune by rune (nativesgo.go),
// under utf8.IsSpace (unicode.IsSpace) and a function literal, and
// IndexAny and LastIndexAny, on seeded strings of spaces, letters,
// multi-byte runes and invalid bytes. It kills sq5-native-trim-left-only,
// sq5-native-fields-drops-last, sq5-native-lastindexany-first and
// sq5-native-isspace-letter, which no other test reaches: std's laws and
// TestStdMatchesGo run the lowered Go, not the evaluator.
func TestFuncNativesMatchGo(t *testing.T) {
	n := 40
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_FUNC_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	parts := []string{" ", "\t", "a", "b", "é", "世", "\xff", "\xe4\xb8", "  "}
	isA := func(r rune) bool { return r == 'a' }
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x5f))
		var b strings.Builder
		for range r.IntN(9) {
			b.WriteString(parts[r.IntN(len(parts))])
		}
		s := b.String()
		chars := parts[r.IntN(len(parts))] + parts[r.IntN(len(parts))]
		lit := strconv.Quote(s)
		src := "import strings\nimport utf8\n\ntest T {\n" +
			"\tassert strings.TrimFunc(" + lit + ", utf8.IsSpace) == " + strconv.Quote(strings.TrimFunc(s, unicode.IsSpace)) + "\n" +
			"\tassert strings.TrimFunc(" + lit + ", func(r rune) bool { r == 'a' }) == " + strconv.Quote(strings.TrimFunc(s, isA)) + "\n" +
			"\tassert strings.Join(strings.FieldsFunc(" + lit + ", utf8.IsSpace), \"|\") == " + strconv.Quote(strings.Join(strings.FieldsFunc(s, unicode.IsSpace), "|")) + "\n" +
			"\tassert len(strings.FieldsFunc(" + lit + ", func(r rune) bool { r == 'a' })) == " + strconv.Itoa(len(strings.FieldsFunc(s, isA))) + "\n" +
			"\tassert strings.IndexAny(" + lit + ", " + strconv.Quote(chars) + ") == " + strconv.Itoa(strings.IndexAny(s, chars)) + "\n" +
			"\tassert strings.LastIndexAny(" + lit + ", " + strconv.Quote(chars) + ") == " + strconv.Itoa(strings.LastIndexAny(s, chars)) + "\n" +
			"}\n"
		if got := runSource(t, src); got != "passed" {
			t.Fatalf("seed %d: %s\nreplay: ERGO_EVAL_FUNC_SEED=%d go test -run TestFuncNativesMatchGo ./internal/sketch/eval\n%s", seed, got, seed, src)
		}
	}
}
