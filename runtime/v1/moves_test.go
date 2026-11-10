package v1

import "testing"

// TestMovesOnlyNamesBodies checks the reviewed table (moves.go): each
// name is a generic body of this package. It kills the mutant "a table
// entry names no body or a non-generic one" (a renamed body would leave a
// stale entry that admits nothing, or admit a body never reviewed).
func TestMovesOnlyNamesBodies(t *testing.T) {
	for name := range movesOnly {
		b, err := BodyOf(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(b.TypeParams) == 0 {
			t.Errorf("%s has no type parameter: the table lists generic bodies only", name)
		}
	}
	// The running table (B3t) names bodies too: a stale entry would admit
	// a running value into a with's result.
	for name := range running {
		if _, err := BodyOf(name); err != nil {
			t.Errorf("running %s: %v", name, err)
		}
	}
}
