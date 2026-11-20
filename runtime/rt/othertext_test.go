package rt_test

import (
	"net/netip"
	"net/url"
	"regexp"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestTextWritesAnyGoValue (binsize BS2): without the gofmt leaf, rt.Text
// writes a value of a Go type it does not know (not a text, number, error
// or Stringer), such as one of a pure-tier package of import go (net/url,
// net/netip) that a Go call panics with, as Go's runtime writes a panic of
// it: the type in parentheses and the data word in hex. So a program that
// imports go needs no fmt. Mutant: bs2-other-text-unknown (rt.Text writes
// the old placeholder instead).
func TestTextWritesAnyGoValue(t *testing.T) {
	prev := rt.UseHostText(nil)
	defer rt.UseHostText(prev)
	panicked := func(f func()) (v any) {
		defer func() { v = recover() }()
		f()
		return nil
	}
	for _, c := range []struct {
		value any
		want  string
	}{
		{panicked(func() { panic(url.URL{Host: "h"}) }), `^\(url\.URL\) 0x[0-9a-f]+$`},
		{panicked(func() { panic([2]netip.Addr{}) }), `^\(\[2\]netip\.Addr\) 0x[0-9a-f]+$`},
		{panicked(func() { panic([]string{"a"}) }), `^\(\[\]string\) 0x[0-9a-f]+$`},
		{panicked(func() { panic(url.Values{}) }), `^\(url\.Values\) 0x[0-9a-f]+$`},
	} {
		if got := rt.Text(c.value); !regexp.MustCompile(c.want).MatchString(got) {
			t.Errorf("Text(%T) = %q, want %s", c.value, got, c.want)
		}
	}
}
