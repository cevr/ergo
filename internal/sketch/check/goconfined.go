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
// use (an atomic, v1.GoConcurrentTypes) is not confined.
//
// Prior art: Rust's rand::rngs::ThreadRng is !Send, so thread::spawn
// refuses a closure that captures one, while AtomicU64 is Sync; Swift
// refuses a non-Sendable class across an isolation boundary. Go itself
// documents math/rand/v2's PCG as unsafe for concurrent use and leaves
// the race to `go test -race`. Gleam and Elixir have no shared mutable
// generator: :rand keeps its state in the process dictionary, one per
// process.

const (
	goConfinedCaptureText = "%s holds %s, whose Go value's memory-only methods are not safe for concurrent use, so it is confined to the task that made it, and this function literal or block may outlive the call that made it or run on another task; make the Go value inside the literal (interop.md, Go values by address 8)"
	goConfinedTypeArgText = "%s holds %s, whose Go value's memory-only methods are not safe for concurrent use, so it is confined to the task that made it, and a type argument may carry it to another task (a generic body, a queue, a Ref, a Task) (interop.md, Go values by address 8)"
	goConfinedLetText     = "the package let %s holds %s, whose Go value's memory-only methods are not safe for concurrent use, so it is confined to one task, and every task reads a package let (interop.md, Go values by address 8)"
	goConfinedHideText    = "%s holds %s, whose Go value's memory-only methods are not safe for concurrent use, and %s would hide it from the rules that keep it on its task (interop.md, Go values by address 8)"
)

// goConfinedRef reports whether t is a go.Ref to a Go value confined to
// one task: its Go type has a memory-only method and is not safe for
// concurrent use. (No Go type of the table's packages holds such a value
// in a field; a Go struct that did would need the walk cells.go does.)
func goConfinedRef(t *Named) bool {
	n, ok := refElem(t).(*Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Host() && goMemoryOnly(n.Obj.Pkg.Path+"."+n.Obj.Name)
}

// goMemoryOnly reports whether the Go type named name (path.Type) has a
// memory-only method and no entry of v1.GoConcurrentTypes.
func goMemoryOnly(name string) bool {
	if v1.GoConcurrentTypes[name] != "" {
		return false
	}
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
