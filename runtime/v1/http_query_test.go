package v1

import (
	"math/rand/v2"
	"net/url"
	"strings"
	"testing"
)

// TestHTTPEncodeQueryIsGos holds the pure HTTPEncodeQuery to the
// encoding it replaces, Go's url.QueryEscape of each name and value
// joined by "=" and "&", over seeded fields of every byte value. Go's is
// the reference: the encoding moved into a pure file (PureImports has no
// net/url), and a loopback client still sends what Go's escaping gives.
// Mutants: query-escape-tilde, query-space-percent (runtime/v1 ledger).
func TestHTTPEncodeQueryIsGos(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for seed := range 2000 {
		var fields []HTTPField
		var want []string
		for range r.IntN(4) {
			text := func() string {
				b := make([]byte, r.IntN(6))
				for i := range b {
					if r.IntN(2) == 0 {
						b[i] = byte(r.IntN(256))
					} else {
						b[i] = " ~-_.+&=%aZ9/"[r.IntN(13)]
					}
				}
				return string(b)
			}
			f := HTTPField{text(), text()}
			fields = append(fields, f)
			want = append(want, url.QueryEscape(f.Name)+"="+url.QueryEscape(f.Value))
		}
		if got := HTTPEncodeQuery(fields); got != strings.Join(want, "&") {
			t.Fatalf("case %d: HTTPEncodeQuery(%q) = %q, Go's gives %q", seed, fields, got, strings.Join(want, "&"))
		}
	}
}
