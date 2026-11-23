package eval

import "testing"

// TestBuilderGrowsAtCheckTime: std html.Escape grows its Builder
// (strings.Grow, the operation bs.Grow, CollGrow) before it writes, and
// runs at check time as the build's. It kills evgrow-unmapped: without
// collNatives' CollGrow entry the call has no native and the test is
// skipped. (The row's old killer, TestQueryParsesAtCheckTime, stopped
// reaching Grow when std http's query parse was rewritten over std url.)
func TestBuilderGrowsAtCheckTime(t *testing.T) {
	pkg := checked(t, `import html

test Escapes {
	assert html.Escape("<a href='x'>&</a>") == "&lt;a href=&#39;x&#39;&gt;&amp;&lt;/a&gt;"
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range rep.Results {
		if r.Name != "Escapes" {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("Escapes: %s (%s), want passed", r.Status, r.Reason)
		}
	}
	if seen != 1 {
		t.Errorf("Escapes did not report: %+v", rep.Results)
	}
}
