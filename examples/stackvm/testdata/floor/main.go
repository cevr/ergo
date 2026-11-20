// Command floor is the Go-runtime floor of examples/stackvm's
// `stackvm bench N` (common.md GO-RUNTIME FLOOR): the same bytecode,
// BenchProgram(N), run by a bytecode interpreter at the least work the Go
// runtime allows for the job, with the same result and the same traps.
// Against the hand Go control (testdata/control: a switch on int opcodes
// with vm.Run's checks on every instruction) it moves every check that the
// program's text decides out of the dispatch loop:
//
//   - verify reads the program once: each instruction's stack depth is the
//     same on every path to it, no instruction pops below the bottom, the
//     deepest stack fits a fixed array, every target is in the program, and
//     it has no call or return. A program it accepts runs on the fast loop,
//     with no underflow check, no target check and no growth: the stack is a
//     fixed array indexed under a mask the verified depth makes a no-op, so
//     Go's bounds checks are gone too.
//   - fuel is checked at each backward jump, not at each instruction: between
//     two backward jumps at most len(program) instructions run, so the fast
//     loop goes on while that many are left, and otherwise the program runs
//     again on the checked loop (the control's), which traps at the exact
//     instruction. The run prints nothing until it ends, so running again
//     shows nothing.
//   - the instruction is 16 bytes, an opcode byte and its operand.
//
// What stays is the job: one dispatch per instruction, its arithmetic, the
// division by zero check, the steps count and the printed values.
//
// `floor fib N` is the floor of `stackvm fib N`, a recursive program. Its
// verifier (verifyCalls) reads subroutines as vm.Verify does: one depth per
// instruction and per Ret in each, a fixed need of the caller's values, no
// Ret in main. The program then runs on fastCalls: the values and the
// return points in one slice sized from the proved depth and maxFrames,
// indexed under its mask, with only the call-depth check, which a
// recursion can pass at run time, and fuel checked when control goes back.
//
//	floor bench N
//	floor fib N
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	opPush = iota
	opPop
	opDup
	opSwap
	opOver
	opAdd
	opSub
	opMul
	opDiv
	opMod
	opLess
	opSame
	opNot
	opJump
	opJumpIf
	opJumpIfNot
	opCall
	opRet
	opPrint
	opHalt
)

type instr struct {
	op uint8
	n  int
}

// benchProgram is the assembled Loop of cmd/stackvm/main.ergo, its targets
// moved past the round count's push.
func benchProgram(rounds int) []instr {
	return []instr{
		{opPush, rounds},
		{opPush, 0},
		{opOver, 0}, // loop
		{opJumpIfNot, 15},
		{opOver, 0},
		{opDup, 0},
		{opMul, 0},
		{opPush, 7},
		{opMod, 0},
		{opAdd, 0},
		{opSwap, 0},
		{opPush, 1},
		{opSub, 0},
		{opSwap, 0},
		{opJump, 2},
		{opPrint, 0}, // done
		{opPop, 0},
	}
}

// fibProgram is FibProgram of cmd/stackvm/main.ergo: rounds times fib(15)
// by the doubly recursive subroutine of cases/fib, summed and printed.
func fibProgram(rounds int) []instr {
	return []instr{
		{opPush, 0},
		{opPush, rounds},
		{opDup, 0}, // loop
		{opJumpIfNot, 12},
		{opSwap, 0},
		{opPush, 15},
		{opCall, 15},
		{opAdd, 0},
		{opSwap, 0},
		{opPush, 1},
		{opSub, 0},
		{opJump, 2},
		{opPop, 0}, // done
		{opPrint, 0},
		{opHalt, 0},
		{opDup, 0}, // fib
		{opPush, 2},
		{opLess, 0},
		{opJumpIf, 28},
		{opDup, 0},
		{opPush, 1},
		{opSub, 0},
		{opCall, 15},
		{opSwap, 0},
		{opPush, 2},
		{opSub, 0},
		{opCall, 15},
		{opAdd, 0},
		{opRet, 0}, // base
	}
}

