# twig

A small language and its interpreter, in Ergo: a lexer, a Pratt parser, a resolver and a tree-walking interpreter for a language with functions, closures, records, lists and loops. Every error names its line and column.

```sh
twig run [-stats] FILE    # run FILE; -stats adds the nodes evaluated and the calls on stderr
twig check FILE           # parse and resolve FILE, and run nothing
twig gen -seed S -n N     # write the workload of N rounds for seed S
```

## The language

```text
fn fib(n) {
	if n < 2 {
		return n
	}
	return fib(n - 1) + fib(n - 2)
}
let p = {x: 1.5, tags: ["a", "b"]}
p.x = p.x * 2.0
push(p.tags, str(fib(10)))
print(p)                  // {x: 3, tags: ["a", "b", "55"]}
```

- **Values.** `nil`, `true` and `false`, ints (64-bit, wrapping), floats, strings (bytes), lists `[a, b]`, records `{k: v}`, and functions. Lists and records are shared: two variables that hold one list see each other's writes. `==` is true for the same kind and value; two lists, records or functions are equal only when they are one.
- **Statements.** `let x = e`, `x = e`, `xs[i] = e`, `r.k = e`, `if c { } else if c { } else { }`, `while c { }`, `for x in xs { }`, `break`, `continue`, `return [e]` (the value starts on the return's line), `fn name(a, b) { }`, and an expression. A newline needs no separator; `(` and `[` continue an expression only on its line.
- **Operators**, loosest first: `or`; `and`; `==` `!=`; `<` `<=` `>` `>=`; `+` `-`; `*` `/` `%`; prefix `-` and `not`; then calls, `[i]` and `.k`. Every infix operator is left-associative. Arithmetic takes two ints or two floats; `+` also joins two strings; `%` takes ints; an int division by zero is an error. A condition, `and`, `or` and `not` take bools only.
- **Names.** A block's functions are bound when the block starts, so they may call each other; any other name is seen after its `let`. A name declared twice in one block, a name that is not declared, and a `break`, `continue` or `return` out of place are errors before the program runs. A variable lives as long as its function's call, so the functions one loop makes share the loop's variable, as in a language with function scope.
- **Builtins.** `print(v)`, `len(xs or s)`, `push(xs, v)`, `str(v)`, `float(n)`, `int(f)` (toward zero; a float out of range is an error) and `sqrt(f)`. A declaration of the same name hides one.
- **Errors.** `twig: FILE:LINE:COL: message` on stderr, status 1, after what the program printed. Calls nest at most 2000 deep: one more is an error, not a crash of the host.

## What it shows

- **Unions and exhaustive `match` all the way down.** Tokens, the tree (`syntax.Expr`, `syntax.Stmt`), the resolved code (`resolve.Node`, `resolve.Code`), values (`eval.Value`) and every failure are unions; every pass is a `match` the checker holds total, so a node added to the tree stops each pass that forgets it.
- **Control flow as typed failures.** `return`, `break` and `continue` leave a statement as the failures `Returned`, `Broke` and `Continued` of the row `Flow`, beside `RunError`; the call and the loop that own them catch them, and a body's signature says which can escape it. This puts failures on the hot path, the workload IR-G's FAIL1 lacked.
- **A machine in the lexer.** A string literal is read by the machine `syntax.Quoting` (body, escape); its laws are proved on its graph (`EscapesReturn`) or tested (`PlainBytesAreThemselves`, `EscapedQuoteStaysOpen`).
- **Laws against models.** `syntax.PrintParsesBack` prints generated trees with the fewest parentheses and parses them back, so a wrong binding power or associativity is caught; `syntax.TokensStandInOrder` checks positions; `resolve.SlotsFitTheirFrames` checks every slot and hop of resolved programs; `eval.ArithmeticAgreesWithModel` holds the interpreter's ints to a fold in Ergo. `script.Sources.Repeatable` is the source contract's law.
- **Value semantics, and where they cost.** Ergo has no pointer fields, so lists, records and frames live in the `eval.Machine` and are named by number, as an arena would name them; a frame of a function that makes no function is reused after its call.

## Layout

- `syntax/`: tokens, the string machine, the lexer, the tree, the Pratt parser, the printer, the generator and their laws.
- `resolve/`: the resolved code, the resolver and its laws.
- `eval/`: values, the machine and the tree walker, and the arithmetic law.
- `script/`: the `Sources` contract and the run from source text to output, error lines and status.
- `files/`: `Sources` over the host's files. `memory/`: `Sources` over texts in memory.
- `workload/`: the generator of the receipts' program.
- `cmd/twig/main.ergo`: binds `files` over `fs.NewHost()`.

## The Go control

`cmd/ergo/testdata/twig/control` is the same design in hand-written Go: the same lexer, Pratt parser, resolver and tree walker, with Go's pointers for lists, records and frames and a small tagged value struct, the form a Go programmer would write. `TestTwigAgreesWithControl` (cmd/ergo, full tier) runs both on seeded generated programs and checks that both print what a reference model computes from the generated tree, and that both print the same for programs with a broken byte. The receipts (instructions, allocations, GC share and peak memory per evaluated node, against the control) are files of the run, not tests.
