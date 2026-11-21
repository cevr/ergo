package eval

import "testing"

// TestStdTextBuffersRunAtCheckTime: std civil's formats (an empty buffer
// from civil.buffer, Go's append of each field, civil.bytesText) run at
// check time and give the format's text only. Before MX-EVBUILDER the
// format was skipped ("a Go function (buffer)"). TestNativesAgreeWithRuntime
// holds the Builder's writes and IndexByte to runtime/v1 value for value;
// this is the one std path it cannot reach (civil's natives take a
// buffer the format then fills), and no example law reads a formatted
// date at check time. It kills evbuilder-civil-buffer-not-empty
// (testdata/mutants.json).
func TestStdTextBuffersRunAtCheckTime(t *testing.T) {
	pkg := checked(t, `import civil

test FormatsADate {
	assert civil.FormatDate(civil.FromUnixDays(0), civil.ISODate()) == "1970-01-01"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range rep.Results {
		if r.Name != "FormatsADate" {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("%s: %s (%s), want passed", r.Name, r.Status, r.Reason)
		}
	}
	if seen != 1 {
		t.Errorf("FormatsADate did not report: %+v", rep.Results)
	}
}
