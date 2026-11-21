package eval

import (
	"strings"
	"testing"
)

// TestErgoProvidersRunAtCheckTime: a derived provider whose field type is
// declared in Ergo (patterns.md, Provider plans 3) runs at check time as
// the test build runs it (lower/script.go): the literal makes each field
// with NewOp, a call records its arguments (the one argument, a tuple of
// several, NoArgs for none) and calls the answer; an operation with no
// result and no failure and no answer is only recorded, and any other
// with no answer is the defect "T[S]: Op has no answer". Before MX-VTLAWS
// each was skipped ("a literal of … yet"); the one example that uses it,
// particles, has no unanswered or several-argument operation, and its
// main never runs a provider, so the examples runner reaches none of the
// mutants it kills (testdata/mutants.json): vtlaws-provider-lit-skipped,
// vtlaws-provider-silent-defect, vtlaws-provider-args-first,
// vtlaws-provider-defect-text.
func TestErgoProvidersRunAtCheckTime(t *testing.T) {
	pkg := checked(t, `import sync

type Op[A, G] { calls sync.Ref[[]A], answer sync.Ref[Option[G]] }

type NoArgs {}

func NewOp[A, G]() Op[A, G] {
	Op[A, G]{calls: sync.NewRef[[]A]([]A{}), answer: sync.NewRef[Option[G]](None)}
}

func Answer[A, G](o Op[A, G], f G) {
	o.answer.Set(Some{f})
}

func Calls[A, G](o Op[A, G]) []A {
	o.calls.Get()
}

func Call[A, G](o Op[A, G], a A) Option[G] {
	o.calls.Update(func(v []A) []A { v.Append(a) })
	o.answer.Get()
}

type Store {
	Put(k string, v int),
	Get(k string) int,
	Size() int,
	Add(a int, b int) int,
}

type fake[S] derive Provider(Of[S](), Of[Op](), Of[NoArgs]())

func use() int needs Store {
	Store.Put("a", 1)
	Store.Get("ab") + Store.Size() + Store.Add(2, 3)
}

test Answered {
	let f = fake[Store]{}
	Answer(f.Get, func(k string) int { len(k) * 10 })
	Answer(f.Size, func() int { 100 })
	Answer(f.Add, func(a int, b int) int { a * b })
	let got = with context { Store: f } { use() }
	assert got == 20 + 100 + 6
	assert len(Calls(f.Put)) == 1 && len(Calls(f.Size)) == 1
	assert Calls(f.Get)[0] == "ab"
	assert Calls(f.Add)[0] == (2, 3)
}

test Unanswered {
	let f = fake[Store]{}
	let got = with context { Store: f } { use() }
	assert got == 0
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		status Status
		reason string
	}{
		"Answered":   {Passed, ""},
		"Unanswered": {Defect, "main.fake[main.Store]: Get has no answer"},
	}
	seen := 0
	for _, r := range rep.Results {
		w, ok := want[r.Name]
		if !ok {
			continue
		}
		seen++
		if r.Status != w.status || !strings.Contains(r.Reason, w.reason) {
			t.Errorf("%s: %s (%s), want %s with %q", r.Name, r.Status, r.Reason, w.status, w.reason)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of the %d tests reported: %+v", seen, len(want), rep.Results)
	}
}
