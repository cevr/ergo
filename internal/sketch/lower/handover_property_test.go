package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The hand-over's seeded property (OWN1; handover.go). Each seed writes a
// program over a record Bag{Items []int, N int} that grows, shares, cuts
// and rebinds values through every site the three shortcuts decide:
//
//   - push(b, v), an Append on a parameter's field: a one-extender site
//     (own-lineage), called on the newest value of a line of pushes and on
//     an older one (two extenders of one array), on a cut b.Items[0:k] and
//     on a let mut root;
//   - the same Append written inline on a let that stays live;
//   - grow(b, v), an Append on a field of a let mut copy that is dead after
//     it (own-field);
//   - doc = slices.Concat(doc, x.Items) on a let mut root, and Concat on a
//     dead local and on a live let (own-consume, and its refusal);
//   - writes of an element of a let mut root that took a pushed value, so
//     a root that shared a one-extender array shows it;
//   - shrinks (SL3, own-reslice): the root cut from both ends then grown
//     and written; a let that keeps a window of the root while the root
//     is cut back to the window's start and grows, so an Append in place
//     would write the window's first row; and a let mut local taken from
//     a live let, cut from both ends, grown and written;
//   - consume by parts (REUSE RU1; parts.go): a literal and an update
//     that move b.Items through a user operation (lit, upd) or the
//     prelude's Append (updRaw), a read of the consumed path after and
//     before the consuming call (after, before), one path consumed twice
//     (two), a whole return beside a consuming route (keepWhole) and after the
//     bind (late), a path consumed under a field the update keeps (side),
//     each on the root and on a let that stays live; and the root's
//     update stored back into it (own-update-self), whose values read the
//     old fields;
//   - a hand-over across a handled failure (REUSE RU2): m = capped(m, v,
//     cap) catch { … }, whose callee fails before it writes its owned
//     parameter, with an arm that yields a fresh value, yields m itself,
//     or keeps m in another binding first, or yields a let mut that
//     stays live (so the join copies it); and a callee that fails after
//     it writes through a nested owning call (cappedLate, which does not
//     own b, so the arm's m is the old value).
//
// The generator is also the model: it runs each statement on Go values
// with value semantics (every bind copies) and computes the line the
// program prints. The program is lowered three ways: the rule (the run
// build, which takes the shortcuts), the copying lowering (valueLowering
// "copy", the reference, which takes none) and the test build, whose
// checking forms copy and poison; the first two print the model's line,
// and the test build's test asserts it.
//
// A failure prints the seed, the Ergo source and both outputs.
// Replay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=N
var handoverSeed = flag.Uint64("handover.seed", 0, "run TestHandoverAgrees on this seed alone")

// hbag is a model Bag.
type hbag struct {
	items []int
	n     int
}

func (b hbag) copy() hbag { return hbag{items: append([]int(nil), b.items...), n: b.n} }

// hdig is the program's dig on the model: the length and a hash of xs.
func hdig(xs []int) string {
	s := 0
	for _, x := range xs {
		s = (s*31 + x) % 1000003
	}
	return fmt.Sprintf("%d/%d", len(xs), s)
}

// handoverGen writes one program and runs it on the model.
type handoverGen struct {
	r       *rand.Rand
	b       strings.Builder
	out     strings.Builder // the model's output
	bags    []hbag          // the lets b0, b1, … of run
	m       hbag            // the root m
	doc     []int           // the root doc
	store   []hbag          // the root st's bags
	reads   int             // the lets r1, r2, … of the store's reads
	peeks   int             // the store's peeks so far
	peekAt  int             // the seed's first peek kind
	reached map[string]bool
	rturn   int // the parts functions rebinding the root so far, which pick the next (no draw)
	lturn   int // the parts functions on a live let so far
	keeps   int // the let muts kc<n> the catch arms keep m in
}

