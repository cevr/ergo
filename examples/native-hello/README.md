# native-hello

The first program of Ergo's native backend (NB-4, round 1). Package `hello` is pure: a loop (`Fib`), a call and a branch (`Greeting`) and the entry `Main(argc int) string`, with a law (`FibAdds`). The binary `cmd/native-hello` prints `hello.Main(1)`.

The same package also compiles with no Go toolchain in the path: `compiler/nativelower` checks it, lowers it to `compiler/ir`, then to SysV AMD64 machine code, and writes an ELF64 object with `compiler/objfmt`. The system C driver (`/usr/bin/cc`, that is `ld`) links it with `runtime/native/ergo_io.c` into an executable, whose C `main` calls `Main` and writes its bytes through `ergo_write`. The test `NativeHelloMatchesTheExampleCase` in `compiler/nativelower` runs that executable against this program's case `hello`, so the Go-built program and the native one agree on the same golden.

`Main(argc int) string` is the lowering's test entry for round 1, not a language form: an ordinary `main` (one that needs a context or can fail) is refused by the native lowering with `NotLowered`, kind `main`.

Tag `cc`: the native leg needs a C compiler.
