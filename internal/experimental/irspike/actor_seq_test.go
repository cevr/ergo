package irspike

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRActorChangesMatchSource is the seeded property over programs that
// iterate actor.Changes (GenerateActorSeq): std's Changes calls the
// runtime's ActorChanges, a std function whose Go result is iter.Seq[T],
// with two function operands the call adapts. Its body has an IR body
// that makes the Seq[T], and the program prints the same under both
// lowerings: the states the adapted functions make, a Seq made before the
// actor stops, a break. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because the Seq runs inside Go's range-over-func rewrite
// and the actor's task; only the compiled program iterates it.
//
// Mutants: adapted-seq-func-refused (a std function's Seq refused again,
// the gate before ADAPTED-RESULTS).
func TestIRActorChangesMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRActorChangesMatchSource", GenerateActorSeq, func(rep lower.IRReport) string {
		for _, f := range rep.IR {
			if strings.HasSuffix(f, "Changes") {
				return ""
			}
		}
		return "std's actor.Changes has no IR body"
	}, actorSeqFuncs...)
}
