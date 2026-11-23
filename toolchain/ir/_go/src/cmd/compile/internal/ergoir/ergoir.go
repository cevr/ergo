// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Experimental (Ergo IR1 spike): typed function bodies from Ergo IR.
//
// With -ergoir=FILE, every function FILE names must be declared in the Go
// source without a body. ergoAttach decodes each IR body, checks every node
// against the package's types2 objects, and builds the typed syntax body
// (with the type records types2 would have made) that the unified writer
// then encodes like any other. Go never parses or type-checks an IR body,
// and a node that does not check is a compile error at its own position.
//
// The decoder is one file per construct family: ergoir.go (format, entry,
// functions), ergoir_types.go, ergoir_stmt.go, ergoir_expr.go and
// ergoir_call.go.

package ergoir

import (
	"encoding/json"
	"internal/types/errors"
	"os"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ergoFile is the IR file: the source files positions refer to, and the
// bodies.
type ergoFile struct {
	V     int        `json:"v"`
	Files []string   `json:"files"`
	Funcs []ergoFunc `json:"funcs"`
}

// ergoFunc is one body: Name is F, or T.M for a method of T. Locals
// declares the body's own variables; index i < number of receiver and
// parameters names those, the rest names Locals in order.
type ergoFunc struct {
	Name   string      `json:"name"`
	Pos    ergoPos     `json:"pos"`
	End    ergoPos     `json:"end"`
	Locals []ergoLocal `json:"locals"`
	Body   []*ergoStmt `json:"body"`
	// Results binds the signature's results, which the Go declaration
	// names, as locals after the parameters (IR2-RESULTS): a frame's
	// deferred end reads the named error result, as the emitter's
	// `defer fs.End(&err)` does.
	Results bool `json:"results,omitempty"`
}

type ergoLocal struct {
	Name string  `json:"name"`
	Type string  `json:"type"`
	Pos  ergoPos `json:"pos"`
}

// ergoPos is file index, line, column.
type ergoPos [3]int

type ergoStmt struct {
	K     string      `json:"k"` // def set ret if range while expr var discard break continue panic
	Pos   ergoPos     `json:"pos"`
	Local int         `json:"local,omitempty"`
	Key   *int        `json:"key,omitempty"`
	Val   *int        `json:"val,omitempty"`
	X     *ergoExpr   `json:"x,omitempty"`
	Then  []*ergoStmt `json:"then,omitempty"`
	Else  []*ergoStmt `json:"else,omitempty"`
	Body  []*ergoStmt `json:"body,omitempty"`
	End   ergoPos     `json:"end"`
	// ElsePos and ElseEnd bound an if's else block.
	ElsePos ergoPos `json:"epos"`
	ElseEnd ergoPos `json:"eend"`
	// Op is an op-assignment's operator (set: x op= X).
	Op string `json:"op,omitempty"`
	// Cases are a "tswitch"'s clauses (ergoir_fail.go); Else is its
	// default clause, at ElsePos.
	Cases []*ergoCase `json:"cases,omitempty"`
	// Label is a loop's or a block's label, or the label a break,
	// continue or goto names (ergoir_label.go).
	Label string `json:"label,omitempty"`
	// Y is a store's value (store: X = Y, X a field; IRG6), or a "count"
	// loop's bound (ergoir_stmt.go).
	Y *ergoExpr `json:"y,omitempty"`
	// Ls are a "tdef"'s locals, one per result, -1 for Go's blank
	// (ergoir_tuple.go).
	Ls []int `json:"ls,omitempty"`
}

type ergoExpr struct {
	K     string      `json:"k"` // local const bin un call mcall field index lit len pcall conv global
	Pos   ergoPos     `json:"pos"`
	I     int         `json:"i,omitempty"`
	T     string      `json:"t,omitempty"`
	V     string      `json:"v,omitempty"`
	Op    string      `json:"op,omitempty"`
	X     *ergoExpr   `json:"x,omitempty"`
	Y     *ergoExpr   `json:"y,omitempty"`
	Name  string      `json:"name,omitempty"`
	Args  []*ergoExpr `json:"args,omitempty"`
	Elems []*ergoExpr `json:"elems,omitempty"`
	Keys  []string    `json:"keys,omitempty"` // a record literal's field names
	// Params, Body and End are a function literal's (func; IRG6): its
	// parameters' locals, its statements and its closing brace.
	Params []int       `json:"params,omitempty"`
	Body   []*ergoStmt `json:"body,omitempty"`
	End    ergoPos     `json:"end"`
}

var ergoBinOps = map[string]syntax.Operator{
	"+": syntax.Add, "-": syntax.Sub, "*": syntax.Mul, "/": syntax.Div, "%": syntax.Rem,
	"==": syntax.Eql, "!=": syntax.Neq, "<": syntax.Lss, "<=": syntax.Leq, ">": syntax.Gtr, ">=": syntax.Geq,
	"&&": syntax.AndAnd, "||": syntax.OrOr,
	"&": syntax.And, "|": syntax.Or, "^": syntax.Xor, "&^": syntax.AndNot, "<<": syntax.Shl, ">>": syntax.Shr,
}

type ergoChecker struct {
	m      *posMap
	pkg    *types2.Package
	info   *types2.Info
	bases  []*syntax.PosBase
	sig    *types2.Signature
	locals []*types2.Var
	// defined are the locals a body defines (name with def, a function
	// literal's parameter or result); reads the first read of each body
	// local, by index: a body local read and never defined is the IR
	// builder's bug, reported as such (ergoReadBeforeDef), not left to
	// gc's writer.
	defined map[*types2.Var]bool
	reads   map[int]ergoPos
	nerr    int
	loops   []*syntax.ForStmt // the loops around the statement being decoded
	// loopLabels are the loops' labels ("" for none); labels the
	// function's declared labels; gotos its gotos (ergoir_label.go).
	loopLabels []string
	labels     map[string]bool
	gotos      []*ergoStmt
	ctxt       *types2.Context // shares the instances of generic types (ergoir_types.go)
	// tparams are the type parameters of the function being decoded, its
	// receiver's then its own (F1): a type spelling names them first.
	tparams []*types2.TypeParam
	// rangeFunc reports a range over a func in the body being decoded,
	// outside its function literals: rangefunc.Rewrite names a literal's
	// results then (ergoir_closure.go).
	rangeFunc bool
	// tdef is the call a "tdef" binds, whose several results call
	// admits (ergoir_tuple.go).
	tdef *ergoExpr
}

// ergoAttach gives every function the IR file names its checked body.
func ergoAttach(m posMap, pkg *types2.Package, info *types2.Info, noders []*noder) {
	data, err := os.ReadFile(ergoFlag.IR)
	if err != nil {
		base.Fatalf("ergoir: %v", err)
	}
	var f ergoFile
	if err := json.Unmarshal(data, &f); err != nil {
		base.Fatalf("ergoir: %s: %v", ergoFlag.IR, err)
	}
	if f.V != 1 {
		base.Fatalf("ergoir: %s: version %d, want 1", ergoFlag.IR, f.V)
	}
	c := &ergoChecker{m: &m, pkg: pkg, info: info}
	for _, name := range f.Files {
		c.bases = append(c.bases, syntax.NewFileBase(name))
	}
	decls := map[string]*syntax.FuncDecl{}
	for _, p := range noders {
		for _, d := range p.file.DeclList {
			fd, ok := d.(*syntax.FuncDecl)
			if !ok {
				continue
			}
			obj, ok := info.Defs[fd.Name].(*types2.Func)
			if !ok {
				continue
			}
			decls[ergoKey(obj)] = fd
		}
	}
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		fd := decls[fn.Name]
		switch {
		case fd == nil:
			c.errorf(fn.Pos, "ergoir: %s is not declared in the Go source", fn.Name)
			continue
		case ergoPlaceholder(info, fd.Body):
			fd.Body = nil // a generic declaration's (ergoir_placeholder.go)
		case fd.Body != nil:
			c.errorf(fn.Pos, "ergoir: %s has a Go body as well as an IR body", fn.Name)
			continue
		}
		c.function(fd, fn)
		delete(decls, fn.Name) // a second IR body for the same name finds it gone
	}
	// A placeholder the IR gave no body is an error, never a panic at run
	// time (ergoir_placeholder.go).
	for _, p := range noders {
		for _, d := range p.file.DeclList {
			if fd, ok := d.(*syntax.FuncDecl); ok && ergoPlaceholder(info, fd.Body) {
				base.ErrorfAt(m.makeXPos(fd.Pos()), errors.BadDecl, "ergoir: %s has the placeholder body and no IR body", fd.Name.Value)
			}
		}
	}
	base.ExitIfErrors()
}