// reachLimit bounds a depth from a subroutine's start, as vm.Reach does.
const reachLimit = 64

// verifyCalls reports whether program runs on fastCalls, and the room it
// needs: a power of two that holds the deepest stack and the return
// points.
func verifyCalls(program []instr) (int, bool) {
	size := len(program)
	entries := []int{0}
	fn := make([]int, size+1)
	for i := range fn {
		fn[i] = -1
	}
	for _, in := range program {
		switch in.op {
		case opJump, opJumpIf, opJumpIfNot, opCall:
			if in.n < 0 || in.n > size {
				return 0, false
			}
		}
		if in.op == opCall && fn[in.n] == -1 {
			fn[in.n] = len(entries)
			entries = append(entries, in.n)
		}
	}
	type summary struct {
		returns     bool
		delta, need int
	}
	sums := make([]summary, len(entries))
	at := make([]int, size)
	var work []int
	high := 0
	changed := true
	demand := func(f, n int) bool {
		if n <= sums[f].need {
			return true
		}
		if n > reachLimit {
			return false
		}
		sums[f].need, changed = n, true
		return true
	}
	reach := func(pc, d int) bool {
		if pc < 0 || pc > size || d > reachLimit || d < -reachLimit {
			return false
		}
		high = max(high, d)
		if pc == size {
			return true
		}
		if at[pc] == reachLimit+1 {
			at[pc] = d
			work = append(work, pc)
			return true
		}
		return at[pc] == d
	}
	for rounds := 0; changed; rounds++ {
		if rounds > (reachLimit+2)*len(entries) {
			return 0, false
		}
		changed, high = false, 0
		for f, entry := range entries {
			for i := range at {
				at[i] = reachLimit + 1
			}
			work = work[:0]
			if !reach(entry, 0) {
				return 0, false
			}
			for len(work) > 0 {
				pc := work[len(work)-1]
				work = work[:len(work)-1]
				in, d := program[pc], at[pc]
				need, delta := effect(in.op)
				if in.op == opCall || in.op == opRet {
					need, delta = 0, 0
				}
				if !demand(f, need-d) {
					return 0, false
				}
				next := d + delta
				ok := true
				switch in.op {
				case opHalt:
				case opRet:
					s := &sums[f]
					switch {
					case f == 0:
						ok = false
					case !s.returns:
						s.returns, s.delta, changed = true, d, true
					default:
						ok = s.delta == d
					}
				case opCall:
					s := sums[fn[in.n]]
					ok = demand(f, s.need-d) && (!s.returns || reach(pc+1, d+s.delta))
				case opJump:
					ok = reach(in.n, next)
				case opJumpIf, opJumpIfNot:
					ok = reach(in.n, next) && reach(pc+1, next)
				default:
					ok = reach(pc+1, next)
				}
				if !ok {
					return 0, false
				}
			}
		}
	}
	if sums[0].need > 0 {
		return 0, false
	}
	bound := (maxFrames+1)*high + maxFrames
	room := 1
	for room <= bound {
		room *= 2
	}
	return room, true
}

