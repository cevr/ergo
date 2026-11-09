package v1

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The hosts property (mu-host; rule 16; the model
// docs/research/actor-machine/host.qnt): a seeded script of steps drives a
// root actor whose steps make hosts with NewHost, and a reference, the
// model's rules over the same script, says what each host must be after
// each step. A step enters a state with fresh hosts (each one filled by the
// entry's work before its first wait, after it, or never) and may carry
// the old state's hosts into the new one, stays with fresh hosts, or
// ignores the signal and drops the hosts it made. A work may also try to
// fill a carried host, which is a defect that ends the actor.
//
// What it holds, after the Ask that stepped each signal returned (so the
// entry is published):
//   - FillBeforePublish: each host the work fills before its first wait is
//     filled already;
//   - a host the work fills after its wait gives, by Get, a child of its
//     own entry;
//   - NoFillAfterEnd and GetSeesItsEntry: each host of an ended entry, a
//     carried one included, is closed, so Get fails Stopped; one the step
//     dropped is closed; one of the open entry that is not filled is open;
//   - a ChildIn on a carried host is the defect ActorHostDefect, with no
//     child started, and every host is closed after the end.
//
// Mutants (each killed by an assertion here): publishBeforeFill (open does
// not wait for the work), fillAfterEnd (fillable accepts any host), and
// getNewer (the end of an entry does not close its hosts, so a carried
// host is filled by the newer entry's work).

type hostChild = *Actor[int, int, int]

// hplan is what an entry's work does with a fresh host.
const (
	fillBefore = iota // fill before the work's first wait
	fillAfter         // fill after the work's first wait
	fillNever         // leave it empty
)

// hsig is a signal of the script.
type hsig struct {
	kind        int // decideEnter, decideStay or decideIgnore
	fresh       int // hosts the step makes
	plans       []int
	carry       bool // the new state keeps the old state's hosts
	fillCarried bool // the work fills a carried host: a defect
	gate        *Queue[int]
}

// hst is a state: its entry, its fresh hosts with their plans, the hosts
// it carried, and the gate its work waits on.
type hst struct {
	entry       int
	fresh       []*Host[hostChild]
	plans       []int
	carried     []*Host[hostChild]
	fillCarried bool
	gate        *Queue[int]
}

type hdec struct {
	kind int
	next hst
}

// peek is h's state now, with no wait.
func (h *Host[A]) peek() uint8 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

var hostStateName = map[uint8]string{hostMade: "made", hostOpen: "open", hostFilled: "filled", hostClosed: "closed"}

// refHost is the reference's record of one host.
type refHost struct {
	entry int   // the entry it belongs to; -1 for a dropped one
	want  uint8 // hostOpen, hostFilled or hostClosed
	after bool  // filled after its work's wait: Get waits for it
	why   string
}

func TestActorHostAgreesWithModel(t *testing.T) {
	seed := int64(20261109)
	if s := os.Getenv("ERGO_ACTOR_HOST_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	reached := map[string]bool{}
	kinds := []string{"fill before publish", "fill after wait", "carried closed", "stay host", "dropped host", "child of a carried host is a defect", "empty host closed at an end"}
	for n := 0; ; n++ {
		all := true
		for _, k := range kinds {
			all = all && reached[k]
		}
		if n >= 200 && all {
			t.Logf("seed %d: %d scripts", seed, n)
			return
		}
		if n >= 5000 {
			t.Fatalf("seed %d: after %d scripts some kinds were never reached: %v", seed, n, reached)
		}
		hostScript(t, seed, n, rng, reached)
	}
}

// hostScript runs one script and checks it against the reference.
func hostScript(t *testing.T, seed int64, n int, rng *rand.Rand, reached map[string]bool) {
	t.Helper()
	root := rt.Root("hosts")
	sl := releaser{make(chan sleep)}
	noTimer := func(hst) time.Duration { return ActorNoTimer() }
	childKind := func(d int) int { return d }
	childNext := func(s, _ int) int { return s }
	// made is the hosts the last step made: the runner writes it in the
	// step, and the test reads it after the Ask's answer.
	var made []*Host[hostChild]
	step := func(st *rt.Task, s hst, e hsig) (hdec, error) {
		fresh := make([]*Host[hostChild], e.fresh)
		for i := range fresh {
			fresh[i] = NewHost[hostChild](st)
		}
		made = fresh
		next := hst{entry: s.entry, fresh: fresh, plans: e.plans, gate: e.gate}
		if e.kind == decideEnter {
			next.entry = s.entry + 1
			next.fillCarried = e.fillCarried
			if e.carry {
				next.carried = append(append([]*Host[hostChild]{}, s.fresh...), s.carried...)
			}
		}
		return hdec{kind: e.kind, next: next}, nil
	}
	fill := func(wt *rt.Task, h *Host[hostChild], entry int) {
		MachineChild(Site("screen"), wt, sl, h, entry,
			func(*rt.Task, int, int) (int, error) { return decideIgnore, nil }, nil,
			func(int) time.Duration { return ActorNoTimer() },
			noWork[int], hasNoWork, runNoWork[int], childKind, childNext, func(*rt.Task, int, func(hsig)) {}, 1)
	}
	work := func(wt *rt.Task, s hst, _ func(hsig)) {
		// A pause that is no runtime wait, so a runner that publishes
		// before the work's first wait shows the entry before its fills.
		time.Sleep(100 * time.Microsecond)
		for i, h := range s.fresh {
			if s.plans[i] == fillBefore {
				fill(wt, h, s.entry)
			}
		}
		if s.fillCarried && len(s.carried) > 0 {
			fill(wt, s.carried[0], s.entry)
		}
		if _, err := s.gate.Pop(wt); err != nil {
			return
		}
		for i, h := range s.fresh {
			if s.plans[i] == fillAfter {
				fill(wt, h, s.entry)
			}
		}
		_, _ = s.gate.Pop(wt) // until the entry ends
	}
	a := MachineStart(Site("root"), root, sl, hst{entry: 0},
		step, nil, noTimer,
		func(s hst) hst { return s },
		func(s hst) bool { return s.gate != nil },
		work,
		func(d hdec) int { return d.kind },
		func(s hst, d hdec) hst {
			if d.kind == decideEnter || d.kind == decideStay {
				return d.next
			}
			return s
		},
		func(*rt.Task, hdec) {}, 2)

	ref := map[*Host[hostChild]]*refHost{}
	var order []*Host[hostChild]
	var trace []string
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d script %d: %s\ntrace:\n  %s", seed, n, fmt.Sprintf(format, args...), strings.Join(trace, "\n  "))
	}
	entry := 0
	var cur hst
	var curGate *Queue[int]
	defected := false
	steps := 2 + rng.Intn(5)
	for i := 0; i < steps && !defected; i++ {
		e := hsig{fresh: rng.Intn(3)}
		switch r := rng.Intn(10); {
		case r < 6:
			e.kind = decideEnter
			e.plans = make([]int, e.fresh)
			for j := range e.plans {
				e.plans[j] = rng.Intn(3)
			}
			e.carry = rng.Intn(3) == 0
			e.fillCarried = e.carry && len(cur.fresh)+len(cur.carried) > 0 && rng.Intn(4) == 0
			e.gate = New[int](2)
		case r < 8:
			e.kind = decideStay
			e.gate = cur.gate
		default:
			e.kind = decideIgnore
			e.gate = cur.gate
		}
		trace = append(trace, fmt.Sprintf("step %d: kind %d fresh %d plans %v carry %v fillCarried %v", i, e.kind, e.fresh, e.plans, e.carry, e.fillCarried))
		var raised any
		func() {
			defer func() { raised = recover() }()
			if _, err := a.Ask(root, e); err != nil {
				raised = err
			}
		}()
		s := a.State(root)
		// The reference: the old entry's hosts close at an Enter.
		if e.kind == decideEnter {
			for _, h := range order {
				if r := ref[h]; r.entry == entry && r.want != hostClosed {
					r.want, r.why = hostClosed, "its entry ended"
				}
			}
			if len(s.carried) > 0 {
				reached["carried closed"] = true
			}
			entry++
			if curGate != nil {
				curGate.Close()
			}
			curGate = e.gate
		}
		fresh := made
		made = nil
		for j, h := range fresh {
			r := &refHost{entry: entry, want: hostOpen, why: "an open entry's empty host"}
			switch e.kind {
			case decideIgnore:
				r.entry, r.want, r.why = -1, hostClosed, "its step entered no state"
				reached["dropped host"] = true
			case decideStay:
				reached["stay host"] = true
			default:
				switch e.plans[j] {
				case fillBefore:
					r.want, r.why = hostFilled, "its work fills it before its first wait"
				case fillAfter:
					r.after = true
				}
			}
			ref[h] = r
			order = append(order, h)
		}
		if e.kind == decideEnter && e.fillCarried {
			// The ChildIn on a carried host is a defect: the actor ends.
			defected = true
			<-a.done
			for _, h := range order {
				ref[h].want, ref[h].why = hostClosed, "the actor ended"
			}
			func() {
				defer func() { raised = recover() }()
				_ = a.Stop()
			}()
			var text strings.Builder
			if x, ok := raised.(*exit); ok {
				report(&text, x.err)
			}
			if !strings.Contains(text.String(), "the host is not an empty host of this work's entry") {
				fail("a ChildIn on a carried host: Stop raised %v, want ActorHostDefect", raised)
			}
			reached["child of a carried host is a defect"] = true
		} else if raised != nil {
			fail("the Ask raised %v", raised)
		}
		if e.kind == decideEnter {
			cur = s
		} else if e.kind == decideStay {
			cur = s
		}
		hostCheck(root, ref, order, fail, reached)
		if !defected && e.kind == decideEnter {
			// Open the gate: the work fills its fillAfter hosts, and Get
			// waits for each one.
			_ = curGate.Push(root, 1)
			for _, h := range fresh {
				if r := ref[h]; r.after {
					c, err := h.Get(root)
					if err != nil {
						fail("Get of a host its work fills after its wait failed %v", err)
					}
					if got := c.State(root); got != r.entry {
						fail("Get of a host of entry %d gave a child of entry %d", r.entry, got)
					}
					r.want, r.after = hostFilled, false
					reached["fill after wait"] = true
				}
			}
		}
	}
	if !defected {
		_ = a.Stop()
		for _, h := range order {
			if ref[h].want == hostOpen {
				reached["empty host closed at an end"] = true
			}
			ref[h].want, ref[h].why = hostClosed, "the actor ended"
		}
		hostCheck(root, ref, order, fail, reached)
	}
}

// hostCheck holds each host to the reference: its state, and for a filled
// one the entry of the child Get gives.
func hostCheck(root *rt.Task, ref map[*Host[hostChild]]*refHost, order []*Host[hostChild], fail func(string, ...any), reached map[string]bool) {
	for i, h := range order {
		r := ref[h]
		if r.after {
			continue // its work fills it once the gate opens
		}
		got := h.peek()
		if got != r.want {
			fail("host %d (entry %d) is %s, want %s: %s", i, r.entry, hostStateName[got], hostStateName[r.want], r.why)
		}
		switch r.want {
		case hostFilled:
			c, err := h.Get(root)
			if err != nil {
				fail("Get of a filled host failed %v", err)
			}
			if e := c.State(root); e != r.entry {
				fail("host %d of entry %d gave a child of entry %d", i, r.entry, e)
			}
			if r.why == "its work fills it before its first wait" {
				reached["fill before publish"] = true
			}
		case hostClosed:
			if _, err := h.Get(root); err == nil {
				fail("Get of host %d (entry %d) gave a child after %s", i, r.entry, r.why)
			}
		}
	}
}
