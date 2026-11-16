package eval

import (
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestTOMLAgreesWithRuntime holds the evaluator's TOML reader
// (nativestoml.go) to runtime/v1.TOMLParse, the body the built program
// calls, and to the public toml-test 1.0 corpus (runtime/v1's testdata):
// every corpus document, and seeded edits of them (a byte dropped,
// doubled or replaced by one of TOML's delimiters, and small MaxBytes and
// MaxDepth), must give the same JSON value, or the same failure: line, key
// and rule. A corpus document the corpus calls invalid must fail. A
// failing seed prints the document, both results and the replay command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o27-toml-dotted-reopens-header,
// oracle-o27-toml-multiline-quotes-lost, oracle-o27-toml-offset-unchecked.
func TestTOMLAgreesWithRuntime(t *testing.T) {
	b, err := os.ReadFile("../../../runtime/v1/testdata/toml-test/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name, Raw string
		Want      json.RawMessage
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	docs := make([]string, len(rows))
	for i, row := range rows {
		text, err := base64.StdEncoding.DecodeString(row.Raw)
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = string(text)
		if row.Want == nil {
			if _, bad := tomlRead(docs[i], 0, 0); bad == nil {
				t.Errorf("%s: the corpus's invalid document is accepted", row.Name)
			}
		}
		tomlAgree(t, row.Name, docs[i], 0, 0)
	}
	n := 2000
	if testing.Short() {
		n = 300
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_TOML_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	const marks = "[]{}=,.\"'#\n \\_:+-ezT0"
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x70))
		doc := []byte(docs[r.IntN(len(docs))])
		for range 1 + r.IntN(3) {
			if len(doc) == 0 {
				break
			}
			k := r.IntN(len(doc))
			switch r.IntN(3) {
			case 0:
				doc = append(doc[:k:k], doc[k+1:]...)
			case 1:
				doc = append(doc[:k+1:k+1], doc[k:]...)
			default:
				doc[k] = marks[r.IntN(len(marks))]
			}
		}
		var maxBytes, maxDepth int64
		if r.IntN(4) == 0 {
			maxBytes = int64(r.IntN(len(doc) + 2))
		}
		if r.IntN(4) == 0 {
			maxDepth = int64(1 + r.IntN(4))
		}
		if !tomlAgree(t, "seed "+strconv.FormatUint(seed, 10), string(doc), maxBytes, maxDepth) {
			t.Fatalf("replay: ERGO_EVAL_TOML_SEED=%d go test -run TestTOMLAgreesWithRuntime ./internal/sketch/eval", seed)
		}
	}
}

func tomlAgree(t *testing.T, name, doc string, maxBytes, maxDepth int64) bool {
	t.Helper()
	got, bad := tomlRead(doc, maxBytes, maxDepth)
	want, err := v1.TOMLParse(doc, int(maxBytes), int(maxDepth))
	switch {
	case err != nil:
		m, ok := err.(v1.TOMLMalformed)
		if !ok {
			t.Errorf("%s: runtime/v1 fails with %v", name, err)
			return false
		}
		if bad == nil || bad.line != m.Line || bad.key != m.Key || bad.reason != m.Reason {
			t.Errorf("%s: %q (MaxBytes %d, MaxDepth %d)\nthe evaluator gives %s %+v\nruntime/v1 fails %+v", name, doc, maxBytes, maxDepth, got, bad, m)
			return false
		}
	case bad != nil:
		t.Errorf("%s: %q (MaxBytes %d, MaxDepth %d)\nthe evaluator fails %+v\nruntime/v1 gives %s", name, doc, maxBytes, maxDepth, *bad, want)
		return false
	default:
		var a, b any
		da, db := json.NewDecoder(strings.NewReader(got)), json.NewDecoder(strings.NewReader(want))
		da.UseNumber()
		db.UseNumber()
		if da.Decode(&a) != nil || db.Decode(&b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: %q\nthe evaluator gives %s\nruntime/v1 gives %s", name, doc, got, want)
			return false
		}
	}
	return true
}
