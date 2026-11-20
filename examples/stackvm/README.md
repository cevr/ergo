# stackvm

A bytecode stack machine with an assembler and a disassembler, and a small expression language compiled to it and checked against a tree-walking interpreter.

```sh
stackvm run < fib.asm      # assemble standard input, run it, print what it prints
stackvm dis < fib.asm      # print it back in canonical form, with L<n> labels
stackvm code < fib.asm     # print its bytecode
stackvm bench 3000000      # a counting loop of 39 million instructions
stackvm fib 100            # fib(15) by recursion 100 times, summed
```

## Layout

- `vm/`: the machine. `instr.ergo` declares `Instr`, a union of 20 instructions (`Push{N}`, `Add`, `Jump{To}`, `Call{To}`, `Ret`, `Print`, `Halt` and the rest), and `Target`/`Retarget`. `code.ergo` encodes a program as bytecode (`[]int`, an opcode and an operand) and decodes it (`Decode` fails `BadCode`). `run.ergo` is the dispatch loop: `Run(program, fuel) Result fails Trap`, where `Trap{PC, Why Cause}` and `Cause = Underflow | DivideByZero | BadTarget{To} | NoFrame | TooDeep | OutOfFuel`. `Run` verifies the program once; `Checked` is the loop that checks every instruction. `verify.ergo` is the verifier: `Verify(program) Option[Verified]` reads the program as subroutines (the main one at 0, one at each call target) and accepts it when, in each, every instruction finds one stack depth on every path, every `Ret` finds one depth (so a call moves the stack by a fixed amount), what a subroutine pops of its caller's values is known (its need), the main one needs nothing and has no `Ret`, and every target is in the program. A recursion needs rounds: each learns a `Ret`'s depth or raises a need, and both are bounded. `Verified` has unexported fields, so only `Verify` builds one, and `Exec(Verified, fuel)` is the loop that takes only verified programs: no underflow, target, growth or return check, one allocation sized by the proved depth (values from the bottom, return points from the top), and fuel checked only when control goes back (with less fuel left than the program is long, the run goes on in the checked loop). The one check left is the call depth, which a recursion can pass at run time. `Exec` has two loops, and the verifier's answer picks one by a single rule: a program without calls (whose stack stays under 64 values) takes the call-free loop `execFlat`, on a `[64]int` with no call cases, which keeps its state in registers; every other verified program takes `execCalls`. `laws.ergo` holds the seeded program generator and the laws.
- `asm/`: the text form. `asm.ergo` is a two-pass assembler: labels (`loop:`), comments (`;`), targets by label or by number; a bad line fails `Bad{Line, Text, Why Problem}`, and `Explain` names each problem. `dis.ergo` is the disassembler: it labels each target `L<n>` and writes an out-of-range target as its number, so any program round-trips.
- `expr/`: `Expr = Lit | Neg | Bin | IfZero | Square`. `Eval` walks the tree and fails `DivideByZero`; `Compile` emits vm code, with `Square` as a call to a subroutine after the `Halt`. `Gen` draws expressions from a seed.
- `cmd/stackvm/main.ergo`: the binary; `Command = Exec | Dis | Code | Bench{Rounds} | Fib{Rounds}`.

## What it shows

- **Exhaustive match.** The loop matches on all 20 instructions; `Opcode`, `Mnemonic`, `Describe`, `Explain`, `Eval` and `emit` each cover their union, so a new instruction does not compile until every one of them handles it.
- **Typed failures.** A trap is a value with a cause union, not a string or a panic; `Decode`, `Assemble` and `Eval` each fail with their own record.
- **Construction authority.** A `Verified` program is a value only `vm.Verify` makes, so the loop that skips the checks cannot be handed an unchecked program.
- **Laws.** `vm.ExecAgreesWithChecked` (a verified program ends on `Exec` as on `Checked`, with the same result or trap, for any fuel, on well-formed, near-miss, random, looping, joined-at-two-depths, low-on-fuel printing and 60-to-67-deep programs, and with calls: well-formed, near-miss, recursive to 9,000 frames, uneven returns, short arguments and a return with no call); `vm.VerifyAcceptsStacked` (the well-formed generators' programs verify, with and without calls, so the first law reaches both loops); `vm.DecodeInvertsEncode`; `vm.FuelBoundsSteps` (fuel bounds a run exactly, on generated programs and on a countdown loop); `asm.AssembleInvertsDisassemble` (any program, with out-of-range targets); `asm.LayoutIsIgnored` (indentation, comments and blank lines change nothing); `expr.CompiledAgrees` (a generated expression compiled and run on the vm gives the tree walk's value, or both divide by zero). `ergo test` runs them, and `GeneratorCoversTheLanguage` shows the generator reaches every kind of expression and the failure.

