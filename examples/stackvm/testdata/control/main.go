// Command control is the hand Go control of examples/stackvm's
// `stackvm bench N`: the same bytecode, BenchProgram(N) (the counting loop
// Loop with N pushed first), run by a hand-written Go loop that switches on
// int opcodes with the checks vm.Run makes (an underflow check on each
// instruction that pops, a jump target check, fuel), the stacks growing by
// append. It prints what `stackvm bench N` prints; `control fib N` runs
// FibProgram(N), the recursive workload of testdata/fib, the same way.
//
//	control bench N
//	control fib N
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

type instr struct{ op, n int }

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

// programOf is the program of `stackvm bench N` or `stackvm fib N`.
func programOf(args []string) ([]instr, bool) {
	if len(args) != 3 {
		return nil, false
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 0 {
		return nil, false
	}
	switch args[1] {
	case "bench":
		return benchProgram(n), true
	case "fib":
		return fibProgram(n), true
	}
	return nil, false
}

type trap struct {
	pc  int
	why string
}

func (t *trap) Error() string { return fmt.Sprintf("trap at %d: %s", t.pc, t.why) }

const maxFrames = 10000

func run(program []instr, fuel int) (printed, left []int, steps int, err error) {
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
		switch in.op {
		case opPush:
			push(in.n)
		case opPop:
			if sp < 1 {
				return nil, nil, 0, underflow(at)
			}
			sp--
		case opDup:
			if sp < 1 {
				return nil, nil, 0, underflow(at)
			}
			push(stack[sp-1])
		case opSwap:
			if sp < 2 {
				return nil, nil, 0, underflow(at)
			}
			stack[sp-1], stack[sp-2] = stack[sp-2], stack[sp-1]
		case opOver:
			if sp < 2 {
				return nil, nil, 0, underflow(at)
			}
			push(stack[sp-2])
		case opAdd, opSub, opMul, opDiv, opMod, opLess, opSame:
			if sp < 2 {
				return nil, nil, 0, underflow(at)
			}
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
			if sp < 1 {
				return nil, nil, 0, underflow(at)
			}
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
			if sp < 1 {
				return nil, nil, 0, underflow(at)
			}
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
			if sp < 1 {
				return nil, nil, 0, underflow(at)
			}
			sp--
			printed = append(printed, stack[sp])
		case opHalt:
			return printed, append([]int(nil), stack[:sp]...), steps, nil
		}
	}
	return printed, append([]int(nil), stack[:sp]...), steps, nil
}

func main() {
	program, ok := programOf(os.Args)
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: control bench N | fib N")
		os.Exit(2)
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
