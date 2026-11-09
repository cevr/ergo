package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestExternAllowlistExcept (std-align S3a): path/filepath is allowed, but
// its members that reach the host are refused as a host package is; a
// lexical member checks clean.
func TestExternAllowlistExcept(t *testing.T) {
	for _, name := range []string{"Abs", "EvalSymlinks", "Glob", "Walk", "WalkDir"} {
		target := "path/filepath." + name
		_, diags := check.CheckSource("extern go " + target + " func f(s string) string\nfunc Use() { _ = f(\"x\") }\n")
		if len(diags) != 1 || diags[0].Code != "S109" || !strings.HasPrefix(diags[0].Message, "extern go "+target+" is not on the reviewed pure list, so it may reach the host") {
			t.Errorf("%s: want one S109 with the host text, got\n\t%s", target, describe(diags))
		}
	}
	if _, diags := check.CheckSource("extern go path/filepath.Clean func f(s string) string\nfunc Use() { _ = f(\"x\") }\n"); len(diags) != 0 {
		t.Errorf("path/filepath.Clean: want clean, got\n\t%s", describe(diags))
	}
}
