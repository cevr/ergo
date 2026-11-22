package anchor

import (
	"strings"
	"testing"
)

const goSrc = `package p

// Close keeps x.
func (a A) Close() { x := 1 }

func (b *B[T]) Close() { x := 1 }

func F() int { return x + 1 }

func G() int { return x + 1 }

var (
	v = x + 1
)
`

const ergoSrc = `package p

import "std/fmt"

// Close keeps x.
func (a A) Close() { let x = 1 }

func (b own *B[T]) Close() { let x = 1 }

func F() int { x + 1 }

law Holds(a int) { x + 1 }

test Holds { x + 1 }

extern go example.com/m.H func H() int { x + 1 }
`

// TestLocate: a row anchored by its declaration finds its from inside that
// declaration's span (doc comment included), whatever else the file holds;
// a method is named with its receiver's type; without a declaration the
// from must be unique in the file. Mutants (testdata/mutants.json):
// anchor-ignores-receiver, anchor-file-wide-count, anchor-span-without-doc.
func TestLocate(t *testing.T) {
	for _, c := range []struct {
		file, src, decl, from string
		inside                string // the text right after from's offset, or "" for an error
	}{
		{"a.go", goSrc, "A.Close", "x := 1", "x := 1 }\n\nfunc (b"},
		{"a.go", goSrc, "B.Close", "x := 1", "x := 1 }\n\nfunc F"},
		{"a.go", goSrc, "F", "x + 1", "x + 1 }\n\nfunc G"},
		{"a.go", goSrc, "G", "x + 1", "x + 1 }\n\nvar"},
		{"a.go", goSrc, "v", "x + 1", "x + 1\n)"},
		{"a.go", goSrc, "A.Close", "keeps x", "keeps x.\nfunc (a"},
		{"a.go", goSrc, "", "x + 1", ""},
		{"a.go", goSrc, "H", "x + 1", ""},
		{"a.ergo", ergoSrc, "A.Close", "let x = 1", "let x = 1 }\n\nfunc (b"},
		{"a.ergo", ergoSrc, "B.Close", "let x = 1", "let x = 1 }\n\nfunc F"},
		{"a.ergo", ergoSrc, "Holds#law", "x + 1", "x + 1 }\n\ntest"},
		{"a.ergo", ergoSrc, "Holds#test", "x + 1", "x + 1 }\n\nextern"},
		{"a.ergo", ergoSrc, "H", "x + 1", "x + 1 }\n"},
		{"a.ergo", ergoSrc, "A.Close", "keeps x", "keeps x.\nfunc (a"},
		{"a.ergo", ergoSrc, "", "x + 1", ""},
		{"a.ergo", ergoSrc, "fmt", "std/fmt", ""},
		{"a.txt", "x + 1", "F", "x + 1", ""},
	} {
		at, err := Locate(c.file, []byte(c.src), c.decl, c.from)
		switch {
		case c.inside == "" && err == nil:
			t.Errorf("Locate(%s, %q, %q) = %d; want an error", c.file, c.decl, c.from, at)
		case c.inside != "" && err != nil:
			t.Errorf("Locate(%s, %q, %q): %v", c.file, c.decl, c.from, err)
		case c.inside != "" && !strings.HasPrefix(c.src[at:], c.inside):
			t.Errorf("Locate(%s, %q, %q) found %q; want %q", c.file, c.decl, c.from, c.src[at:min(len(c.src), at+len(c.inside))], c.inside)
		}
	}
}

// TestEnclosing: the migration names the declaration that holds a text, and
// refuses a text outside every named declaration or across two. Mutant:
// anchor-enclosing-across.
func TestEnclosing(t *testing.T) {
	for _, c := range []struct {
		file, src, text, want string
		ok                    bool
	}{
		{"a.go", goSrc, "return x + 1 }\n\nfunc G", "", false},
		{"a.go", goSrc, "func F() int", "F", true},
		{"a.go", goSrc, "package p", "", false},
		{"a.ergo", ergoSrc, "std/fmt", "", false},
		{"a.ergo", ergoSrc, "law Holds", "Holds#law", true},
	} {
		at := strings.Index(c.src, c.text)
		got, ok := Enclosing(c.file, []byte(c.src), at, len(c.text))
		if got != c.want || ok != c.ok {
			t.Errorf("Enclosing(%s, %q) = %q, %v; want %q, %v", c.file, c.text, got, ok, c.want, c.ok)
		}
	}
}

// TestApply: the edit is made at the located occurrence, not the first in
// the file. Mutant: anchor-apply-first.
func TestApply(t *testing.T) {
	out, err := Apply("a.go", []byte(goSrc), "G", "x + 1", "x - 1")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(goSrc, "func G() int { return x + 1 }", "func G() int { return x - 1 }", 1); string(out) != want {
		t.Errorf("Apply edited the wrong occurrence:\n%s", out)
	}
}