func (g *handoverGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

// bind adds the let b<len> = expr with the model value v.
func (g *handoverGen) bind(expr string, v hbag) {
	g.line(fmt.Sprintf("let b%d = %s", len(g.bags), expr))
	g.bags = append(g.bags, v.copy())
}

func (g *handoverGen) pick() int {
	// The newest bindings most often, so lines of pushes grow long.
	n := len(g.bags)
	if g.r.IntN(3) > 0 {
		return n - 1 - g.r.IntN(min(n, 2))
	}
	return g.r.IntN(n)
}

func (g *handoverGen) print(name string, xs []int) {
	g.line(fmt.Sprintf("out = out + dig(%s) + \" \"", name))
	g.out.WriteString(hdig(xs) + " ")
}

func (g *handoverGen) stmt() {
	i := g.pick()
	src := g.bags[i]
	v := g.r.IntN(90) + 1
	push := func(b hbag, v int) hbag {
		return hbag{items: append(append([]int(nil), b.items...), v), n: b.n + 1}
	}
	ring := func(b hbag, v int) hbag {
		b = b.copy()
		if len(b.items) < 4 {
			b.items = append(b.items, v)
			return b
		}
		b.items[b.n%4] = v
		b.n++
		return b
	}
	arm := func(b hbag, v int) hbag {
		if v%2 == 0 {
			return push(b, v)
		}
		return push(b, v+1)
	}
	mk := func(b hbag, v int) hbag {
		if v%3 == 0 {
			return b.copy()
		}
		return hbag{items: append(append([]int(nil), b.items...), v), n: b.n}
	}
	ins := func(xs []int, v int) []int { return append(append([]int(nil), xs...), v) }
	ow := func(xs []int, v int) []int {
		t := append([]int(nil), xs...)
		if len(t) > 0 {
			t[0] = v
		}
		return append(t, v)
	}
	first := func(xs []int) int {
		if len(xs) > 0 {
			return xs[0]
		}
		return 0
	}
	// The parts functions (consume by parts, REUSE RU1; parts.go) on the
	// model.
	parts := map[string]func(b hbag, v int) hbag{
		"lit":    func(b hbag, v int) hbag { return hbag{items: ins(b.items, v), n: b.n + 1} },
		"upd":    func(b hbag, v int) hbag { return hbag{items: ins(b.items, v), n: b.n} },
		"updRaw": func(b hbag, v int) hbag { return hbag{items: ins(b.items, v), n: b.n} },
		"after":  func(b hbag, v int) hbag { return hbag{items: ow(b.items, v), n: first(b.items)} },
		"before": func(b hbag, v int) hbag { return hbag{items: ow(b.items, v), n: first(b.items)} },
		"two":    func(b hbag, v int) hbag { return hbag{items: ow(b.items, v), n: len(b.items) + 1} },
		"keepWhole": func(b hbag, v int) hbag {
			if v%3 == 0 {
				return b.copy()
			}
			return hbag{items: ow(b.items, v), n: b.n}
		},
		"side": func(b hbag, v int) hbag { return hbag{items: append([]int(nil), b.items...), n: len(b.items) + 1} },
		"late": func(b hbag, v int) hbag {
			if v%2 == 0 {
				return b.copy()
			}
			return hbag{items: ow(b.items, v), n: b.n}
		},
	}
	partNames := []string{"lit", "upd", "updRaw", "after", "before", "two", "keepWhole", "side", "late"}
	switch k := g.r.IntN(45); k {
	case 28, 29:
		// A parts function rebinding the root, then a write through it.
		f := partNames[g.rturn%len(partNames)]
		g.rturn++
		g.reached["root "+f] = true
		g.line(fmt.Sprintf("m = %s(m, %d)", f, v))
		g.m = parts[f](g.m, v)
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v+9))
			g.m.items[0] = v + 9
		}
	case 30, 32, 33:
		// A parts function on a live let, which stays readable.
		f := partNames[g.lturn%len(partNames)]
		g.lturn++
		g.reached[f+" on a live let"] = true
		g.bind(fmt.Sprintf("%s(b%d, %d)", f, i, v), parts[f](src, v))
	case 31:
		// An update of the root stored back (own-update-self): the
		// values read the old fields.
		g.reached["root updates itself"] = true
		g.line(fmt.Sprintf("m = Bag{..m, N: m.N + %d, Items: m.Items.Append(m.N)}", v))
		g.m = hbag{items: ins(g.m.items, g.m.n), n: g.m.n + v}
		if g.r.IntN(2) == 0 {
			// The mirror order: the later value reads the field the
			// earlier one names.
			g.reached["root updates itself, mirror"] = true
			g.line("m = Bag{..m, Items: m.Items.Append(m.N), N: len(m.Items)}")
			g.m = hbag{items: ins(g.m.items, g.m.n), n: len(g.m.items)}
		}
	case 34:
		// A window of the root kept in a let, then the root written.
		g.reached["window kept"] = true
		k := g.r.IntN(len(g.m.items) + 1)
		g.bind(fmt.Sprintf("Bag{N: 0, Items: slices.Slice(m.Items, 0, %d)}", k), hbag{items: append([]int(nil), g.m.items[:k]...)})
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v))
			g.m.items[0] = v
		}
	case 35:
		// A window a pure call keeps (keep returns it): copied.
		g.reached["window kept by a call"] = true
		k := g.r.IntN(len(g.m.items) + 1)
		g.bind(fmt.Sprintf("Bag{N: 0, Items: keep(slices.Slice(m.Items, 0, %d))}", k), hbag{items: append([]int(nil), g.m.items[:k]...)})
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v))
			g.m.items[0] = v
		}
	case 36:
		// pop and popDig through a borrow, then an Append on the root.
		k := g.r.IntN(len(g.m.items) + 1)
		w := append([]int(nil), g.m.items[k:]...)
		if g.r.IntN(2) == 0 {
			g.reached["window popped"] = true
			g.bind(fmt.Sprintf("Bag{N: 0, Items: pop(&m, %d)}", k), hbag{items: w})
		} else {
			g.reached["window read"] = true
			g.line(fmt.Sprintf("out = out + popDig(&m, %d) + \" \"", k))
			g.out.WriteString(hdig(w) + " ")
		}
		g.m.items = append(append([]int(nil), g.m.items[:k]...), v)
		g.line(fmt.Sprintf("m.Items = m.Items.Append(%d)", v))
	case 41, 42, 43:
		// A hand-over across a handled failure: the arm yields a fresh
		// value (41), m itself (42), m after keeping it in kc<n>, or the
		// let mut kc<n>, which stays live (43).
		cap := g.r.IntN(len(g.m.items) + 2)
		fails := len(g.m.items) >= cap
		arm := fmt.Sprintf("Bag{Items: []int{%d}, N: 0}", v)
		name := map[int]string{41: "root catch fresh", 42: "root catch yields itself", 43: "root catch arm keeps it"}[k]
		other := k == 43 && g.keeps%2 == 1
		if other {
			name = "root catch yields another binding"
		}
		g.reached[name] = true
		kc := ""
		var kept hbag
		switch k {
		case 42:
			arm = "m"
		case 43:
			kc = fmt.Sprintf("kc%d", g.keeps)
			g.keeps++
			if other {
				g.line(fmt.Sprintf("let %s = Bag{Items: []int{%d, %d}, N: 0}", kc, v+1, v+2))
				kept = hbag{items: []int{v + 1, v + 2}}
				arm = kc
			} else {
				g.line(fmt.Sprintf("let mut %s = Bag{Items: []int{}, N: 0}", kc))
				arm = kc + " = m\n\t\tm"
			}
		}
		g.line(fmt.Sprintf("m = capped(m, %d, %d) catch {\n\tcase Full:\n\t\t%s\n\t}", v, cap, arm))
		switch {
		case !fails:
			g.m = hbag{items: ow(g.m.items, v), n: g.m.n}
		case k == 41:
			g.m = hbag{items: []int{v}}
		case other:
			g.m = kept.copy()
		case k == 43:
			kept = g.m.copy()
		}
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v+5))
			g.m.items[0] = v + 5
		}
		if kc != "" {
			g.print(kc+".Items", kept.items)
		}
	case 44:
		// cappedLate fails after it writes through ow, a nested owning
		// call: it does not own b, so the arm yields m as it was, which
		// the next line prints.
		g.reached["root catch late failure"] = true
		cap := g.r.IntN(len(g.m.items) + 2)
		g.line(fmt.Sprintf("m = cappedLate(m, %d, %d) catch {\n\tcase Full:\n\t\tm\n\t}", v, cap))
		if t := ow(g.m.items, v); len(t) <= cap {
			g.m = hbag{items: t, n: g.m.n}
		}
		g.print("m.Items", g.m.items)
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v+6))
			g.m.items[0] = v + 6
		}
	case 39:
		// A cut of the root handed to an owned parameter whose result
		// rebinds the root (consumable, LWR-INLINE-OWNED), then a write.
		g.reached["root fills a cut"] = true
		k := g.r.IntN(len(g.m.items) + 1)
		a := 0
		if g.r.IntN(2) == 0 {
			a = g.r.IntN(k + 1)
		}
		g.line(fmt.Sprintf("m.Items = fill(m.Items[%d:%d], %d)", a, k, v))
		g.m.items = append(append([]int(nil), g.m.items[a:k]...), v, v+1)
		g.line(fmt.Sprintf("m.Items[0] = %d", v+2))
		g.m.items[0] = v + 2
	case 40:
		// The statement's other operand reads the root whole: the cut is
		// not handed over, or fillv's Append writes the row it reads.
		g.reached["root fills a cut it reads"] = true
		for len(g.m.items) < 2 {
			g.line(fmt.Sprintf("m.Items = m.Items.Append(%d)", v))
			g.m.items = append(append([]int(nil), g.m.items...), v)
		}
		k := g.r.IntN(len(g.m.items) - 1)
		g.line(fmt.Sprintf("m.Items = fillv(m.Items[0:%d], m.Items, %d)", k, v))
		g.m.items = append(append([]int(nil), g.m.items[:k]...), v, g.m.items[k])
	case 37, 38:
		// A window read by a call whose later operand writes the root:
		// the call reads the window as it was.
		g.reached["window read in flight"] = true
		k := g.r.IntN(len(g.m.items) + 1)
		g.line(fmt.Sprintf("out = out + digAfter(slices.Slice(m.Items, 0, %d), set0(&m, %d)) + \" \"", k, v))
		g.out.WriteString(hdig(g.m.items[:k]) + " ")
		if len(g.m.items) > 0 {
			g.m.items[0] = v
		}
	case 25:
		// A one-extender site on a parameter the hand-over does not own:
		// m takes both's result fresh only if c's array is not extended in
		// place (the token matches bX, both's last result).
		g.reached["root both"] = true
		x := hbag{items: append(append([]int(nil), src.items...), v), n: 0}
		g.bind(fmt.Sprintf("both(b0, b%d, %d)", i, v), x)
		n := len(g.bags) - 1
		g.line(fmt.Sprintf("m = both(m, b%d, %d)", n, v+1))
		g.m = hbag{items: append(append([]int(nil), x.items...), v+1), n: g.m.n}
		g.line(fmt.Sprintf("m.Items[0] = %d", v+7))
		g.m.items[0] = v + 7
	case 21:
		// A body fresh from unshared arguments (freshAll) through match
		// arms, rebinding the root, then a write through it.
		g.reached["root threads arms"] = true
		g.line(fmt.Sprintf("m = arm(m, %d)", v))
		g.m = arm(g.m, v)
		g.line(fmt.Sprintf("m.Items[0] = %d", v+3))
		g.m.items[0] = v + 3
	case 22:
		g.reached["arm on a live let"] = true
		g.bind(fmt.Sprintf("arm(b%d, %d)", i, v), arm(src, v))
	case 23:
		// A parameter owned through its fields and returned whole on one
		// path (fieldOwned).
		g.reached["root mk"] = true
		g.line(fmt.Sprintf("m = mk(m, %d)", v))
		g.m = mk(g.m, v)
		if len(g.m.items) > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v+5))
			g.m.items[0] = v + 5
		}
	case 24:
		g.reached["mk on a live let"] = true
		g.bind(fmt.Sprintf("mk(b%d, %d)", i, v), mk(src, v))
	case 26:
		// A window of the root kept by a let: a second owner of part of
		// the root's array, live across the root's later reslices and
		// Appends (SL3: an Append after a shrink must not write a row the
		// window shows).
		// The toktree shape: the root is cut back to the window's start
		// and grows, so an Append in place writes the window's first row.
		g.reached["let keeps a window of the root"] = true
		for len(g.m.items) < 2 {
			g.line(fmt.Sprintf("m.Items = m.Items.Append(%d)", v))
			g.m.items = append(append([]int(nil), g.m.items...), v)
		}
		b := 1 + g.r.IntN(len(g.m.items))
		a := g.r.IntN(b)
		g.bind(fmt.Sprintf("Bag{Items: m.Items[%d:%d], N: 0}", a, b), hbag{items: g.m.items[a:b]})
		g.line(fmt.Sprintf("m.Items = m.Items[0:%d]", a))
		g.line(fmt.Sprintf("m.Items = m.Items.Append(%d)", v+2))
		g.m.items = append(append([]int(nil), g.m.items[:a]...), v+2)
	case 27:
		// The toktree shape on a let mut local taken from a live let:
		// the local shrinks from both ends, then grows and is written,
		// while the let it came from stays live (SL3).
		g.reached["local shrinks and grows"] = true
		n := len(g.bags)
		k := g.r.IntN(len(src.items) + 1)
		a := g.r.IntN(k + 1)
		g.line(fmt.Sprintf("let mut s%d = b%d.Items", n, i))
		g.line(fmt.Sprintf("s%d = s%d[%d:%d]", n, n, a, k))
		g.line(fmt.Sprintf("s%d = s%d.Append(%d)", n, n, v))
		g.line(fmt.Sprintf("s%d[0] = %d", n, v+1))
		s := append(append([]int(nil), src.items[a:k]...), v)
		s[0] = v + 1
		g.bind(fmt.Sprintf("Bag{Items: s%d, N: 0}", n), hbag{items: s})
	case 20:
		g.reached["trim through a borrow"] = true
		g.line("trim(&m)")
		if len(g.m.items) > 0 {
			g.m.items = g.m.items[:len(g.m.items)-1]
		}
	case 17:
		// A reslice of the root into itself shares the root's own array
		// (storeread.go, own-reslice); an Append and a write follow.
		g.reached["root reslices"] = true
		// The cut may drop rows from the front too (xs = xs[a:k]).
		k := g.r.IntN(len(g.m.items) + 1)
		a := 0
		if g.r.IntN(2) == 0 {
			a = g.r.IntN(k + 1)
		}
		g.line(fmt.Sprintf("m.Items = m.Items[%d:%d]", a, k))
		g.line(fmt.Sprintf("m.Items = m.Items.Append(%d)", v))
		g.line(fmt.Sprintf("m.Items[0] = %d", v+1))
		g.m.items = append(append([]int(nil), g.m.items[a:k]...), v)
		g.m.items[0] = v + 1
	case 18:
		// A cut of a let is no reslice of the root: it is copied.
		g.reached["root takes a let's cut"] = true
		k := g.r.IntN(len(src.items) + 1)
		g.line(fmt.Sprintf("m.Items = b%d.Items[0:%d]", i, k))
		g.m.items = append([]int(nil), src.items[:k]...)
		if k > 0 {
			g.line(fmt.Sprintf("m.Items[0] = %d", v))
			g.m.items[0] = v
		}
	case 19:
		g.reached["let keeps the root"] = true
		g.bind("Bag{Items: m.Items, N: 0}", hbag{items: g.m.items})
	case 14:
		g.reached["store add"] = true
		g.line(fmt.Sprintf("st.bags = st.bags.Append(b%d)", i))
		g.store = append(g.store, src.copy())
	case 15:
		// A read of a store element through a borrow, a write of the
		// store, then a read of the let (storeread.go, own-read).
		if len(g.store) == 0 {
			return
		}
		j := g.r.IntN(len(g.store))
		kinds := []string{"Kind", "Item", "Push", "Pass", "Method", "Bump", "CycleA", "CycleB", "CallBump", "CallPoke", "Chain", "EnterBump", "EnterPoke", "Ret", "RetPoke", "EnterPath", "EnterPathPoke"}
		// Four peeks of the element; seed s takes the kinds s-1, s+3,
		// s+7, …: seeds 2, 3 and 4 alone reach all seventeen (seed 1
		// adds no bag to the store).
		for range 4 {
			kind := kinds[(g.peeks*4+g.peekAt)%len(kinds)]
			g.peeks++
			g.reached["peek "+kind] = true
			if strings.HasPrefix(kind, "Enter") {
				// The store's bags, or a path of them, enter a let mut of
				// the callee (by value, unkept); the caller reads its
				// element after.
				g.line(fmt.Sprintf("out = out + %s(st.bags, %d, %d) + \"/\" + show(st.bags[%d]) + \" \"", strings.ToLower(kind[:1])+kind[1:], j, v, j))
				b := g.store[j].copy()
				items, n := b.items, 1
				if strings.HasSuffix(kind, "Poke") {
					items, n = append([]int(nil), b.items...), 0
					if len(items) > 0 {
						items[0] = v
					}
				}
				g.out.WriteString(hdig(items) + fmt.Sprintf(",%d/", n) + hdig(b.items) + fmt.Sprintf(",%d ", b.n))
				continue
			}
			g.line(fmt.Sprintf("out = out + peek%s(&st, %d, %d) + \" \"", kind, j, v))
			b := g.store[j].copy()
			e := &g.store[j]
			switch kind {
			case "Kind", "Bump", "CallBump", "Chain", "Ret":
				e.n += v
			case "Push":
				e.items = append(append([]int(nil), e.items...), v)
			default:
				if len(e.items) > 0 {
					e.items[0] = v
				}
			}
			g.out.WriteString(hdig(b.items) + fmt.Sprintf(",%d ", b.n))
		}
	case 16:
		// The same through the let mut root st itself.
		if len(g.store) == 0 {
			return
		}
		j := g.r.IntN(len(g.store))
		g.reached["root read then write"] = true
		g.reads++
		n := g.reads
		g.line(fmt.Sprintf("let r%d = st.bags[%d]", n, j))
		b := g.store[j].copy()
		if g.r.IntN(2) == 0 {
			g.line(fmt.Sprintf("st.bags[%d].N = %d", j, v))
			g.store[j].n = v
		} else if len(g.store[j].items) > 0 {
			g.line(fmt.Sprintf("st.bags[%d].Items[0] = %d", j, v))
			g.store[j].items[0] = v
		}
		g.line(fmt.Sprintf("out = out + dig(r%d.Items) + \",\\(r%d.N) \"", n, n))
		g.out.WriteString(hdig(b.items) + fmt.Sprintf(",%d ", b.n))
	case 12:
		g.reached["ring on a live let"] = true
		g.bind(fmt.Sprintf("ring(b%d, %d)", i, v), ring(src, v))
	case 13:
		g.reached["root rings"] = true
		g.line(fmt.Sprintf("m = ring(m, %d)", v))
		g.m = ring(g.m, v)
	case 11:
		// Two extenders of the newest value: the second must copy.
		g.reached["branch"] = true
		i = len(g.bags) - 1
		src = g.bags[i]
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v), push(src, v))
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v+1), push(src, v+1))
	case 0, 1, 2:
		g.reached["push"] = true
		g.bind(fmt.Sprintf("push(b%d, %d)", i, v), push(src, v))
	case 3:
		g.reached["inline append on a live let"] = true
		g.bind(fmt.Sprintf("Bag{N: b%d.N + 1, Items: b%d.Items.Append(%d)}", i, i, v), push(src, v))
	case 4:
		g.reached["grow"] = true
		g.bind(fmt.Sprintf("grow(b%d, %d)", i, v), push(src, v))
	case 5:
		if len(src.items) == 0 {
			return
		}
		g.reached["cut"] = true
		k := g.r.IntN(len(src.items) + 1)
		g.bind(fmt.Sprintf("Bag{N: 0, Items: b%d.Items[0:%d]}", i, k), hbag{items: src.items[:k]})
	case 6:
		g.reached["root takes a push"] = true
		g.line(fmt.Sprintf("m = push(b%d, %d)", i, v))
		g.m = push(src, v)
		if len(g.m.items) > 0 {
			j := g.r.IntN(len(g.m.items))
			g.line(fmt.Sprintf("m.Items[%d] = %d", j, 500+v))
			g.m.items[j] = 500 + v
		}
	case 7:
		g.reached["root pushes"] = true
		g.line(fmt.Sprintf("m = push(m, %d)", v))
		g.m = push(g.m, v)
	case 8:
		g.reached["concat on a root"] = true
		g.line(fmt.Sprintf("doc = slices.Concat(doc, b%d.Items)", i))
		g.doc = append(g.doc, src.items...)
		if len(g.doc) > 0 {
			j := g.r.IntN(len(g.doc))
			g.line(fmt.Sprintf("doc[%d] = %d", j, 700+v))
			g.doc[j] = 700 + v
		}
	case 9:
		g.reached["concat on a live let"] = true
		j := g.pick()
		g.bind(fmt.Sprintf("Bag{N: 0, Items: slices.Concat(b%d.Items, b%d.Items)}", i, j), hbag{items: append(append([]int(nil), src.items...), g.bags[j].items...)})
	case 10:
		g.reached["concat on a dead local"] = true
		n := len(g.bags)
		g.line(fmt.Sprintf("let t%d = []int{%d, %d}", n, v, v+1))
		g.bind(fmt.Sprintf("Bag{N: 0, Items: slices.Concat(t%d, b%d.Items)}", n, i), hbag{items: append([]int{v, v + 1}, src.items...)})
	}
	if g.r.IntN(3) == 0 {
		j := g.r.IntN(len(g.bags))
		g.print(fmt.Sprintf("b%d.Items", j), g.bags[j].items)
	}
}

