package lower

import (
	"bytes"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
)

// vouchedProgram passes a live []byte to bytes.IndexByte, whose binding
// carries a vouched `reads b` (its body is assembly Go's analysis cannot
// see).
const vouchedProgram = `import go bytes

func find(b []byte, c byte) int needs go.Foreign {
	bytes.IndexByte(b, c) + len(b)
}

func main() needs Console | go.Foreign {
	Console.Println("\(find([]byte{120, 97, 121}, byte(97)))")
}
`

// TestVouchedReadsActOnlyValidated is LF2's admission (LAWFFI r2 §1;
// interop.md, Foreign facts 7): a vouched reads fact skips the copy in the
// run build exactly when the shipped ledger holds its guard's line current
// and validated by a named run; a missing, stale, unguarded or tested line
// keeps the copy; a current refuted line stops the build at the call. The
// test build copies and compares at every level that acts. The lowered
// text is the check because the copy's absence is the promise.
//
// Mutants it alone catches: ffi-vouch-unguarded-acts (a vouched fact acts
// with no ledger line), ffi-vouch-stale-hash-acts (with a stale line) and
// ffi-vouch-refuted-acts (a refuted line is not a refusal).
func TestVouchedReadsActOnlyValidated(t *testing.T) {
	file, err := syntax.Parse(vouchedProgram)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	var row gofacts.Row
	for _, r := range gofacts.Of("bytes.IndexByte") {
		row = r
	}
	if row.Target == "" {
		t.Fatal("no vouched row for bytes.IndexByte")
	}
	current := LedgerEntry{Fact: row.Key(), Verdict: "validated", By: "guard", With: toolchain.Version, Digest: row.Digest(toolchain.Version)}
	stale := current
	stale.Digest = row.Digest("go1.0")
	old := current
	old.With = "go1.0"
	unnamed := current
	unnamed.By = ""
	tested := current
	tested.Verdict = "tested"
	refuted := current
	refuted.Verdict = "refuted"
	saved := shippedLedger
	t.Cleanup(func() { shippedLedger = saved })
	clones := func(o *lowered, s string) int {
		n := 0
		for _, f := range o.files {
			if strings.HasSuffix(f.Path, ".go") {
				n += bytes.Count(f.Data, []byte(s))
			}
		}
		return n
	}
	for _, c := range []struct {
		name  string
		entry *LedgerEntry
		takes bool
	}{
		{"missing", nil, false},
		{"stale", &stale, false},
		{"other toolchain", &old, false},
		{"no run named", &unnamed, false},
		{"tested", &tested, false},
		{"validated", &current, true},
	} {
		entries := map[string]LedgerEntry{}
		if c.entry != nil {
			entries[c.entry.Key()] = *c.entry
		}
		shippedLedger = func() string { return FormatLedger(entries) }
		u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: vouchedProgram}}}
		run := mustLowerUnit(t, "vouched_run", u, false)
		if n := clones(run, "Clone("); (n == 0) != c.takes {
			t.Errorf("%s: the run build copies %d operands of bytes.IndexByte; the fact acts: %v, want %v", c.name, n, n == 0, c.takes)
		}
		test := mustLowerUnit(t, "vouched_test", u, true)
		if n := clones(test, ".GoReadsCheck("); (n == 1) != c.takes {
			t.Errorf("%s: the test build compares %d operands after the call; want one exactly when the fact acts (%v)", c.name, n, c.takes)
		}
	}
	entries := map[string]LedgerEntry{refuted.Key(): refuted}
	shippedLedger = func() string { return FormatLedger(entries) }
	_, err = Lower(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: vouchedProgram}}})
	const want = "bytes.IndexByte is vouched to only read b, and its guard refuted it"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a refuted guard line: the build gives %v, want it stopped with %q", err, want)
	}
}
