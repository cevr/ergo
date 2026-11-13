package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// genHandleRun writes one admitted program over a declared root that holds
// handles (EXH1; resources R17, R39), for TestOwnershipRunModel: a Board
// that implements Closer and holds a sync.Ref, a slice of them and an int;
// main calls functions that read a handle while the board is lent (an
// operation call, a pass to a lending function, a for-in), write the
// borrowed board in place, and take the board and update it (Board{..b}).
// The reference is the board's state, printed by each step; the board's
// release prints its state once, at main's end. A second owner's release
// would print a second close, or a step after it would read -1; a release
// of the value the board was registered with, not the board written in
// place, prints a stale close (a frame with b.Bump() takes the list form,
// since a static slot is a copy).
func genHandleRun(seed int) (string, string) {
	r := rand.New(rand.NewPCG(uint64(seed), 0xe4a2))
	var src, want strings.Builder
	src.WriteString(`import sync

type Board { h sync.Ref[int], hs []sync.Ref[int], n int } implements Closer

func Close(b Board) needs Console {
	Console.Println("close \(b.h.Get()) \(len(b.hs)) \(b.n)")
	b.h.Set(-1)
}

func Read(b Board) int { b.h.Get() }

func peek(h sync.Ref[int]) int { h.Get() }

func Pass(b Board) int { peek(b.h) }

func Sum(b Board) int {
	let mut s = 0
	for x in b.hs {
		s = s + x.Get()
	}
	s
}

func Bump(b *Board) {
	b.h.Set(b.h.Get() + 1)
	b.hs = b.hs.Append(sync.NewRef[int](b.n))
	b.n = b.n + 1
}

func Renew(b Board) Board takes b {
	let n = b.n + 10
	Board{..b, n: n}
}

func main() needs Console {
`)
	var ops strings.Builder
	mut := false
	h, n := 0, 0
	var hs []int
	for range 3 + r.IntN(6) {
		switch r.IntN(5) {
		case 0:
			ops.WriteString("\tConsole.Println(\"read \\(Read(b))\")\n")
			fmt.Fprintf(&want, "read %d\n", h)
		case 1:
			ops.WriteString("\tConsole.Println(\"pass \\(Pass(b))\")\n")
			fmt.Fprintf(&want, "pass %d\n", h)
		case 2:
			ops.WriteString("\tConsole.Println(\"sum \\(Sum(b))\")\n")
			s := 0
			for _, v := range hs {
				s += v
			}
			fmt.Fprintf(&want, "sum %d\n", s)
		case 3:
			ops.WriteString("\tb.Bump()\n")
			mut = true
			h++
			hs = append(hs, n)
			n++
		case 4:
			ops.WriteString("\tb = Renew(b)\n")
			mut = true
			n += 10
		}
	}
	let := "let"
	if mut {
		let = "let mut"
	}
	fmt.Fprintf(&src, "\t%s b = Board{h: sync.NewRef[int](0), hs: []sync.Ref[int]{}, n: 0}\n%s}\n", let, ops.String())
	fmt.Fprintf(&want, "close %d %d %d\n", h, len(hs), n)
	return src.String(), want.String()
}
