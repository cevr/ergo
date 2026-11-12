package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The static release lowering (P-L8; runtime/v1 static.go). A declared
// function whose frame scope exists only for its owned values (a plain
// frame, v1.NewOwnerScope) and whose release plan is static keeps each
// owned value in a Go local of its own (a slot) and its presence in one
// bit of a uint64; one deferred function releases the values whose bit is
// set, last acquired first, on every exit edge, and settles the exit as
// Scope.End does. No entry, closure or list is allocated per value.
//
// The plan is static when every owned value of the frame (its taken
// parameters, its owned locals and temporaries, outside function
// literals, at most 64) has the release of its type's own Close (no
// generated release of parts, B2), when no release point carries a drop
// flag or a field mask (Info.Releases, Info.TryReleases), no owned local
// is reassigned (R41's two entries), and no bodiless std call holds a taken value
// (holdTaken, D-C). A value returned whole (v1.Returned) moves to a kept
// bit: released only when the frame's exit fails.
//
// The list lowering (B1) stays selectable: releaseLowering = "scope", by
// -ldflags -X or SetReleaseLoweringForTest. TestStaticReleaseAgrees holds
// the two equal on B1's generated programs.

// releaseLowering selects the release lowering of plain frames: "" the
// static form where the plan allows it, "scope" the list form (B1) always.
// It is a string for -ldflags -X
// (ergo.local/prototype/internal/sketch/lower.releaseLowering=scope).
var releaseLowering = ""

// SetReleaseLoweringForTest selects the release lowering ("" or "scope")
// and returns the previous one.
func SetReleaseLoweringForTest(mode string) string {
	prev := releaseLowering
	releaseLowering = mode
	return prev
}

// staticFrame is the state of a frame lowered in the static form.
type staticFrame struct {
	live, kept string // the Go names of the bit sets
	slots      []staticSlot
	bit        map[any]int // a Var or a temporary's expression: its slot
	keeps      bool        // some return keeps a value (kept is used)
}

type staticSlot struct {
	name, goType string
	release      string // the Go statements that release the slot's value
}

// staticMarker stands where the frame's prologue goes, until the body is
// written and the slots are known.
const staticMarker = "\x00static-prologue\x00"

// staticPlan reports a declared function whose plain frame may use the
// static form: params are its parameters, body its statements.
func (l *lowerer) staticPlan(params []*check.Var, body []syntax.Stmt) bool {
	if releaseLowering == "scope" {
		return false
	}
	info := l.info
	keys := map[any]bool{}
	ok := true
	add := func(key any, d *check.Drop) {
		if d == nil {
			return
		}
		if d.Glue() || d.Close == nil {
			ok = false
		}
		keys[key] = true
	}
	for _, v := range params {
		add(v, info.OwnedVars[v])
	}
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			if !ok {
				return false
			}
			if capture := info.TaskCaptures[n]; capture != nil && len(capture.Vars) > 0 {
				// Moving callbacks transport B1 Owned handles at admission.
				// A static value slot is not such a handle.
				ok = false
				return false
			}
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.AssignStmt:
				for _, lhs := range n.Lhs {
					if id, isID := unparen(lhs).(*syntax.Ident); isID {
						if v, _ := info.Uses[id].(*check.Var); v != nil && info.OwnedVars[v] != nil {
							ok = false // R41: the new value's entry, then the old one's release
						}
					}
				}
			case *syntax.CompositeLit:
				for _, r := range info.Replaced[n] {
					add(r.Temp, info.OwnedTemps[r.Temp]) // no node of the tree
				}
			case *syntax.SelectorExpr:
				if _, taken := info.FieldTakes[n]; taken {
					ok = false
				}
			case *syntax.CallExpr:
				if c := info.Calls[n]; c != nil && (c.Kind == check.CallWith || takesHeld(c)) {
					// A with's local environment ends before the hoisted defer;
					// B1 captures it at the acquisition instead.
					ok = false
				}
			case *syntax.Ident:
				if v, isVar := info.Defs[n].(*check.Var); isVar {
					add(v, info.OwnedVars[v])
				}
			}
			if x, isExpr := n.(syntax.Expr); isExpr {
				add(x, info.OwnedTemps[x])
			}
			return ok
		})
	}
	if !ok || len(keys) == 0 || len(keys) > 64 {
		return false
	}
	flagged := func(rs []check.Release) bool {
		for _, r := range rs {
			var key any = r.Temp
			if r.Var != nil {
				key = r.Var
			}
			if keys[key] && (r.Flag || r.Fields != 0 || r.MaybeFields != 0) {
				return true
			}
		}
		return false
	}
	for _, rs := range info.Releases {
		if flagged(rs) {
			return false
		}
	}
	for _, rs := range info.TryReleases {
		if flagged(rs) {
			return false
		}
	}
	return true
}

// takesHeld reports a bodiless std call that takes a value (holdTaken):
// its entry stays until the call returns, which the static form does not
// write.
func takesHeld(c *check.Call) bool {
	switch {
	case c.Kind == check.CallOp && c.Op != nil && c.Op.Sig != nil:
		return len(c.Op.Sig.Takes) > 0
	case c.Kind == check.CallFunc && c.Func != nil && c.Func.Pkg != nil && c.Func.Pkg.Std() && c.Func.Decl != nil && c.Func.Decl.Extern != nil:
		return len(c.Func.Sig.Takes) > 0
	}
	return false
}

