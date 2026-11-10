package v1

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestReleaseErrorIsDefect is the seeded property of a frame's owned
// values (task.go, Owned; resources R5, R9, R10, R21, Q9) against a reference
// model of the frame's list. Each case registers values whose release
// succeeds, panics, or (OwnErr) returns nil or an error; hands some off
// (Hand); releases some early (Release, whose defect leaves the body at
// once); returns one (Returned); and ends with success, a typed failure or
// a defect. The model predicts the order of the releases and the exit:
// every release runs once, last registered first; a returned value is
// released only when the exit fails; each defect and each release error
// is kept, in release order, after the body's own reason. ERGO_SEED=n
// replays one seed; a failure prints the case.
//
// Mutants it catches that nothing else does (runtime/v1 has no other
// owner of the owned values): a release error dropped; a release defect stops the
// later releases; Returned released on success; Returned dropped on a
// failed exit; Hand leaves the entry; Release runs twice.
func TestReleaseErrorIsDefect(t *testing.T) {
	seeds := 400
	first := 1
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ERGO_SEED %q: %v", s, err)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+seeds; seed++ {
		c := genOwnCase(seed)
		got := c.run()
		want := c.model()
		if got != want {
			t.Fatalf("seed %d (ERGO_SEED=%d replays)\ncase: %s\ngot:  %s\nwant: %s", seed, seed, c, got, want)
		}
	}
}

// ownKind is how a value's release ends.
type ownKind uint8

const (
	relOK      ownKind = iota // a release that returns
	relPanic                  // a release that panics (a defect in Close)
	relErrNil                 // OwnErr, returning nil
	relErrFail                // OwnErr, returning an error (Q9)
)

type ownOp struct {
	what byte // 'o' own, 'h' hand, 'r' release early
	id   int
	kind ownKind
}

type ownCase struct {
	ops []ownOp
	ret int  // the returned value's id, -1 for none
	end byte // 's' success, 'f' typed failure, 'd' defect
}

func (c ownCase) String() string {
	var b strings.Builder
	for _, o := range c.ops {
		fmt.Fprintf(&b, "%c%d", o.what, o.id)
		if o.what == 'o' {
			fmt.Fprintf(&b, "/%d", o.kind)
		}
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "ret %d end %c", c.ret, c.end)
	return b.String()
}

func genOwnCase(seed int) ownCase {
	r := rand.New(rand.NewPCG(uint64(seed), 197))
	c := ownCase{ret: -1, end: "sfdx"[r.IntN(4)]}
	n := 1 + r.IntN(5)
	var live []int
	for id := 0; id < n; id++ {
		c.ops = append(c.ops, ownOp{what: 'o', id: id, kind: ownKind(r.IntN(4))})
		live = append(live, id)
		if r.IntN(4) == 0 && len(live) > 0 {
			k := r.IntN(len(live))
			what := byte('h')
			if r.IntN(2) == 0 {
				what = 'r'
			}
			c.ops = append(c.ops, ownOp{what: what, id: live[k]})
			live = append(live[:k], live[k+1:]...)
		}
	}
	if (c.end == 's' || c.end == 'x') && len(live) > 0 && r.IntN(2) == 0 {
		c.ret = live[r.IntN(len(live))]
	}
	return c
}