## Cases

`fib` (fib(0..15) by double recursion, 49,000 steps), `dis` and `code` (canonical text and bytecode), `bench` (100,000 rounds; the hand Go control prints the same two lines), `fib-rounds` (`stackvm fib 20`, the recursive workload), `trap` (division by zero, exit 1), `bad-asm` (an undefined label, exit 1), `usage` (exit 2).

## Benchmark

`stackvm bench N` runs the counting loop `Loop` for N rounds. The control, a hand-written Go loop that switches on int opcodes over the same bytecode, is in the run dir (`bench-pending-ex-f.md`).

`stackvm fib N` sums fib(15) N times by the doubly recursive subroutine, on `execCalls` (`testdata/fib/workload`; `go run ./scripts/opcount -control examples/stackvm/testdata/fib`). `testdata/control` runs both programs (`control bench N`, `control fib N`) with `vm.Run`'s checks on every instruction; `testdata/floor` runs both at the least work the Go runtime allows, with a verifier for calls like `vm.Verify` and a call loop like `execCalls`.

## Limits found

From SVM-VERIFY and SVM-CALLS (wf10): `Verify` proves facts that the compiler cannot carry to `Exec` yet. The stack's depth stays within its room, so `Exec` indexes it under a mask (`& 63`, or the room less one), a no-op that only lets Go drop the bounds check. Two loops exist only because Go's register allocator spills the call loop's extra state (395 against 436 instructions a round of `bench`). From SVM-FLOOR-GAP: a call left in the loop, even on a path that runs once (the low-fuel `resume`), makes Go spill the loop's state at the top of every round, so the loops mark `low`, leave, and call `resume` after (436 to 395 a round of `bench`, 725,400 to 639,500 a round of `fib`). The rest of the gap to the floor is the lowering's: every function that can fail takes the `env` parameter even when nothing it runs reads it, one more value live in the loop (395 against 357 with it deleted by hand; unit ENV-FREE-FALLIBLE). And every target is in `0..len(code)`, but Go still checks `code[at]` once per instruction (one `panicBounds` site in `Exec`), because it cannot see that `pc` is never negative. The fix is a record invariant on `Verified` (`where`, docs/spec/facts.md, designed, not built) whose proved facts reach gc's `Bounded` flag (FORK's FACT-BOUNDED). Then the mask goes and the check on `code[at]` goes. The floor (testdata/floor) pays the same check.

New gaps (`gaps-ex-f.md`):

- G-ex-f-8: the match on `Instr` lowers to a Go type switch over boxed interfaces; a rough run takes 0.20 s against the hand Go loop's 0.156 s.

Also hit before: G-ex-b-2, G-ex-f-1 (the stack keeps its own `sp`).

Closed in batch 12: G-ex-f-7 (L5/L6: `let parsed = try parse(…)` and `for line in lines` beside the types `parsed` and `line`), G-ex-c-2 (`marked` is gone: `maps.GetOr(targets, n, false)` in the `if` header), G-ex-d-5 (W3: `Same`, `SameResult` and every `slices.Equal` are `==`), G-ex-d-10 (L3: `main` returns from its `with`; `run` is gone), G-ex-d-9 (T4 sizes grow; the vm laws still build programs from a seed because a generated program seldom has its targets inside it), G-ex-b-16 / G-ex-c-10 (K3: `let jump = match`), `pad` → `strings.PadRight` (U2), and the property tests that restated the laws (V1). `Trap` and `Bad` stay one record with a union inside (named rows exist now, L2): the record carries the PC or the line once.
