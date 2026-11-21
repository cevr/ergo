package lower

// IR builder: case of the known variant across an inlined call
// (IR-GETMATCH). A match whose subject is a local that only variants
// built in place write, such as the copy of an inlined `c.Get(k)`
// (ir_forms_inline.go) whose returns build `Some{…}` or `None`, builds a
// union value through memory and then dispatches on its tag, for a
// variant each write already knows. The text path's knownmatch.go sends
// each such return straight to its arm; Go's SSA has no jump threading
// through a phi of constant tags, so the switch stays unless the IR
// removes it.
//
// knownVariants rewrites, in a body the inliner changed, a statement list
// of the shape
//
//	var S                      (or def S = variant)
//	…                          (writes of S: set S = variant, then a goto)
//	switch S.tag / S.(type)    (the erased match form, Was)
//
// into: a `var x` for each arm's `T as x`; each write of S becomes the
// write of its variant's value into the binding of the arm that takes its
// tag (or a discard of it), and a goto to that arm; the arms follow as
// labeled blocks, each then jumping past the last. No union value is
// built and no tag is tested. It applies when:
//   - the match form's layout is a flat union or a sealed interface, and
//     S is the local its subject is read into;
//   - every write of S between its declaration and the match is a set of
//     a variant form of that layout, outside function literals, and S is
//     read nowhere else (the match's subject and its arms' bindings only);
//   - control cannot reach the match except through those writes: after
//     the rewrite, the statements before it end in a jump (a label that
//     only the writes' own gotos reached goes with them);
//   - no arm breaks out of the switch.
//
// The reference is irKnownLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irKnownLowering=off).
//
// Prior art: GHC's case-of-known-constructor with join points; OCaml's
// flambda and ReScript's Lambda simplifier fold a switch on a block whose
// tag they see built; the text path's knownmatch.go (VI2). IRF-1's
// fuseMatch (ir_forms_match.go) does this for a callee it writes at the
// match from its syntax; this pass covers a callee the IR inliner copied.

import "strconv"

// irKnownLowering is "off" for the reference: every match keeps its switch.
var irKnownLowering string

// IRKnownOn reports that the known-variant pass runs (the property's reach check).
func IRKnownOn() bool { return irKnownLowering != "off" }

// knownVariants applies the rewrite to list and every list nested in it,
// outside function literals.
func (in *irInliner) knownVariants(list []*irStmt) []*irStmt {
	if irKnownLowering == "off" {
		return list
	}
	for _, s := range list {
		s.Then, s.Else, s.Body = in.knownVariants(s.Then), in.knownVariants(s.Else), in.knownVariants(s.Body)
		for _, c := range s.Cases {
			c.Body = in.knownVariants(c.Body)
		}
	}
	for i := 0; i < len(list); i++ {
		sw := list[i]
		if sw.Was == nil || (sw.K != "switch" && sw.K != "tswitch") || sw.Was.Subj < 0 || (sw.Was.Layout != layoutFlat && sw.Was.Layout != layoutIface) {
			continue
		}
		subj := sw.Was.Subj
		j := -1
		for k := i - 1; k >= 0; k-- {
			if d := list[k]; (d.K == "var" || d.K == "def") && d.Local == subj {
				j = k
				break
			}
		}
		if j < 0 {
			continue
		}
		if out, ok := in.knownSite(list, j, i); ok {
			i = len(out) - (len(list) - (i + 1)) - 1 // the last statement the rewrite wrote
			list = out
		}
	}
	return list
}

