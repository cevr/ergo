package v1

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// WithAcquire must use the with's existing release list and rt's acquiring
// region. Stop during a successful open still closes it; a failure or
// defect closes only prior successes. Nested masks restore their caller.
func TestWithAcquireOutcomes(t *testing.T) {
	for depth := 0; depth <= 3; depth++ {
		for _, stop := range []bool{false, true} {
			for _, outcome := range []string{"success", "failure", "defect", "transfer"} {
				t.Run(fmt.Sprintf("depth%d/stop%v/%s", depth, stop, outcome), func(t *testing.T) {
					var r recorder
					err := runMain(func(root *rt.Task) error {
						for range depth {
							m := root.Mask()
							defer m.Unmask()
						}
						return frame(root, func(s *Scope) error {
							w := s.OpenWith()
							if err := w.WithAcquire(func() (func(), error) {
								return func() { r.add("A") }, nil
							}); err != nil {
								return err
							}
							err := w.WithAcquire(func() (func(), error) {
								if !root.Masked() {
									t.Error("constructor is outside the acquisition region")
								}
								if stop {
									root.Interrupt()
									root.Check()
								}
								switch outcome {
								case "failure":
									// A partial result accompanying a failure is not acquired.
									return func() { r.add("BAD") }, bad{"constructor"}
								case "defect":
									panic("open defect")
								case "transfer":
									return nil, nil
								}
								return func() { r.add("B") }, nil
							})
							if root.Masked() != (depth > 0) {
								t.Error("acquisition did not restore the enclosing mask")
							}
							wantEntries := 1
							if outcome == "success" {
								wantEntries++
							}
							if w.n != wantEntries {
								t.Errorf("with holds %d releases, want %d in its one list", w.n, wantEntries)
							}
							if err != nil {
								return err
							}
							root.Check()
							w.EndWith(nil)
							return nil
						})
					})
					want := "A"
					if outcome == "success" {
						want = "B A"
					}
					if got := r.String(); got != want {
						t.Errorf("closed %q, want %q", got, want)
					}
					if outcome == "defect" {
						if err == nil || !strings.Contains(err.Error(), "open defect") {
							t.Errorf("lost constructor defect: %v", err)
						}
					} else if outcome == "failure" {
						if err == nil || !strings.Contains(err.Error(), "constructor") {
							t.Errorf("lost constructor failure: %v", err)
						}
					} else if stop != (err != nil) {
						t.Errorf("stop=%v exit=%v", stop, err)
					}
				})
			}
		}
	}
}
