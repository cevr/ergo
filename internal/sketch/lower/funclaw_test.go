package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFuncLawEvidenceKeysReachedBodies is the key of a top-level rewrite
// law's ledger entry (LF6a; owner q195: a law over Ergo functions is
// admitted, keyed by the content of every body both sides reach). From a
// program whose laws have current validated entries, each edit of one
// function's body (parentheses around it: new tokens, the same value), or
// of the toolchain that keys a Go function, keeps a law admitted exactly
// when the law does not reach what changed. HT is over Ergo bodies only
// and reaches dup through Twice; UT reaches strings.ToUpper through Up.
//
// Mutant: lf6-func-law-bodies-unkeyed (the digest holds the law's own
// tokens only). The toolchain edit holds that an import go function is
// keyed (LFL's lfl-foreign-key-dropped).
func TestFuncLawEvidenceKeysReachedBodies(t *testing.T) {
	funcs := []struct{ name, sig, body string }{
		{"dup", "(s string) string", "s + s"},
		{"Twice", "(s string) string", "dup(s)"},
		{"Half", "(s string) string", "s[:len(s) / 2]"},
		{"Up", "(s string) string", "strings.ToUpper(s)"},
		{"Other", "(s string) string", "s + \"o\""},
	}
	reach := map[string]map[string]bool{
		"HT": {"dup": true, "Twice": true, "Half": true},
		"UT": {"Up": true, "go": true},
	}
	source := func(edit string) string {
		var b strings.Builder
		b.WriteString("import go strings\n\n")
		for _, f := range funcs {
			body := f.body
			if f.name == edit {
				body = "(" + body + ")"
			}
			b.WriteString("func " + f.name + f.sig + " { " + body + " }\n\n")
		}
		b.WriteString("law HT(s string) { Half(Twice(s)) } == { s }\n\nlaw UT(s string) { Up(Up(s)) } == { Up(s) }\n\nfunc main() {\n\tlet _ = Other(\"x\")\n}\n")
		return b.String()
	}
	keysAt := func(toolchain string) ForeignKeys {
		return func(target string) ForeignKey { return ForeignKey{Key: "go " + toolchain, Shown: toolchain} }
	}
	laws := func(src string) map[string]*check.Law {
		pkg, diags := check.CheckSource(src)
		if len(diags) > 0 {
			t.Fatalf("%v\n%s", diags, src)
		}
		out := map[string]*check.Law{}
		for _, l := range pkg.Laws {
			out[l.Name] = l
		}
		return out
	}
	base := laws(source(""))
	entries := map[string]LedgerEntry{}
	for name, l := range base {
		if l.FuncRewrite == nil {
			t.Fatalf("law %s is not in the rewrite's scope", name)
		}
		digest, with, _ := FuncLawDigest(l, keysAt("go1.27.4"))
		e := LedgerEntry{Law: l.Pkg.Path + "." + name, Verdict: "validated", By: "native", With: with, Digest: digest}
		entries[e.Key()] = e
	}
	if entries[base["UT"].Pkg.Path+".UT"].With != "go1.27.4" || entries[base["HT"].Pkg.Path+".HT"].With != "" {
		t.Fatalf("the foreign keys: %v", entries)
	}
	edits := []string{"go"}
	for _, f := range funcs {
		edits = append(edits, f.name)
	}
	for _, edit := range append([]string{""}, edits...) {
		src, toolchain := source(edit), "go1.27.4"
		if edit == "go" {
			src, toolchain = source(""), "go1.28.0"
		}
		for name, l := range laws(src) {
			e := NewLedgerEvidence(l.Pkg, LedgerNames{}, entries, keysAt(toolchain))
			got, why := e.AdmitFunc(l)
			if want := !reach[name][edit]; got != want {
				t.Errorf("edit of %q: law %s admitted %v (%s), want %v", edit, name, got, why, want)
			}
		}
	}
}
