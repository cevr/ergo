package selfhostcheck

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// TestFunctionClaims checks that a module can use a structural receiver
// through its declaring contract, and that invalid claims point to the clause.
// TestCompilerCheckAgrees runs the independent checker over the same files.
func TestFunctionClaims(t *testing.T) {
	root := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "agree", "func-claim")
	for _, tc := range []struct {
		dir  string
		code string
		text string
	}{
		{dir: "client"},
		{dir: "wrong-owner", code: "S108", text: "contract's package"},
		{dir: "own-type", code: "S108", text: "on the type"},
		{dir: "no-receiver", code: "S108", text: "first parameter"},
		{dir: "projection", code: "S106", text: "re-applies Self"},
		{dir: "incomplete", code: "S106", text: "no function"},
		{dir: "overlap", code: "S106", text: "overlaps"},
		{dir: "foreign-include", code: "S106", text: "in its declaring package"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			a, err := sketchfront.CheckDir(filepath.Join(root, tc.dir))
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" {
				if len(a.Diagnostics) != 0 {
					t.Fatalf("valid claims: %v", a.Diagnostics)
				}
				return
			}
			if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != tc.code || !strings.Contains(a.Diagnostics[0].Message, tc.text) {
				t.Fatalf("want one %s containing %q, got %v", tc.code, tc.text, a.Diagnostics)
			}
		})
	}
}
