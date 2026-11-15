package regexpplan

type opcode uint8

const (
	acceptOp opcode = iota
	byteOp
	splitOp
	tagOp
	jumpOp
)

type inst struct {
	op   opcode
	x, y int
	tag  int
	set  [4]uint64
}

type compiler struct {
	code     []inst
	ids      map[*Node]int
	limit    int
	overflow bool
}

func (c *compiler) add(i inst) int {
	if len(c.code) >= c.limit {
		c.overflow = true
		return 0
	}
	c.code = append(c.code, i)
	return len(c.code) - 1
}

func (c *compiler) node(n *Node, next int) int {
	switch n.Kind {
	case Empty:
		return next
	case Bytes:
		return c.add(inst{op: byteOp, set: n.Set, x: next})
	case Concat:
		for i := len(n.Children) - 1; i >= 0; i-- {
			next = c.node(n.Children[i], next)
		}
		return next
	case Alternative:
		start := c.node(n.Children[len(n.Children)-1], next)
		for i := len(n.Children) - 2; i >= 0; i-- {
			start = c.add(inst{op: splitOp, x: c.node(n.Children[i], next), y: start})
		}
		return start
	case Capture:
		close := c.add(inst{op: tagOp, tag: -c.ids[n], x: next})
		return c.add(inst{op: tagOp, tag: c.ids[n], x: c.node(n.Children[0], close)})
	case Repeat:
		start := next
		if n.Max == -1 {
			split := c.add(inst{op: splitOp, y: next})
			body := c.node(n.Children[0], split)
			if !c.overflow {
				c.code[split].x = body
			}
			start = split
		} else {
			for i := n.Min; i < n.Max; i++ {
				start = c.add(inst{op: splitOp, x: c.node(n.Children[0], start), y: next})
			}
		}
		for i := 0; i < n.Min; i++ {
			start = c.node(n.Children[0], start)
		}
		return start
	}
	panic("unchecked AST")
}

// Event is a symbolic capture action at a byte position: positive Tag opens a
// capture; negative Tag closes it. Tags index Names(), starting at one.
// The balanced event stream preserves nested records and every repeat occurrence.
type Event struct{ Tag, Pos int }

type Match struct {
	Start, End int
	Events     []Event
	OK         bool
}

type history struct {
	event Event
	prev  *history
}

type thread struct {
	pc int
	h  *history
}

func materialize(h *history) []Event {
	n := 0
	for p := h; p != nil; p = p.prev {
		n++
	}
	events := make([]Event, n)
	for i := n - 1; i >= 0; i-- {
		events[i], h = h.event, h.prev
	}
	return events
}

func (p *Plan) closure(in []thread, pos int) []thread {
	seen := make([]bool, len(p.code))
	var out []thread
	var visit func(thread)
	visit = func(t thread) {
		if seen[t.pc] {
			return
		}
		seen[t.pc] = true
		i := p.code[t.pc]
		switch i.op {
		case splitOp:
			visit(thread{i.x, t.h})
			visit(thread{i.y, t.h})
		case jumpOp:
			visit(thread{i.x, t.h})
		case tagOp:
			visit(thread{i.x, &history{Event{i.tag, pos}, t.h}})
		default:
			out = append(out, t)
		}
	}
	for _, t := range in {
		visit(t)
	}
	return out
}

// Reference executes canonical NFA transitions. It is retained independently of
// engine selection for differential checks and reference-mode integration.
func (p *Plan) Reference(text string) Match {
	lastStart := 0
	if p.mode == Find {
		lastStart = len(text)
	}
	for start := 0; start <= lastStart; start++ {
		current := []thread{{p.start, nil}}
		best := Match{}
		for pos := start; ; pos++ {
			ready := p.closure(current, pos)
			next := []thread{}
			for _, t := range ready {
				i := p.code[t.pc]
				if i.op == acceptOp && (p.mode == Find || pos == len(text)) {
					best = Match{start, pos, materialize(t.h), true}
					// Only higher-priority consuming threads can improve this.
					break
				}
				if i.op == byteOp && pos < len(text) && i.set[text[pos]/64]&(1<<(text[pos]%64)) != 0 {
					next = append(next, thread{i.x, t.h})
				}
			}
			if pos == len(text) || len(next) == 0 {
				break
			}
			current = next
		}
		if best.OK {
			return best
		}
	}
	return Match{}
}

// Match uses the selected engine; every call owns all of its scratch.
func (p *Plan) Match(text string) Match {
	if p.info.Engine == NFA {
		return p.Reference(text)
	}
	lastStart := 0
	if p.mode == Find {
		lastStart = len(text)
	}
	for start := 0; start <= lastStart; start++ {
		state := 0
		var events []Event
		for pos := start; ; pos++ {
			b := 256
			if pos < len(text) {
				b = int(text[pos])
			}
			edge := p.ir.states[state].edges[b]
			// Prefix-free admission means accepting paths never compete with a
			// consuming path. Find can accept before EOF; Parse cannot.
			if edge.accept && (p.mode == Find || pos == len(text)) {
				if events == nil && len(edge.tags) > 0 {
					events = make([]Event, 0, 2*len(p.names))
				}
				for _, tag := range edge.tags {
					events = append(events, Event{tag, pos})
				}
				return Match{start, pos, events, true}
			}
			if edge.to < 0 || pos == len(text) {
				break
			}
			if events == nil && len(edge.tags) > 0 {
				events = make([]Event, 0, 2*len(p.names))
			}
			for _, tag := range edge.tags {
				events = append(events, Event{tag, pos})
			}
			state = edge.to
		}
	}
	return Match{}
}
