package check

import (
	"strings"

	"ergo.local/prototype/internal/gofacts"
	v1 "ergo.local/prototype/runtime/v1"
)

// Go values confined to one task (GB-RACE; docs/spec/interop.md, Go values
// by address 8). A memory-only Go method (GBX6, gofacts.Effects) charges
// no go.Foreign, so any task may call it, and a PCG's Uint64 reads and
// writes its two state words with no lock: two tasks that call it through
// one go.Ref race. A go.Ref to such a Go type is therefore confined to the
// task that holds it, by the provider cell's rules (cells.go): no escaping
// frame captures it, no type argument (a queue, a Ref, a Task, a generic
// body) carries it, no package let holds it, and no Ergo operation type
// hides it. A Go type whose memory-only methods are safe for concurrent
// use (an atomic, v1.GoConcurrentTypes()) is not confined.
//
// Prior art: Rust's rand::rngs::ThreadRng is !Send, so thread::spawn
// refuses a closure that captures one, while AtomicU64 is Sync; Swift
// refuses a non-Sendable class across an isolation boundary. Go itself
// documents math/rand/v2's PCG as unsafe for concurrent use and leaves
// the race to `go test -race`. Gleam and Elixir have no shared mutable
// generator: :rand keeps its state in the process dictionary, one per
// process.

const (
	goConfinedCaptureText = "%s holds %s, which reaches a Go value whose memory-only methods are not safe for concurrent use, so it is confined to the task that made it, and this function literal or block may outlive the call that made it or run on another task; make the Go value inside the literal (interop.md, Go values by address 8)"
	goConfinedTypeArgText = "%s holds %s, which reaches a Go value whose memory-only methods are not safe for concurrent use, so it is confined to the task that made it, and a type argument may carry it to another task (a generic body, a queue, a Ref, a Task) (interop.md, Go values by address 8)"
	goConfinedLetText     = "the package let %s holds %s, which reaches a Go value whose memory-only methods are not safe for concurrent use, so it is confined to one task, and every task reads a package let (interop.md, Go values by address 8)"
	goConfinedHideText    = "%s holds %s, which reaches a Go value whose memory-only methods are not safe for concurrent use, and %s would hide it from the rules that keep it on its task (interop.md, Go values by address 8)"
)

// goConfinedRef reports whether t is a confined Go holder: a go.Ref, or a
// Go type of import go, that reaches a confined Go type (GB-RACE2).
func goConfinedRef(t *Named) bool {
	if e := refElem(t); e != nil {
		return goReaches(e, map[Type]bool{})
	}
	return t.Obj != nil && t.Obj.Pkg.Host() && goReaches(t, map[Type]bool{})
}

// goReaches reports whether t, through goWalk's structure, reaches a Go
// type with a memory-only method that v1.GoConcurrentTypes() does not list.
// A listed type stops the walk: its methods are safe for concurrent use.
// A Go interface has no memory-only method: its methods are foreign. A Go
// method that reaches a field the binding refuses is foreign or is
// memory-only itself.
func goReaches(t Type, seen map[Type]bool) bool {
	return goWalk(t, seen, func(n *Named) (hit, stop bool) {
		name := n.Obj.Pkg.Path + "." + n.Obj.Name
		if v1.GoConcurrentTypes()[name] != "" {
			return false, true
		}
		return goMemoryOnly(name), false
	})
}

// goTouchesHost reports whether t, through goWalk's structure, reaches a
// Go type with a host-touching method: one whose binding charges a
// capability (go.Foreign), or any method of a Go interface, which is
// foreign (GBX1b). A Go type whose reachable methods are all memory-only
// rows (atomic.Int64, Bool) or that has none touches no host.
func (c *checker) goTouchesHost(t Type) bool {
	return goWalk(t, map[Type]bool{}, func(n *Named) (hit, stop bool) {
		if asInterface(n) != nil {
			return true, false
		}
		for _, m := range n.origin().mlist {
			if sig := c.funcSig(m); sig == nil || !sig.Uses.Empty() {
				return true, false
			}
		}
		return false, false
	})
}

// goWalk reports whether t, through the fields (an embedded one is a field
// too), elements, Map keys, go.Refs and Options its Ergo form shows,
// reaches a Go type of import go that at reports hit; a Go type at reports
// stop ends the walk there, and a Go interface's structure is never walked.
// A field the binding refuses is not reached: no Ergo source can name it.
func goWalk(t Type, seen map[Type]bool, at func(n *Named) (hit, stop bool)) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch t := t.(type) {
	case *Named:
		if t.Obj != nil && t.Obj.Pkg.Host() {
			hit, stop := at(t)
			if hit {
				return true
			}
			if stop {
				return false
			}
		}
		for _, a := range t.TArgs {
			if goWalk(a, seen, at) {
				return true
			}
		}
		if asInterface(t) != nil {
			return false
		}
		return goWalk(t.underlying(), seen, at)
	case *Struct:
		for _, f := range t.Fields {
			if goWalk(f.Type, seen, at) {
				return true
			}
		}
	case *Union:
		for _, v := range t.Variants {
			if goWalk(v, seen, at) {
				return true
			}
		}
	case *Slice:
		return goWalk(t.Elem, seen, at)
	case *Array:
		return goWalk(t.Elem, seen, at)
	case *Map:
		return goWalk(t.Key, seen, at) || goWalk(t.Elem, seen, at)
	}
	return false
}

// goMemoryOnly reports whether the Go type named name (path.Type) has a
// memory-only method (goReaches asks v1.GoConcurrentTypes() first).
func goMemoryOnly(name string) bool {
	for _, e := range gofacts.Effects {
		if strings.HasPrefix(e.Target, name+".") {
			return true
		}
	}
	return false
}

// confinedText is the text of a refusal of the confined holder h: cell's
// for a provider cell, goText's for a confined go.Ref.
func confinedText(h *Named, cell, goText string) string {
	if goConfinedRef(h) {
		return goText
	}
	return cell
}
