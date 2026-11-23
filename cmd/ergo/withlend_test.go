package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
)

// TestWithResourceLending builds Go because a checked package can still
// omit a wrapped provider's law or fail to install a live held context
// when running the compiled law. The CLI also locates imported types and
// reports their diagnostics using module-relative source positions.
func TestWithResourceLending(t *testing.T) {
	needsBuild(t)
	const fixture = "cmd/ergo/testdata/withlend"
	root := filepath.Join(smokeWorkspace(t, fixture), filepath.FromSlash(fixture))
	binary := buildTestCLI(t)
	t.Setenv("ERGO_LAWCACHE", "off")

	check := func(t *testing.T, pkg string, wantCode int) report.CheckEnvelope {
		t.Helper()
		out, stderr, code := runSmokeCLI(t, binary, root, "", "check", "./"+pkg)
		var got report.CheckEnvelope
		if err := json.Unmarshal(out, &got); code != wantCode || err != nil {
			t.Fatalf("check %s: exit %d, want %d; decode: %v\n%s\n%s", pkg, code, wantCode, err, out, stderr)
		}
		if got.Checked != (wantCode == 0) || got.DiagnosticsTruncated {
			t.Fatalf("check %s: wrong verdict or truncated diagnostics\n%s", pkg, out)
		}
		return got
	}

	for _, tc := range []struct {
		pkg  string
		laws []string
	}{
		{"lending", []string{"ok law LendingKeepsTheOwner "}},
		{"held", []string{"ok law HeldWhileLent "}},
		{"wrapping", []string{"ok law res.Store.GetIsStable under base ", "ok law res.Store.GetIsStable under overMem "}},
	} {
		t.Run(tc.pkg, func(t *testing.T) {
			if got := check(t, tc.pkg, 0); len(got.Diagnostics) != 0 {
				t.Fatalf("check %s: want no diagnostics, got %+v", tc.pkg, got.Diagnostics)
			}
			stdout, stderr, code := runSmokeCLI(t, binary, root, "", "test", "./"+tc.pkg)
			if code != 0 {
				t.Fatalf("test %s: exit %d\n%s\n%s", tc.pkg, code, stdout, stderr)
			}
			for _, want := range tc.laws {
				if line, _ := verdictAt(string(stdout), want); line == "" {
					t.Errorf("test %s: missing %q\n%s", tc.pkg, want, stdout)
				}
			}
		})
	}

	t.Run("refused", func(t *testing.T) {
		got := check(t, "refused", 1).Diagnostics
		want := []struct {
			line, column int
			message      string
		}{
			{6, 3, "lent to the with at 5:2"},
			{14, 11, "lent to the with at 13:2"},
			{23, 15, "(resources R22); add ..Closer to Store in package withlend/res"},
		}
		if len(got) != len(want) {
			t.Fatalf("want exactly %d diagnostics, got %+v", len(want), got)
		}
		for i, w := range want {
			d := got[i]
			if d.Code != "S108" || d.File != "refused/refused.ergo" ||
				d.Span.Line != w.line || d.Span.Column != w.column || !strings.Contains(d.Message, w.message) {
				t.Errorf("diagnostic %d: got %+v, want S108 at refused/refused.ergo:%d:%d containing %q", i+1, d, w.line, w.column, w.message)
			}
		}
	})
}
