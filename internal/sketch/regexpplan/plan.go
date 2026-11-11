// Package regexpplan builds bounded, immutable byte matchers from a closed AST.
// It has no pattern parser and does not consume sampled law evidence.
package regexpplan

import (
	"crypto/sha256"
	"fmt"
)

type Kind uint8

const (
	Empty Kind = iota
	Bytes
	Concat
	Alternative
	Repeat
	Capture
)

// Node is construction input, not retained by Compile. Max=-1 means unbounded.
// Offset is a decoded-pattern byte offset for a later source-map adapter.
type Node struct {
	Kind     Kind
	Set      [4]uint64
	Children []*Node
	Min, Max int
	Name     string
	Offset   int
}

func Byte(b byte) *Node {
	n := &Node{Kind: Bytes}
	n.Set[b/64] = 1 << (b % 64)
	return n
}

type Mode uint8

const (
	Parse Mode = iota
	Find
)

type Engine string

const (
	NFA    Engine = "nfa"
	Direct Engine = "direct"
	Table  Engine = "table"
)

// Limits may lower, but not raise, the compiler's hard bounds.
type Limits struct {
	Nodes, Instructions, States, Work, DirectBytes, TableBytes int
}

func DefaultLimits() Limits { return Limits{4096, 65536, 4096, 1000000, 8192, 65536} }

type Error struct {
	Offset int
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("pattern byte %d: %s", e.Offset, e.Reason) }

type Inspection struct {
	Profile, CapturePolicy, Reason string
	Engine                         Engine
	Nodes, Instructions, States    int
	Work, GoBytes, TableBytes      int
}

type Plan struct {
	code  []inst
	start int
	mode  Mode
	names []string
	ir    *candidate
	info  Inspection
	key   string
}

func (p *Plan) Inspect() Inspection { return p.info }
func (p *Plan) Key() string         { return p.key }
func (p *Plan) Names() []string     { return append([]string(nil), p.names...) }

// Compile checks the base construction before it attempts any optimization.
// A failed one-pass construction is not evidence: it selects canonical NFA.
func Compile(root *Node, mode Mode, limits Limits) (*Plan, error) {
	hard := DefaultLimits()
	got := []int{limits.Nodes, limits.Instructions, limits.States, limits.Work, limits.DirectBytes, limits.TableBytes}
	max := []int{hard.Nodes, hard.Instructions, hard.States, hard.Work, hard.DirectBytes, hard.TableBytes}
	for i, v := range got {
		if v < 0 || v > max[i] {
			return nil, &Error{0, "invalid compiler limit"}
		}
	}
	if mode != Parse && mode != Find {
		return nil, &Error{0, "invalid match mode"}
	}
	p := &Plan{mode: mode}
	p.info = Inspection{Profile: "byte-ascii-v1", CapturePolicy: "all-occurrences", Engine: NFA}
	ids := map[*Node]int{}
	names := map[string]bool{}
	visiting := map[*Node]bool{}
	var check func(*Node, int) (int, bool, error)
	check = func(n *Node, depth int) (int, bool, error) {
		fail := func(s string) (int, bool, error) {
			off := 0
			if n != nil {
				off = n.Offset
			}
			return 0, false, &Error{off, s}
		}
		if n == nil || depth > 256 || visiting[n] {
			return fail("nil, cyclic or excessively deep pattern")
		}
		visiting[n] = true
		defer delete(visiting, n)
		if len(n.Children) > limits.Nodes {
			return fail("expanded node budget")
		}
		size, nullable := 1, false
		childNull := []bool{}
		for _, child := range n.Children {
			cost, empty, err := check(child, depth+1)
			if err != nil {
				return 0, false, err
			}
			size += cost
			if size > limits.Nodes {
				return fail("expanded node budget")
			}
			childNull = append(childNull, empty)
		}
		switch n.Kind {
		case Empty:
			if len(n.Children) != 0 {
				return fail("empty node has children")
			}
			nullable = true
		case Bytes:
			if len(n.Children) != 0 || n.Set == [4]uint64{} {
				return fail("empty byte language or byte node with children")
			}
		case Concat, Alternative:
			if len(n.Children) == 0 {
				return fail("structural node needs children")
			}
			nullable = n.Kind == Concat
			for _, empty := range childNull {
				if n.Kind == Concat {
					nullable = nullable && empty
				} else {
					nullable = nullable || empty
				}
			}
		case Capture:
			if len(n.Children) != 1 || n.Name == "" || names[n.Name] {
				return fail("capture needs one child and a unique name")
			}
			names[n.Name] = true
			ids[n] = len(p.names) + 1
			p.names = append(p.names, n.Name)
			nullable = childNull[0]
		case Repeat:
			if len(n.Children) != 1 || n.Min < 0 || n.Max < -1 || (n.Max >= 0 && n.Max < n.Min) {
				return fail("invalid repeat bounds")
			}
			if childNull[0] {
				return fail("nullable repeated body")
			}
			copies := n.Max
			if copies == -1 {
				if n.Min >= limits.Nodes {
					return fail("expanded node budget")
				}
				copies = n.Min + 1
			}
			body := size - 1
			if copies > limits.Nodes || (copies > 0 && body > (limits.Nodes-1)/copies) {
				return fail("expanded node budget")
			}
			size = 1 + body*copies
			nullable = n.Min == 0
		default:
			return fail("unsupported AST kind")
		}
		if size > limits.Nodes {
			return fail("expanded node budget")
		}
		return size, nullable, nil
	}
	nodes, _, err := check(root, 0)
	if err != nil {
		return nil, err
	}
	p.info.Nodes = nodes
	builder := compiler{limit: limits.Instructions, ids: ids}
	accept := builder.add(inst{op: acceptOp})
	p.start = builder.node(root, accept)
	if builder.overflow {
		return nil, &Error{root.Offset, "NFA instruction budget"}
	}
	p.code = builder.code
	p.info.Instructions = len(p.code)
	p.ir, p.info.Work, p.info.Reason = construct(p, limits)
	if p.ir != nil {
		if err := validate(p, p.ir); err != nil {
			return nil, fmt.Errorf("internal construction validation: %w", err)
		}
		p.info.States = len(p.ir.states)
		p.info.Engine = Direct
		// Measure the actual serialized body, not a guessed machine-code size.
		direct, err := p.emit(Direct)
		if err != nil {
			return nil, err
		}
		p.info.GoBytes = len(direct)
		if len(direct) > limits.DirectBytes {
			p.info.TableBytes = tableSize(p.ir)
			if p.info.TableBytes <= limits.TableBytes {
				p.info.Engine, p.info.Reason = Table, "direct source cap"
			} else {
				p.info.Engine, p.info.Reason = NFA, "direct and table caps"
			}
		}
	}
	source, err := p.emit(p.info.Engine)
	if err != nil {
		return nil, err
	}
	if err := p.ValidateGo(source); err != nil {
		return nil, err
	}
	p.info.GoBytes = len(source)
	// No cache is rooted here. RC4b can dedup this versioned semantic identity.
	p.key = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("regexpplan-v1/byte-ascii-v1/%d/all-occurrences/%v/%v/%d", mode, p.names, p.code, p.start))))
	return p, nil
}
