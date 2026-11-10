package eval

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

type integer interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

type float interface{ ~float32 | ~float64 }

type ordered interface{ integer | float | ~string }

// binop applies op to a and b, two values of one checked type (a shift's
// count may have another integer type). Go's operators on the values' Go
// types give the target's wrapping and rounding; the defects Go panics on
// (an integer divided by zero, a negative shift) are defects here.
func (in *interp) binop(fr *frame, at syntax.Node, op syntax.Token, a, b Value) (Value, *abrupt) {
	switch op {
	case syntax.EQL:
		return in.equal(a, b), nil
	case syntax.NEQ:
		return !in.equal(a, b), nil
	case syntax.SHL, syntax.SHR:
		n, negative, ok := shiftCount(b)
		if !ok {
			ice("a shift count of %T", b)
		}
		if negative {
			return nil, fr.defect(at, "negative shift amount")
		}
		if v, ok := shift(op, a, n); ok {
			return v, nil
		}
		ice("a shift of %T", a)
	}
	if s, ok := a.(string); ok && op == syntax.ADD {
		t := b.(string)
		if ab := in.charge(fr, at, int64(len(s))+int64(len(t))); ab != nil {
			return nil, ab
		}
		return s + t, nil
	}
	v, defect, ok := arith(op, a, b)
	switch {
	case !ok:
		return nil, fr.unsupported(at, "the operator %s on %T yet", op, a)
	case defect != "":
		return nil, fr.defect(at, "%s", defect)
	}
	return v, nil
}

// arith dispatches on the operands' Go type.
func arith(op syntax.Token, a, b Value) (Value, string, bool) {
	switch a := a.(type) {
	case int8:
		return intOp(op, a, b.(int8))
	case int16:
		return intOp(op, a, b.(int16))
	case int32:
		return intOp(op, a, b.(int32))
	case int64:
		return intOp(op, a, b.(int64))
	case uint8:
		return intOp(op, a, b.(uint8))
	case uint16:
		return intOp(op, a, b.(uint16))
	case uint32:
		return intOp(op, a, b.(uint32))
	case uint64:
		return intOp(op, a, b.(uint64))
	case float32:
		return floatOp(op, a, b.(float32))
	case float64:
		return floatOp(op, a, b.(float64))
	case string:
		v, ok := compare(op, a, b.(string))
		return v, "", ok
	}
	return nil, "", false
}

func intOp[T integer](op syntax.Token, a, b T) (Value, string, bool) {
	switch op {
	case syntax.ADD:
		return a + b, "", true
	case syntax.SUB:
		return a - b, "", true
	case syntax.MUL:
		return a * b, "", true
	case syntax.QUO:
		if b == 0 {
			return nil, "integer divide by zero", true
		}
		return a / b, "", true
	case syntax.REM:
		if b == 0 {
			return nil, "integer divide by zero", true
		}
		return a % b, "", true
	case syntax.AND:
		return a & b, "", true
	case syntax.OR:
		return a | b, "", true
	case syntax.XOR:
		return a ^ b, "", true
	case syntax.AND_NOT:
		return a &^ b, "", true
	}
	v, ok := compare(op, a, b)
	return v, "", ok
}

func floatOp[T float](op syntax.Token, a, b T) (Value, string, bool) {
	switch op {
	case syntax.ADD:
		return a + b, "", true
	case syntax.SUB:
		return a - b, "", true
	case syntax.MUL:
		return a * b, "", true
	case syntax.QUO:
		return a / b, "", true
	}
	v, ok := compare(op, a, b)
	return v, "", ok
}

func compare[T ordered](op syntax.Token, a, b T) (Value, bool) {
	switch op {
	case syntax.LSS:
		return a < b, true
	case syntax.LEQ:
		return a <= b, true
	case syntax.GTR:
		return a > b, true
	case syntax.GEQ:
		return a >= b, true
	}
	return nil, false
}

// shiftCount is an integer shift count as a uint64, and whether it is
// negative.
func shiftCount(v Value) (uint64, bool, bool) {
	switch v := v.(type) {
	case int8:
		return uint64(v), v < 0, true
	case int16:
		return uint64(v), v < 0, true
	case int32:
		return uint64(v), v < 0, true
	case int64:
		return uint64(v), v < 0, true
	case uint8:
		return uint64(v), false, true
	case uint16:
		return uint64(v), false, true
	case uint32:
		return uint64(v), false, true
	case uint64:
		return v, false, true
	}
	return 0, false, false
}

func shift(op syntax.Token, a Value, n uint64) (Value, bool) {
	switch a := a.(type) {
	case int8:
		return shiftOp(op, a, n), true
	case int16:
		return shiftOp(op, a, n), true
	case int32:
		return shiftOp(op, a, n), true
	case int64:
		return shiftOp(op, a, n), true
	case uint8:
		return shiftOp(op, a, n), true
	case uint16:
		return shiftOp(op, a, n), true
	case uint32:
		return shiftOp(op, a, n), true
	case uint64:
		return shiftOp(op, a, n), true
	}
	return nil, false
}

func shiftOp[T integer](op syntax.Token, a T, n uint64) Value {
	if op == syntax.SHL {
		return a << n
	}
	return a >> n
}

// unop applies a unary operator.
func unop(op syntax.Token, a Value) (Value, bool) {
	switch op {
	case syntax.ADD:
		return a, true
	case syntax.NOT:
		b, ok := a.(bool)
		return !b, ok
	case syntax.SUB:
		switch a := a.(type) {
		case int8:
			return -a, true
		case int16:
			return -a, true
		case int32:
			return -a, true
		case int64:
			return -a, true
		case uint8:
			return -a, true
		case uint16:
			return -a, true
		case uint32:
			return -a, true
		case uint64:
			return -a, true
		case float32:
			return -a, true
		case float64:
			return -a, true
		}
	case syntax.XOR:
		switch a := a.(type) {
		case int8:
			return ^a, true
		case int16:
			return ^a, true
		case int32:
			return ^a, true
		case int64:
			return ^a, true
		case uint8:
			return ^a, true
		case uint16:
			return ^a, true
		case uint32:
			return ^a, true
		case uint64:
			return ^a, true
		}
	}
	return nil, false
}
