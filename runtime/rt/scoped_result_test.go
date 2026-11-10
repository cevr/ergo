package rt

import (
	"errors"
	"math/rand"
	"testing"
)

// TestScopedResultReleaseModel checks conservation at the public scope boundary.
// Its unique mutant drops a returned aggregate when a scope release fails;
// existing scope tests have only plain results and cannot see that leak.
func TestScopedResultReleaseModel(t *testing.T) {
	for seed := int64(0); seed < 160; seed++ {
		r := rand.New(rand.NewSource(seed))
		bodyFails, bodyPanics := r.Intn(2) == 0, r.Intn(2) == 0
		stop, releasePanics, resultPanics := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		task := Root("scoped-result")
		closed, masked := 0, false
		// A record holding a resource needs generic glue, not a Close assertion.
		type aggregate struct{ id int64 }
		v, err := Scoped(task, func(v aggregate) {
			if v.id != seed+1 {
				t.Fatalf("seed %d: released failed candidate %+v", seed, v)
			}
			closed++
			masked = task.Masked()
			task.Check()
			if resultPanics {
				panic("result release")
			}
		}, func(s *Scope) (aggregate, error) {
			s.Defer(func() {
				if stop {
					task.Interrupt()
				}
				if releasePanics {
					panic("scope release")
				}
			})
			if bodyPanics {
				panic("body")
			}
			if bodyFails {
				return aggregate{}, errors.New("body")
			}
			return aggregate{seed + 1}, nil
		})
		returned := !bodyFails && !bodyPanics
		failed := bodyFails || bodyPanics || stop || releasePanics
		want := 0
		if returned && failed {
			want = 1
		}
		if closed != want || closed > 0 && !masked || (err != nil) != failed || task.Masked() {
			t.Fatalf("seed %d: closed=%d want=%d masked=%v exit=%v", seed, closed, want, masked, err)
		}
		if failed && v.id != 0 || !failed && v.id != seed+1 {
			t.Fatalf("seed %d: value %+v, failed=%v", seed, v, failed)
		}
		if want > 0 && resultPanics {
			found := false
			var cause Cause
			cause.Add(err)
			for _, reason := range cause.Reasons {
				found = found || reason.Kind == KindDefect && reason.InRelease && reason.Value == "result release"
			}
			if !found {
				t.Fatalf("seed %d: lost result-release defect: %v", seed, err)
			}
		}
	}
}