// run executes the case on a lowered-shape frame and renders the releases
// and the exit.
func (c ownCase) run() string {
	var log []string
	hs := map[int]*Owned{}
	body := func(s *Scope) (int, error) {
		for _, o := range c.ops {
			switch o.what {
			case 'o':
				id, kind := o.id, o.kind
				if kind == relErrNil || kind == relErrFail {
					hs[id] = OwnErr(s, Site(fmt.Sprint("x.ergo:", id)), func() error {
						log = append(log, fmt.Sprint("close ", id))
						if kind == relErrFail {
							return errors.New(fmt.Sprint("close fails ", id))
						}
						return nil
					})
					continue
				}
				h := s.Owner()
				Owning(h, id, func(v int) {
					log = append(log, fmt.Sprint("close ", v))
					if kind == relPanic {
						panic(fmt.Sprint("close panics ", v))
					}
				})
				hs[id] = h
			case 'h':
				Hand(hs[o.id], o.id)
			case 'r':
				hs[o.id].Release()
			}
		}
		if c.ret >= 0 {
			return Returned(hs[c.ret], c.ret), nil
		}
		switch c.end {
		case 'f':
			return 0, bad{"end"}
		case 'd':
			panic("body defect")
		}
		return -1, nil
	}
	var v int
	var err error
	root := rt.Root("main")
	if c.end == 'x' {
		root.Interrupt() // a stop is pending: registration still runs (R8)
	}
	p := recovered(func() { v, err = frameValue(root, body) })
	if p == rt.Interrupted {
		return strings.Join(log, ", ") + " | interrupted"
	}
	if p != nil {
		x, ok := p.(*exit)
		if !ok {
			return fmt.Sprintf("%s | raised %#v", strings.Join(log, ", "), p)
		}
		err = x.err
	}
	return strings.Join(log, ", ") + " | " + renderExit(v, err)
}

// renderExit names an exit by its reasons.
func renderExit(v int, err error) string {
	if err == nil {
		return fmt.Sprint("value ", v)
	}
	if f, ok := rt.Failure(err); ok {
		return "failure " + f.Error()
	}
	var parts []string
	for _, r := range reasons(err) {
		switch r.Kind {
		case rt.KindFailure:
			parts = append(parts, "failure "+r.Err.Error())
		case rt.KindDefect:
			val := r.Value
			if p, ok := val.(*Panic); ok {
				val = p.Value
			}
			if e, ok := val.(error); ok {
				val = e.Error()
			}
			parts = append(parts, fmt.Sprint("defect ", val))
		default:
			parts = append(parts, "interrupted")
		}
	}
	return strings.Join(parts, "; ")
}

// benchTrio is a record of three resources, as B2's generated release
// sees it.
type benchTrio struct{ a, b, c benchRes }

// BenchmarkReleaseParts is the plain generated release (B2) of a record
// of three resources and of a slice of eight, in the shape the lowering
// writes (OwningParts, one Drop, a Part per Close), against hand Go's
// deferred Closes in reverse order. P-L8's static path targets hand's
// numbers.
func BenchmarkReleaseParts(b *testing.B) {
	var n int
	root := rt.Root("main")
	b.Run("record/owned", func(b *testing.B) {
		for range b.N {
			frameNoRow(root, func(s *Scope) {
				h := s.Owner()
				OwningParts(h, benchTrio{benchRes{&n}, benchRes{&n}, benchRes{&n}}, func(v benchTrio, taken uint64) {
					d := s.Drop()
					if taken&4 == 0 {
						d.Part(func() { v.c.Close() })
					}
					if taken&2 == 0 {
						d.Part(func() { v.b.Close() })
					}
					if taken&1 == 0 {
						d.Part(func() { v.a.Close() })
					}
					d.End()
				})
			})
		}
	})
	b.Run("record/hand", func(b *testing.B) {
		for range b.N {
			func() {
				v := benchTrio{benchRes{&n}, benchRes{&n}, benchRes{&n}}
				defer v.a.Close()
				defer v.b.Close()
				defer v.c.Close()
			}()
		}
	})
	b.Run("slice/owned", func(b *testing.B) {
		for range b.N {
			frameNoRow(root, func(s *Scope) {
				h := s.Owner()
				OwningParts(h, make([]benchRes, 8), func(v []benchRes, _ uint64) {
					d := s.Drop()
					for i := len(v) - 1; i >= 0; i-- {
						e := v[i]
						d.Part(func() { e.n = &n; e.Close() })
					}
					d.End()
				})
			})
		}
	})
	b.Run("slice/hand", func(b *testing.B) {
		for range b.N {
			func() {
				v := make([]benchRes, 8)
				defer func() {
					for i := len(v) - 1; i >= 0; i-- {
						v[i].n = &n
						v[i].Close()
					}
				}()
			}()
		}
	})
}

