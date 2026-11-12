package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestExitCodeIsRefined catches replacing the Code field's refined type
// with int: the old range check accepted a computed int at that field.
func TestExitCodeIsRefined(t *testing.T) {
	_, ds := check.CheckSource("import os\nfunc f(n int) os.Exit { os.Exit{Code: n, Message: \"x\"} }\n")
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "os.Code") {
		t.Fatalf("want a refined Code assignment refusal, got %s", describe(ds))
	}
	_, ds = check.CheckSource("import os\nfunc f() os.Exit { os.Exit{Code: 0, Message: \"x\"} }\n")
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "where is 1 <= it && it <= 125") {
		t.Fatalf("want the refinement diagnostic, got %s", describe(ds))
	}
}
