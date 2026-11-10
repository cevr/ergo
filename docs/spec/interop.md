# Go interop: `import go`, bindings and adapters

This file is the v1 specification of Go imports ([#202](https://github.com/cevr/ergo/issues/202)). It replaces the retired [docs/interop.md](../interop.md), which stays as design reference. The plan with its owner answers (q32 to q38, 2026-11-08) is the wf10 import-go plan; the rules below are its §1 and §2. Terms (Go import, Host package, Ambient) are in the [glossary](../../GLOSSARY.md).

Status per section: **Binding files** and **Type mapping** are implemented by `ergo bind go` (`internal/gobind`, IG1), and the bindings of Go's standard library are embedded (`internal/sketch/std/gostd`). **Checking** is implemented (IG2): the checker reads the bindings, and a binding declaration in user source stays S109 (only a binding declares one). **Lowering** is implemented (IG3): Level 1 calls are direct Go calls. The Level 2 adapters are IG4.

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
| `func(…) …` parameter | an Ergo function type without parameter names | a callback (IG6 adds rows and escape facts) |
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
4. **Generator.** `ergo bind go [-o DIR] PACKAGE…` runs `go list -deps -export -json` and reads the export data with `go/importer`; it is the only code that runs Go tools for signatures. With `-o` it writes `DIR/<import path>/<last element>.ergo`.
5. **Platforms.** A binding holds for darwin/arm64 and linux/amd64 with `CGO_ENABLED=0`. A declaration missing on one, or with another Go text or Ergo text there, is refused "platform-specific".
6. **Go's standard library.** The compiler embeds the bindings of every standard-library package outside `internal` and `vendor` (and outside `builtin`, `unsafe` and the cgo and sanitizer runtimes) at its toolchain `go1.N`, made by `go generate ./internal/sketch/std/gostd`. `TestGoStdBindingsMatchToolchain` regenerates them and compares. The checker reads them as files and runs no Go tool.
7. **Third-party and local packages** get committed bindings under `<module root>/ergo.bind/<import path>/` and a plain `go.mod`/`go.sum` beside `ergo.mod` (q37; IG7).

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
3. **Values out.** A Map or slice result is copied unless the reviewed table `freshGoResults` (`lower/gocall.go`) lists the function as returning new memory (`strings.Split`, `regexp.Regexp.FindAllString`); a result such as `regexp.Regexp.SubexpNames`, which shares Go's state, is copied, so a write to it never reaches Go. A result that is itself an operand of another Go call (a receiver, as `w.Header()` in `w.Header().Set(k, v)`, or an argument) is never an Ergo value, so it is not copied on either side: Go's own map reaches the Go method, and its write reaches the map Go keeps (`http.ResponseWriter.Header`), as in hand Go; a copy there would lose the write with no message (G2, H4; `TestImportGoHeaderWriteReachesGo`, in process with `httptest`). Both lowerings share it: it is the rule, not an elision. *Prior art:* a Gleam external's foreign value is opaque and never copied, and ReScript's bindings pass the shared JS reference at no cost; Elixir copies at a NIF or port boundary only where the program asks. A Go result bound to a local (`let h = w.Header()`) is an Ergo value and is copied by this rule. Several Go results are a tuple; a last `error` is `fails go.Error` (`v1.GoErrorOf`, as G4 lowers an extern).
4. **Nil.** A nil pointer or interface whose Ergo type is `Option[T]` is `None`, else `Some`. A `go.Ref[S]` result that is nil with a nil error is a defect at the call, `Go returned nil without an error at <file:line:col>: <function>` (`v1.GoNilDefect`): Go's convention that a nil error comes with a value is checked, not assumed.
5. **Variadic.** The last argument of a `// ergo:variadic` function is spread: `filepath.Join(parts...)`.
6. **Panics.** A Go panic in an imported call is a defect; the call's `//line` directive names the Ergo call site in the report, and the task's existing recover catches it, with no recover per call.
7. **Not yet.** A Go function used as a value, and a callback parameter of import go, are IG6; an `import go` of a module's own Go package is IG7 (the lowering imports Go's standard library only).

*Prior art:* Gleam's `@external` and ReScript's `@module` externals are direct calls with no adapter, as here when no Map or slice crosses shared; Kotlin/Native's cinterop and Swift's Clang importer map a nullable pointer to an optional the same way; Swift copies an `Array` passed to C only when its buffer is shared, as the value rule copies a live operand.