// benchRes is a resource whose release counts.
type benchRes struct{ n *int }

func (r benchRes) Close() { *r.n++ }

// BenchmarkReleaseLocal is the plain lowering of one owned local (B1):
// a frame scope, the registration and the frame's release at its end,
// against hand Go's defer of Close (the "hand" sub-benchmark). P-L8's
// static path targets hand's numbers.
func BenchmarkReleaseLocal(b *testing.B) {
	var n int
	root := rt.Root("main")
	b.Run("owned", func(b *testing.B) {
		for range b.N {
			frameNoRow(root, func(s *Scope) {
				h := s.Owner()
				r := Owning(h, benchRes{&n}, func(v benchRes) { v.Close() })
				_ = r
			})
		}
	})
	b.Run("hand", func(b *testing.B) {
		for range b.N {
			func() {
				r := benchRes{&n}
				defer r.Close()
			}()
		}
	})
}

// BenchmarkReleaseLoop is one owned local per iteration, released at the
// iteration's end (R6), against hand Go's function literal per iteration
// with its defer.
func BenchmarkReleaseLoop(b *testing.B) {
	var n int
	root := rt.Root("main")
	b.Run("owned", func(b *testing.B) {
		frameNoRow(root, func(s *Scope) {
			for range b.N {
				h := s.Owner()
				Owning(h, benchRes{&n}, func(v benchRes) { v.Close() })
				h.Release()
			}
		})
	})
	b.Run("hand", func(b *testing.B) {
		for range b.N {
			func() {
				r := benchRes{&n}
				defer r.Close()
			}()
		}
	})
}

// model is the reference: the frame's list as a stack of ids.
func (c ownCase) model() string {
	var log, reasons []string
	var stack []int
	kinds := map[int]ownKind{}
	release := func(id int) {
		log = append(log, fmt.Sprint("close ", id))
		switch kinds[id] {
		case relPanic:
			reasons = append(reasons, fmt.Sprint("defect close panics ", id))
		case relErrFail:
			reasons = append(reasons, fmt.Sprintf("defect release of the value acquired at x.ergo:%d: close fails %d", id, id))
		}
	}
	remove := func(id int) {
		for i, x := range stack {
			if x == id {
				stack = append(stack[:i], stack[i+1:]...)
				return
			}
		}
	}
	var body []string // the body's own reason
	early := false
	for _, o := range c.ops {
		switch o.what {
		case 'o':
			kinds[o.id] = o.kind
			stack = append(stack, o.id)
		case 'h':
			remove(o.id)
		case 'r':
			remove(o.id)
			release(o.id)
			if len(reasons) > 0 {
				early = true // the release's defect leaves the body
			}
		}
		if early {
			break
		}
	}
	if early {
		body, reasons = reasons, nil
	} else {
		switch c.end {
		case 'f':
			body = []string{"failure bad end"}
		case 'd':
			body = []string{"defect body defect"}
		}
		if c.ret >= 0 {
			remove(c.ret)
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		release(stack[i])
	}
	all := append(body, reasons...)
	if c.end == 'x' {
		all = append(all, "interrupted") // the pending stop wins (Exit causes 5)
	}
	if !early && c.ret >= 0 {
		if len(all) == 0 {
			return strings.Join(log, ", ") + " | " + fmt.Sprint("value ", c.ret)
		}
		mark := len(reasons)
		release(c.ret) // the exit failed: the returned value is released last
		all = append(all, reasons[mark:]...)
	}
	exit := "value -1"
	switch {
	case len(all) == 1 && strings.HasPrefix(all[0], "failure "):
		exit = all[0]
	case len(all) > 0:
		exit = strings.Join(all, "; ")
	}
	return strings.Join(log, ", ") + " | " + exit
}
