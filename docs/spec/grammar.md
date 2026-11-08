# Grammar

Status: the parser `internal/sketch/syntax` and the formatter `internal/sketch/format` **implement the decided grammar of [#196](https://github.com/cevr/ergo/issues/196)**, and nothing else. The source is the appendix "Decided Go-shaped sketch" of the [2026-10-28 preview](../previews/2026-10-28-without-go-syntax.md) (62 productions), its decisions table and its seventh, eighth and ninth rounds. Where the sketch is silent, the rules below decide; each says why. A syntax error is S001 ([diagnostics](diagnostics.md)), with one exception, S108 for a type that mixes fields and operations ([Declarations](#declarations) rule 5).

A Go form that this file does not admit is an error, never a passthrough to the host (AGENTS.md). The old Go-shaped spelling has no compatibility path ([#217](https://github.com/cevr/ergo/issues/217)); a Go habit gets a message that names the Ergo form ([Old spellings](#old-spellings)).

## Source files

1. A source file ends in `.ergo`. Every surface refuses any other extension with one message, "source file must have .ergo extension", the same for `.ef` and for `.txt`. The directory names the package: a file has no `package` clause.
   - **Packages and modules** (196b U3; **implemented**). A package is a folder: every `.ergo` file directly in it, in byte order of the names; a name that starts with `.` or `_` is ignored, and a subfolder is another package. Its name is the last element of its import path. A file `ergo.mod` marks the module root; its one line, `module NAME`, names the module, and NAME is an import path whose first element is not a std package path or `prelude` (else S108 in `ergo.mod`). A local package's import path is the module name and its folder's path from the root: `import tidewalk/walk`. A program with no `ergo.mod` above its folder imports std only; another path is S104 "unknown package "x": no ergo.mod above the program's folder declares a module, …". There are no imports across modules in v1. A folder below the root that holds its own `ergo.mod` ends the module there, as a nested `go.mod` ends a Go module: a package in or under it is that module's, and importing it from the outer module is S104 "unknown package "app/sub/x": the folder sub is module other (sub/ergo.mod); …". An import cycle is S108 "import cycle: a imports b imports a", at the import in the first file, by name, of the package that closes it, found before anything is checked. A package is checked once, after the packages it imports; a package whose import has diagnostics is not checked. Every diagnostic of such a program names its file relative to the module root. `ergo check|diagnostics|build|run|test DIR` reads the package in DIR; `ergo build FILE` keeps the meaning of one file, which imports std only ([tooling](../tooling.md)). *Prior art:* Go's `go.mod` module path and its folder packages; Gleam names the project in `gleam.toml` and a module path is its file path. Both refuse import cycles.
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

Fixtures: `internal/sketch/testdata/probes/letter-name.ergo` (builds), `nonletter-name.ergo` (S001); `TestIdentifierRunesMatchGo` sweeps every non-ASCII code point against `go/token.IsIdentifier`.

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
- **`needs` and `provides`** (owner, 2026-10-27). `needs A | B` lists what a function, a function type, an operation or a context requires; `provides A | B` lists what a context exposes ([rows.md](rows.md#context)). Each is a row, a union, so it is spelled as the error row is: `fails NotFound | Declined needs Orders | Payments` (owner, 2026-11-03, 196n). *Prior art:* Roc's `provides` and `requires`; Effect's `Layer<ROut, E, RIn>`, whose requirement channel is the type union `R1 | R2`.
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
2. **Predeclared names** are declared in the universe block: the basic types (`string`, `bool`, `int`, `int64`, `float64`, `byte`, `rune`), `duration`, `comparable`, `ordered`, `number`, and the prelude names of [stdlib.md](stdlib.md#prelude), `Map` and `Option` among them. A prelude name cannot be redeclared (eighth round), with one exception: a package-level type declaration, a union's inline variant included, may take the name of a prelude or universe type (`Option`, `Some`, `None`, `Map`, `Range`, `Seq`, `Task`, `int`, `string`, …), as Go's package scope hides its universe scope (L6, owner q30, 2026-11-08). Inside that package the name means the package's type, in every type position, a composite literal and a pattern; other packages, std's results and the prelude's own declarations keep the prelude's type, so `slices.Max(xs)` still returns the prelude `Option`, and a range literal `0..n` still builds the prelude `Range`. Every other declaration of a prelude name stays S108 (D-D3): a function, a package `let`, a context, an import alias, a type parameter, a parameter and a local, and any declaration of a prelude constant or function name (`true`, `len`), a type's included. A package's own type of such a name is a duplicate for every other package-level name. The parser still reads `Map[K, V]` as the map type (Parser rule 8); in a package that declares its own `Map`, the checker reads that form as an instance of the package's `Map`. The prelude's functions on slice and Map types (`Append`, `Get`, `Delete`) are no names in any scope and may be declared again: they are reached only by the dot form (check-v1 D-D3 amended by D-D6, D-D8).
3. **Prototype limits (not language rules).** The prototype reserves, as S109: Go's predeclared identifiers, `rt`, `time`, `fmt`, `init`, `Error`, `timeout` and `duration`; every name that contains `__` or ends in `_`; and the field names `Error`, names with `__` and names ending in `_`. These keep its generated Go free of collisions. The main compiler must mangle generated names instead (README, Gap 16).

## Literals

```ebnf
int_lit       = decimal_lit | binary_lit | octal_lit | hex_lit .
decimal_lit   = "0" | ( "1" … "9" ) [ [ "_" ] decimals ] .
binary_lit    = "0" ( "b" | "B" ) [ "_" ] binary_digit { [ "_" ] binary_digit } .
octal_lit     = "0" ( "o" | "O" ) [ "_" ] octal_digit { [ "_" ] octal_digit } .
hex_lit       = "0" ( "x" | "X" ) [ "_" ] hex_digit { [ "_" ] hex_digit } .
decimals      = decimal_digit { [ "_" ] decimal_digit } .
float_lit     = decimals "." [ decimals ] [ exponent ] | decimals exponent | "." decimals [ exponent ] .
exponent      = ( "e" | "E" ) [ "+" | "-" ] decimals .
duration_lit  = decimal_lit duration_unit .
duration_unit = "ns" | "us" | "ms" | "s" | "m" | "h" .
rune_lit      = "'" ( unicode_value | byte_value ) "'" .
string_lit    = interpreted_string | raw_string .
interpreted_string = `"` { unicode_value | byte_value | interpolation } `"` .
interpolation = `\(` Expression ")" .
import_path   = path_element { ( "/" | "." ) path_element } .
path_element  = letter { letter | digit | "-" } .
```

1. **Integers.** An integer literal is decimal, binary (`0b1010`), octal (`0o600`) or hexadecimal (`0x80`, `0xFF`), as in Go; the prefix and the hexadecimal digits may be either case. A `_` may stand between two digits and after a base prefix (`1_000_000`, `0xFF_FF`, `0x_1F`), and in a float or duration literal between two decimal digits (`1_000.5`, `1_500ms`); it does not change the value. A misplaced `_`, a digit outside the base, a prefix with no digits, a hexadecimal float (`0x1p4`) and a duration with a base prefix are S001. A decimal literal with a leading zero and more than one digit (`07`, `0_7`) is an error (S108), because Go would read it as octal: write `0o7`. A literal of any base is typed by its context like any integer literal (196u) and its value must fit the type it takes (S108): `let m uint8 = 0xFF` holds 255 and `let m uint8 = 0x100` is S108. `ergo fmt` keeps the spelling (base, case and separators); the lowering writes the value. *Prior art:* Go 1.13 number literals, which Ergo copies; Gleam (`0x`, `0o`, `0b`, `_`), Rust and Swift have the same forms.
2. **Floats.** Go's decimal float forms are literals: `2.5`, `1.`, `.5`, `1e9`, `2.5e-3`. Imaginary literals (`2i`) are S001. `1..n` is the integer `1`, the range operator and `n`, never the float `1.` followed by `.n`. A literal takes the type its context expects (196u): there are no untyped constants.
3. **Durations.** An integer immediately followed by a unit is a `duration` literal: `500ms`, `2s`, `5m`. Its value in nanoseconds must fit `int64` (S108). Any other letter suffix is an error (S001).
4. **Strings.** An interpreted string `"…"` uses Go's escapes and is decoded with Go's rules; an invalid escape is an error (S108). A raw string is enclosed in back quotes and never interpolates. A newline inside an interpreted string, or a missing closing quote, is S001. The value of a string is the bytes its spelling decodes to.
5. **Interpolation** (196s). `"ok \(id)"` inserts the value of the expression between `\(` and its matching `)`. In v1 a value of a basic type prints as Go's `%v` prints it (`fmt.Sprint` of the one value: a string inserts its bytes, an integer its decimal digits, a bool `true` or `false`), so `Console.Println("\(a) \(b)")` prints what Go's `fmt.Println(a, b)` prints; the translator writes old `Console.Println` calls of several operands that way (#196 round 6, A3). A value has a text form exactly when its type is a basic type or a type defined over one, at any depth (`type Warm Celsius`), and a defined type prints as its basic value does (check-v1 E6a, D-E6, Q5). A value with no text form is refused by the checker, once, at the value, and the string is still a string, so nothing else is reported: a record, a union or a variant, a tuple, a slice, a Map (and so `Option[T]`) or a type parameter is S109 "a value of type T has no text form yet", naming derived text forms ([#49](https://github.com/cevr/ergo/issues/49)), so interpolate one of its fields; a function, an operation-type value such as a `Task`, or a pointer is S108 "a value of type T has no text form". The lowering writes the text with Go's `strconv` functions and a duration's `String`, never through `fmt` or an `interface{}` (196s: no boxing), in one allocation for a string with literal text. The expression may be any expression, strings and nested interpolations included: `"a \(f("b \(c)")) d"`. `\\(` is a backslash followed by a literal `(`. An unterminated `\(` is S001 at the `\`. `\(` is not a Go escape, so the form cannot be misread. *Prior art:* Swift's `\( … )`; Kotlin and Gleam-style templates.
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
TopLevelDecl   = TypeDecl | FuncDecl | ExternDecl | TestDecl | LawDecl | LetDecl .
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
NeedsClause    = "needs" TypeName { "|" TypeName } .
LetDecl        = "let" [ "mut" ] Pattern { "," Pattern } [ Type ] [ "=" Expression ] .
ProvidesClause = "provides" TypeName { "|" TypeName } .
ContextBody    = "{" [ ContextEntry { "," ContextEntry } [ "," ] ] "}" .
ContextEntry   = [ "replace" ] TypeName { "," TypeName } ":" Expression | TypeName .
```

1. **Imports.** One `import` per line: `import [go] [alias] path`, the path unquoted. With two words after `import`, or after `import go`, the first is the alias: `import ourjson example.com/json`, `import go gohttp net/http`. An alias is only for a name clash: the path's last element must be the name another import of the file binds or a package-level name, and any other alias, `_` included, is S108 "an import alias is only for a name clash; write import path" (fourth round, item 3; check-v1 D-D7). A keyword is never a bound name, so a path whose last element is a keyword clashes and needs an alias: Go's context package is `import go gocontext context`, and `import go context` is S108 "context is a keyword; import the package under another name: import go gocontext context" (196w; the message recognises a Go habit, #220). The same rule holds for a last element that is not a name, such as a folder `okra-repo`: `import app/okra-repo` is S108 "okra-repo is not a name; import the package under a name: import okrarepo app/okra-repo", and the alias is then not a needless one (196b Q3). A std path always names std, whatever folder the module holds (C30): a local folder `log` is `import app/log`. There is no grouped import and no dot import. `import go path` imports a Go package; every call through it charges `go.Foreign` ([interop](../interop.md)). *Prior art:* Gleam and Elixir import one module per line.
2. **Extern.** `extern go strings.ToUpper func Upper(s string) string` binds one Go function with stated types. The target is one unquoted token; the Go function's name is the part after the last `.` that follows the last `/`, and the rest is the import path. A function declaration needs a body: a bodiless Go binding is an `extern go` declaration. **Implemented** (check-v1 stage E4, 196x):
   - An extern is a function value when its call needs nothing but its arguments (G1 of batch 12): every user extern, and a std extern whose `runtime/v1` body takes no hidden argument (no frame, call site, task or Scheduler), no function and no type parameter, and returns no deferred `Seq`. `apply(strings.ToUpper, "a")`, `let f = Upper`, `Box{F: Upper}` and `slices.Transform(xs, strings.TrimSpace)` are values; the lowering wraps the Go target in a literal of the uniform function shape. Any other extern is call-only: a generic one (`queue.New[int]`, `slices.WithCapacity[int]`) and one with a hidden argument (`scope.OnExit`, `task.Go`), so a binding that needs its caller's frame never leaves its call site. A direct call is always a use: `Upper("x")`, `(Upper)("x")` and `queue.New[int](4)`. Any other use of a call-only extern, such as `let mk = queue.New[int]`, is S108 "an extern function is called directly; wrap it in a literal: func() { f() }" at the reference. A machine step and a context constructor entry check an extern value as any function there.
   - A user extern may take a **callback**: a parameter of an unnamed function type whose parameters are basic types, with at most one basic result and no `fails` or `needs` row (G1): `extern go strings.Map func Mapper(f func(r rune) rune, s string) string`, `Mapper(func(r rune) rune { r + 1 }, "abc")` is `bcd`. The lowering passes a Go function of the callback's shape that calls the Ergo value with the caller's environment, so a value that reads it (a method value of an operation type takes the caller's task) runs on its caller's task; a defect or a stop unwinds through the Go target as a panic. The value is evaluated once, before the target runs. A callback with a row is S109 (#202): Go code between the call and the callback cannot carry a failure or a capability.
   - A user extern has one domain. Its target is a function of a package in the **pure allowlist**: `crypto/sha256`, `encoding/base64`, `encoding/hex`, `hash/crc32`, `math`, `math/bits`, `path/filepath`, `slices`, `sort`, `strconv`, `strings`, `unicode`, `unicode/utf8` and `unicode/utf16`, matched by exact import path (wave std-1, unit G9; `path/filepath` from std-align S3a; `math/bits` from std-text-runes; `unicode` and `unicode/utf16` from G2 of batch 12; the four encoding and digest packages from G3 std-digest), except the members that reach the host, `v1.PureImportsExcept` (`filepath.Abs`, `EvalSymlinks`, `Glob`, `Walk`, `WalkDir`), which are S109 as a host package is. A function of those packages reads only its arguments. Any other package of Go's standard library may reach the host (`os`, `time`, `fmt`, `math/rand`, …), and a user extern has no `needs` row, so its call would be a hidden effect. Such a user extern is S109 at the declaration: "extern go os.Getenv reaches the host; wait for std os.Process or #202", with the target named and the allowlist listed. A subpackage (`strings/x`), a longer name (`stringsx`) or a vendored path (`vendor/strings`) is not in the list. A pure package outside the list, such as `math/cmplx`, `html` or `encoding/base32`, is S109 too until the list grows, with its own text: "extern go math/cmplx.Abs: math/cmplx is not on the pure allowlist (crypto/sha256, encoding/base64, …) yet; a user extern binds only those (#202)". It says "not listed", not "reaches the host": a reviewed table, `v1.PureUnlisted`, names the standard-library packages whose functions of basic types read only their arguments, and only words the message; nothing trusts it, and a package leaves it when it joins the allowlist. The allowlist is one list, the imports a pure std body's file may have (std's purity fact, G5b), so a user extern and a pure std body trust the same packages. Std's own externs bind `runtime/v1` and are not under this rule. A target whose first path element has a dot is outside Go's standard library, by Go's own rule for module paths, and keeps its #202 text below. Its signature takes and returns predeclared basic types only (`bool`, `string`, the integer, float and complex kinds, `duration`), with no type parameter, no `needs` row, no `fails` row but `fails go.Error` (below), and at most one result. Any other user extern is S109 at the declaration, naming [#202](https://github.com/cevr/ergo/issues/202) (Go imports): a dotted target such as `example.com/json.Valid`, a slice, Map, record, defined type, union, function or operation type in the signature, a type parameter, a row or a tuple result.
   - A user extern whose target is in the Ergo runtime (`ergo.local/prototype/runtime/…`) is S108: only std binds the runtime, because its functions take hidden arguments that std's bindings supply ([stdlib](stdlib.md)).
   - A user extern whose target is in Go's `unsafe` package (`unsafe.Sizeof`, `unsafe.Pointer`, …) is S108 "Ergo has no unsafe; unsafe stays at the FFI edge" at the declaration, whatever its signature ([NORTH_STAR](../../NORTH_STAR.md), Owner rules). So no Go built-in is a target.
   - A user extern whose Go target's last result is `error` declares **`fails go.Error`** (G4 of batch 12, decision O14): `extern go strconv.ParseUint func ParseUint(s string, base int, bits int) uint64 fails go.Error`. `go.Error { Text string }` is predeclared; a call whose target returns a non-nil error fails `go.Error{Text: err.Error()}`, and a `catch` or `try` handles it as any failure. The value Go returns beside an error (strconv's limit on a range error) is dropped: a failed call has no value. Ergo has no `errors.Is`, so the Go error itself is not kept. The rule is checked at the declaration against `v1.PureErrorFuncs`, the allowlist's functions whose last result is `error` (held to Go's sources by `TestPureErrorFuncs`): a target in it without the row is S108 "extern go strconv.ParseUint returns an error; declare it in the row: fails go.Error", and one outside it with the row is S108 "extern go strings.ToUpper returns no error; a user extern fails go.Error only when its Go function's last result is error". Any other `fails` row, `go.Error` with another type included, is S109 (#202). *Prior art:* Gleam's `@external` returns a `Result` the Erlang side builds; ReScript's `external` raises a JS exception, caught as `Js.Exn.Error`; Effect's `Effect.try` maps a thrown error to a typed one; Go's own `(T, error)` is the source.
   - The checker trusts the rest of the written signature and does not read the Go target; `go build` does.
   - An extern's parameter names follow a function's binder rules, in std and in user code alike: a prelude name is D-D3's S108, and a name used twice is S108 "x is already a parameter of f" (checker-fixes prelude-scope S2). The names bind nothing, because an extern has no body.

   *Prior art:* Gleam `@external` and ReScript `external` trust a written type, and both make an external a value (`list.map(xs, string.uppercase)`; ReScript passes a callback as a JS function). Go passes `strings.ToUpper` as a value. cgo functions and Go's built-ins are call-only. Elixir makes a function value only by an explicit capture (`&:math.sqrt/1`), as the generated literal does. Gleam, ReScript and Elixir let an external reach any host function, because none of them tracks effects; Ergo tracks them, so a user extern stays inside the pure allowlist and host access goes through std operation types (Roc keeps all host access in the platform for the same reason).
3. **Type parameters** are declared in brackets, each with an optional inline constraint: `[T ordered]`, `[A, B]`, `[K comparable, V]`. The kinds stay implicit: a parameter after `fails` is an error row and one after `needs` a capability row (196g as amended). A bare upper-case letter is not a parameter by itself.
4. **One type shape.** `type Name [TypeParams] { Members }` holds fields, or operations and laws: the members decide the kind (fifth round). An empty `{}` is a record. A type with laws and no operations is S108 from the checker, once, at its name: "T has laws and no operations; write it as a top-level law: law X(…) { … }" (law-top; its laws are still checked). Fields and members are separated by commas (sixth round). A member that is a bare name (Go embedding) is S001. `type Name = Variant | …` is a union; a variant is a type name, with optional type arguments and optional members that declare it in place (`Paid{ID string}`, `Pending{}`). The leading `|` is optional, and a union may continue on the next line ([Source files](#source-files) rule 5). A declaration names its fields: `Some{T}` is S001. `type Name = (A, B)` is S001, because `=` means a union. `type Name Type` is a defined type over another type, not an alias.
5. **Records hold data.** A type with both fields and operations, or with a `law` among fields, is **S108** from the parser: "a record holds data; declare the operations in their own type". The tree cannot hold the mix.
6. **Machines.** `type Breaker machine[State, Signal] { initial: Closed{}, step: Step }` ([runtime.md](runtime.md#machines-and-actors)). The entries are `initial` and `step`, each once.
7. **Results.** A function has at most one result type, which may be a tuple, then `fails`, then `needs`. A result tuple whose last element is `error`, and a bare `error[…]` result, are S001 with the `fails` message: cheap recognition of Go's `(T, error)` and of the old row.
8. **No receivers.** `func Fee(o Order) int64` takes the receiver as its first parameter (196m). A method value is a call's business, not a selector's.
9. **Capability rows are written with `|`** (196n). A `needs` or `provides` row separates its names with `|` in every position, a list included: `func g(h func() needs A | B)`, because `|` does not end a parameter. A comma between the names is S001 at the comma, "a capability row separates its names with |: write needs A | B" (or `provides`), wherever the comma cannot end something else: in a declaration, a function literal, a context header, a member before a lone name, and a named parameter before a name with no type. In a list of unnamed parameters or type arguments a comma still ends the element, so `func(func() needs A, B)` takes two parameters. A parenthesised row, `needs (A, B)` or `needs (A | B)`, is S001 at the `(`, "a capability row takes no parentheses: write needs A | B". `with E, F`, a context entry's keys `A, B: x`, type arguments and parameters keep their commas: each lists values or keys, not a row.
10. **`let`.** `let x = e`, `let x T = e`, `let x T`, `let q, r = e` and `let Point{X: px} = p` bind names; `let mut` binds a name that may be reassigned (196v). `let x` needs a type or a value. `let mut` binds names only, never a pattern. A package-level `let` is immutable, so a package-level `let mut` is an error. A binder never reuses a name bound in an enclosing scope of the same function, function literals and trailing blocks included; sibling scopes may reuse a name, and a local may reuse a package-level name other than a prelude name (no shadowing, 196v rule 2; check-v1 D-D2). `let Name = context { … }` at package level is a named context; `let w = context { … }` in a body is a local context, a recipe that each `with` naming it builds, used only as a `with` operand or embedded in another context literal, never with `mut` or a written type ([rows.md](rows.md#context) rule 9). A local context may reuse a package context's name, and then hides it.
11. **Tests and laws.** `test Name { … }` is an example; `test Name(x T) { … }` a property. `law Name(params) { body }` is a top-level declaration, beside `test`, or a member of an operation type (196l; law-top, 2026-11-04). A top-level law has no type parameters: `law L[T](x T) { … }` is S001 at the `[`, "a law has no type parameters; state it at concrete types, or as a member of the protocol's operation type". A generic law is a member of the protocol's operation type. A top-level declaration starts with `type`, `func`, `extern`, `test`, `law` or `let`; any other token there is S001 "expected a declaration (type, func, extern, test, law or let), found …". Tests and `assert` are **implemented** (check-v1 stage E3a); laws are checked, not run (stage E3b; law-top; [types](types.md#laws)).
    - A test declares no value: its name is in no value scope. Its body is a failure target, so a failure that escapes it fails the test, and a service it calls comes only from its own `with` (S102).
    - A top-level law declares no value either. Top-level laws and tests share one namespace in a package, because a report names each by its name alone: a second top-level law of a name is S108 "law X is declared twice", and a top-level law named like an earlier test is S108 "law X is named like test X" (the reverse order reads "test X is named like law X"), each at the second in source order. A top-level law may share its name with a function, a type or a member law.
    - `assert e` stands only in a test body or a law body, at any statement position; anywhere else it is S108. What decides is its lexical owner, the innermost test, law, function declaration or function literal around it: a function literal inside a test or a law is not that body, so an `assert` in it is S108 (a trailing block and a `with` body stay in their owner). Its operand is a `bool` (S108).
    - The test build (`lower.LowerTests`) lowers each test to a Go function and adds a runner: it prints `ok Name` or a `FAIL` line naming the failed assert's position and text, runs a property's 100 cases from a seed (`--seed N`, default 1), and exits 1 when a test fails. A case's verdict is the exit that survives its unwinding: failed asserts, with typed failures beside them, fail the test and the run goes on; any other exit, such as a defect an exit action raises after an assert failed, prints the test's `FAIL` line and stops the run with the runtime's report of every reason (exit 2). The run build, which `ergo build` and `ergo run` use, contains no Go from a test. `ergo test FILE [--seed N]` builds the test binary in test mode, a module of the emission cache apart from the run build's, and runs it with the given seed; its exit status is the runner's (0, 1, or 2 for a defect), and a refuted property prints each argument as an Ergo literal of its type, so the case can be pasted into a test ([runtime](runtime.md#tests)).
    - A failed `assert a OP b`, OP one of `==`, `!=`, `<`, `<=`, `>`, `>=` (parentheses around the comparison allowed), shows its operands after the text: `double(2) == 4: left 5, right 4`, each operand an Ergo literal of its type, by the printer of a refuted property's arguments. Each operand is evaluated once, left to right, before the comparison. When both operands are strings (or defined types over string), they differ and either holds more than one line, a line diff follows: `--- left`, `+++ right`, then each line as `  line` (in both), `- line` (left only) or `+ line` (right only), by the longest common subsequence of lines. A comparison of two constants, any other assert (`&&`, a call, a bool), and an operand whose type has no literal here (a type of another package, of std or of the prelude, an operation type, a function, a borrow, a machine) show the text alone (T3, batch 12). *Prior art:* Go's `testing` convention "got X, want Y"; ExUnit's `assert left == right` shows `left:` and `right:` with a diff; pytest rewrites `assert` to show both sides; Gleam's `should.equal` prints both values.
12. **Visibility.** See [Visibility](#visibility).

## Visibility

Status: **implemented** for rules 2 and 3 on fields, operations and construction: checked by `internal/sketch/check` (196b Q6 and Q7, [decisions](../decisions.md#decided-by-principle-2026-11-05); `TestFieldVisibility`, `TestConstructionAuthority`). Owner decision, 2026-10-27 (README Gap 8).

1. A package-level name, a record field or an operation is **exported** when its first character is an upper-case letter (Unicode category Lu). Every other name is unexported.
2. Another package may name only exported declarations, fields and operations. Outside its package, an unexported field is not read, written, named as a keyed literal's key or named in a record pattern, and an unexported operation is not called: each is S104, "units is not an exported field of lib.Q", "inner is not an exported operation of lib.Ops". An operation type with an unexported operation is implemented only by a type of its package, as in Go: another package's type is S108 "inner is an unexported operation of lib.Ops, so only a type of package lib implements it". An unexported operation's identity is its name and its package: main's `inner` is not lib's, whether it is a function, an operation of main's operation type or of a type parameter's constraint, so `func lift[T Local](v T) lib.Ops { v }` is S108 when main's `Local` declares `inner`, and `func f[T lib.Ops](v T) lib.Ops { v }` checks.
3. Outside its package, a record type with an unexported field has no composite literal, not even `Quantity{}`, no record update and no implicit zero value (S114; [types.md](types.md#zero-values-and-definite-initialisation), rule 6). It can be built there only through the functions its package exports.
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

1. **One `for`.** `for x in xs` binds the element; `for i, x in xs` the index and the element; `for i in 0..n` counts; `for cond { }` and `for { }` loop. A Map follows the same rule: `for v in m` binds each value, and `for k, v in m` the key and the value, as the index and the element of a slice; iteration order is unspecified, as in Go. A string follows it too, over its runes: `for r in s` binds each rune, and `for i, r in s` the byte offset where the rune starts and the rune, as Go's `for i, r := range s` (L4). A `Range[T]` or `Seq[T]` takes one name. There is no three-clause `for`, no `range` and no `:=`. The domains and their typing are [types.md, Loops and len](types.md#loops-and-len).
2. **Assignment** has one left side and one right side: `x = e`, `x += 1`. Its target is a `let mut` local, or a field or element reached from one; a parameter, a `let`, a pattern, `as`, `case err:` or loop binding and a package-level `let` are immutable, and so are their fields and elements (196v; check-v1 D-D1, Q2). A write through a pointer changes the pointee, not the root, and needs no `let mut`; a borrow `&x`, or a call `x.f()` of a function whose first parameter is `*T`, needs `x` rooted at a `let mut` (pointers are #222's). A `let mut` that is never reassigned is an error (S110, Q3). There is no `++` or `--` and no multiple assignment.
3. **`break` and `continue`** act on the innermost `for`, also inside a `match` arm (seventh round, item 4). There are no labels.
4. There is no bare block statement, and no init statement in an `if` or `match` header (eighth round): write `let x = f()` on the line before.
5. A `with` block and a trailing block yield their last expression. A `return` in a `with` block, a `catch` arm's included, returns from the enclosing function: its value is computed inside the block, then the `with`'s providers are released, innermost `with` first, then the function's exit actions run, last first, as at any return (rows.md, With 5; resources.md, Context ownership as built 3). `break` and `continue` that would leave a `with` block, and `return`, `break` and `continue` that would leave a trailing block, are errors (S108): a trailing block is a function literal, so its `return` would be ambiguous. *Prior art:* Go runs deferred calls at `return`; Kotlin returns from an inline `use {}` lambda; Python's `with` exits its context managers on `return`. A `func` literal's `return` returns from the literal, as in Go. A `catch` arm or a `match` arm holds statements; a `return` there leaves the enclosing function. An `else` block is a branch of its `if`, like the `if` body: a jump in it leaves what a jump in the body leaves.

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
LiteralValue   = "{" [ ( ".." Expression { "," Element } | Element { "," Element } ) [ "," ] ] "}" .
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
7. **`with`.** `with E, F { body }` provides the contexts `E` and `F` to its block, and means `with E { with F { body } }` ([rows.md](rows.md#with) rule 7); `with (W) { … }` is the same as `with W { … }`, and the formatter writes the second.
8. **Context literals.** `context [provides …] [needs …] { A: x, B, C: y, replace D: z, Shared }` builds a context: an entry binds one or more services to a provider, `replace` replaces an entry of an embedded context, and a bare name embeds a context, named or local ([rows.md](rows.md#context)). `provides` comes before `needs`. A context literal stands only as a `with` operand, after `let x =` in a body (a local context) or after `let Name =` at package level (a named context); anywhere else it is S108.
9. **Trailing blocks.** A trailing block is notation for a function-literal argument, over a restricted domain: its body may not contain `return`, `break` or `continue`. Within that domain, `f(a) { body }` is exactly `f(a, func() T { return body })`, where `T` and the literal's rows come from `f`'s final parameter type. A trailing block needs the call's parentheses (Parser rule 6). *Prior art:* Swift trailing closures and Kotlin trailing lambdas.
10. **A type is never a value.** `Primary` has no type operand. A bare type in value position, such as `func(int) int`, `escaping func()`, `[]int`, the conversion `[]byte(s)` or the map type `Map[string, int]` (Parser rule 8) with no literal braces, is S001 at its first token: "a type is not a value; write a literal or a conversion". A slice type stands in value position only as a composite literal's type (`[]int{1, 2}`), and a conversion is a call of a named type (`int64(x)`). A type argument is a type, not a value: `Map[string, []int]{}` and `Option[func() int]{}` are unchanged. The parser commits to a function literal at `func`, so an error in its signature stands where it is: `func() needs A | B[T] { … }` is S001 at the `[` (Parser rule 12). *Prior art:* Gleam has no type in expression position; rustc commits to a closure once it starts.
11. **Record update.** `T{..base, F: v}` is a composite literal of the record type `T` that copies `base` and replaces the named fields (2026-11-05, [decisions.md](../decisions.md)). The `..` and its base come first, once, and the elements after it are keyed. The literal names its type, `TypeName [ TypeArgs ]`, as every record literal does. These shapes are S001, each at its own token: a positional element after the base, a second base, a base that is not first (`T{F: v, ..base}`, Rust's order), a base in a slice or Map literal or in a literal with an elided type, and `..` in a pattern. `..` is unambiguous at the start of an element, because the range operator `..` is binary. The base's type, the fields and the order of evaluation are [types.md, Structs 3](types.md#structs). *Prior art:* Gleam `Walk(..w, turn: 1)`, MoonBit `{ ..w, turn: 1 }` and ReScript `{...w, turn: 1}` put the base first; Rust `Walk { turn: 1, ..w }` puts it last; Elm `{ w | turn = 1 }`, OCaml and F# `{ w with turn = 1 }`.

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
8. **`Map[K, V]`.** The bare name `Map` with two type arguments is the syntax of the map type, so the parser need not resolve it. Where a package declares its own type `Map` (Reserved names 2; L6), the checker reads the same tree as `Map` applied to the two arguments, the package's type; a non-generic package `Map` then refuses the arguments (S108).
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
