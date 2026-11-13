package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestKeepingTypeReleasesWhatItKeeps is NP3c's model property (STD-NOPRIV;
// resources R13): a user package's operation type that keeps the values it
// takes declares the operation that gives them back, and its generated
// release calls that operation and releases each value it gives, last
// first, as queue.Queue[T]'s release does. Each seed generates one keeping
// type (its drain operation's name and its place among the operations are
// drawn, so the rule reads the shape, not a name) implemented over a std
// queue, and cases that push, pop and end in a drawn order. A reference
// model of the owned values gives the order in which each Res is released:
// a popped value when its taker ends, a Push after End at once, and what is
// left when the keeper's owner ends, last entered first. A failing seed
// prints the program, both orders and its replay (ERGO_KEEPING_SEED=n).
// Mutants: keeps-release-skips-user-type (the release of a type that is not
// runtime-backed calls no drain) leaves the kept values unreleased, and
// keeps-drain-read-by-name (only std types drain) refuses the program.
//
// It builds Go because the subject is the release the built program runs
// at the keeper's end; the checker sees only that a release exists.
func TestKeepingTypeReleasesWhatItKeeps(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if s := os.Getenv("ERGO_KEEPING_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_KEEPING_SEED=%q: %v", s, err)
		}
		seeds = []uint64{n}
	}
	var programs []*lowered
	wants := map[string][]string{}
	srcs := map[string]string{}
	for _, seed := range seeds {
		src, want := keepingProgram(rand.New(rand.NewPCG(seed, 0x6b656570)))
		name := fmt.Sprintf("keeping_%d", seed)
		programs = append(programs, mustLower(t, name, src))
		wants[name], srcs[name] = want, src
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for _, p := range programs {
		stdout, stderr, code := runExe(t, exes[p.name])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s\n%s", p.name, code, firstLines(stderr, 8), srcs[p.name])
		}
		got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if !slices.Equal(got, wants[p.name]) {
			t.Errorf("%s (replay with ERGO_KEEPING_SEED=%s):\ngot  %q\nwant %q\n%s", p.name, strings.TrimPrefix(p.name, "keeping_"), got, wants[p.name], srcs[p.name])
		}
	}
}

// keepingProgram is one seed's program and the lines the model says it
// prints: per case, "case k", then one "close id" per released Res in
// release order.
func keepingProgram(r *rand.Rand) (string, []string) {
	drainName := []string{"drain", "giveBack", "leftover"}[r.IntN(3)]
	ops := []string{
		"\tPush(v Res) fails queue.Closed takes v,\n",
		"\tPop() Res fails queue.Closed,\n",
		"\tEnd(),\n",
	}
	at := r.IntN(len(ops) + 1)
	ops = slices.Insert(ops, at, "\t"+drainName+"() []Res takes,\n")
	var b strings.Builder
	b.WriteString(`import queue
import sync

type Res { id int, log sync.Ref[[]int] }

func Close(r Res) {
	let id = r.id
	r.log.Update(func(xs []int) []int { xs.Append(id) })
}

type Keeper {
` + strings.Join(ops, "") + `}

type qk { q queue.Queue[Res] }

func Push(k qk, v Res) fails queue.Closed takes v { try k.q.Push(v) }

func Pop(k qk) Res fails queue.Closed { try k.q.Pop() }

func End(k qk) { k.q.End() }

func ` + drainName + `(k qk) []Res takes k {
	k.q.End()
	let mut out = []Res{}
	for {
		match k.q.Poll() {
		case Some{Value: v}:
			out = out.Append(v)
		case None:
			return out
		}
	}
	out
}

func consume(r Res) takes r {}

`)
	var want []string
	const cases = 6
	for k := range cases {
		n := 1 + r.IntN(8)
		fmt.Fprintf(&b, "func case%d(log sync.Ref[[]int]) {\n\tlet k Keeper = qk{q: queue.New[Res](%d)}\n", k, n)
		var kept, released []int
		ended := false
		id, pops := 0, 0
		for range n {
			switch c := r.IntN(3); {
			case c == 0 || (c == 1 && len(kept) == 0 && !ended):
				id++
				fmt.Fprintf(&b, "\tk.Push(Res{id: %d, log: log}) catch {\n\tcase queue.Closed:\n\t}\n", id)
				if ended {
					released = append(released, id)
				} else {
					kept = append(kept, id)
				}
			case c == 1:
				pops++
				fmt.Fprintf(&b, "\tlet r%d = k.Pop() catch {\n\tcase queue.Closed:\n\t\tRes{id: -1, log: log}\n\t}\n\tconsume(r%d)\n", pops, pops)
				if len(kept) > 0 {
					released = append(released, kept[0])
					kept = kept[1:]
				} else {
					released = append(released, -1)
				}
			default:
				b.WriteString("\tk.End()\n")
				ended = true
			}
		}
		b.WriteString("}\n\n")
		for i := len(kept) - 1; i >= 0; i-- {
			released = append(released, kept[i])
		}
		want = append(want, fmt.Sprintf("case %d", k))
		for _, x := range released {
			want = append(want, fmt.Sprintf("close %d", x))
		}
	}
	b.WriteString("func main() needs Console {\n")
	for k := range cases {
		fmt.Fprintf(&b, "\tlet log%d = sync.NewRef[[]int]([]int{})\n\tcase%d(log%d)\n\tConsole.Println(\"case %d\")\n\tfor id in log%d.Get() {\n\t\tConsole.Println(\"close \\(id)\")\n\t}\n", k, k, k, k, k)
	}
	b.WriteString("}\n")
	return b.String(), want
}