// handoverProgram is seed's program and the line it must print.
func handoverProgram(seed uint64, reached map[string]bool) (string, string) {
	g := &handoverGen{r: rand.New(rand.NewPCG(seed, 0x6f776e31)), reached: reached, rturn: int(seed) * 4, lturn: int(seed) * 7, reads: int(seed), peekAt: int((seed + 3) % 4)}
	g.b.WriteString(`import slices

type Bag { Items []int, N int }

func dig(xs []int) string {
	let mut s = 0
	for x in xs {
		s = (s * 31 + x) % 1000003
	}
	"\(len(xs))/\(s)"
}

// push extends a parameter's field: a one-extender site.
func push(b Bag, v int) Bag {
	Bag{Items: b.Items.Append(v), N: b.N + 1}
}

// grow extends a field of its own copy, dead after the Append.
func grow(b Bag, v int) Bag {
	let mut c = b
	c.N = c.N + 1
	Bag{N: c.N, Items: c.Items.Append(v)}
}

// ring takes its parameter over through its fields (handover.go,
// fieldOwned): a ring of four slots, as GP's ring-copy.
func ring(b Bag, v int) Bag {
	if len(b.Items) < 4 {
		return Bag{Items: b.Items.Append(v), N: b.N}
	}
	let mut items = b.Items
	items[b.N % 4] = v
	Bag{Items: items, N: b.N + 1}
}

// A store of bags, read through a borrow (storeread.go, own-read): each
// peek reads an element into a let, writes the store, then reads the let.
type Store { bags []Bag }

func show(b Bag) string {
	"\(dig(b.Items)),\(b.N)"
}

// The write is to a scalar field: the let needs no copy.
func peekKind(s mut Store, i int, v int) string {
	let b = s.bags[i]
	s.bags[i].N = s.bags[i].N + v
	show(b)
}

// The write is to an element of the slice the let holds: it must copy.
func peekItem(s mut Store, i int, v int) string {
	let b = s.bags[i]
	if len(s.bags[i].Items) > 0 {
		s.bags[i].Items[0] = v
	}
	show(b)
}

// An Append in place on the element's field.
func peekPush(s mut Store, i int, v int) string {
	let b = s.bags[i]
	s.bags[i].Items = s.bags[i].Items.Append(v)
	show(b)
}

func poke(s mut Store, i int, v int) {
	if len(s.bags[i].Items) > 0 {
		s.bags[i].Items[0] = v
	}
}

// The borrow passed on writes the element's slice.
func peekPass(s mut Store, i int, v int) string {
	let b = s.bags[i]
	poke(s, i, v)
	show(b)
}

func set(s mut Store, i int, v int) {
	if len(s.bags[i].Items) > 0 {
		s.bags[i].Items[0] = v
	}
}

// A pointer method on the borrow writes the element's slice.
func peekMethod(s mut Store, i int, v int) string {
	let b = s.bags[i]
	s.set(i, v)
	show(b)
}

func bump(s mut Store, i int, v int) {
	s.bags[i].N = s.bags[i].N + v
}

// The borrow passed on writes only a scalar field (storeread_pass.go,
// own-read-pass): the let needs no copy.
func peekBump(s mut Store, i int, v int) string {
	let b = s.bags[i]
	bump(s, i, v)
	show(b)
}

// view keeps nothing of b (keeps.go): an argument read from a root is
// unkept.
func view(b Bag, w int) string {
	if w != 0 {
		return ""
	}
	show(b)
}

func bumpN(s mut Store, i int, v int) int {
	s.bags[i].N = s.bags[i].N + v
	0
}

func pokeN(s mut Store, i int, v int) int {
	if len(s.bags[i].Items) > 0 {
		s.bags[i].Items[0] = v
	}
	0
}

// The read operand is unkept; the next operand passes the borrow on and
// writes only a scalar field (keeps.go, own-call-pass): no copy.
func peekCallBump(s mut Store, i int, v int) string {
	view(s.bags[i], bumpN(s, i, v))
}

// The next operand writes the element's slice: the read copies.
func peekCallPoke(s mut Store, i int, v int) string {
	view(s.bags[i], pokeN(s, i, v))
}

// A let of the element's field after the let of the element (storeread.go,
// letKeepsNothing); the borrow passed on writes only a scalar field: no
// copy.
func peekChain(s mut Store, i int, v int) string {
	let b = s.bags[i]
	let it = b.Items
	bump(s, i, v)
	"\(dig(it)),\(b.N)"
}

// items returns a part of its borrow (borrowret.go, own-borrow-return):
// its callers copy the result only when they keep it past a write.
func items(s mut Store, i int) []int {
	s.bags[i].Items
}

// A let of it that keeps nothing, then a scalar write: no copy.
func peekRet(s mut Store, i int, v int) string {
	let it = items(s, i)
	bump(s, i, v)
	"\(dig(it)),\(s.bags[i].N - v)"
}

// The borrow then writes the slice: the caller copies.
func peekRetPoke(s mut Store, i int, v int) string {
	let it = items(s, i)
	poke(s, i, v)
	"\(dig(it)),\(s.bags[i].N)"
}

// A let mut that holds its parameter's bags and writes only its own
// counts through a borrow (storefield.go, own-enter-field): the bags enter
// it shared.
type Walk { bags []Bag, n []int }

func tally(w mut Walk, v int) {
	w.n = w.n.Append(v)
}

func enterBump(bags []Bag, i int, v int) string {
	let mut w = Walk{bags: bags, n: []int{}}
	tally(&w, v)
	"\(dig(w.bags[i].Items)),\(len(w.n))"
}

func scribble(w mut Walk, i int, v int) {
	if len(w.bags[i].Items) > 0 {
		w.bags[i].Items[0] = v
	}
}

// The borrow writes the bags' storage: they enter the let mut copied, and
// the caller's store is unchanged.
func enterPoke(bags []Bag, i int, v int) string {
	let mut w = Walk{bags: bags, n: []int{}}
	scribble(&w, i, v)
	"\(dig(w.bags[i].Items)),\(len(w.n))"
}

// A let mut whose field takes a path of its parameter, at its literal
// and by an assignment, and whose borrow writes only its own counts
// (own-enter-field): the items enter it shared.
type Tray { items []int, n []int }

func count(t mut Tray, v int) {
	t.n = t.n.Append(v)
}

func enterPath(bags []Bag, i int, v int) string {
	let mut t = Tray{items: bags[0].Items, n: []int{}}
	t.items = bags[i].Items
	count(&t, v)
	"\(dig(t.items)),\(len(t.n))"
}

func stamp(t mut Tray, v int) {
	if len(t.items) > 0 {
		t.items[0] = v
	}
}

// The borrow writes the items: they enter the let mut copied.
func enterPathPoke(bags []Bag, i int, v int) string {
	let mut t = Tray{items: bags[0].Items, n: []int{}}
	t.items = bags[i].Items
	stamp(&t, v)
	"\(dig(t.items)),\(len(t.n))"
}

// pokeA writes the element's slice; pokeB only passes the borrow back to
// pokeA: through the recursion, both write it.
func pokeA(s mut Store, i int, v int, d int) {
	if d > 0 {
		pokeB(s, i, v, d - 1)
	} else if len(s.bags[i].Items) > 0 {
		s.bags[i].Items[0] = v
	}
}

func pokeB(s mut Store, i int, v int, d int) {
	pokeA(s, i, v, d)
}

// The borrow passed into the recursion writes the element's slice.
func peekCycleA(s mut Store, i int, v int) string {
	let b = s.bags[i]
	pokeA(s, i, v, 1)
	show(b)
}

// The same, entered through the function that writes nothing itself.
func peekCycleB(s mut Store, i int, v int) string {
	let b = s.bags[i]
	pokeB(s, i, v, 1)
	show(b)
}

// arm pushes in one of two arms: its result is fresh when b is unshared
// (freshAll; endingReturn's last values).
func arm(b Bag, v int) Bag {
	if v % 2 == 0 {
		push(b, v)
	} else {
		push(b, v + 1)
	}
}

// both extends c's field, keeping b's count: a one-extender site on a
// parameter other than the one a rebinding m = both(m, …) hands over.
func both(b Bag, c Bag, v int) Bag {
	Bag{Items: c.Items.Append(v), N: b.N}
}

// mk takes its parameter over through its fields and returns it whole on
// one path (fieldOwned).
func mk(b Bag, v int) Bag {
	if v % 3 == 0 {
		return b
	}
	let mut items = b.Items
	items = items.Append(v)
	Bag{Items: items, N: b.N}
}

// Consume by parts (REUSE RU1; parts.go). ins and ow own their slice; ow
// writes its first element, so a read of the path it consumed that ran
// after it in place would see the write.
func ins(xs []int, v int) []int {
	let mut t = xs
	t = t.Append(v)
	t
}

func ow(xs []int, v int) []int {
	let mut t = xs
	if len(t) > 0 {
		t[0] = v
	}
	t.Append(v)
}

func first(xs []int) int {
	if len(xs) > 0 {
		return xs[0]
	}
	0
}

// lit moves a part into a literal through a user operation.
func lit(b Bag, v int) Bag { Bag{Items: ins(b.Items, v), N: b.N + 1} }

// upd moves a part into an update through a user operation.
func upd(b Bag, v int) Bag { Bag{..b, Items: ins(b.Items, v)} }

// updRaw extends a part with the prelude's Append in an update.
func updRaw(b Bag, v int) Bag { Bag{..b, Items: b.Items.Append(v)} }

// after reads the consumed path after the consuming call; before, ahead.
func after(b Bag, v int) Bag { Bag{Items: ow(b.Items, v), N: first(b.Items)} }

func before(b Bag, v int) Bag { Bag{N: first(b.Items), Items: ow(b.Items, v)} }

// two consumes one path twice: the second must see the first's source.
func two(b Bag, v int) Bag { Bag{Items: ow(b.Items, v), N: len(ow(b.Items, v + 1))} }

// keepWhole returns its parameter whole beside a consuming route.
func keepWhole(b Bag, v int) Bag {
	if v % 3 == 0 {
		return b
	}
	Bag{..b, Items: ow(b.Items, v)}
}

// side consumes a path under a field the update keeps from its base: the
// base's Items must not see ow's write.
func side(b Bag, v int) Bag { Bag{..b, N: len(ow(b.Items, v))} }

// late returns its parameter whole after the bind that took it: that
// route reads the value the bind's writes would change.
func late(b Bag, v int) Bag {
	let mut t = b
	t.Items = ow(t.Items, v)
	if v % 2 == 0 {
		return b
	}
	t
}

// pop cuts the window [k, len) off a borrowed field and keeps it; the
// field's next Append writes the slot the window starts at, so the window
// is copied (gobody.go, goBodyView; wf10 DF1).
func pop(b mut Bag, k int) []int {
	let w = slices.Slice(b.Items, k, len(b.Items))
	b.Items = b.Items[0:k]
	w
}

// popDig reads the window and keeps nothing of it: no copy.
func popDig(b mut Bag, k int) string {
	let s = dig(slices.Slice(b.Items, k, len(b.Items)))
	b.Items = b.Items[0:k]
	s
}

// keep returns its parameter: a pure call that keeps the window.
func keep(xs []int) []int {
	xs
}

// set0 writes the first item of a borrowed field.
func set0(b mut Bag, v int) int {
	if len(b.Items) > 0 {
		b.Items[0] = v
	}
	v
}

// digAfter reads its window after its second operand ran.
func digAfter(xs []int, v int) string {
	dig(xs)
}

// fill owns its slice (V5) and extends it: m.Items = fill(m.Items[a:k], v)
// hands the cut over (consumable).
func fill(xs []int, v int) []int {
	let mut t = xs
	for k in 0..2 {
		t = t.Append(v + k)
	}
	t
}

// fillv extends its owned slice, then reads ys at the slot it wrote.
func fillv(xs []int, ys []int, v int) []int {
	let mut t = xs
	let k = len(t)
	t = t.Append(v)
	t.Append(ys[k])
}

type Full { Cap int }

// capped fails before it writes its owned b (RU2).
func capped(b Bag, v int, cap int) Bag fails Full {
	if len(b.Items) >= cap {
		return Full{Cap: cap}
	}
	let mut t = b
	t.Items = ow(t.Items, v)
	t
}

// cappedLate writes, then may fail: it does not own b.
func cappedLate(b Bag, v int, cap int) Bag fails Full {
	let mut t = b
	t.Items = ow(t.Items, v)
	if len(t.Items) > cap {
		return Full{Cap: cap}
	}
	t
}

// trim reslices a borrowed field into itself: no copy (own-reslice).
func trim(b mut Bag) {
	if len(b.Items) > 0 {
		b.Items = b.Items[:len(b.Items)-1]
	}
}

func run() string {
	let mut out = ""
	let mut st = Store{bags: []Bag{}}
	let mut m = Bag{Items: []int{}, N: 0}
	let mut doc = []int{}
	let b0 = Bag{Items: []int{}, N: 0}
	doc = slices.Concat(doc, b0.Items)
`)
	g.bags = []hbag{{items: []int{}}}
	g.m = hbag{items: []int{}}
	for range 170 {
		g.stmt()
	}
	for i, b := range g.bags {
		g.print(fmt.Sprintf("b%d.Items", i), b.items)
	}
	g.print("m.Items", g.m.items)
	// The root's count: an update stored back into m writes it.
	g.line(`out = out + "\(m.N) "`)
	g.out.WriteString(fmt.Sprintf("%d ", g.m.n))
	g.print("doc", g.doc)
	want := g.out.String()
	g.b.WriteString("\tout\n}\n\nfunc main() needs Console {\n\tConsole.Println(run())\n}\n\n")
	g.b.WriteString(fmt.Sprintf("test Agrees {\n\tassert run() == %q\n}\n", want))
	return g.b.String(), want + "\n"
}