// ergoKey is F or T.M.
func ergoKey(obj *types2.Func) string {
	sig := obj.Type().(*types2.Signature)
	if sig.Recv() == nil {
		return obj.Name()
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types2.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types2.Unalias(t).(*types2.Named); ok {
		return n.Obj().Name() + "." + obj.Name()
	}
	return "?." + obj.Name()
}

func (c *ergoChecker) pos(p ergoPos) syntax.Pos {
	if p[0] < 0 || p[0] >= len(c.bases) {
		return syntax.Pos{}
	}
	return syntax.MakePos(c.bases[p[0]], uint(p[1]), uint(p[2]))
}

func (c *ergoChecker) errorf(p ergoPos, format string, args ...any) {
	c.nerr++
	pos := c.pos(p)
	if !pos.IsKnown() {
		base.Errorf(format, args...)
		return
	}
	base.ErrorfAt(c.m.makeXPos(pos), 0, format, args...)
}

// ergoReadBeforeDef reports each body local (index first and up) that the
// body reads and never defines: gc's writer would otherwise assert on the
// undeclared name with a dump that names no IR local.
func (c *ergoChecker) ergoReadBeforeDef(fn string, first int) {
	for i := first; i < len(c.locals); i++ {
		p, read := c.reads[i]
		if read && !c.defined[c.locals[i]] {
			c.errorf(p, "ergoir: local %d (%s) read before any def in %s", i, c.locals[i].Name(), fn)
		}
	}
}

func (c *ergoChecker) function(fd *syntax.FuncDecl, fn *ergoFunc) {
	obj := c.info.Defs[fd.Name].(*types2.Func)
	c.sig = obj.Type().(*types2.Signature)
	c.locals, c.labels, c.gotos = nil, nil, nil
	if r := c.sig.Recv(); r != nil {
		c.locals = append(c.locals, r)
	}
	for i := 0; i < c.sig.Params().Len(); i++ {
		c.locals = append(c.locals, c.sig.Params().At(i))
	}
	if fn.Results {
		for i := 0; i < c.sig.Results().Len(); i++ {
			r := c.sig.Results().At(i)
			if r.Name() == "" || r.Name() == "_" {
				c.errorf(fn.Pos, "ergoir: %s: a result bound as a local needs a name in the Go declaration", fn.Name)
				return
			}
			c.locals = append(c.locals, r)
		}
	}
	if !ergoSigOK(c.sig) || c.sig.Variadic() {
		c.errorf(fn.Pos, "ergoir: %s: only plain signatures with at most one result take an IR body", fn.Name)
		return
	}
	// A generic function's body names its type parameters (F1): the
	// receiver's, then its own, as the Go declaration lists them.
	c.tparams = nil
	for _, l := range []*types2.TypeParamList{c.sig.RecvTypeParams(), c.sig.TypeParams()} {
		for i := 0; i < l.Len(); i++ {
			c.tparams = append(c.tparams, l.At(i))
		}
	}
	for _, l := range fn.Locals {
		t := c.typ(l.Pos, l.Type)
		if t == nil {
			return
		}
		c.locals = append(c.locals, types2.NewVar(c.pos(l.Pos), c.pkg, l.Name, t))
	}
	before := c.nerr
	first := len(c.locals) - len(fn.Locals)
	c.defined, c.reads = map[*types2.Var]bool{}, map[int]ergoPos{}
	body := &syntax.BlockStmt{List: c.stmts(fn.Body), Rbrace: c.pos(fn.End)}
	body.SetPos(c.pos(fn.Pos))
	c.ergoLabelsDone() // ergoir_label.go
	c.ergoReadBeforeDef(fn.Name, first)
	if c.nerr != before {
		return
	}
	if c.sig.Results().Len() == 1 && ergoFallible(c.sig) {
		// A fallible body with no value ends in its success (ergoir_fail.go).
		if last := lastNonEmptyStmt(body.List); last == nil || !ergoTerminates(last) {
			body.List = append(body.List, c.ret(&ergoStmt{K: "ret", Pos: fn.End}))
		}
	} else if c.sig.Results().Len() >= 1 {
		if last := lastNonEmptyStmt(body.List); last == nil || !ergoTerminates(last) {
			c.errorf(fn.End, "ergoir: %s: missing return", fn.Name)
			return
		}
	}
	fd.Body = body
}
