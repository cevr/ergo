package lower

import (
	"strings"
	"testing"
)

// tupleResultSource has coord's shape (stores EX5a, memory/leases.ergo):
// sync.Modify's literal returns (stepped, the next state), with stepped an
// 88-byte union past the register bound; and a second union of the same
// size that a literal returns alone to tasks.Transform.
const tupleResultSource = `import sync
import tasks

type Grant { Holder string, Lease string, At int, Until int, Seq int, Turn int }

type stepped = moved{Grant Grant} | refused{Why string} | idle{}

type mapped = got{Grant Grant} | missed{Why string}

func turn(r sync.Ref[int], i int) stepped {
	sync.Modify(r, func(n int) (stepped, int) {
		if i % 2 == 0 { (refused{Why: "busy"}, n) } else { (moved{Grant: Grant{Holder: "h", Lease: "l", At: n, Until: n, Seq: i, Turn: n}}, n + 1) }
	})
}

func main() needs Console {
	let r = sync.NewRef[int](0)
	let out = tasks.Transform([]int{1, 2}, 1, func(k int) mapped {
		if k == 1 { missed{Why: "no"} } else { got{Grant: Grant{Holder: "h", Lease: "l", At: k, Until: k, Seq: k, Turn: k}} }
	})
	match turn(r, 1) {
	case moved as m:
		Console.Println("\(m.Grant.Seq) \(len(out))")
	case refused, idle:
		Console.Println("none")
	}
}
`

// TestTupleResultIsNotCarried is MSB3b's promised form (carry.go,
// tupleCarry): a union in a function value's tuple result is flat past the
// register bound, as sync.Modify's literal returns coord's stepped; a union
// a function value returns alone is carried, so past the register bound
// it stays an interface (tasks.Transform's mapped). Measured both ways in
// tupleCarry's comment. Mutant: msb3b-tuple-carried.
func TestTupleResultIsNotCarried(t *testing.T) {
	p := mustLower(t, "tupleresult", tupleResultSource)
	if !strings.Contains(p.mainGo(), "type stepped struct {") {
		t.Errorf("stepped, in sync.Modify's tuple result, is not flat")
	}
	if !strings.Contains(p.mainGo(), "type mapped interface{") {
		t.Errorf("mapped, a literal's lone result past the register bound, is not an interface")
	}
	typecheckGo(t, []*lowered{p})
}