// knownSite is list with the match list[i] on the local list[j] declares
// rewritten, through list[i]; ok is false when the shape does not hold
// (list is then unchanged).
func (in *irInliner) knownSite(list []*irStmt, j, i int) (out []*irStmt, ok bool) {
	sw, decl := list[i], list[j]
	m, subj := sw.Was, sw.Was.Subj
	// The subject: the tag of S (flat) or S itself (interface).
	switch {
	case m.Layout == layoutFlat && (sw.X == nil || sw.X.K != "field" || sw.X.X == nil || sw.X.X.K != "local" || sw.X.X.I != subj):
		return nil, false
	case m.Layout == layoutIface && (sw.X == nil || sw.X.K != "local" || sw.X.I != subj):
		return nil, false
	}
	// The arms: each case's body without its binding; tags to arms.
	type arm struct {
		body  []*irStmt
		bind  int // the binding's local, or -1
		label string
		used  bool
	}
	in.known++
	prefix := "known" + strconv.Itoa(in.known) + "_"
	arms := make([]*arm, len(sw.Cases))
	byTag := map[int]*arm{}
	var def *arm
	for a, c := range sw.Cases {
		ar := &arm{body: c.Body, bind: -1, label: prefix + strconv.Itoa(a)}
		if m.Arms[a].Bind != nil {
			if len(c.Body) == 0 || c.Body[0].K != "def" {
				return nil, false
			}
			ar.bind, ar.body = c.Body[0].Local, c.Body[1:]
		}
		arms[a] = ar
		for _, t := range m.Arms[a].Tags {
			if _, dup := byTag[t]; !dup {
				byTag[t] = ar
			}
		}
		if c.Def {
			def = ar
		}
	}
	if sw.Else != nil {
		if def != nil {
			return nil, false
		}
		def = &arm{body: sw.Else, bind: -1, label: prefix + "else"}
		arms = append(arms, def)
	}
	for _, ar := range arms {
		if irBreaks(ar.body, "") || irNames(ar.body, subj, true) {
			return nil, false
		}
	}
	if irNames(list[i+1:], subj, true) {
		return nil, false
	}
	end := prefix + "end"
	endUsed := false
	target := func(tag int) *arm {
		if ar := byTag[tag]; ar != nil {
			return ar
		}
		return def
	}
	// The writes, on a copy of the statements between: each becomes the
	// write of its arm's binding and a goto; the goto after it (to the
	// end of an inlined body) is dead and goes.
	region := in.clone(list[j+1:i], nil)
	if decl.K == "def" {
		region = append([]*irStmt{{K: "set", Pos: decl.Pos, Local: subj, X: decl.X}}, region...)
	}
	bad := false
	var rewrite func(l []*irStmt) []*irStmt
	rewrite = func(l []*irStmt) []*irStmt {
		var o []*irStmt
		for k := 0; k < len(l); k++ {
			s := l[k]
			if s.K == "set" && s.Local == subj {
				x := s.X
				if s.Op != "" || x == nil || x.Was == nil || x.Was.Layout != m.Layout {
					bad = true
					return l
				}
				payload := x.X
				if m.Layout == layoutFlat {
					if len(x.Args) != 1 {
						bad = true
						return l
					}
					payload = x.Args[0]
				}
				if irExprNames(payload, subj) {
					bad = true
					return l
				}
				ar := target(x.Was.Tag)
				var body []*irStmt
				label := end
				switch {
				case ar == nil:
					body = append(body, &irStmt{K: "discard", Pos: s.Pos, X: payload})
					endUsed = true
				case ar.bind >= 0:
					body = append(body, &irStmt{K: "set", Pos: s.Pos, Local: ar.bind, X: payload})
					label, ar.used = ar.label, true
				default:
					body = append(body, &irStmt{K: "discard", Pos: s.Pos, X: payload})
					label, ar.used = ar.label, true
				}
				body = append(body, &irStmt{K: "goto", Pos: s.Pos, Label: label})
				o = append(o, &irStmt{K: "block", Pos: s.Pos, End: s.Pos, Body: body})
				if k+1 < len(l) && l[k+1].K == "goto" {
					k++ // dead: control left by the goto above
				}
				continue
			}
			if irStmtNamesOwn(s, subj) {
				bad = true
				return l
			}
			s.Then, s.Else, s.Body = rewrite(s.Then), rewrite(s.Else), rewrite(s.Body)
			for _, c := range s.Cases {
				c.Body = rewrite(c.Body)
			}
			o = append(o, s)
		}
		return o
	}
	region = rewrite(region)
	if bad || irNamesInLiteral(region, subj) {
		return nil, false
	}
	// A label at the top that no goto reaches any more goes; then the
	// statements must end in a jump.
	var kept []*irStmt
	for _, s := range region {
		if s.K == "block" && s.Label != "" && len(s.Body) == 0 && !irJumpsTo(region, s.Label) {
			continue
		}
		kept = append(kept, s)
	}
	region = kept
	if !irListTerminates(region) {
		return nil, false
	}
	out = append(out, list[:j]...)
	for _, ar := range arms {
		if ar.bind >= 0 && ar.used {
			out = append(out, &irStmt{K: "var", Pos: decl.Pos, Local: ar.bind})
		}
	}
	out = append(out, region...)
	for _, ar := range arms {
		if !ar.used {
			continue
		}
		out = append(out, &irStmt{K: "block", Pos: sw.Pos, End: sw.End, Label: ar.label, Body: ar.body})
		if !irListTerminates(ar.body) {
			out = append(out, &irStmt{K: "goto", Pos: sw.End, Label: end})
			endUsed = true
		}
	}
	if endUsed {
		out = append(out, &irStmt{K: "block", Pos: sw.End, End: sw.End, Label: end})
	}
	in.st.report.KnownVariants++
	return append(out, list[i+1:]...), true
}

// irExprNames reports a read of local x in e, function literals included.
func irExprNames(e *irExpr, x int) bool {
	found := false
	irExprWalk(e, false, func(e *irExpr, _ bool) {
		found = found || e.K == "local" && e.I == x
	})
	return found || e != nil && e.K == "func" && irNames(e.Body, x, true)
}

// irStmtNamesOwn reports a reference to x by s itself (its local, key,
// value or locals, or its expressions), not by its nested lists.
func irStmtNamesOwn(s *irStmt, x int) bool {
	switch s.K {
	case "def", "set", "var", "def2", "mapget", "count":
		if s.Local == x {
			return true
		}
	}
	if s.Key != nil && *s.Key == x || s.Val != nil && *s.Val == x {
		return true
	}
	for _, l := range s.Ls {
		if l == x {
			return true
		}
	}
	if irExprNames(s.X, x) || irExprNames(s.Y, x) {
		return true
	}
	for _, c := range s.Cases {
		for _, v := range c.Vals {
			if irExprNames(v, x) {
				return true
			}
		}
	}
	return false
}

// irJumpsTo reports a goto to label in list, outside function literals.
func irJumpsTo(list []*irStmt, label string) bool {
	found := false
	irWalk(list, false, func(s *irStmt, lit bool) {
		found = found || !lit && s.K == "goto" && s.Label == label
	}, func(*irExpr, bool) {})
	return found
}
