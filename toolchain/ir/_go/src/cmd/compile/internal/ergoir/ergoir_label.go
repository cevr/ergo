// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ergoir

// Ergo IR labels (IRG3b): a loop that a break inside a switch leaves
// carries a label, and the break names it, since Go's unlabeled break
// leaves the innermost switch; a match tree's shared arm is a labeled
// block that its leaves reach by goto, as the Ergo emitter writes it
// (matchtree.go).
//
//	{"k": "while"|"range", "label": "loop1", ...}  loop1: for ... { ... }
//	{"k": "break"|"continue", "label": "loop1"}   break loop1
//	{"k": "block", "label": "arm1", "body": [...]} arm1: { ... }
//	{"k": "block", "body": [...]}                 { ... }
//	{"k": "goto", "label": "arm1"}                goto arm1
//
// A label is a Go identifier, declared once per function; a break or
// continue names a loop around it; a goto names a block of the function
// (checked when the function's body is done, ergoLabelsDone).

import (
	"go/token"

	"cmd/compile/internal/syntax"
)

// labeled is st, labeled with s.Label when it has one.
func (c *ergoChecker) labeled(s *ergoStmt, st syntax.Stmt) syntax.Stmt {
	if s.Label == "" || st == nil {
		return st
	}
	if !c.declareLabel(s) {
		return nil
	}
	l := &syntax.LabeledStmt{Label: syntax.NewName(c.pos(s.Pos), s.Label), Stmt: st}
	l.SetPos(c.pos(s.Pos))
	return l
}

func (c *ergoChecker) declareLabel(s *ergoStmt) bool {
	if !token.IsIdentifier(s.Label) || s.Label == "_" {
		c.errorf(s.Pos, "ergoir: label %q", s.Label)
		return false
	}
	if c.labels == nil {
		c.labels = map[string]bool{}
	}
	if c.labels[s.Label] {
		c.errorf(s.Pos, "ergoir: label %s declared twice", s.Label)
		return false
	}
	c.labels[s.Label] = true
	return true
}

// labelStmt decodes a block or a goto, and a labeled break or continue.
func (c *ergoChecker) labelStmt(s *ergoStmt) (syntax.Stmt, bool) {
	pos := c.pos(s.Pos)
	switch s.K {
	case "block":
		return c.labeled(s, c.block(s.Pos, s.End, s.Body)), true
	case "goto":
		if !token.IsIdentifier(s.Label) {
			c.errorf(s.Pos, "ergoir: goto %q", s.Label)
			return nil, true
		}
		c.gotos = append(c.gotos, s)
		st := &syntax.BranchStmt{Tok: syntax.Goto, Label: syntax.NewName(pos, s.Label)}
		st.SetPos(pos)
		return st, true
	}
	return nil, false
}

// branch is a break or continue of the loop labeled s.Label, or of the
// innermost loop.
func (c *ergoChecker) branch(s *ergoStmt) syntax.Stmt {
	pos := c.pos(s.Pos)
	if len(c.loops) == 0 {
		c.errorf(s.Pos, "ergoir: %s outside a loop", s.K)
		return nil
	}
	st := &syntax.BranchStmt{Tok: syntax.Break, Target: c.loops[len(c.loops)-1]}
	if s.K == "continue" {
		st.Tok = syntax.Continue
	}
	if s.Label != "" {
		target := -1
		for i, l := range c.loopLabels {
			if l == s.Label {
				target = i
			}
		}
		if target < 0 {
			c.errorf(s.Pos, "ergoir: %s %s names no loop around it", s.K, s.Label)
			return nil
		}
		st.Target, st.Label = c.loops[target], syntax.NewName(pos, s.Label)
	}
	st.SetPos(pos)
	return st
}

// ergoLabelsDone checks that every goto of the function names one of its
// blocks, and forgets the function's labels.
func (c *ergoChecker) ergoLabelsDone() {
	for _, g := range c.gotos {
		if !c.labels[g.Label] {
			c.errorf(g.Pos, "ergoir: goto %s names no block", g.Label)
		}
	}
	c.labels, c.gotos = nil, nil
}