// fastCalls runs a program verifyCalls accepted.
func fastCalls(program []instr, fuel, room int) (printed, stack []int, steps int, err error) {
	mem := make([]int, room)
	top := len(mem) - 1
	sp, fp, pc, end := 0, 0, 0, len(program)
	if fuel <= end || len(mem) == 0 { // len(mem) > 0 lets Go drop the masked indexes' checks
		return nil, nil, 0, errFuel
	}
	left := fuel // fuel left: one counter for the fuel and the steps
	for pc < end {
		in := program[pc]
		at := pc
		pc++
		left--
		switch in.op {
		case opPush:
			mem[sp&top] = in.n
			sp++
		case opPop:
			sp--
		case opDup:
			mem[sp&top] = mem[(sp-1)&top]
			sp++
		case opSwap:
			a, b := &mem[(sp-1)&top], &mem[(sp-2)&top]
			*a, *b = *b, *a
		case opOver:
			mem[sp&top] = mem[(sp-2)&top]
			sp++
		case opAdd:
			mem[(sp-2)&top] += mem[(sp-1)&top]
			sp--
		case opSub:
			mem[(sp-2)&top] -= mem[(sp-1)&top]
			sp--
		case opMul:
			mem[(sp-2)&top] *= mem[(sp-1)&top]
			sp--
		case opDiv:
			b := mem[(sp-1)&top]
			if b == 0 {
				return nil, nil, 0, &trap{at, "division by zero"}
			}
			mem[(sp-2)&top] /= b
			sp--
		case opMod:
			b := mem[(sp-1)&top]
			if b == 0 {
				return nil, nil, 0, &trap{at, "division by zero"}
			}
			mem[(sp-2)&top] %= b
			sp--
		case opLess:
			r := 0
			if mem[(sp-2)&top] < mem[(sp-1)&top] {
				r = 1
			}
			mem[(sp-2)&top] = r
			sp--
		case opSame:
			r := 0
			if mem[(sp-2)&top] == mem[(sp-1)&top] {
				r = 1
			}
			mem[(sp-2)&top] = r
			sp--
		case opNot:
			r := 0
			if mem[(sp-1)&top] == 0 {
				r = 1
			}
			mem[(sp-1)&top] = r
		case opJump:
			pc = in.n
			if pc <= at && left <= end {
				return nil, nil, 0, errFuel
			}
		case opJumpIf:
			sp--
			if mem[sp&top] != 0 {
				pc = in.n
				if pc <= at && left <= end {
					return nil, nil, 0, errFuel
				}
			}
		case opJumpIfNot:
			sp--
			if mem[sp&top] == 0 {
				pc = in.n
				if pc <= at && left <= end {
					return nil, nil, 0, errFuel
				}
			}
		case opCall:
			if fp == maxFrames {
				return nil, nil, 0, &trap{at, fmt.Sprintf("calls nested deeper than %d", maxFrames)}
			}
			mem[(top-fp)&top] = pc
			fp++
			pc = in.n
			if pc <= at && left <= end {
				return nil, nil, 0, errFuel
			}
		case opRet:
			fp--
			pc = mem[(top-fp)&top]
			if pc <= at && left <= end {
				return nil, nil, 0, errFuel
			}
		case opPrint:
			sp--
			printed = append(printed, mem[sp&top])
		case opHalt:
			pc = end
		}
	}
	return printed, append([]int(nil), mem[:sp]...), fuel - left, nil
}

// depth is the size of the fast loop's stack, a power of two.
const depth = 64

// effect is what an instruction needs on the stack and what it leaves.
func effect(op uint8) (need, delta int) {
	switch op {
	case opPush:
		return 0, 1
	case opPop, opJumpIf, opJumpIfNot, opPrint:
		return 1, -1
	case opDup:
		return 1, 1
	case opSwap:
		return 2, 0
	case opOver:
		return 2, 1
	case opNot:
		return 1, 0
	case opJump, opHalt:
		return 0, 0
	}
	return 2, -1 // the binary operators
}

// verify reports whether program runs on the fast loop.
func verify(program []instr) bool {
	at := make([]int, len(program)+1)
	for i := range at {
		at[i] = -1
	}
	work := []int{0}
	at[0] = 0
	reach := func(pc, d int) bool {
		if pc < 0 || pc > len(program) {
			return false
		}
		if at[pc] == -1 {
			at[pc] = d
			work = append(work, pc)
			return true
		}
		return at[pc] == d
	}
	for len(work) > 0 {
		pc := work[len(work)-1]
		work = work[:len(work)-1]
		if pc == len(program) {
			continue
		}
		in, d := program[pc], at[pc]
		if in.op == opCall || in.op == opRet {
			return false
		}
		need, delta := effect(in.op)
		if d < need || d+delta >= depth {
			return false
		}
		next := d + delta
		switch in.op {
		case opHalt:
		case opJump:
			if !reach(in.n, next) {
				return false
			}
		case opJumpIf, opJumpIfNot:
			if !reach(in.n, next) || !reach(pc+1, next) {
				return false
			}
		default:
			if !reach(pc+1, next) {
				return false
			}
		}
	}
	return true
}