// TestHandoverAgrees is the seeded property (see above).
//
// It builds Go because it compares the runs of built programs with a
// model: the question is the values the program computes at run time,
// where an in-place write either shows through another binding or does
// not; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestHandoverAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 5}
	if *handoverSeed != 0 {
		seeds = []uint64{*handoverSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
		name      string
		test      bool
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := handoverProgram(seed, reached)
		name := fmt.Sprintf("ho%d", seed)
		programs = append(programs, lowerValueMode(t, name, src, true))
		runs = append(runs, run{seed, src, want, name, false})
		programs = append(programs, lowerValueMode(t, name+"copy", src, true, "copy"))
		runs = append(runs, run{seed, src, want, name + "copy", false})
		programs = append(programs, lowerTestBuild(t, name+"test", src))
		runs = append(runs, run{seed, src, want, name + "test", true})
	}
	// The rule takes every shortcut somewhere: else the property proves
	// nothing about it.
	for _, form := range []string{"ergo.LineageAppend[", "ergo.SlicesConcatOwned[", "ringOwned(", "mkOwned(", "ergo.AppendPoison[", "ergo.LineageAppendCheck["} {
		found := false
		for _, p := range programs {
			if strings.Contains(p.mainGo(), form) {
				found = true
			}
		}
		if !found {
			t.Errorf("no lowering of seeds %v calls %s", seeds, form)
		}
	}
	// own-read is taken where it may be and refused where it may not
	// (programs[0] is the rule's lowering of the first seed).
	body := func(src, name string) string {
		// A function whose first parameter is a borrow lowers to a method.
		i := strings.Index(src, "func "+name+"(")
		if i < 0 {
			i = strings.Index(src, ") "+name+"(")
		}
		if i < 0 {
			return ""
		}
		end := strings.Index(src[i:], "\nfunc ")
		if end < 0 {
			return src[i:]
		}
		return src[i : i+end]
	}
	for name, copies := range map[string]bool{"peekKind": false, "peekItem": true, "peekPass": true, "peekBump": false, "peekCycleA": true, "peekCycleB": true, "peekCallBump": false, "peekCallPoke": true, "peekChain": false, "enterBump": false, "enterPoke": true, "enterPath": false, "enterPathPoke": true, "items": false, "peekRet": false, "peekRetPoke": true, "trim": false} {
		b := body(programs[0].mainGo(), name)
		if b == "" || strings.Contains(b, "Clone") != copies {
			var named []string
			for _, l := range strings.Split(programs[0].mainGo(), "\n") {
				if strings.Contains(l, name) {
					named = append(named, l)
				}
			}
			t.Errorf("the rule's %s copies %v, want %v:\n%s\n%s", name, !copies, copies, b, strings.Join(named, "\n"))
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the hand-over programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if r.test {
			if code != 0 {
				t.Errorf("seed %d, %s: the test build's assertion of the model fails (exit %d)\nReplay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=%d\n%s\n--- test run\n%s%s", r.seed, r.name, code, r.seed, r.src, out, stderr)
			}
			continue
		}
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the value model differ\nReplay: go test -run '^TestHandoverAgrees$' ./internal/sketch/lower -args -handover.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	if *handoverSeed == 0 {
		for _, s := range []string{"push", "branch", "ring on a live let", "root rings", "inline append on a live let", "grow", "cut", "root takes a push", "root pushes", "concat on a root", "concat on a live let", "concat on a dead local", "store add", "peek Kind", "peek Item", "peek Push", "peek Pass", "peek Method", "root read then write", "root reslices", "root takes a let's cut", "let keeps the root", "trim through a borrow", "root threads arms", "arm on a live let", "root mk", "mk on a live let", "root both", "let keeps a window of the root", "local shrinks and grows", "root lit", "root upd", "root updRaw", "root after", "root before", "root two", "root keepWhole", "lit on a live let", "upd on a live let", "updRaw on a live let", "after on a live let", "before on a live let", "two on a live let", "keepWhole on a live let", "root side", "side on a live let", "root late", "late on a live let", "root updates itself", "root updates itself, mirror", "window kept", "window kept by a call", "window popped", "window read", "window read in flight", "root fills a cut", "root fills a cut it reads", "root catch fresh", "root catch yields itself", "root catch arm keeps it", "root catch yields another binding", "root catch late failure", "peek Kind", "peek Item", "peek Push", "peek Pass", "peek Method", "peek Bump", "peek CycleA", "peek CycleB", "peek CallBump", "peek CallPoke", "peek Chain", "peek EnterBump", "peek EnterPoke", "peek EnterPath", "peek EnterPathPoke", "peek Ret", "peek RetPoke"} {
			if !reached[s] {
				t.Errorf("seeds %v never write %q", seeds, s)
			}
		}
	}
}
