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
			if sliceUnits(o.Text, m[0], m[1], enc) != "url" {
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

// TestProjectionIsTheWire catches r3b-reach (project every exported type,
// not the ones the operation types reach). Smallest repro of the R3' bug: a
// package that also exports a machine (or a float record no operation uses)
// failed its whole mapped file. No other test mixes a contract with
// exported types that are not on its wire.
func TestProjectionIsTheWire(t *testing.T) {
	const src = "type Spend { Cost int }\ntype Grant { Left int }\n" +
		"type Tank machine[Grant, Spend] { initial: Grant{Left: 1}, step: step }\n" +
		"func step(s Grant, e Spend) Decision[Grant, Grant] { return Ignore }\n" +
		"type Gauge { Ratio float64 }\n" +
		"type API { Take(s Spend) Grant }\n"
	a := sketchfront.Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatal(a.Diagnostics)
	}
	o, err := Project(a.Root().Package, a.Root().Files[0].Syntax, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export const Spend", "export const Grant", "export class API"} {
		if !strings.Contains(o.Text, want) {
			t.Fatalf("missing %q:\n%s", want, o.Text)
		}
	}
	for _, not := range []string{"Tank", "Gauge"} {
		if strings.Contains(o.Text, not) {
			t.Fatalf("%s is not on the wire but was projected:\n%s", not, o.Text)
		}
	}
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
