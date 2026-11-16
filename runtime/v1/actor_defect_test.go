package v1

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestDefectReasonIsTheReport: over drawn exits (typed failures,
// interruptions and defects of several panic values, in any order, some
// raised by an exit action), defectReason is the text the runtime's report
// writes after "panic: " for the exit's first defect, and "" for an exit
// with none (FK13-g1: the reason a crash view shows is the one the report
// would print).
func TestDefectReasonIsTheReport(t *testing.T) {
	seed := rand.Uint64()
	r := rand.New(rand.NewPCG(seed, 0))
	values := func() any {
		switch r.IntN(4) {
		case 0:
			return fmt.Sprintf("boom %d", r.IntN(100))
		case 1:
			return UnreachableAt(fmt.Sprintf("no route %d", r.IntN(100)), "a.ergo:9:3")
		case 2:
			return errors.New("an error value")
		default:
			return r.IntN(1000)
		}
	}
	for i := range 200 {
		var c rt.Cause
		for range r.IntN(4) {
			switch r.IntN(3) {
			case 0:
				c.Reasons = append(c.Reasons, rt.Reason{Kind: rt.KindFailure, Err: errors.New("typed")})
			case 1:
				c.Reasons = append(c.Reasons, rt.Reason{Kind: rt.KindInterruption})
			default:
				c.Reasons = append(c.Reasons, rt.Reason{Kind: rt.KindDefect, Value: values(), InRelease: r.IntN(2) == 0})
			}
		}
		err := error(&c)
		var out bytes.Buffer
		report(&out, err)
		want := ""
		for _, line := range strings.Split(out.String(), "\n") {
			if text, ok := strings.CutPrefix(line, "panic: "); ok {
				want = strings.TrimSuffix(text, " (in an exit action)")
				break
			}
		}
		if got := defectReason(err); got != want {
			t.Fatalf("case %d (seed %d): defectReason %q; the report says %q\nexit: %#v", i, seed, got, want, c.Reasons)
		}
	}
}
