package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// dieStmt checks `die e` (NO-UNREACHABLE §3): a terminating statement that
// raises a structured deliberate defect carrying an owned snapshot-safe
// payload. The operand is evaluated once under ordinary failure, capability,
// and ownership rules.
func (c *checker) dieStmt(s *syntax.DieStmt) {
	c.chargeDefect(s, "die")
	op := c.valueExpr(s.X, nil)
	if op.invalid() {
		return
	}
	if why := c.defectPayloadReason(op.typ); why != "" {
		c.errorf(s.X, CodeType, "%s", why)
	}
}

// chargeDefect enforces the deliberate-defect authority and handler rules
// (NO-UNREACHABLE §3):
//   - immediate execution inside a catch arm cannot escalate a checked
//     failure into a defect (nou-catch-escalates);
//   - deliberate defects require an explicit `defects` signature and cannot
//     run directly in main or package initializers (only inside an admitted
//     child task such as task.Go).
func (c *checker) chargeDefect(at syntax.Node, what string) {
	if c.fn == nil || c.fn.t == nil {
		c.errorf(at, CodeType, "%s is not allowed in a package initializer: deliberate defects require a defects signature inside a child task", what)
		return
	}
	if c.fn.t.inCatchArm > 0 {
		c.errorf(at, CodeType, "a catch arm cannot escalate a typed failure into a defect with %s", what)
		return
	}
	if !c.fn.t.defects {
		if c.fn.t.main {
			c.errorf(at, CodeType, "%s is not allowed directly in main: run deliberate defects inside a child task (task.Go) and observe via task.ExitOf", what)
			return
		}
		c.errorf(at, CodeType, "%s requires defects in %s's signature", what, c.fn.t.name)
	}
}

// defectPayloadReason is "" when t is an admitted owned snapshot-safe die
// payload, else S108's reason naming the first forbidden field or type.
func (c *checker) defectPayloadReason(t Type) string {
	path, bad := c.defectPayloadWalk(t, map[Type]bool{})
	if bad == nil {
		return ""
	}
	if path == "" {
		return fmt.Sprintf("die payload %s is not snapshot-safe: it %s", t, bad.why)
	}
	return fmt.Sprintf("die payload %s is not snapshot-safe: its field %s%s, of type %s, %s", t, typeBaseName(t), path, bad.typ, bad.why)
}

func (c *checker) defectPayloadWalk(t Type, seen map[Type]bool) (string, *wireFault) {
	if t == nil || isInvalid(t) {
		return "", nil
	}
	if IsGoAny(t) {
		return "", &wireFault{t, "is go.Any"}
	}
	if isCapability(t) || isAmbient(t) {
		return "", &wireFault{t, "is a capability"}
	}
	if isHostNamed(t) {
		return "", &wireFault{t, "is a foreign host handle"}
	}
	if c.mayHoldAny(t) {
		return "", &wireFault{t, "holds a live resource handle"}
	}
	if _, own := WireWriteOf(t); own {
		return "", &wireFault{t, "uses a custom wire codec (WireWrite)"}
	}
	if _, own := WireReadOf(t); own {
		return "", &wireFault{t, "uses a custom wire codec (WireRead)"}
	}
	if c.wirePair(t) != nil {
		return "", &wireFault{t, "uses a custom wire codec"}
	}
	if IsProviderMap(t) {
		return "", &wireFault{t, "is a Map with a provider"}
	}
	if _, ok := t.(*TypeParam); ok {
		return "", &wireFault{t, "is an uninstantiated type parameter"}
	}
	if n, ok := t.(*Named); ok {
		if seen[n] {
			return "", nil
		}
		seen[n] = true
		if n.Obj != nil && n.Obj.Decl != nil && n.Obj.Decl.Extern != nil {
			return "", &wireFault{t, "is an extern host handle"}
		}
	}
	switch u := under(t).(type) {
	case *Basic:
		if isBoolean(u) || isString(u) || isFloat(u) || (isInteger(u) && u.Kind != Uintptr) {
			return "", nil
		}
		return "", &wireFault{t, "is not a snapshot-safe scalar"}
	case *Struct:
		for _, f := range u.Fields {
			if p, bad := c.defectPayloadWalk(f.Type, seen); bad != nil {
				return "." + f.Name + p, bad
			}
		}
		return "", nil
	case *Union:
		for _, v := range u.Variants {
			if p, bad := c.defectPayloadWalk(v, seen); bad != nil {
				return fmt.Sprintf(".(%s)", v) + p, bad
			}
		}
		return "", nil
	case *Slice:
		p, bad := c.defectPayloadWalk(u.Elem, seen)
		return "[_]" + p, bad
	case *Array:
		p, bad := c.defectPayloadWalk(u.Elem, seen)
		return "[_]" + p, bad
	case *Map:
		if p, bad := c.defectPayloadWalk(u.Key, seen); bad != nil {
			return "[k]" + p, bad
		}
		p, bad := c.defectPayloadWalk(u.Elem, seen)
		return "[v]" + p, bad
	case *Tuple:
		for i, e := range u.Types {
			if p, bad := c.defectPayloadWalk(e, seen); bad != nil {
				return fmt.Sprintf(".%d", i) + p, bad
			}
		}
		return "", nil
	case *Interface:
		return "", &wireFault{t, "is an operation type"}
	case *Signature:
		return "", &wireFault{t, "is a function"}
	case *Pointer:
		return "", &wireFault{t, "is a borrowed reference"}
	}
	return "", &wireFault{t, "is not snapshot-safe data"}
}
