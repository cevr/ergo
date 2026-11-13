package regexpplan

import (
	"fmt"
	"slices"
)

type edge struct {
	to     int
	tags   []int
	accept bool
}

type state struct {
	pc    int // finite correspondence to the canonical NFA
	edges [257]edge
}

type candidate struct{ states []state }

type symbolic struct {
	pc   int
	tags []int
}

// symbolicClosure defines the tag transition semantics once. Each position is
// a symbol: ordered tag identities, not sampled positions, establish equality.
// Reconvergent epsilon paths are refused even if their actions happen to agree.
func symbolicClosure(p *Plan, pc int, charge func(int) bool) ([]symbolic, string) {
	seen := map[int]bool{}
	var out []symbolic
	var visit func(int, []int) string
	visit = func(pc int, tags []int) string {
		if !charge(1 + len(tags)) {
			return "one-pass work budget"
		}
		if seen[pc] {
			return "ambiguous epsilon history"
		}
		seen[pc] = true
		i := p.code[pc]
		switch i.op {
		case splitOp:
			if why := visit(i.x, tags); why != "" {
				return why
			}
			return visit(i.y, tags)
		case jumpOp:
			return visit(i.x, tags)
		case tagOp:
			return visit(i.x, append(slices.Clone(tags), i.tag))
		default:
			out = append(out, symbolic{pc, tags})
		}
		return ""
	}
	why := visit(pc, nil)
	return out, why
}

func construct(p *Plan, limits Limits) (*candidate, int, string) {
	work := 0
	charge := func(n int) bool {
		if n > limits.Work-work {
			return false
		}
		work += n
		return true
	}
	c := &candidate{}
	index := map[int]int{}
	add := func(pc int) int {
		if v, ok := index[pc]; ok {
			return v
		}
		if len(c.states) >= limits.States {
			return -1
		}
		v := len(c.states)
		index[pc] = v
		c.states = append(c.states, state{pc: pc})
		return v
	}
	if add(p.start) < 0 {
		return nil, work, "one-pass state budget"
	}
	for q := 0; q < len(c.states); q++ {
		configs, why := symbolicClosure(p, c.states[q].pc, charge)
		if why != "" {
			return nil, work, why
		}
		for b := 0; b <= 256; b++ {
			selected := edge{to: -1}
			found := false
			for _, conf := range configs {
				if !charge(1 + len(conf.tags)) {
					return nil, work, "one-pass work budget"
				}
				i := p.code[conf.pc]
				matches := i.op == acceptOp || (i.op == byteOp && b < 256 && i.set[b/64]&(1<<uint(b%64)) != 0)
				if !matches {
					continue
				}
				if found {
					return nil, work, "competing byte or acceptance histories"
				}
				found = true
				selected.tags = slices.Clone(conf.tags)
				if i.op == acceptOp {
					selected.accept = true
				} else {
					selected.to = add(i.x)
					if selected.to < 0 {
						return nil, work, "one-pass state budget"
					}
				}
			}
			c.states[q].edges[b] = selected
		}
	}
	return c, work, "unique symbolic histories"
}

// validate checks the complete finite correspondence, including EOF, all byte
// classes, unreachable/duplicate states and exact ordered actions. It never sees
// a law verdict. A candidate cannot supply its own canonical reference.
func validate(p *Plan, c *candidate) error {
	if c == nil || len(c.states) == 0 || c.states[0].pc != p.start {
		return fmt.Errorf("invalid start correspondence")
	}
	seenPC := map[int]bool{}
	reachable := map[int]bool{0: true}
	for q, s := range c.states {
		if s.pc < 0 || s.pc >= len(p.code) || seenPC[s.pc] {
			return fmt.Errorf("invalid state correspondence %d", q)
		}
		seenPC[s.pc] = true
		configs, why := symbolicClosure(p, s.pc, func(int) bool { return true })
		if why != "" {
			return fmt.Errorf("unrepresentable history: %s", why)
		}
		for b, got := range s.edges {
			var expected *symbolic
			for j := range configs {
				i := p.code[configs[j].pc]
				if i.op == acceptOp || (i.op == byteOp && b < 256 && i.set[b/64]&(1<<uint(b%64)) != 0) {
					if expected != nil {
						return fmt.Errorf("ambiguous edge %d/%d", q, b)
					}
					expected = &configs[j]
				}
			}
			if expected == nil {
				if got.to != -1 || got.accept || len(got.tags) != 0 {
					return fmt.Errorf("invented edge %d/%d", q, b)
				}
				continue
			}
			i := p.code[expected.pc]
			if !slices.Equal(got.tags, expected.tags) || got.accept != (i.op == acceptOp) {
				return fmt.Errorf("tag/acceptance mismatch %d/%d", q, b)
			}
			if i.op == acceptOp {
				if got.to != -1 {
					return fmt.Errorf("acceptance consumes input")
				}
			} else {
				if got.to < 0 || got.to >= len(c.states) || c.states[got.to].pc != i.x {
					return fmt.Errorf("successor mismatch %d/%d", q, b)
				}
				if reachable[q] {
					reachable[got.to] = true
				}
			}
		}
	}
	// State numbering is BFS construction order. Do not depend on that order
	// for reachability validation of an externally corrupted candidate.
	for changed := true; changed; {
		changed = false
		for q := range reachable {
			for _, e := range c.states[q].edges {
				if e.to >= 0 && !reachable[e.to] {
					reachable[e.to], changed = true, true
				}
			}
		}
	}
	if len(reachable) != len(c.states) {
		return fmt.Errorf("unreachable state correspondence")
	}
	return nil
}
