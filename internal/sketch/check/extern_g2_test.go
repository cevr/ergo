package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// G2 gx-allowlist (lane G, batch 12; f-11), as IG2 left it: a user extern
// of a Go package off the pure allowlist is S109 with one text, which says
// the target may reach the host and names import go, the route that
// charges go.Foreign. IG2 deleted v1.PureUnlisted, the table that worded a
// pure package off the list apart: the reviewed list is the one fact.
// unicode and unicode/utf16 joined the list.

// offList is a reviewed sample of Go standard-library packages off the
// allowlist: pure ones (bytes, regexp) and ones that reach the host or
// call unknown code (os, net/url).
var offList = []string{
	"bytes", "cmp", "container/list", "crypto/md5", "encoding/base32",
	"hash/fnv", "html", "maps", "math/big", "path", "regexp",
	"bufio", "context", "crypto/rand", "container/heap", "encoding/json",
	"errors", "fmt", "io", "math/rand/v2", "net/http", "net/url", "os",
	"reflect", "sync", "syscall", "time",
}

// TestExternAdmissionTable is G2's property: an allowlisted package checks
// clean; any other gets exactly one S109 with the host text.
func TestExternAdmissionTable(t *testing.T) {
	table := map[string]bool{}
	for _, p := range allowlist {
		table[p] = true
	}
	for _, p := range offList {
		if table[p] {
			t.Fatalf("%s is both listed and off the list", p)
		}
		table[p] = false
	}
	for path, listed := range table {
		t.Run(path, func(t *testing.T) {
			target := path + ".F"
			_, diags := check.CheckSource("extern go " + target + " func f(n int) int\nfunc Use(n int) int { f(n) }\n")
			if listed {
				if len(diags) != 0 {
					t.Fatalf("listed: want clean, got\n\t%s", describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != 1 {
				t.Fatalf("want one S109 on line 1, got\n\t%s", describe(diags))
			}
			if prefix := "extern go " + target + " is not on the reviewed pure list, so it may reach the host; call it through import go " + path + ","; !strings.HasPrefix(diags[0].Message, prefix) {
				t.Fatalf("want %q…, got %s", prefix, diags[0].Message)
			}
		})
	}
}

// TestExternHostText pins the whole message once.
func TestExternHostText(t *testing.T) {
	_, diags := check.CheckSource("extern go math/cmplx.Abs func Abs(x complex128) float64\n")
	const want = "extern go math/cmplx.Abs is not on the reviewed pure list, so it may reach the host; call it through import go math/cmplx, which charges go.Foreign (a user extern binds only the pure Go packages crypto/sha256, encoding/base64, encoding/hex, hash/crc32, math, math/bits, path/filepath, slices, sort, strconv, strings, unicode, unicode/utf16, unicode/utf8)"
	if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Message != want {
		t.Fatalf("want one S109 %q, got\n\t%s", want, describe(diags))
	}
}

// TestExternUnicodeAdmitted: the f-11 repro's shapes over the two new
// packages check clean, and a host member of an allowed package is still
// refused with the host text.
func TestExternUnicodeAdmitted(t *testing.T) {
	src := `extern go unicode.ToLower func Lower(r rune) rune
extern go unicode.IsUpper func IsUpper(r rune) bool
extern go unicode/utf16.IsSurrogate func IsSurrogate(r rune) bool
extern go math/bits.OnesCount func OnesCount(x uint) int
func Use(r rune) bool { IsUpper(Lower(r)) || IsSurrogate(r) || OnesCount(uint(r)) > 3 }
`
	if _, diags := check.CheckSource(src); len(diags) != 0 {
		t.Fatalf("want clean, got\n\t%s", describe(diags))
	}
}