type trap struct {
	pc  int
	why string
}

func (t *trap) Error() string { return fmt.Sprintf("trap at %d: %s", t.pc, t.why) }

// errFuel is the fast loop's way out when the fuel may run out before the
// next backward jump: the checked loop runs the program instead.
var errFuel = &trap{-1, "fuel"}

// fast runs a verified program.
func fast(program []instr, fuel int) (printed, left []int, steps int, err error) {
	var stack [depth]int
	sp, pc, end := 0, 0, len(program)
	if fuel-steps <= end {
		return nil, nil, 0, errFuel
	}
	for pc < end {
		in := program[pc]
		at := pc
		pc++
		steps++
		switch in.op {
		case opPush:
			stack[sp&(depth-1)] = in.n
			sp++
		case opPop:
			sp--
		case opDup:
			stack[sp&(depth-1)] = stack[(sp-1)&(depth-1)]
			sp++
		case opSwap:
			a, b := &stack[(sp-1)&(depth-1)], &stack[(sp-2)&(depth-1)]
			*a, *b = *b, *a
		case opOver:
			stack[sp&(depth-1)] = stack[(sp-2)&(depth-1)]
			sp++
		case opAdd:
			stack[(sp-2)&(depth-1)] += stack[(sp-1)&(depth-1)]
			sp--
		case opSub:
			stack[(sp-2)&(depth-1)] -= stack[(sp-1)&(depth-1)]
			sp--
		case opMul:
			stack[(sp-2)&(depth-1)] *= stack[(sp-1)&(depth-1)]
			sp--
		case opDiv, opMod:
			b := stack[(sp-1)&(depth-1)]
			if b == 0 {
				return nil, nil, 0, &trap{at, "division by zero"}
			}
			if in.op == opDiv {
				stack[(sp-2)&(depth-1)] /= b
			} else {
				stack[(sp-2)&(depth-1)] %= b
			}
			sp--
		case opLess:
			r := 0
			if stack[(sp-2)&(depth-1)] < stack[(sp-1)&(depth-1)] {
				r = 1
			}
			stack[(sp-2)&(depth-1)] = r
			sp--
		case opSame:
			r := 0
			if stack[(sp-2)&(depth-1)] == stack[(sp-1)&(depth-1)] {
				r = 1
			}
			stack[(sp-2)&(depth-1)] = r
			sp--
		case opNot:
			r := 0
			if stack[(sp-1)&(depth-1)] == 0 {
				r = 1
			}
			stack[(sp-1)&(depth-1)] = r
		case opJump:
			pc = in.n
			if pc <= at && fuel-steps <= end {
				return nil, nil, 0, errFuel
			}
		case opJumpIf:
			sp--
			if stack[sp&(depth-1)] != 0 {
				pc = in.n
				if pc <= at && fuel-steps <= end {
					return nil, nil, 0, errFuel
				}
			}
		case opJumpIfNot:
			sp--
			if stack[sp&(depth-1)] == 0 {
				pc = in.n
				if pc <= at && fuel-steps <= end {
					return nil, nil, 0, errFuel
				}
			}
		case opPrint:
			sp--
			printed = append(printed, stack[sp&(depth-1)])
		case opHalt:
			pc = end
		}
	}
	return printed, append([]int(nil), stack[:sp]...), steps, nil
}

const maxFrames = 10000