// staticState is the static form of the frame being written, or nil.
func (fc *funcCtx) staticState() *staticFrame {
	if fc.frame == nil {
		return nil
	}
	return fc.frame.static
}

// openStatic writes the marker of the static frame's prologue.
func (fc *funcCtx) openStatic() {
	st := fc.frame.static
	st.live = fc.scope.alloc("live")
	st.kept = fc.scope.alloc("kept")
	st.bit = map[any]int{}
	fc.line(staticMarker)
}

// staticOwn registers val, a value of type t that key owns, in its slot:
// the Go expression that yields it (v1.OwnStatic, the step that receives
// it, R8).
func (fc *funcCtx) staticOwn(key any, d *check.Drop, t check.Type, val string, site syntax.Node) string {
	st := fc.frame.static
	k, seen := st.bit[key]
	if !seen {
		k = len(st.slots)
		st.bit[key] = k
		name := fc.scope.alloc("slot")
		st.slots = append(st.slots, staticSlot{
			name:    name,
			goType:  fc.goType(t),
			release: fc.providerRelease(&check.Acquisition{Close: d.Close}, fc.envExpr(site), name, site),
		})
	}
	if fc.l.owned == nil {
		fc.l.owned = map[any]string{}
	}
	fc.l.owned[key] = st.slots[k].name
	return fc.runtimeCall("OwnStatic", fc.ownBinding("OwnStatic", site), "", "", []string{"&" + st.live, staticBit(k), "&" + st.slots[k].name, val}, "", nil, nil, site)
}

func staticBit(k int) string { return "1<<" + strconv.Itoa(k) }

// staticHand is the hand-off of val, the value of key's slot: its bit is
// cleared, or moved to the kept set when the frame returns it.
func (fc *funcCtx) staticHand(key any, val string, returned bool, site syntax.Node) string {
	st := fc.frame.static
	k := st.bit[key]
	if returned {
		st.keeps = true
		return fc.runtimeCall("ReturnStatic", fc.ownBinding("ReturnStatic", site), "", "", []string{"&" + st.live, "&" + st.kept, staticBit(k), val}, "", nil, nil, site)
	}
	return fc.runtimeCall("HandStatic", fc.ownBinding("HandStatic", site), "", "", []string{"&" + st.live, staticBit(k), val}, "", nil, nil, site)
}

// staticRelease releases key's value now, if its bit is set: the end of a
// scope inside the frame. A defect is raised at once; the deferred
// release then releases the rest (R9).
func (fc *funcCtx) staticRelease(key any) {
	st := fc.frame.static
	k, ok := st.bit[key]
	if !ok {
		return
	}
	b := staticBit(k)
	fc.line("if " + st.live + "&(" + b + ") != 0 {")
	fc.line("\t" + st.live + " &^= " + b)
	fc.line("\t" + st.slots[k].release)
	fc.line("}")
}

// closeStatic replaces the marker in body with the prologue: the bit sets,
// the slots and the one deferred release, whose error result is errName
// ("" in a frame with no error row).
func (fc *funcCtx) closeStatic(body, errName string, site syntax.Node) string {
	st := fc.frame.static
	ind := "\t"
	var b strings.Builder
	line := func(s string) { b.WriteString(ind + s + "\n") }
	line("var " + st.live + " uint64")
	if st.keeps {
		line("var " + st.kept + " uint64")
	}
	for _, s := range st.slots {
		line("var " + s.name + " " + s.goType)
	}
	errArg := "nil"
	if errName != "" {
		errArg = "&" + errName
	}
	rel := fc.scope.alloc("rel")
	line("defer func() {")
	begin := fc.runtimeCall("StaticBegin", fc.ownBinding("StaticBegin", site), "", "", []string{errArg, "recover()"}, fc.envExpr(site), nil, nil, site)
	line("\t" + rel + " := " + begin)
	part := func(set string, k int) {
		s := st.slots[k]
		line("\tif " + set + "&(" + staticBit(k) + ") != 0 {")
		line("\t\t" + fc.runtimeCall("Static.Part", fc.ownBinding("Static.Part", site), "", "&"+rel, []string{"func() { " + s.release + " }"}, "", nil, nil, site))
		line("\t}")
	}
	for k := len(st.slots) - 1; k >= 0; k-- {
		part(st.live, k)
	}
	if st.keeps {
		line("\tif " + st.kept + " != 0 && " + fc.runtimeCall("Static.Failed", fc.ownBinding("Static.Failed", site), "", "&"+rel, nil, "", nil, nil, site) + " {")
		for k := len(st.slots) - 1; k >= 0; k-- {
			line("\t\tif " + st.kept + "&(" + staticBit(k) + ") != 0 {")
			line("\t\t\t" + fc.runtimeCall("Static.Part", fc.ownBinding("Static.Part", site), "", "&"+rel, []string{"func() { " + st.slots[k].release + " }"}, "", nil, nil, site))
			line("\t\t}")
		}
		line("\t}")
	}
	line("\t" + fc.runtimeCall("Static.End", fc.ownBinding("Static.End", site), "", "&"+rel, []string{errArg}, "", nil, nil, site))
	line("}()")
	i := strings.Index(body, staticMarker)
	if i < 0 {
		ice(site, "a static frame with no prologue marker")
	}
	// The marker is a whole line, indented by fc.line.
	start := strings.LastIndex(body[:i], "\n") + 1
	end := i + len(staticMarker)
	if end < len(body) && body[end] == '\n' {
		end++
	}
	return body[:start] + b.String() + body[end:]
}
