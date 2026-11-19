package irspike

import "testing"

// TestIRMonoCopiesMatchSource is the seeded property over monomorphic
// copies (FLIP F7 PORT-COPY, GenerateMonoCopies): each program builds and
// prints the same under both lowerings, and each copy has an IR body
// under its own name. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because a copy's body and its function's are two Go
// declarations, and a method's copy takes its receiver after the
// environment: only the Go build of the IR file shows a body under the
// wrong name or with its parameters in the method's order.
func TestIRMonoCopiesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRMonoCopiesMatchSource", GenerateMonoCopies, nil, "countMono_i1", "countMono_i2", "pairsMono_i1", "BoxPickMono_i1", "BoxPickMono_i2")
}
