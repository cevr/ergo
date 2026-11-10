package check

import (
	"strings"
	"testing"
)

// TestGoErrorBuiltinRows holds the diagnostics of go.Is, go.As and the
// native go.Error (IG5; docs/spec/interop.md, Go errors): each row is one
// refusal's text at its use. The behaviour of admitted calls is
// TestGoErrorChainsAgree's (lower) and TestGoErrorChainsModel's
// (runtime/v1).
//
// Mutants: goAsTarget admits any type (string, go.Ref[url.URL]); the
// pointer-receiver rule of goAsTarget dropped (strconv.NumError by
// value, for which errors.As would never match); go.Error left comparable
// (a Map key; Go's == would compare the native error's identity); Text
// left addressable (the lowering reads it as a method).
func TestGoErrorBuiltinRows(t *testing.T) {
	const head = "import go strconv\nimport go net/url\n\nfunc f(e go.Error) needs go.Foreign {\n\t"
	rows := []struct{ body, want string }{
		{`let _ = go.As[string](e)`, "go.As[string]: string is not a Go type of import go"},
		{`let _ = go.As[go.Ref[url.URL]](e)`, "url.URL has no method Error() string"},
		{`let _ = go.As[strconv.NumError](e)`, "strconv.NumError implements error through a pointer receiver; write go.Ref[strconv.NumError]"},
		{`let _ = go.As(e)`, "go.As needs the Go type to find in the chain"},
		{`let _ = go.Is(e)`, "go.Is takes 2 go.Error arguments, not 1"},
		{`let _ = go.Is(e, "x")`, "takes only a string type, not go.Error"},
		{`go.Is(e, e)`, "the value of go.Is is not used"},
		{`let g = go.Is`, "go.Is is only called"},
		{`let _ = Map[go.Error, int]{}`, "map key type go.Error is not comparable"},
		{"let mut m = e\n\tm.Text = \"x\"", "cannot assign to m.Text"},
	}
	for _, r := range rows {
		_, diags := CheckSource(head + r.body + "\n}\n\nfunc main() {}\n")
		found := false
		var all []string
		for _, d := range diags {
			all = append(all, d.String())
			if strings.Contains(d.Message, r.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want a diagnostic containing %q, got %v", r.body, r.want, all)
		}
	}
	// The admitted forms check clean.
	_, diags := CheckSource(head + "let _ = go.Is(e, strconv.ErrSyntax)\n\tlet _ = go.As[go.Ref[strconv.NumError]](e)\n\tlet _ = go.As[go.Ref[url.Error]](e)\n\tlet _ = e == go.Error{Text: \"x\"}\n}\n\nfunc main() {}\n")
	if len(diags) > 0 {
		t.Errorf("admitted go.Is, go.As and ==: %v", diags)
	}
}
