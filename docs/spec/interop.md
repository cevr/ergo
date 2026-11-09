# Go interop: `import go`, bindings and adapters

This file is the v1 specification of Go imports ([#202](https://github.com/cevr/ergo/issues/202)). It replaces the retired [docs/interop.md](../interop.md), which stays as design reference. The plan with its owner answers (q32 to q38, 2026-11-08) is the wf10 import-go plan; the rules below are its §1 and §2. Terms (Go import, Host package, Ambient) are in the [glossary](../../GLOSSARY.md).

Status per section: **Binding files** and **Type mapping** are implemented by `ergo bind go` (`internal/gobind`, IG1), and the bindings of Go's standard library are embedded (`internal/sketch/std/gostd`). **Checking** is implemented (IG2): the checker reads the bindings, and a binding declaration in user source stays S109 (only a binding declares one). **Lowering** is implemented (IG3): Level 1 calls are direct Go calls. **Level 2 adapters** are implemented (IG4): a user extern names a binding's function and is checked against it. **Go modules** are implemented (IG7): go.mod and go.sum beside ergo.mod, stamped bindings under ergo.bind/.

## Two levels, one mechanism

1. **Level 1.** `import go net/url` makes every exported declaration of the Go package usable, with no declaration written by the user. The checker reads the package's **binding** (below): an Ergo file that `ergo bind go` generates from go/types.
2. **Level 2.** A user `extern go P.F func name(sig)` whose written signature is an **adapter** of the Level 1 signature, checked against the binding ([grammar.md](grammar.md#declarations), Declarations 2; IG4).
3. An ordinary Ergo function over Level 1 calls maps `go.Error` to a closed union with a `catch`. No construct is added for error mapping.

*Prior art:* Borgo writes committed declaration files from Go source; Swift's Clang importer imports headers automatically and reads API notes for the facts headers lack; Gleam and ReScript bind one host function at a time with a trusted, written type. Ergo imports automatically (Level 1) and checks a written type against the binding (Level 2), so neither level is trusted.

## Which Go declarations import

| Go declaration | Ergo form |
| --- | --- |
| exported function | `extern go P.F func F(sig)`; a call `url.QueryEscape(s)` |
| generic function, each constraint `any`, `comparable`, `cmp.Ordered` or a core type `~[]E` or `~map[K]V` | `func F[E ordered](…)`; a parameter with a core-type constraint is replaced by its core type (`slices.Sort[S ~[]E, E cmp.Ordered](x S)` is `func Sort[E ordered](x []E)`). Any other constraint is refused. An unconstrained type parameter before a constrained one is refused, because Ergo's `[A, B comparable]` constrains both. |
| variadic function `...T` | the parameter is `[]T` and the binding marks the function `// ergo:variadic`; the lowering spreads it (`xs...`) |
| method of a named non-interface type, promoted ones included | `extern go P.T.M func (r T) M(sig)` when the method is in the method set of `T`, `(r *T)` when only `*T` has it |
| exported named type | `extern go P.T type T …` (Type mapping) |
| constant | `extern go P.C let C T`: a typed constant keeps its type; an untyped one takes Go's default type when its value fits (`math.Pi` is `float64`), and is refused otherwise (`math.MaxUint64`): Ergo has no untyped constants (196u) |
| variable of type `error` (`io.EOF`) | `extern go P.V let V go.Error`, a sentinel for `go.Is` (IG5) |
| any other variable | refused: "a Go variable is host state; call a function" |
| generic named type | refused, except `iter.Seq[T]`, which is the prelude `Seq[T]` |
| alias declaration | refused; a use of the alias maps the type it stands for |
| unexported declaration, a package under `internal` or `vendor`, `unsafe`, `builtin`, `C` | not bound |

A declaration that does not map is listed in the binding with its reason, so the diagnostic comes at its **use**, never at the import: one unmappable function does not block its package.

## Type mapping

| Go type | Ergo type | Rule |
| --- | --- | --- |
| `bool`, `string`, the integer, float and complex types, `byte`, `rune` | the same basic type | direct |
| `time.Duration` | `duration` | direct |
| `uintptr`, `unsafe.Pointer` | none | refused |
| named type defined over a basic type | the host type, nominal | `extern go io/fs.FileMode type FileMode uint32` |
| `[]T` | `[]T` | value rule (types.md Values): copied at the call unless fresh or dead (Lowering 2-3) |
| `map[K]V` | `Map[K, V]` | as `[]T` |
| `[N]T` | none | refused: Ergo has no fixed arrays ([grammar.md](grammar.md), Old spellings) |
| `*S`, `S` a named non-interface type, as a parameter | `*S` | a borrow (types.md Borrows); a `go.Ref[S]` is accepted too (q38) |
| `*S` as the one result beside a last `error` | `go.Ref[S]` | a nil result with a nil error is a defect "Go returned nil without an error" (IG3) |
| `*S` as another result or a field | `Option[go.Ref[S]]` | nil is `None` |
| `*S` in an element, a Map value or a callback result; a pointer to an unnamed type or an interface | none | refused: a pointer there may be nil, and v1 maps a pointer only as a parameter, a result or a field |
| named struct `S` | `extern go P.S type S { Field T, … }` | the exported fields of mappable type, **read only** (q34); a field that does not map is listed refused |
| named interface `I` | `extern go P.I type I { M(sig), … }`, an operation type | its exported methods; an interface with no exported method, an empty one and a constraint interface are refused |
| `error` | `go.Error` | the native error crosses unchanged |
| last result `error` | the row `fails go.Error` | the other results are dropped on failure |
| `any`, `interface{}`, an unnamed interface or struct | none | refused: "Ergo has no any" |
| `func(…) …` parameter, or a parameter of a named Go function type (`fs.WalkDirFunc`) | an Ergo function type without parameter names, of the underlying signature | a callback with one result at most: the call's rows when synchronous, else `escaping` ([Callbacks](#callbacks)) |
| `func` result, field or element; a function inside a callback | none | refused |
| `chan T` | none | refused: "Ergo has no channels (196c); wrap the Go channel in a Go function of your module, or use std queue" |
| several results without `error`, `(T, bool)` included | a tuple | `(T, bool)` stays a tuple: `strings.CutPrefix` returns a meaningful value when the flag is false |
| named type defined over a function, a channel, an array or a pointer | none | refused |

A Go pointer is never Ergo's `*T` outside a parameter: `*T` is a borrow everywhere, Go types included, and a stored or returned Go pointer is the distinct handle type `go.Ref[T]`, opaque and never nil (owner q32 and q38: "consistency is important").

## Purity and authority

Each call's row comes from the reviewed tables of `runtime/v1`, never from the Go signature:

| tier | which Go | row |
| --- | --- | --- |
| reviewed pure | a package of `v1.PureImports`, minus the members of `v1.PureImportsExcept` | none; transparent in footprints (q35) |
| foreign | every other Go function, method and interface method | `needs go.Foreign` |

`go.Foreign` is given only by the entry, `func main() needs … | go.Foreign`; a test never gets it (q33). No user source asserts a trust fact in v1 (q36).

## Binding files

1. **Form.** A binding is an Ergo source file. Its first line is `// Code generated by ergo bind go; DO NOT EDIT.`, its second `// ergo:bind <import path> <go1.N> <platforms>`. Then the imports of the Go packages its signatures name, by their Go package name: `import go io`, and an alias when the package name is a keyword or taken (`import go gocontext context`).
2. **Entries.** Each exported Go declaration is one entry, in name order, each type followed by its methods. An entry starts with `// ergo:go <Go declaration>`. A mapped entry follows with `// ergo:variadic` when the function is variadic, then its declaration: `extern go <target> func …`, `extern go <target> func (r T) M…`, `extern go <target> type T …` or `extern go <target> let C T`. A refused entry follows with `// ergo:refused <Name>: <reason>`; `<Name>` is `T.M` for a method or a member. These comment lines are a fixed form the checker and `ergo inspect` read.
3. **Grammar.** The binding declarations extend `ExternDecl` ([grammar.md](grammar.md#declarations)): `"extern" "go" import_path "." identifier ( "func" [ "(" identifier Type ")" ] … | TypeDecl | LetDecl )`. Only a binding declares them; in a user package each is S109 until #202's checking reads them.
4. **Generator.** `ergo bind go [-o DIR] PACKAGE…` runs `go list -deps -export -json` and reads the export data with `go/importer`; it is the only code that runs Go tools for signatures. With `-o` it writes `DIR/<import path>/<last element>.ergo`. Inside an Ergo module, without `-o`, it writes them under the module's `ergo.bind/` (Go modules 2); outside one, it prints them.
5. **Platforms.** A binding holds for darwin/arm64 and linux/amd64 with `CGO_ENABLED=0`. A declaration missing on one, or with another Go text or Ergo text there, is refused "platform-specific".
6. **Go's standard library.** The compiler embeds the bindings of every standard-library package outside `internal` and `vendor` (and outside `builtin`, `unsafe` and the cgo and sanitizer runtimes) at its toolchain `go1.N`, made by `go generate ./internal/sketch/std/gostd`. `TestGoStdBindingsMatchToolchain` regenerates them and compares. The checker reads them as files and runs no Go tool.
7. **Third-party and local packages** get committed bindings under `<module root>/ergo.bind/<import path>/` and a plain `go.mod`/`go.sum` beside `ergo.mod` (q37; IG7; see Go modules below). Such a binding has a third line, `// ergo:module <module> <version>`, with ` => <replacement> [<version>]` when go.mod replaces the module, or `// ergo:module <module>` for a package of go.mod's own module.

8. **Closed.** A declaration that names a refused type, of its package or another, is refused too ("it names the refused type syscall.RawConn"); a struct field or an interface method that names one is a refused member, and the type keeps its other members. A use of a prelude type (`Map`, `Seq`, `Option`) is refused in a package that declares a type of that name, which would hide it (`go/types.Map`). So every binding checks clean; `TestGoStdBindingsCheckClean` resolves every declaration of every embedded binding.

## Checking

1. **Import.** `import go P` reads P's binding: Go's standard library's (embedded; a standard-library path always names it), else the module's own at `ergo.bind/<P>/<last element>.ergo`. A path follows Go's rule (`module.CheckImportPath`, #231): `github.com/0xAX/x` is a path. No binding is S104 at the path, with the `ergo bind go` command. The name the import binds is the Go package name: the last element, or the one before a major-version element (`math/rand/v2` binds `rand`). One program shares one set of Go packages, so a host type has one identity.
2. **Lazily, per declaration.** The import reads nothing. The first use of a member parses the binding and resolves that member only; a host type's underlying type resolves when it is first read. A program that imports no Go package reads no binding. The checker never runs a Go tool.
3. **Uses.** A call, a constant, a type, a method (Go's method set) and a read-only field check by the binding's declaration. A method in the method set of `*T` only is called on a `go.Ref[T]` or a borrow, not on a copy (S109). `go.Ref[T]` reaches T's fields and methods; a borrow `*T` is accepted where Go takes `*T`, and is never kept as a `go.Ref` (S108). A Go interface is a host type, not a capability. A declaration the binding refuses is S109 at its use, with the binding's reason.
4. **Rows.** The binding's rows are the call's: `fails go.Error` when Go's last result is `error`, `needs go.Foreign` outside the reviewed pure list (Purity and authority). A field read through a `go.Ref` or a borrow charges `go.Foreign`. `go.Foreign` is inferred for an unexported function and written by an exported one (S102); only the entry provides it ([rows.md](rows.md), Entry 4), so a test never calls foreign Go.
5. **A wrong binding.** A binding that does not check (an edited or stale file) is S108 at the import, with the first diagnostic and the command that regenerates it.

*Prior art:* Kotlin/Native's cinterop writes a klib of stubs at build time; Swift caches imported modules; Borgo commits its generated `.brg` files. A committed binding keeps `ergo check` hermetic: it reads files, as `go.sum` is read.

## Lowering

1. **Direct calls.** A call of a Go function, a method (of a Go struct through a value, a `go.Ref` or a borrow, or of a Go interface) and a field read lower to Go's own: `url.Parse(raw)`, `u.Hostname()`, `u.Path`, with Go's import in the emitted file (named apart when its name clashes). No environment, no wrapper and no poll: a Go call is not an interruption point ([runtime.md](runtime.md)). `go.Ref[T]` is Go's `*T`; a Go value passed where a Go interface is expected is Go's implicit conversion. `go.Foreign` has no operation, so the environment holds nothing for it. A call into the reviewed pure tier that takes no function is transparent in footprints (owner q35, reversing [stdlib.md](stdlib.md) Pure bodies 3 for `import go`): a function that makes only such calls takes no environment and polls nothing, as hand Go; a user extern stays opaque.
2. **Values in.** An argument (or a receiver) that is a Map or a slice, or a Go type over one, goes where Go may write or keep it: it is copied unless it is fresh (a literal, a call's result) or a local dead after the call, the value rule of [types.md](types.md) (q26). `sort.Ints(xs)` on a live `xs` sorts a copy; on a dead one it sorts in place, as hand Go does. The copy-always lowering is kept as the reference (`lower.SetHostCopyAlwaysForTest`), and a seeded property holds the two equal to the value model (`TestHostCopyElisionAgrees`).
3. **Values out.** A Map or slice result is copied unless the reviewed table `freshGoResults` (`lower/gocall.go`) lists the function as returning new memory (`strings.Split`, `regexp.Regexp.FindAllString`); a result such as `regexp.Regexp.SubexpNames`, which shares Go's state, is copied, so a write to it never reaches Go. Several Go results are a tuple; a last `error` is `fails go.Error` (`v1.GoErrorOf`, as G4 lowers an extern).
4. **Nil.** A nil pointer or interface whose Ergo type is `Option[T]` is `None`, else `Some`. A `go.Ref[S]` result that is nil with a nil error is a defect at the call, `Go returned nil without an error at <file:line:col>: <function>` (`v1.GoNilDefect`): Go's convention that a nil error comes with a value is checked, not assumed.
5. **Variadic.** The last argument of a `// ergo:variadic` function is spread: `filepath.Join(parts...)`.
6. **Panics.** A Go panic in an imported call is a defect; the call's `//line` directive names the Ergo call site in the report, and the task's existing recover catches it, with no recover per call.
7. **Callbacks** are lowered as [Callbacks](#callbacks) says.
8. **Not yet.** A Go function used as a value is a later unit.

*Prior art:* Gleam's `@external` and ReScript's `@module` externals are direct calls with no adapter, as here when no Map or slice crosses shared; Kotlin/Native's cinterop and Swift's Clang importer map a nullable pointer to an optional the same way; Swift copies an `Array` passed to C only when its buffer is shared, as the value rule copies a live operand.

## Go modules

Implemented (IG7, owner q37). A module imports Go packages outside Go's standard library through the go command's own files and committed bindings; `ergo check` reads files only.

1. **Layout.** Beside `ergo.mod` stand a plain `go.mod` and `go.sum`, managed with the go command (`go mod init`, `go get example.com/x@v1.4.0`, `go mod edit -replace`). `ergo.mod` keeps its one line. The bindings are committed under `ergo.bind/<import path>/<last element>.ergo`. A folder name with a `.` is no Ergo import path, so `ergo.bind` never loads as a package; `ergo fmt` of a folder and the examples runner skip it. `go mod tidy` sees no Go import in an Ergo module and drops the requirements: do not run it there.
2. **Binding.** Inside a module, `ergo bind go PACKAGE…` runs `go list` in the module root with `-mod=readonly` (it never edits go.mod or go.sum), writes each binding under `ergo.bind/`, and also writes the binding of every package outside the standard library that a written binding names, so the set checks on its own. A standard-library package, a `main` package and an `internal` or `vendor` package are refused: the first is embedded, the second is a program, and Go admits the others only inside the tree that holds them, which the emitted program never is.
3. **Stamp.** Each module binding carries the stamp of its Go module (Binding files 7), as `go list` reports it for the go.mod where it ran. The loader computes the same stamp from go.mod (the required version, then the replacement of that exact version, else of every version) and compares. A missing go.mod, a module go.mod does not require, or another stamp is S104 at the import: "the binding of Go package "example.com/x" is stale: it was made for example.com/x v1.4.0, and go.mod now names example.com/x v1.5.0; run ergo bind go example.com/x". `import go` of a path with an `internal` or `vendor` element is S109 at the import.
4. **Emitted module.** The emitted go.mod requires and replaces what go.mod requires and replaces, each local folder made absolute, and go.mod's own module, replaced by the module root; go.sum is copied as it is. go.mod, go.sum and every module binding read are inputs of the build key. go build checks each emitted call at its `//line`: a binding edited to lie (another target, another type) checks, and fails the build at the Ergo line of the call.
5. **Proof.** `TestGoModuleImports` (cmd/ergo) binds, checks and runs a module that imports a Go module replaced by a local folder and one required from a `file://` module proxy (no network), and shows the stale, missing and internal diagnostics with only a stub `go` on PATH that would mark a start. `TestGoModMatchesGo` holds the loader's reading of go.mod and its stamps to `go mod edit -json` on seeded go.mod texts.

Prior art: Borgo commits generated declaration files per package (`std/`) and regenerates them with a script; Kotlin/Native's cinterop writes a klib at build time; Swift caches precompiled Clang modules keyed by the headers. None checks the summary against the dependency's version; Ergo's stamp does, as go.sum checks a module's content.

## Go errors

`go.Error { Text string }` keeps the Go error it was made from (IG5, #202: "with `errors.Is` and `errors.As` kept").

1. **Made.** A Go call whose last result is a non-nil `error` fails with the `go.Error` of that error; a typed nil (a nil `*T` in a non-nil `error`) is an error, as Go's `err != nil` says. A binding's error variable (`io.EOF`, `fs.ErrNotExist`) is the `go.Error` of Go's variable. A Go `error` read from a field or a non-last result is a `go.Error` too (`go.Error{}` when nil). `go.Error{Text: s}` written in Ergo makes a Go error of its own whose text is `s`; two of the same text are `go.Is` of each other.
2. **Text** is the Go error's `Error()` when the field is read, never a copy taken at the failure: a failure no code prints costs what hand Go's `return err` costs. `Text` is read-only (S108 "cannot assign"); `go.Error{..e, Text: s}` makes a new one.
3. **Passed to Go.** A `go.Error` argument for a Go `error` parameter (and each element of a `[]go.Error` for `...error`) is Go's own error, so `errors.Join` and Go code see the chain Go made; `go.Error{}` is `nil`.
4. **`go.Is(e go.Error, target go.Error) bool`** is `errors.Is` on the two Go errors: `go.Is(e, fs.ErrNotExist)`.
5. **`go.As[T](e go.Error) Option[T]`** is `errors.As`: `Some` of the first error in the chain that is a `T`. `T` is a type of a Go binding whose Go method set has `Error() string` (a value type whose method has a value receiver, or a `go.Ref[S]`), or a Go interface; any other `T` is S108 at the call, because `errors.As` panics on it. `go.As[go.Ref[fs.PathError]](e)`; `go.As[strconv.NumError](e)` is S108 ("implements error through a pointer receiver; write go.Ref[strconv.NumError]").
6. **Equality.** `==` on `go.Error` compares `Text` (G4's rule, kept). Go's `==` on errors compares identity and panics on an error type Go cannot compare, so `go.Error` is equatable but not comparable: it is no `Map` key and does not satisfy `comparable` (S108).
7. **Lowering.** `v1.GoError` holds the Go error; `Text` lowers to the method `Text()`, `go.Is` to `errors.Is`, `go.As[T]` to `errors.As` into a `T` and an `Option`. The eager form (the text computed at the failure, G4's) is kept as the reference lowering (`lower.SetGoErrorEagerForTest`); `TestGoErrorChainsAgree` runs generated chains under both against Go's own `errors` package, and `runtime/v1`'s `TestGoErrorChainsModel` covers chains only a Go package can build (custom `Unwrap`, a typed nil).

*Prior art:* Go's own `errors.Is`/`errors.As` are the semantics. Swift's `NSError **` convention keeps the `NSError` object behind `throws`, and Swift code casts it (`as? URLError`) as `go.As` does; Kotlin/JVM keeps the Java exception object and `is`-checks it. Gleam and Elixir keep the host's error term as data (`{error, enoent}`); ReScript's `Js.Exn.Error` keeps the JS exception. Effect's `Cause` keeps the original defect object for the same reason: a typed failure that drops the host value cannot be asked about it later.

## Callbacks

A Go parameter of a function type takes an Ergo function value (IG6, #202). One rule, Ergo's row generics (q39), covers what Go code between the call and the callback cannot carry by itself: a failure and a capability.

1. **Synchronous.** The reviewed table `v1.GoSyncCallbacks` (`runtime/v1/gocallback.go`) lists the Go functions and methods that call each callback only during the call, on the caller's goroutine, and keep none (`strings.Map`, `slices.SortFunc`, `path/filepath.WalkDir`, `regexp.Regexp.ReplaceAllStringFunc`, `sync.Once.Do`, …); trust facts come only from reviewed tables (owner q36). A listed function's binding takes row parameters `CE` and `CR`: its callback needs `CR`, and fails `CE` when Go's callback returns an error that the call returns, and the call fails `CE | go.Error` and needs `CR`: `extern go path/filepath.WalkDir func WalkDir[CE, CR](root string, fn func(string, fs.DirEntry, go.Error) fails CE needs CR) fails CE | go.Error needs CR | go.Foreign`. A callback literal fails `Hit` and reads `Tag`, so the call fails `Hit | go.Error` and needs `Tag`. A callback whose Go type returns an error the call does not return fails `go.Error`. A method takes no row parameters in v1: its synchronous callback needs nothing and fails `go.Error` when Go's returns an error.
2. **Escaping.** Any other callback may run after the call, on any goroutine: its type is `escaping func(…)`, with no `needs` row, so it reads no capability (nothing `with`-bound escapes: [rows.md](rows.md), Footprints 5; a use is S102), and fails `go.Error` at most, Go's own error: `extern go time.AfterFunc func AfterFunc(d duration, f escaping func()) …`.
3. **Shape.** A callback has one result at most; a pointer result is refused (Go may return nil); a Go error parameter is a `go.Error`. Gobind writes a `fails` row exactly when Go's callback returns an error, so the lowering reads Go's function type from the binding.
4. **Lowering.** The argument becomes a Go function literal of Go's exact type (`func(string, fs.DirEntry, error) error`), bound once before the call: it calls the Ergo value in the environment of the code that makes the call, so the callback runs on its caller's task with its caller's capabilities, as hand Go's closure would. Go's arguments become Ergo values (a Go error the `go.Error` that keeps it; a slice or a Map a copy), and the result Go's (a `go.Error` its native error; a slice or a Map a copy). A defect in the callback is a Go panic that unwinds through the Go function to the task's recover, a defect of the outer call.
5. **Failures through Go.** A callback's `go.Error` failure is Go's own error (`v1.GoCallbackError`), so Go acts on it: `fs.SkipDir` skips, `fs.SkipAll` stops. Any other failure is wrapped in `*v1.GoTunnel` and returned to Go, and the outer call's error is `v1.GoUntunnel`: the tunnelled Ergo failure, typed again, when Go's error chain holds one (Go returns the callback's error as it is, or wrapped), else the `go.Error`. The eager go.Error reference has `v1.GoUntunnelEager`. `TestCallbackTunnelModel` runs generated walks of `filepath.WalkDir` and `filepath.Walk` over generated trees under both go.Error lowerings against Go's walker with a Go callback.

*Prior art:* Swift's `rethrows` is this rule: a function that takes a throwing closure throws exactly when the closure does, and a closure parameter is non-escaping unless written `@escaping`, which Ergo's `escaping` follows; Kotlin's `inline` lambdas may return from and throw through the caller, while `crossinline`/non-inline ones may not. Effect's `Effect.forEach` gives the callback's `E` and `R` to the whole call, as `CE` and `CR` do. Gleam and ReScript pass a host closure straight through, with no adapter, and leave a failure inside it to the host's exceptions; the tunnel is the typed form of that: an exception that carries the Ergo failure through the host frames and is caught at the call.

## Level 2 adapters

A user `extern go P.F func name(sig)` names a function of P's binding, and its written signature must be one adapter of the Level 1 signature the binding gives (IG4). The checker reads the binding and runs no Go tool, so an adapter is checked, not trusted ([grammar.md](grammar.md#declarations), Declarations 2):

| written | Level 1 (Go) | lowering |
| --- | --- | --- |
| the Level 1 signature, any names | any | as an `import go` call |
| `T` | `Option[T]`: a pointer or interface result that may be nil, with no error | a nil result is the defect `v1.GoNilDefect` naming the target and the Ergo call |
| `Option[T]` | `(T, bool)` | `Some(v)` when Go's bool is true, else `None` |
| `(R…, Option[go.Error])`, no `fails` row | `R… fails go.Error` (`(R…, error)`) | Go's values are kept beside the error; the error is `Some(go.Error)` when non-nil; refused when an `R` is a `go.Ref` |

1. **Parameters** are Level 1's, of identical types. A generic target binds its type parameters from the written parameters, as Go infers them, and each must satisfy its constraint; a written type parameter is refused.
2. **Rows.** `needs go.Foreign` exactly when the binding has it (the foreign tier); none on the reviewed pure tier. A target that returns an error writes `fails go.Error` or the Partial adapter. Adapters do not compose.
3. **Trust** facts no adapter can check (purity, a synchronous callback, a read-only slice argument, a fresh result) come only from reviewed tables in `runtime/v1` (owner q36); user source states none.
4. **Lowering.** An extern whose adapter is the Level 1 signature over predeclared basic types and G1's callbacks lowers as before, a direct call that go build also checks, and may be a function value. Any other is call-only and lowers by the rules of Lowering above (the copy rule, the spread, Go's results bound), then the adapter's check. An extern stays opaque in footprints, as before (stdlib.md, Pure bodies 3).

*Prior art:* Swift's API notes change an imported C signature (nullability, the `NSError **` convention, names) without touching the header, and the compiler applies them at the call; Kotlin's platform types are the opposite choice, an unchecked `T!`. Gleam and ReScript externals trust their written types; Borgo maps `(T, bool)` to `Option<T>` for every function, which loses `strings.CutPrefix`'s `after` when not found, so Ergo makes it an opt-in adapter.
