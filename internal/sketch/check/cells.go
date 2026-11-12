package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Provider cells (types.md, Interface assignability 6; owner q121,
// 2026-11-11: "a value provider converts to an operation type by moving
// into a cell, only for an operation type sealed in its own package, and
// the cell is task-confined").
//
// A value provider P (Laws 10) is a named type whose functions provide the
// operations of an operation type T, one or more of them on *P. A value of
// P converts to T by moving into a cell: the value the operations write.
// The conversion is admitted only when
//
//  1. T is sealed: one of its operations is unexported, so only a type of
//     T's package implements it (sealedBy), and T is no Go interface;
//  2. the conversion is written in that package;
//  3. it is no provider of a context (a with's provider is shared by the
//     tasks of its block).
//
// A cell holds its own value: the lowering copies a source that stays
// live (values.go), so a write through the cell is never seen through the
// source, nor the reverse. A P that holds a resource keeps R22 and R23.
//
// Task confinement. A cell type is a sealed operation type that a type of
// its package provides through a *P function (cellType): decided from the
// declarations, so no order of checking changes it. A type holds a cell
// when it is a cell type or holds one in a field, element, variant or type
// argument (holdsCell). Nothing that holds a cell reaches another task:
//
//   - an escaping frame never captures a local that holds one (escaping.go,
//     the capture hook; a non-escaping frame and a with's block run on the
//     calling task, before the call returns);
//   - no type argument holds one: a generic body, queue.Queue, sync.Ref and
//     Task could carry it to another task;
//   - no package let holds one: every task reads a package let;
//   - a value that holds one converts to no other operation type, which
//     would hide the cell from these rules.
//
// Prior art: Swift's non-Sendable classes are confined to their isolation
// domain and the compiler refuses a capture by a @Sendable closure or a
// send to another actor; Rust's Rc and RefCell are !Send, so thread::spawn
// refuses a closure that captures one; Kotlin/Native's legacy memory model
// froze shared objects. Gleam, Elixir and ReScript have no mutable cell a
// process shares: an Erlang process owns its state and sends copies.

const (
	cellCaptureText = "%s holds the provider cell %s, which is confined to the task that made it, and this function literal or block may outlive the call that made it or run on another task; use the cell outside the literal, or make a new one inside it (types.md, Interface assignability 6)"
	cellTypeArgText = "%s holds the provider cell %s, which is confined to the task that made it, and a type argument may carry it to another task (a generic body, a queue, a Ref, a Task) (types.md, Interface assignability 6)"
	cellLetText     = "the package let %s holds the provider cell %s, which is confined to the task that made it, and every task reads a package let (types.md, Interface assignability 6)"
	cellHideText    = "%s holds the provider cell %s, and %s is no cell type, so the cell would leave the rules that keep it on its task (types.md, Interface assignability 6)"
)

// sealedPkg is the package that seals the operation type it: the package
// of one of its unexported operations, or nil when every one is exported.
func sealedPkg(it *Interface) *Package {
	for _, m := range it.Methods {
		if m.pkg != nil {
			return m.pkg
		}
	}
	return nil
}

// cellRefusal is why a value of v, which provides the operation type t
// (it) only through *v functions, does not move into a cell here, or "".
func (c *checker) cellRefusal(v, t Type, it *Interface) string {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg.Host() {
		return fmt.Sprintf("a cell converts only to an operation type of the program, and %s is not one", t)
	}
	if c.providing {
		return fmt.Sprintf("a cell is confined to one task, and the provider of a with is shared by the tasks of its block; provide a type whose operations take %s by value", v)
	}
	p := sealedPkg(it)
	if p == nil {
		return fmt.Sprintf("so %s converts to %s only by moving into a cell, and a cell needs an operation type sealed in its provider's package (an unexported operation); %s has none (types.md, Interface assignability 6)", v, t, t)
	}
	if p != c.pkg {
		return fmt.Sprintf("so %s converts to %s only by moving into a cell, and a cell of %s is made only in package %s, which seals it (types.md, Interface assignability 6)", v, t, t, p.Name)
	}
	return ""
}

// cellType reports whether t is a cell type: a sealed operation type of
// the program that a type of its package provides, every operation, with
// one or more of them on *P.
func (c *checker) cellType(t Type) bool {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg.Host() {
		return false
	}
	it := asInterface(n)
	if it == nil || it.Set != SetAll || len(it.Methods) == 0 {
		return false
	}
	o := n.origin()
	if v, ok := c.cellTypes[o]; ok {
		return v
	}
	if c.cellTypes == nil {
		c.cellTypes = map[*Named]bool{}
	}
	c.cellTypes[o] = false
	p := sealedPkg(it)
	if p == nil {
		return false
	}
	for _, tn := range p.Types {
		pn, ok := tn.Type.(*Named)
		if !ok || asInterface(pn) != nil {
			continue
		}
		all, ptr := true, false
		for _, m := range it.Methods {
			f := pn.method(m.Name)
			if f == nil {
				all = false
				break
			}
			ptr = ptr || f.Ptr
		}
		if all && ptr {
			c.cellTypes[o] = true
			return true
		}
	}
	return false
}

// holdsCell is the cell type t is or holds, or nil.
func (c *checker) holdsCell(t Type) *Named {
	if t == nil {
		return nil
	}
	h, _ := c.cellWalk(t, map[int32]bool{})
	return h
}

// cellDepth bounds holdsCell's path. A valid program's types nest less
// deeply; an instance chain past it is an instantiation cycle (mono.go).
const cellDepth = 128

// cellWalk is holdsCell's walk over interned types (a type built twice is
// one id): cut reports that it stopped at a type already on its path, so a
// nil answer is not final for t and is not memoized.
func (c *checker) cellWalk(t Type, path map[int32]bool) (h *Named, cut bool) {
	switch t.(type) {
	case *Named, *Struct, *Tuple, *Union, *Slice, *Array, *Map:
	default:
		// A basic type, a borrow, a row, a function type and a type
		// parameter hold no cell (a type argument is checked where it is
		// bound).
		return nil, false
	}
	id, infer := c.types.intern(t)
	if h, ok := c.cellHolds[id]; ok {
		return h, false
	}
	if path[id] || len(path) > cellDepth {
		// A cycle, or an instance chain that grows without end (an
		// instantiation cycle, which mono.go refuses): stop here.
		return nil, true
	}
	path[id] = true
	defer func() {
		delete(path, id)
		if !infer && (h != nil || !cut) {
			if c.cellHolds == nil {
				c.cellHolds = map[int32]*Named{}
			}
			c.cellHolds[id] = h
		}
	}()
	return c.cellParts(t, path)
}

// cellParts is cellWalk's step: the cell t is, or the first one a part of
// t holds.
func (c *checker) cellParts(t Type, path map[int32]bool) (*Named, bool) {
	cut := false
	visit := func(u Type) *Named {
		h, k := c.cellWalk(u, path)
		cut = cut || k
		return h
	}
	switch t := t.(type) {
	case *Named:
		if t.foreign || t.Obj == nil || t.Obj.Pkg.Host() {
			return nil, false
		}
		if c.cellType(t) {
			return t, false
		}
		for _, a := range t.TArgs {
			if h := visit(a); h != nil {
				return h, false
			}
		}
		if asInterface(t) != nil {
			// An operation type's value holds no cell: no value that
			// holds one converts to it (cellHideText).
			return nil, cut
		}
		h := visit(t.underlying())
		return h, cut
	case *Struct:
		for _, f := range t.Fields {
			if h := visit(f.Type); h != nil {
				return h, false
			}
		}
	case *Tuple:
		for _, e := range t.Types {
			if h := visit(e); h != nil {
				return h, false
			}
		}
	case *Union:
		for _, e := range t.Variants {
			if h := visit(e); h != nil {
				return h, false
			}
		}
	case *Slice:
		h := visit(t.Elem)
		return h, cut
	case *Array:
		h := visit(t.Elem)
		return h, cut
	case *Map:
		if h := visit(t.Key); h != nil {
			return h, false
		}
		h := visit(t.Elem)
		return h, cut
	}
	return nil, cut
}

// cellSite is a type argument recorded at an instantiation, decided once
// every declaration is known (checkCells).
type cellSite struct {
	file *fileInfo
	at   syntax.Node
	arg  Type
}

// recordCellArgs records the type arguments of an instantiation.
func (c *checker) recordCellArgs(targs []Type, at syntax.Node) {
	if c.silent > 0 || at == nil {
		return
	}
	for _, a := range targs {
		if a != nil {
			c.cellSites = append(c.cellSites, cellSite{file: c.cur, at: at, arg: a})
		}
	}
}

// checkCells refuses a type argument and a package let that hold a cell.
func (c *checker) checkCells() {
	seen := map[syntax.Node]bool{}
	for _, s := range c.cellSites {
		if seen[s.at] || containsInvalid(s.arg) {
			continue
		}
		if h := c.holdsCell(s.arg); h != nil {
			seen[s.at] = true
			c.cur = s.file
			c.errorf(s.at, CodeType, cellTypeArgText, s.arg, h)
		}
	}
	for _, f := range c.files {
		c.cur = f
		for _, d := range f.syntax.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, s := range vd.Specs {
				for _, id := range s.Names {
					v, ok := c.pkg.Info.Defs[id].(*Var)
					if !ok || v.Type == nil || containsInvalid(v.Type) {
						continue
					}
					if h := c.holdsCell(v.Type); h != nil {
						c.errorf(id, CodeType, cellLetText, id.Name, h)
					}
				}
			}
		}
	}
}

// cellCapture checks a use of the local v in an escaping frame.
func (w *escWalk) cellCapture(id *syntax.Ident, v *Var) {
	if v.Type == nil || containsInvalid(v.Type) {
		return
	}
	if w.escapingFrom(v) == nil {
		return
	}
	if h := w.c.holdsCell(v.Type); h != nil {
		w.c.errorf(id, CodeType, cellCaptureText, id.Name, h)
	}
}
