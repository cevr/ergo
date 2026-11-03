# Grammar

Status: the parser `internal/sketch/syntax` and the formatter `internal/sketch/format` **implement the decided grammar of [#196](https://github.com/cevr/ergo/issues/196)**, and nothing else. The source is the appendix "Decided Go-shaped sketch" of the [2026-10-28 preview](../previews/2026-10-28-without-go-syntax.md) (62 productions), its decisions table and its seventh, eighth and ninth rounds. Where the sketch is silent, the rules below decide; each says why. A syntax error is S001 ([diagnostics](diagnostics.md)), with one exception, S108 for a type that mixes fields and operations ([Declarations](#declarations) rule 5). The prototype checker (`internal/sketch/proto`, behind its bridge) checks a subset of the parsed forms and refuses every other form with S109 until the checker of #196 implements it.

A Go form that this file does not admit is an error, never a passthrough to the host (AGENTS.md). The old Go-shaped spelling has no compatibility path ([#217](https://github.com/cevr/ergo/issues/217)); a Go habit gets a message that names the Ergo form ([Old spellings](#old-spellings)).

## Source files

1. A source file ends in `.ergo`. Every surface refuses any other extension with one message, "source file must have .ergo extension", the same for `.ef` and for `.txt`. The directory names the package: a file has no `package` clause.
2. A source file is UTF-8 text. A byte sequence that is not valid UTF-8 is an error in identifier position. Inside a string literal, the literal's decoded bytes are its value (see [Literals](#literals)).
3. White space is space (U+0020), tab, carriage return and newline. Newlines are significant for semicolon insertion.
4. A comment starts with `//` and runs to the end of the line. A block comment `/* … */` does not nest, as in Go; an unterminated block comment is S001. A block comment that spans lines counts as a newline for semicolon insertion. Inside an interpolation `\( … )` a block comment on one line is a comment, as anywhere else: `"x \(a /* note */ + b)"`. A `//` comment there runs to the end of the line, and a block comment that spans lines puts a newline in the string, so both leave the string unterminated (S001).
5. **Semicolon insertion.** The lexer inserts a semicolon at a newline, and at the end of the file, when the line's last token is:
   - an identifier that is not a keyword, or one of the keywords `return`, `break` and `continue`;
   - an integer, float, rune, duration or string literal, or the end of an interpolated string;
   - one of `)`, `]` or `}`;
   - one of the removed operators `++` and `--`, so that `x++` is refused at its own token.

   *Prior art:* Go's rule, kept so that Go formatting habits carry over. **Union continuation:** an inserted semicolon followed by `|` inside a union declaration does not end it, so a union may be written one variant per line, with or without the leading `|` ([Declarations](#declarations) rule 4). *Prior art:* OCaml and F# write a variant per line with a leading `|`.

## Identifiers

```ebnf
identifier = letter { letter | digit } .
letter     = "_" | <a code point with Unicode general category L> .
digit      = <a code point with Unicode general category Nd> .
```

1. The lexer decodes each code point before it tests it. It must never test a single byte of a multi-byte sequence.
2. An identifier starts with a letter or `_`, and continues with letters, digits or `_`. This is Go's identifier rule (`go/token.IsIdentifier`). `xª` is an identifier; `x⪪` is not.
3. A code point that cannot start an identifier, or invalid UTF-8, where an identifier may start, is an error (S001). The same holds for a code point that cannot continue the identifier it follows: `x⪪` is S001, not the identifier `x` followed by an operator.

Fixtures: `internal/sketch/proto/testdata/probes/letter-name.ergo` (builds), `nonletter-name.ergo` (S001); `TestIdentifierRunesMatchGo` sweeps every non-ASCII code point against `go/token.IsIdentifier`.

## Keywords

The decided grammar has **27 keywords**:

```text
break     case      continue  else      for       func      if
import    return    type
try       catch     needs     provides  context   machine
let       mut       in        match     escaping  fails
test      law       assert    extern    with
```

- Ten are Go's (`break` to `type`); six are the earlier constructs (`try` to `machine`); eleven come from the decided stages: `let` and `mut` (196v), `in` (one `for`), `match` (196p), `escaping` (196e), `fails` (fifth round), `test`, `law` and `assert` (196l), `extern` (196x) and `with`, which is now a keyword because a `with` block is a construct (fifth round).
- **`as`** is a keyword too (ninth round, `case Declined as d:`), so the parser reserves 28 words. It binds the value a pattern matched, narrowed to the pattern's type ([Expressions](#expressions) rule 6).
- **Contextual words.** `replace` (in a context), `initial` and `step` (in a machine), and `go` (after `import` and `extern`) have their meaning only in those positions. `uses` is an ordinary identifier with no compatibility path ([#217](https://github.com/cevr/ergo/issues/217)): `func f() uses A {}` is the plain S001 any stray identifier gets.
- **`needs` and `provides`** (owner, 2026-10-27). `needs A, B` lists what a function, a function type, an operation or a context requires; `provides A, B` lists what a context exposes ([rows.md](rows.md#context)). *Prior art:* Roc's `provides` and `requires`; Effect's `Layer<ROut, E, RIn>`.
- Constructs use keyword syntax; a dot form is always library (owner, 2026-10-12). `timeout.After`, `task.Go` and `scope.OnExit` are therefore library functions ([runtime.md](runtime.md)).

## Reserved words

The 15 Go keywords that Ergo does not use stay **reserved**:

```text
chan      const     default   defer     fallthrough
go        goto      interface map       package
range     select    struct    switch    var
```

No name may use them. Generated Go reserves them, and the parser must recognise them to name the Ergo form a Go habit should have used ([Old spellings](#old-spellings)). `go` is also the contextual word of `import go` and `extern go`, and the qualifier of `go.Error` and `go.Foreign`.

## Reserved names

1. A keyword or a reserved word cannot be an identifier.
2. **Predeclared names** are declared in the universe block: the basic types (`string`, `bool`, `int`, `int64`, `float64`, `byte`, `rune`), `duration`, `comparable`, `ordered`, `number`, and the prelude names of [stdlib.md](stdlib.md#prelude), `Map` and `Option` among them. A prelude name cannot be redeclared (eighth round); so `Map[K, V]` always means the map type, and the parser reads it as one (Parser rule 8). The prelude's functions on slice and Map types (`Append`, `Get`, `Delete`) are no names in any scope and may be declared again: they are reached only by the dot form (check-v1 D-D3 amended by D-D6, D-D8).
3. **Prototype limits (not language rules).** The prototype reserves, as S109: Go's predeclared identifiers, `rt`, `time`, `fmt`, `init`, `Error`, `timeout` and `duration`; every name that contains `__` or ends in `_`; and the field names `Error`, names with `__` and names ending in `_`. These keep its generated Go free of collisions. The main compiler must mangle generated names instead (README, Gap 16).

## Literals

```ebnf
int_lit       = "0" | ( "1" … "9" ) { decimal_digit } .
float_lit     = decimals "." [ decimals ] [ exponent ] | decimals exponent | "." decimals [ exponent ] .
exponent      = ( "e" | "E" ) [ "+" | "-" ] decimals .
duration_lit  = int_lit duration_unit .
duration_unit = "ns" | "us" | "ms" | "s" | "m" | "h" .
rune_lit      = "'" ( unicode_value | byte_value ) "'" .
string_lit    = interpreted_string | raw_string .
interpreted_string = `"` { unicode_value | byte_value | interpolation } `"` .
interpolation = `\(` Expression ")" .
import_path   = path_element { ( "/" | "." ) path_element } .
path_element  = letter { letter | digit | "-" } .
```

1. **Integers.** An integer literal is decimal. A leading zero on a literal longer than one digit is an error (S108), because Go would read it as octal. The value must fit `int64` (S108). Hexadecimal, octal, binary and digit separators stay S001 with a message that names them ([#81](https://github.com/cevr/ergo/issues/81)).
2. **Floats.** Go's decimal float forms are literals: `2.5`, `1.`, `.5`, `1e9`, `2.5e-3`. Imaginary literals (`2i`) are S001. `1..n` is the integer `1`, the range operator and `n`, never the float `1.` followed by `.n`. A literal takes the type its context expects (196u): there are no untyped constants.
3. **Durations.** An integer immediately followed by a unit is a `duration` literal: `500ms`, `2s`, `5m`. Its value in nanoseconds must fit `int64` (S108). Any other letter suffix is an error (S001).
4. **Strings.** An interpreted string `"…"` uses Go's escapes and is decoded with Go's rules; an invalid escape is an error (S108). A raw string is enclosed in back quotes and never interpolates. A newline inside an interpreted string, or a missing closing quote, is S001. The value of a string is the bytes its spelling decodes to.
5. **Interpolation** (196s). `"ok \(id)"` inserts the value of the expression between `\(` and its matching `)`. In v1 a value of a basic type prints as Go's `%v` prints it (`fmt.Sprint` of the one value: a string inserts its bytes, an integer its decimal digits, a bool `true` or `false`), so `Console.Println("\(a) \(b)")` prints what Go's `fmt.Println(a, b)` prints; the translator writes old `Console.Println` calls of several operands that way (#196 round 6, A3). A value of a basic type runs (check-v1 stage D3b); a value of any other type (a record, a defined type, a union, a slice) is refused at build with S109 at the value, naming check-v1 stage E. The expression may be any expression, strings and nested interpolations included: `"a \(f("b \(c)")) d"`. `\\(` is a backslash followed by a literal `(`. An unterminated `\(` is S001 at the `\`. `\(` is not a Go escape, so the form cannot be misread. *Prior art:* Swift's `\( … )`; Kotlin and Gleam-style templates.
6. **Import paths and extern targets** are one unquoted token after `import` (with its optional `go` and alias) and after `extern go`: `net/http`, `example.com/json`, `example.com/json.Valid`. A quoted path is S001 with the habit message.

## Operators and punctuation

Go's operators and punctuation, plus `..`, without the removed ones.

1. **`..`** (range) is non-associative and binds looser than `||`: `1..len(xs)-1` is `1..(len(xs)-1)`, and `a..b..c` is S001. *Prior art:* Rust's `..`, with the same precedence.
2. **Removed operators still lex.** `:=`, `<-`, `++`, `--` and `...` stay tokens, so `x <-y` is never read as `x < -y` and `x--` never as `x - -`. Each is S001 wherever it appears, with its habit message. `~` is S001.
3. Binary precedence, from loosest to tightest: `..`; `||`; `&&`; `==`, `!=`, `<`, `<=`, `>`, `>=`; `+`, `-`, `|`, `^`; `*`, `/`, `%`, `<<`, `>>`, `&`, `&^`. Below `..`, this is Go's order. [types.md](types.md#operators) states which operators type-check.

## Declarations

```ebnf
SourceFile     = { ImportDecl ";" } { TopLevelDecl ";" } .
ImportDecl     = "import" [ "go" ] [ identifier ] import_path .
TopLevelDecl   = TypeDecl | FuncDecl | ExternDecl | TestDecl | LetDecl .
TypeDecl       = "type" identifier [ TypeParams ] ( Members | MachineType | "=" UnionType | Type ) .
TypeParams     = "[" TypeParam { "," TypeParam } "]" .
TypeParam      = identifier { "," identifier } [ TypeName ] .
Members        = "{" [ Member { "," Member } [ "," ] ] "}" .
Member         = FieldDecl | identifier Signature | LawDecl .
FieldDecl      = identifier { "," identifier } Type .
LawDecl        = "law" identifier Params Block .
UnionType      = [ "|" ] Variant { "|" Variant } .
Variant        = TypeName [ TypeArgs ] [ Members ] .
MachineType    = "machine" TypeArgs "{" MachineEntry { "," MachineEntry } [ "," ] "}" .
MachineEntry   = identifier ":" Expression .
Type           = TypeName [ TypeArgs ] | SliceType | FuncType | PointerType | "(" Type { "," Type } ")" .
TypeName       = identifier [ "." identifier ] .
TypeArgs       = "[" Type { "," Type } "]" .
SliceType      = "[" "]" Type .
PointerType    = "*" Type .
FuncType       = [ "escaping" ] "func" Signature .
FuncDecl       = "func" identifier [ TypeParams ] Signature Block .
ExternDecl     = "extern" "go" import_path "." identifier "func" identifier [ TypeParams ] Signature .
TestDecl       = "test" identifier [ Params ] Block .
Signature      = Params [ Type ] [ FailsClause ] [ NeedsClause ] .
Params         = "(" [ Param { "," Param } [ "," ] ] ")" .
Param          = [ identifier { "," identifier } ] Type .
FailsClause    = "fails" TypeName [ TypeArgs ] { "|" TypeName [ TypeArgs ] } .
NeedsClause    = "needs" TypeName { "," TypeName } .
LetDecl        = "let" [ "mut" ] Pattern { "," Pattern } [ Type ] [ "=" Expression ] .
ProvidesClause = "provides" TypeName { "," TypeName } .
ContextBody    = "{" [ ContextEntry { "," ContextEntry } [ "," ] ] "}" .
ContextEntry   = [ "replace" ] TypeName { "," TypeName } ":" Expression | TypeName .
```

1. **Imports.** One `import` per line: `import [go] [alias] path`, the path unquoted. With two words after `import`, or after `import go`, the first is the alias: `import ourjson example.com/json`, `import go gohttp net/http`. An alias is only for a name clash: the path's last element must be the name another import of the file binds or a package-level name, and any other alias, `_` included, is S108 "an import alias is only for a name clash; write import path" (fourth round, item 3; check-v1 D-D7). A keyword is never a bound name, so a path whose last element is a keyword clashes and needs an alias: Go's context package is `import go gocontext context`, and `import go context` is S108 "context is a keyword; import the package under another name: import go gocontext context" (196w; the message recognises a Go habit, #220). There is no grouped import and no dot import. `import go path` imports a Go package; every call through it charges `go.Foreign` ([interop](../interop.md)). *Prior art:* Gleam and Elixir import one module per line.
2. **Extern.** `extern go example.com/json.Valid func Valid(b []byte) bool` binds one Go function with stated types. The target is one unquoted token; the Go function's name is the part after the last `.` that follows the last `/`, and the rest is the import path. A function declaration needs a body: a bodiless Go binding is an `extern go` declaration. *Prior art:* Gleam `@external`, ReScript `external`.
3. **Type parameters** are declared in brackets, each with an optional inline constraint: `[T ordered]`, `[A, B]`, `[K comparable, V]`. The kinds stay implicit: a parameter after `fails` is an error row and one after `needs` a capability row (196g as amended). A bare upper-case letter is not a parameter by itself.
4. **One type shape.** `type Name [TypeParams] { Members }` holds fields, or operations and laws: the members decide the kind (fifth round). An empty `{}` is a record. Fields and members are separated by commas (sixth round). A member that is a bare name (Go embedding) is S001. `type Name = Variant | …` is a union; a variant is a type name, with optional type arguments and optional members that declare it in place (`Paid{ID string}`, `Pending{}`). The leading `|` is optional, and a union may continue on the next line ([Source files](#source-files) rule 5). A declaration names its fields: `Some{T}` is S001. `type Name = (A, B)` is S001, because `=` means a union. `type Name Type` is a defined type over another type, not an alias.
5. **Records hold data.** A type with both fields and operations, or with a `law` among fields, is **S108** from the parser: "a record holds data; declare the operations in their own type". The tree cannot hold the mix.
6. **Machines.** `type Breaker machine[State, Signal] { initial: Closed{}, step: Step }` ([runtime.md](runtime.md#machines-and-actors)). The entries are `initial` and `step`, each once.
7. **Results.** A function has at most one result type, which may be a tuple, then `fails`, then `needs`. A result tuple whose last element is `error`, and a bare `error[…]` result, are S001 with the `fails` message: cheap recognition of Go's `(T, error)` and of the old row.
8. **No receivers.** `func Fee(o Order) int64` takes the receiver as its first parameter (196m). A method value is a call's business, not a selector's.
9. **`needs` inside a list.** Inside a parameter or result list, `needs` takes one name, or a parenthesised list `needs (A, B)`, because a comma ends the parameter. A declaration's own `needs` clause, and a function literal's, is unparenthesised: `func() needs A, B { … }` (`needs (A, B)` on a literal is S001).
10. **`let`.** `let x = e`, `let x T = e`, `let x T`, `let q, r = e` and `let Point{X: px} = p` bind names; `let mut` binds a name that may be reassigned (196v). `let x` needs a type or a value. `let mut` binds names only, never a pattern. A package-level `let` is immutable, so a package-level `let mut` is an error. A binder never reuses a name bound in an enclosing scope of the same function, function literals and trailing blocks included; sibling scopes may reuse a name, and a local may reuse a package-level name other than a prelude name (no shadowing, 196v rule 2; check-v1 D-D2). `let Name = context { … }` at package level is a named context.
11. **Tests and laws.** `test Name { … }` is an example; `test Name(x T) { … }` a property. `law Name(params) { body }` is a member of an operation type (196l). Tests and `assert` are **implemented** (check-v1 stage E3a); laws stay S109 until stage E3b.
    - A test declares no value: its name is in no value scope. Its body is a failure target, so a failure that escapes it fails the test, and a service it calls comes only from its own `with` (S102).
    - `assert e` stands only in a test body or a law body, at any statement position; anywhere else it is S108. What decides is its lexical owner, the innermost test, law, function declaration or function literal around it: a function literal inside a test or a law is not that body, so an `assert` in it is S108 (a trailing block and a `with` body stay in their owner). Its operand is a `bool` (S108).
    - The test build (`lower.LowerTests`) lowers each test to a Go function and adds a runner: it prints `ok Name` or a `FAIL` line naming the failed assert's position and text, runs a property's 100 cases from a seed (`--seed N`, default 1), and exits 1 when a test fails. A case's verdict is the exit that survives its unwinding: failed asserts, with typed failures beside them, fail the test and the run goes on; any other exit, such as a defect an exit action raises after an assert failed, prints the test's `FAIL` line and stops the run with the runtime's report of every reason (exit 2). The run build, which `ergo build` and `ergo run` use, contains no Go from a test. The `ergo test` command is not wired to the test build yet.
12. **Visibility.** See [Visibility](#visibility).

## Visibility

Status: **designed, not yet implemented**. The prototype compiles one file, so visibility does not arise there. Owner decision, 2026-10-27 (README Gap 8).

1. A package-level name, a record field or an operation is **exported** when its first character is an upper-case letter (Unicode category Lu). Every other name is unexported.
2. Another package may name only exported declarations, fields and operations.
3. Outside its package, a record type with an unexported field has no composite literal, not even `Quantity{}`, and no implicit zero value ([types.md](types.md#zero-values-and-definite-initialisation), rule 6). It can be built there only through the functions its package exports.
4. There is no `pub` keyword.
5. Exported functions spell their rows ([rows.md](rows.md#capability-rows)).

*Prior art:* Go exports by capitalisation, which this rule copies; Gleam, Rust and MoonBit use a `pub` keyword; Elixir uses `def` against `defp`.

## Statements

```ebnf
Block          = "{" { Statement ";" } "}" .
Statement      = LetDecl | Expression | Assignment | ReturnStmt | "break" | "continue" | ForStmt | "assert" Expression .
Assignment     = Expression assign_op Expression .
ReturnStmt     = "return" [ Expression ] .
ForStmt        = "for" [ Expression | Pattern { "," Pattern } "in" Expression ] Block .
```

1. **One `for`.** `for x in xs` binds the element; `for i, x in xs` the index and the element; `for i in 0..n` counts; `for cond { }` and `for { }` loop. A Map follows the same rule: `for v in m` binds each value, and `for k, v in m` the key and the value, as the index and the element of a slice; iteration order is unspecified, as in Go. A `Range[T]` or `Seq[T]` takes one name. There is no three-clause `for`, no `range` and no `:=`. The domains and their typing are [types.md, Loops and len](types.md#loops-and-len).
2. **Assignment** has one left side and one right side: `x = e`, `x += 1`. Its target is a `let mut` local, or a field or element reached from one; a parameter, a `let`, a pattern, `as`, `case err:` or loop binding and a package-level `let` are immutable, and so are their fields and elements (196v; check-v1 D-D1, Q2). A write through a pointer changes the pointee, not the root, and needs no `let mut`; a borrow `&x`, or a call `x.f()` of a function whose first parameter is `*T`, needs `x` rooted at a `let mut` (pointers are #222's). A `let mut` that is never reassigned is an error (S110, Q3). There is no `++` or `--` and no multiple assignment.
3. **`break` and `continue`** act on the innermost `for`, also inside a `match` arm (seventh round, item 4). There are no labels.
4. There is no bare block statement, and no init statement in an `if` or `match` header (eighth round): write `let x = f()` on the line before.
5. A `with` block and a trailing block yield their last expression. `return`, `break` and `continue` that would leave them are errors (S108). A `func` literal's `return` returns from the literal, as in Go. A `catch` arm or a `match` arm holds statements; a `return` there leaves the enclosing function.

## Expressions

```ebnf
ExpressionList = Expression { "," Expression } .
Expression     = [ "try" ] CatchExpr .
CatchExpr      = Binary [ "catch" "{" { CaseClause } "}" ] .
Binary         = Unary { binary_op Unary } .
Unary          = { unary_op } Postfix .
Postfix        = Primary { Selector | Index | Slice | Call } .
Selector       = "." identifier .
Index          = "[" ExpressionList "]" .
Slice          = "[" [ Expression ] ":" [ Expression ] [ ":" Expression ] "]" .
Call           = "(" [ ExpressionList [ "," ] ] ")" [ Block ] .
Primary        = Literal | identifier | CompositeLit | FuncLit | IfExpr | MatchExpr | WithExpr | ContextLit | "(" ExpressionList ")" .
Literal        = int_lit | float_lit | rune_lit | string_lit | duration_lit .
CompositeLit   = LiteralType LiteralValue .
LiteralType    = TypeName [ TypeArgs ] | SliceType .
LiteralValue   = "{" [ Element { "," Element } [ "," ] ] "}" .
Element        = [ ( Expression | LiteralValue ) ":" ] ( Expression | LiteralValue ) .
FuncLit        = "func" "(" [ LitParam { "," LitParam } ] ")" [ Type ] [ FailsClause ] [ NeedsClause ] Block .
LitParam       = identifier [ Type ] .
IfExpr         = "if" Expression Block [ "else" ( IfExpr | Block ) ] .
MatchExpr      = "match" Expression "{" { CaseClause } "}" .
CaseClause     = "case" Pattern { "," Pattern } ":" { Statement ";" } .
WithExpr       = "with" ExpressionList Block .
ContextLit     = "context" [ ProvidesClause ] [ NeedsClause ] ContextBody .
Pattern        = PatternCore [ "as" identifier ] .
PatternCore    = "_" | identifier | [ "-" ] Literal | TypeName [ TypeArgs ] [ "{" [ FieldPattern { "," FieldPattern } ] "}" ] | "(" Pattern { "," Pattern } ")" .
FieldPattern   = [ identifier ":" ] Pattern .
```

1. **`try` and `catch`.** `try` stands only at the start of an Expression, and covers the whole expression to its right (sixth round): `a + try f()` is S001; write `try a + f()`. `catch` binds looser than every binary operator, so `a + f() catch { … }` covers `a + f()`. `try e catch { … }` is one partial catch ([rows.md](rows.md#catch)). There is one `catch` form, with no binding after `catch`: an arm binds with `as` (`case NotFound as e:`) or with `case err:`.
2. **`if` and `match` are expressions** (196r). At the start of a statement, `if`, `match` and `for` form the statement and are not continued by a binary operator, a selector or a call (Parser rule 3). In operand position they are expressions: `let v = if c { 1 } else { 2 }`.
3. **`match`.** A case lists patterns, not expressions: `case limit + 1:` is S001 with a message that points to `if`. `case _:` matches the rest; there is no `default`. A tuple subject is written in parentheses, `match (a, b)`, and its cases are tuple patterns.
4. **Composite literals** are all keyed or all positional; mixing them is S001. A composite literal is not parsed in an `if`, `match`, `for` or `with` header unless parenthesised, as in Go. A keyed, positional or empty literal written bare there, `with Cfg{Port: 1} { … }`, `if p == Point{1, 2} { … }` or `if p == Point{-1, one()} { … }`, is S001 at its type: "a composite literal in a header needs parentheses: write (Cfg{…}), or bind it with let first". A positional literal is recognised by its first element: any complete expression (a unary, call, selector, index, literal or parenthesised expression) on the brace's line followed by a comma, or by the `}` and the body's `{`; no statement starts so. A `context { … }` value starts with its keyword and is allowed there.
5. **Function literals.** Parameters are all typed, with Go's grouping (`func(a, b int64) int64 { … }`), or all untyped (`func(acc, x) { acc + x }`, #214); a mix is S001. `func(x)` followed by a block is a literal with a parameter `x`. In value position `func` always starts a literal: without a block, `func(x)` is S001 (rule 10).
6. **Patterns** mirror construction (196p): `_`, a name, a literal (a numeric literal may take a leading `-`), a type name with optional type arguments and field patterns, a tuple. `P as name` matches `P` and binds the matched value, with `P`'s type, to `name` (ninth round): `case Declined as d:`, `case NotFound{ID: id} as e:`, and nested, `Declined{Reason: Fraud as f}`. The checker gives patterns their meaning: what a name binds, what a type tests, totality and reachability ([rows.md](rows.md#totality), Totality 1, 2 and 6). *Prior art:* Gleam and OCaml `as`, Python 3.10 `case Point() as p`, Rust `p @ Pattern`, Swift `case let p as T`.
7. **`with`.** `with E, F { body }` provides the contexts `E` and `F` to its block; `with (W) { … }` is the same as `with W { … }`, and the formatter writes the second.
8. **Context literals.** `context [provides …] [needs …] { A: x, B, C: y, replace D: z, Shared }` builds a context: an entry binds one or more services to a provider, `replace` replaces an entry of an embedded context, and a bare name embeds a context ([rows.md](rows.md#context)). `provides` comes before `needs`.
9. **Trailing blocks.** A trailing block is notation for a function-literal argument, over a restricted domain: its body may not contain `return`, `break` or `continue`. Within that domain, `f(a) { body }` is exactly `f(a, func() T { return body })`, where `T` and the literal's rows come from `f`'s final parameter type. A trailing block needs the call's parentheses (Parser rule 6). *Prior art:* Swift trailing closures and Kotlin trailing lambdas.
10. **A type is never a value.** `Primary` has no type operand. A bare type in value position, such as `func(int) int`, `escaping func()`, `[]int`, the conversion `[]byte(s)` or the map type `Map[string, int]` (Parser rule 8) with no literal braces, is S001 at its first token: "a type is not a value; write a literal or a conversion". A slice type stands in value position only as a composite literal's type (`[]int{1, 2}`), and a conversion is a call of a named type (`int64(x)`). A type argument is a type, not a value: `Map[string, []int]{}` and `Option[func() int]{}` are unchanged. The parser commits to a function literal at `func`, so an error in its signature stands where it is: `func() needs A, B[T] { … }` is S001 at the `[` (Parser rule 12). *Prior art:* Gleam has no type in expression position; rustc commits to a closure once it starts.

## Parser

Status: **implemented** in `internal/sketch/syntax`. `syntax.Parse` returns one file's tree or the first diagnostic, located by byte offset, line and byte column. It never returns a partial tree.

1. **The decided grammar, and nothing else.** The parser accepts exactly the productions of this file plus the lexical grammar it inherits from Go (identifiers, literals, `binary_op`, `unary_op`, `assign_op`). Every Go form outside them is S001 at its own token (`TestOldCorpusRefusedOrSame` proves that an old file either is refused or means what its translation means).
2. **Desugaring.** A decided form that means the same as an existing node becomes that node: `let` is a `VarDecl`, `fails` an `ErrorRow` after the result, `match` on literal patterns a `SwitchStmt` and any other `match` a `TypeSwitchStmt`, `with E { … }` a call of `with` with a trailing block, a package-level `let Name = context { … }` a `ContextDecl`. Nodes no decided form produces stay declared until their consumers are rewritten; the parser never builds them (`TestDecidedOnly`). *Prior art:* ReScript's parser produces the tree the old front end produced, so OCaml's type checker stays unchanged; Rust lowers `for` and `?` before type checking.
3. **Statement-leading forms.** `if`, `match` and `for` at the start of a statement form the statement, and a following binary operator, selector or call is S001 (Rust's rule). `try` is admitted only at the start of an Expression.
4. **Headers.** A composite literal is not parsed in an `if`, `match`, `for` or `with` header unless parenthesised. One pair of parentheses around a single `with` context or a `match` subject that reads the same without them is dropped from the tree, so the formatter's spelling has the same tree as the source.
5. **Nesting limit.** A tree higher than 1000 levels is S001, at the first token past the limit. The limit bounds the height of the returned tree, not only the path the parser has open, so it holds for every composition. Levels count as follows:
   - A type, a statement, a block, an expression (the value of a declaration, an argument, an index, an element, the inside of parentheses) and an `else if` each open one level below their parent.
   - A selector, an index, a call, a composite literal, a binary operator and a unary operator each wrap the tree built so far in one more level, as in `go/parser`. So `a.b.c…` with 1001 links is S001, and parentheses are two levels: the operand and the expression inside.
   - A chain counts its operand in full. In `let x = (a` + 900 links + `)` + k links, the value opens 1 level, the parentheses 2 and the inner chain 900, so k = 97 is exactly 1000 levels and the 98th link after the parenthesis is S001.
   - An argument or an index sits one level below its own link, not below the links that follow it.
   - The `{` after a control clause header opens the body, a sibling of the header, never a link of its chain. In `func f() { if a` + k links + ` {} }`, the function block, the `if` statement and the header expression are 3 levels, so k = 997 is exactly 1000 levels and the 998th link is S001. `match` and `for` headers count the same way.
   - A prefix operator (`!`, `-`, `+`, `^`, `*`, `&`) opens its level before the operand is read, so past the limit the S001 is at the operator itself.

   A hostile input therefore cannot exhaust the stack of the parser or of any consumer of the tree. *Prior art:* `go/parser` stops at a nesting depth of 100 000 with "exceeded max nesting depth"; the lower bound here keeps the formatter's recursion small.
6. **Paren-less trailing block.** A trailing block needs the call's parentheses: `task.Go() { … }`. `task.Go { … }` is S001, because without name resolution it cannot be told apart from the composite literal `task.Go{ f() }`.
7. **Type assertions.** `x.(type)` and `x.(T)` are S001 with the `match` message.
8. **`Map[K, V]`.** The bare name `Map` with two type arguments is the map type: a prelude name cannot be redeclared (eighth round), so the parser need not resolve it.
9. **Context entry keys.** In a context body, a run of type names separated by commas and ended by `:` is the key list of one entry, whatever the layout: `{ A, B: p }` binds both `A` and `B` to `p`, on one line or several. An embedded context therefore never stands just before an entry; write `{ B: p, A }`. Entry order has no meaning. *Prior art:* Go's `case a, b:` is one comma list ended by `:`, with no layout rule.
10. **Tuples have two or more elements.** `(x,)` is S001 in expressions, types and patterns alike, so `(x)` and `(x,)` never denote different trees.
11. **`Name (` in a type body is an operation.** `A (int64, string)` and `A(int64, string)` are the same tokens, so layout cannot make one a field and the other an operation: both are the operation `A` with two unnamed parameters. A record with one field of tuple type names the tuple: `type Pair (int64, string)`, then `A Pair`. A field group before a tuple type, `X, Y (int64, int64)`, stays a field group (FieldDecl).
12. **Capability names take no type arguments.** `needs`, `provides` and context entry keys take exactly `TypeName`: `B[T]` there is S001 at the `[`, "a capability name takes no type arguments". With no brackets in a key, rule 9 holds with no exception.

## Old spellings

Every old spelling is S001 at its own token, with a byte span, line and column. A **Go habit** gets a message that names the Ergo form, as code in backticks, because a host-language habit is not compatibility ([#220](https://github.com/cevr/ergo/issues/220)). *Prior art:* rustc recognises other languages' habits and names the Rust form ("Rust has no postfix increment operator"). The table lives in `internal/sketch/syntax/habits.go`:

| Go spelling | Message |
| --- | --- |
| `package p` | Ergo has no package clause; the directory names the package |
| `import ( … )` | write one import per line: `import net/http` |
| `import "p"` | an import path is unquoted: `import net/http` |
| `x := e` | Ergo declares names with `let x = e`, or `let mut x = e` for a name that is reassigned |
| `var x T` | Ergo has no var; declare with `let x = e`, `let x T = e` or `let mut x = e` |
| `const x = e` | Ergo has no const; declare with `let x = e`: a literal takes the type its context expects |
| `if x := f(); c` | an if or match header has no init statement; write `let x = f()` on the line before |
| `x++`, `x--` | Ergo has no ++ or --; write `x += 1` or `x -= 1` |
| `for i := 0; i < n; i++` | Ergo has no three-clause for; write `for i in 0..n` |
| `for k, v := range xs` | Ergo has no range; write `for x in xs` or `for i, x in xs` |
| `switch` | Ergo has no switch; write `match x { case p: … }` |
| `x.(type)` | Ergo has no type switch; write `match x { case T as t: … }` |
| `x.(T)` | Ergo has no type assertions; write `match x { case T as t: … }` |
| `default:` | write `case _:` for the arm that matches the rest |
| `struct` | Ergo has no struct keyword; declare a record with `type Name { fields }` |
| `interface` | Ergo has no interface keyword; declare operations with `type Name { operations }` and a union with `type Name = A \| B` |
| `func (r T) M()` | Ergo has no receivers; make the receiver the first parameter: `func M(r T, …)` |
| `(T, error)` | Ergo writes failures after the result: `func F(…) T fails E` |
| `go f()` | Ergo has no go statement; start a task with `task.Go() { f() }` |
| `defer f()` | Ergo has no defer; register cleanup with `scope.OnExit` |
| `chan T` | Ergo has no channels; use a typed queue |
| `<-ch` | Ergo has no channel operator; use a typed queue |
| `select` | Ergo has no select; race tasks with `task.Race` |
| `map[K]V` | Ergo has no map keyword; write `Map[K, V]` |
| `...T` | Ergo has no variadic parameters; take a slice `[]T` |
| `[N]T` | Ergo has no fixed arrays; use a slice `[]T` |
| `L:` | Ergo has no labels; break and continue act on the innermost for |
| `goto` | goto is not part of the language; use structured control flow |
| `fallthrough` | fallthrough is not part of the language; list the patterns in one case |

**Old Ergo-only spellings** (`error[…]` rows, `wire Name { A = x }`, `catch e { … }`, bodiless functions with `//ef:go`, `uses`, `try` in operand position) get the plain S001 any stray token gets, with no rename hint ([#217](https://github.com/cevr/ergo/issues/217)). The one-off translator `scripts/migrate196` rewrites old sources into the decided syntax; it is deleted after v1.
