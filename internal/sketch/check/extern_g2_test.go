package check_test

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
)

// G2 gx-allowlist (lane G, batch 12; f-11). A user extern of a Go package
// that reads only its arguments but is not on the pure allowlist yet is
// S109 "… is not on the pure allowlist (…) yet; a user extern binds only
// those (#202)", not "reaches the host"; a package that may reach the host
// keeps the host text; unicode and unicode/utf16 joined the list.

// pureUnlisted is the reviewed table of pure packages not on the allowlist,
// written out independently of v1.PureUnlisted (the oracle for it).
var pureUnlisted = []string{
	"bytes", "cmp", "container/list", "container/ring",
	"crypto/md5", "crypto/sha1", "crypto/sha512", "crypto/subtle",
	"encoding/ascii85", "encoding/base32",
	"hash/adler32", "hash/crc64", "hash/fnv",
	"html", "maps", "math/big", "math/cmplx", "path", "regexp", "regexp/syntax",
}

// hostPackages is the reviewed table of Go standard-library packages that
// may reach the host or call unknown code: the host text stays theirs.
var hostPackages = []string{
	"bufio", "context", "crypto/hmac", "crypto/rand", "container/heap", "embed",
	"encoding/csv", "encoding/json", "errors", "flag", "fmt", "hash/maphash",
	"html/template", "io", "io/fs", "iter", "log", "math/rand", "math/rand/v2",
	"mime", "net", "net/http", "net/netip", "net/url", "os", "os/exec",
	"os/signal", "plugin", "reflect", "runtime", "runtime/debug", "sync",
	"sync/atomic", "syscall", "testing", "text/template", "time", "unique",
}

// TestExternAdmissionTable is G2's property: for every package of the
// reviewed tables, the checker's admission of a user extern agrees with
// the table: an allowlisted package checks clean; a pure package not
// listed gets exactly one S109 with the unlisted text; any other gets
// exactly one S109 with the host text.
func TestExternAdmissionTable(t *testing.T) {
	type class int
	const (
		listed class = iota
		unlisted
		host
	)
	table := map[string]class{}
	for _, p := range allowlist {
		table[p] = listed
	}
	for _, p := range pureUnlisted {
		if _, dup := table[p]; dup {
			t.Fatalf("%s is in two classes of the reviewed table", p)
		}
		table[p] = unlisted
	}
	for _, p := range hostPackages {
		if _, dup := table[p]; dup {
			t.Fatalf("%s is in two classes of the reviewed table", p)
		}
		table[p] = host
	}
	for path, want := range table {
		t.Run(path, func(t *testing.T) {
			target := path + ".F"
			_, diags := check.CheckSource("extern go " + target + " func f(n int) int\nfunc Use(n int) int { f(n) }\n")
			switch want {
			case listed:
				if len(diags) != 0 {
					t.Fatalf("listed: want clean, got\n\t%s", describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != 1 {
				t.Fatalf("want one S109 on line 1, got\n\t%s", describe(diags))
			}
			msg := diags[0].Message
			unlistedPrefix := "extern go " + target + ": " + path + " is not on the pure allowlist ("
			hostPrefix := "extern go " + target + " reaches the host; wait for "
			switch want {
			case unlisted:
				if !strings.HasPrefix(msg, unlistedPrefix) || strings.Contains(msg, "reaches the host") {
					t.Fatalf("pure but not listed: want %q…, got %s", unlistedPrefix, msg)
				}
			case host:
				if !strings.HasPrefix(msg, hostPrefix) || strings.Contains(msg, "not on the pure allowlist") {
					t.Fatalf("host: want %q…, got %s", hostPrefix, msg)
				}
			}
		})
	}
}

// TestPureUnlistedList pins v1.PureUnlisted to the reviewed table and keeps
// it apart from v1.PureImports: a package moves from one list to the other,
// it is never in both.
func TestPureUnlistedList(t *testing.T) {
	got := slices.Sorted(slices.Values(v1.PureUnlisted))
	if !slices.Equal(got, pureUnlisted) {
		t.Fatalf("v1.PureUnlisted is %v, want the reviewed %v", got, pureUnlisted)
	}
	for _, p := range v1.PureUnlisted {
		if slices.Contains(v1.PureImports, p) {
			t.Errorf("%s is in both v1.PureImports and v1.PureUnlisted", p)
		}
	}
}

// TestExternUnlistedText pins the whole unlisted message once (f-11).
func TestExternUnlistedText(t *testing.T) {
	_, diags := check.CheckSource("extern go math/cmplx.Abs func Abs(x complex128) float64\n")
	const want = "extern go math/cmplx.Abs: math/cmplx is not on the pure allowlist (crypto/sha256, encoding/base64, encoding/hex, hash/crc32, math, math/bits, path/filepath, slices, sort, strconv, strings, unicode, unicode/utf16, unicode/utf8) yet; a user extern binds only those (#202)"
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
