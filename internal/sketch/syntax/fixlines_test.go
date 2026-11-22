package syntax

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// TestFixLinesRoundTrip: any fix the dump writes (compiler/syntax/dump
// FixLines) reads back as the same class and edits through failureOf,
// and the error's own fields stay those of its first line. The texts mix
// spaces, quotes, backslashes, newlines, tabs, non-ASCII and invalid
// UTF-8; a failing seed prints itself and the dump text. Mutants
// fixlines-class-dropped and fixlines-edits-one.
func TestFixLinesRoundTrip(t *testing.T) {
	pieces := []string{"", " ", "let", "let mut", `"`, `\`, "\n", "\t", "é", "→", "\xff", "x :=", "`", "\\x20"}
	for seed := int64(0); seed < 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		class := []string{"exact", "suggested"}[r.Intn(2)]
		want := &Fix{Class: class}
		var offs, lens []int
		var texts []string
		for range 1 + r.Intn(4) {
			text := ""
			for range r.Intn(4) {
				text += pieces[r.Intn(len(pieces))]
			}
			e := Edit{Offset: r.Intn(1000), Length: r.Intn(20), Text: text}
			want.Edits = append(want.Edits, e)
			offs, lens, texts = append(offs, e.Offset), append(lens, e.Length), append(texts, e.Text)
		}
		lines, err := linked.FixLines(context.Background(), class, offs, lens, texts)
		if err != nil {
			t.Fatal(err)
		}
		text := "S001 3 4 1 4 a message with spaces" + lines
		var e *Error
		if !errors.As(failureOf(text), &e) {
			t.Fatalf("seed %d: %q does not decode", seed, text)
		}
		if e.Code != "S001" || e.Offset != 3 || e.Length != 4 || e.Line != 1 || e.Column != 4 || e.Message != "a message with spaces" {
			t.Errorf("seed %d: the error's fields are %+v (from %q)", seed, e, text)
		}
		if !reflect.DeepEqual(e.Fix, want) {
			t.Errorf("seed %d: fix %+v, want %+v (from %q)", seed, e.Fix, want, text)
		}
	}
}
