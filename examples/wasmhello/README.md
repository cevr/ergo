# wasmhello

Tag: `default`. The first program of the WASM backend (native backend unit NB-6, round 1 of `compiler/wasmlower`): a greeting and four integer functions, each a `count` or `while` loop, the subset the lowering takes from `compiler/ir` to a WASM32 module.

- `numbers/numbers.ergo`: `Fib` (a count loop), `Gcd` (a while loop over `int32` remainders), `Collatz` (a while loop over `uint64` with an if/else), `Sum35` (a count loop with `break`, `continue` and `||`), with their laws.
- `cmd/wasmhello` prints the greeting and one result of each.

`compiler/wasmlower`'s engine law (`WasmRun.LoweredProgramsAgreeWithErgoOnBunAndNode`, in `exec_test.ergo`) lowers the same functions and a `main` that prints `hello, wasm` through WASI `fd_write` and exits with `fib(10)` through `proc_exit`, runs the module on Bun and Node, and compares every result with these functions run by Ergo. Its IR is built row by row as the IR builder writes it, until a later round reads the IR file `ergo build` writes for this example.
