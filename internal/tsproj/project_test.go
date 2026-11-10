package tsproj

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// TestProjectionSourceSpans catches r3-span (byte/UTF-16 confusion) and
// r3-alias (changed wire names claimed verbatim). Existing codec and RPC
// properties never consume mapper spans. The promised mapping form is the spec.
func TestProjectionSourceSpans(t *testing.T) {
	src := "// café 🧭\ntype Item { URL string, Count int }\ntype API { Read() Item }\n"
	a := sketchfront.Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatal(a.Diagnostics)
	}
	for _, enc := range []string{"utf-8", "utf-16"} {
		o, err := Project(a.Root().Package, a.Root().Files[0].Syntax, src, Options{Encoding: enc})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range o.Mappings {
			if sliceUnits(o.Text, m[0], m[1], enc) != "uRL" {
				continue
			}
			found = true
			if sliceUnits(src, m[2], m[3], enc) != "URL" || m[4] != 2 {
				t.Fatalf("%s: wrong alias %v", enc, m)
			}
		}
		if !found {
			t.Fatalf("%s: no mapped wire field: %s", enc, o.Text)
		}
	}
}

func sliceUnits(s string, start, length int, enc string) string {
	if enc == "utf-8" {
		return s[start : start+length]
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if i >= start && i < start+length {
			b.WriteRune(r)
		}
		i += n
	}
	return b.String()
}

// TestProjectionRefusesUnsupported catches r3-refusal (discard a projection
// error and publish incomplete schemas). No Go codec test checks admission
// to the narrower TypeScript wire profile. This row pins the diagnostic text.
func TestProjectionRefusesUnsupported(t *testing.T) {
	const src = "type Value { F float64 }\ntype API { Read() Value }\n"
	a := sketchfront.Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatal(a.Diagnostics)
	}
	_, err := Project(a.Root().Package, a.Root().Files[0].Syntax, src, Options{})
	if err == nil || !strings.Contains(err.Error(), "non-finite JSON float form needs an explicit schema") {
		t.Fatalf("projection error: %v", err)
	}
}
