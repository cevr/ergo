package eval

import "testing"

// TestMappedSizeEvaluates is the smallest repro of a fixed bug (CAP-FILE):
// the evaluator gave fs.Mapped only Text and Close, so a law that read
// Size or PhysicalPath of a Mapped held in memory (fs.MapText) stopped
// `ergo check` with an internal error instead of a verdict. It found the
// bug through a program that provides fs/mapped.Maps, whose laws read
// both. Mutant: file-eval-size-missing.
func TestMappedSizeEvaluates(t *testing.T) {
	const src = `import fs

law MapTextSize(s string) {
	let m = fs.MapText(s)
	let ok = m.Size() == len(s) && m.PhysicalPath() == ""
	m.Close()
	ok
}

func main() {}
`
	rep := evalSeeded(t, src, 7)
	if len(rep.Results) != 1 {
		t.Fatalf("want one result, got %+v", rep.Results)
	}
	if r := rep.Results[0]; r.Status != Passed {
		t.Errorf("%s: %s (%s), want passed", r.Name, r.Status, r.Reason)
	}
}
