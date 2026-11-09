# stackvm

A bytecode stack machine with an assembler and a disassembler, and a small expression language compiled to it and checked against a tree-walking interpreter.

```sh
stackvm run < fib.asm      # assemble standard input, run it, print what it prints
stackvm dis < fib.asm      # print it back in canonical form, with L<n> labels
stackvm code < fib.asm     # print its bytecode
stackvm bench 3000000      # a counting loop of 39 million instructions
```

## Layout

- `vm/`: the machine. `instr.ergo` declares `Instr`, a union of 20 instructions (`Push{N}`, `Add`, `Jump{To}`, `Call{To}`, `Ret`, `Print`, `Halt` and the rest), and `Target`/`Retarget`. `code.ergo` encodes a program as bytecode (`[]int`, an opcode and an operand) and decodes it (`Decode` fails `BadCode`). `run.ergo` is the dispatch loop: `Run(program, fuel) Result fails Trap`, where `Trap{PC, Why Cause}` and `Cause = Underflow | DivideByZero | BadTarget{To} | NoFrame | TooDeep | OutOfFuel`. `laws.ergo` holds the seeded program generator and the laws.
- `asm/`: the text form. `asm.ergo` is a two-pass assembler: labels (`loop:`), comments (`;`), targets by label or by number; a bad line fails `Bad{Line, Text, Why Problem}`, and `Explain` names each problem. `dis.ergo` is the disassembler: it labels each target `L<n>` and writes an out-of-range target as its number, so any program round-trips.
- `expr/`: `Expr = Lit | Neg | Bin | IfZero | Square`. `Eval` walks the tree and fails `DivideByZero`; `Compile` emits vm code, with `Square` as a call to a subroutine after the `Halt`. `Gen` draws expressions from a seed.
- `cmd/stackvm/main.ergo`: the binary; `Command = Exec | Dis | Code | Bench{Rounds}`.

## What it shows

- **Exhaustive match.** The loop matches on all 20 instructions; `Opcode`, `Mnemonic`, `Describe`, `Explain`, `Eval` and `emit` each cover their union, so a new instruction does not compile until every one of them handles it.
- **Typed failures.** A trap is a value with a cause union, not a string or a panic; `Decode`, `Assemble` and `Eval` each fail with their own record.
- **Laws.** `vm.DecodeInvertsEncode`; `vm.FuelBoundsSteps` (fuel bounds a run exactly, on generated programs and on a countdown loop); `asm.AssembleInvertsDisassemble` (any program, with out-of-range targets); `asm.LayoutIsIgnored` (indentation, comments and blank lines change nothing); `expr.CompiledAgrees` (a generated expression compiled and run on the vm gives the tree walk's value, or both divide by zero). `ergo test` runs them, and `GeneratorCoversTheLanguage` shows the generator reaches every kind of expression and the failure.

## Cases

`fib` (fib(0..15) by double recursion, 49,000 steps), `dis` and `code` (canonical text and bytecode), `bench` (100,000 rounds; the hand Go control prints the same two lines), `trap` (division by zero, exit 1), `bad-asm` (an undefined label, exit 1), `usage` (exit 2).

## Benchmark

`stackvm bench N` runs the counting loop `Loop` for N rounds. The control, a hand-written Go loop that switches on int opcodes over the same bytecode, is in the run dir (`bench-pending-ex-f.md`).

## Limits found

New gaps (`gaps-ex-f.md`):

- G-ex-f-8: the match on `Instr` lowers to a Go type switch over boxed interfaces; a rough run takes 0.20 s against the hand Go loop's 0.156 s.
- G-ex-f-9: `try` stands only at the start of an expression, so `0 - try Eval(x)` needs a `let`.

Also hit before: G-ex-b-2, G-ex-f-1 (the stack keeps its own `sp`).

Closed in batch 12: G-ex-f-7 (L5/L6: `let parsed = try parse(…)` and `for line in lines` beside the types `parsed` and `line`), G-ex-c-2 (`marked` is gone: `maps.GetOr(targets, n, false)` in the `if` header), G-ex-d-5 (W3: `Same`, `SameResult` and every `slices.Equal` are `==`), G-ex-d-10 (L3: `main` returns from its `with`; `run` is gone), G-ex-d-9 (T4 sizes grow; the vm laws still build programs from a seed because a generated program seldom has its targets inside it), G-ex-b-16 / G-ex-c-10 (K3: `let jump = match`), `pad` → `strings.PadRight` (U2), and the property tests that restated the laws (V1). `Trap` and `Bad` stay one record with a union inside (named rows exist now, L2): the record carries the PC or the line once.