// checked runs any program with every check on every instruction, as
// vm.Run does: the fast loop's fallback.
func checked(program []instr, fuel int) (printed, left []int, steps int, err error) {
	var stack, frames []int
	sp, fp, pc := 0, 0, 0
	push := func(v int) {
		if sp == len(stack) {
			stack = append(stack, v)
		} else {
			stack[sp] = v
		}
		sp++
	}
	jump := func(at, to int) (int, error) {
		if to < 0 || to > len(program) {
			return 0, &trap{at, fmt.Sprintf("jump to %d, outside the program", to)}
		}
		return to, nil
	}
	underflow := func(at int) error { return &trap{at, "stack underflow"} }
	for pc < len(program) {
		if steps == fuel {
			return nil, nil, 0, &trap{pc, "out of fuel"}
		}
		steps++
		at := pc
		pc++
		in := program[at]
		need, _ := effect(in.op)
		if in.op != opCall && in.op != opRet && sp < need {
			return nil, nil, 0, underflow(at)
		}
		switch in.op {
		case opPush:
			push(in.n)
		case opPop:
			sp--
		case opDup:
			push(stack[sp-1])
		case opSwap:
			stack[sp-1], stack[sp-2] = stack[sp-2], stack[sp-1]
		case opOver:
			push(stack[sp-2])
		case opAdd, opSub, opMul, opDiv, opMod, opLess, opSame:
			a, b := stack[sp-2], stack[sp-1]
			var r int
			switch in.op {
			case opAdd:
				r = a + b
			case opSub:
				r = a - b
			case opMul:
				r = a * b
			case opDiv, opMod:
				if b == 0 {
					return nil, nil, 0, &trap{at, "division by zero"}
				}
				if in.op == opDiv {
					r = a / b
				} else {
					r = a % b
				}
			case opLess:
				if a < b {
					r = 1
				}
			case opSame:
				if a == b {
					r = 1
				}
			}
			stack[sp-2] = r
			sp--
		case opNot:
			if stack[sp-1] == 0 {
				stack[sp-1] = 1
			} else {
				stack[sp-1] = 0
			}
		case opJump:
			if pc, err = jump(at, in.n); err != nil {
				return nil, nil, 0, err
			}
		case opJumpIf, opJumpIfNot:
			sp--
			if (stack[sp] != 0) == (in.op == opJumpIf) {
				if pc, err = jump(at, in.n); err != nil {
					return nil, nil, 0, err
				}
			}
		case opCall:
			if fp == maxFrames {
				return nil, nil, 0, &trap{at, fmt.Sprintf("calls nested deeper than %d", maxFrames)}
			}
			if fp == len(frames) {
				frames = append(frames, pc)
			} else {
				frames[fp] = pc
			}
			fp++
			if pc, err = jump(at, in.n); err != nil {
				return nil, nil, 0, err
			}
		case opRet:
			if fp < 1 {
				return nil, nil, 0, &trap{at, "return with no call"}
			}
			fp--
			pc = frames[fp]
		case opPrint:
			sp--
			printed = append(printed, stack[sp])
		case opHalt:
			return printed, append([]int(nil), stack[:sp]...), steps, nil
		}
	}
	return printed, append([]int(nil), stack[:sp]...), steps, nil
}

func run(program []instr, fuel int) ([]int, []int, int, error) {
	if verify(program) {
		printed, left, steps, err := fast(program, fuel)
		if err != errFuel {
			return printed, left, steps, err
		}
	} else if room, ok := verifyCalls(program); ok {
		printed, left, steps, err := fastCalls(program, fuel, room)
		if err != errFuel {
			return printed, left, steps, err
		}
	}
	return checked(program, fuel)
}

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "bench" && os.Args[1] != "fib") {
		fmt.Fprintln(os.Stderr, "usage: floor bench N | fib N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil || n < 0 {
		fmt.Fprintln(os.Stderr, "floor: bad round count")
		os.Exit(2)
	}
	program := benchProgram(n)
	if os.Args[1] == "fib" {
		program = fibProgram(n)
	}
	printed, left, steps, err := run(program, 4611686018427387904)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stackvm:", err)
		os.Exit(1)
	}
	for _, v := range printed {
		fmt.Println(v)
	}
	texts := make([]string, len(left))
	for i, v := range left {
		texts[i] = strconv.Itoa(v)
	}
	fmt.Printf("halted after %d steps; stack [%s]\n", steps, strings.Join(texts, " "))
}
