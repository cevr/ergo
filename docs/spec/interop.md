# Go interop: `import go`, bindings and adapters

This file is the v1 specification of Go imports ([#202](https://github.com/cevr/ergo/issues/202)). It replaces the retired [docs/interop.md](../interop.md), which stays as design reference. The plan with its owner answers (q32 to q38, 2026-11-08) is the wf10 import-go plan; the rules below are its §1 and §2. Terms (Go import, Host package, Ambient) are in the [glossary](../../GLOSSARY.md).

Status per section: **Binding files** and **Type mapping** are implemented by `ergo bind go` (`internal/gobind`, IG1), and the bindings of Go's standard library are embedded (`internal/sketch/std/gostd`). The checker reads them from IG2 on; until then `import go P` is S109 and a binding declaration in user source is S109 (#202). The lowering is IG3 and the Level 2 adapters are IG4.

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
| `[]T` | `[]T` | value rule (types.md Values); IG3 lowers the copy |
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
3. **Grammar.** The binding declarations extend `ExternDecl` ([grammar.md](grammar.md#declarations)): `"extern" "go" import_path "." identifier ( "func" [ Receiver ] … | TypeDecl | LetDecl )`. Only a binding declares them; in a user package each is S109 until #202's checking reads them.
4. **Generator.** `ergo bind go [-o DIR] PACKAGE…` runs `go list -deps -export -json` and reads the export data with `go/importer`; it is the only code that runs Go tools for signatures. With `-o` it writes `DIR/<import path>/<last element>.ergo`.
5. **Platforms.** A binding holds for darwin/arm64 and linux/amd64 with `CGO_ENABLED=0`. A declaration missing on one, or with another Go text or Ergo text there, is refused "platform-specific".
6. **Go's standard library.** The compiler embeds the bindings of every standard-library package outside `internal` and `vendor` (and outside `builtin`, `unsafe` and the cgo and sanitizer runtimes) at its toolchain `go1.N`, made by `go generate ./internal/sketch/std/gostd`. `TestGoStdBindingsMatchToolchain` regenerates them and compares. The checker reads them as files and runs no Go tool.
7. **Third-party and local packages** get committed bindings under `<module root>/ergo.bind/<import path>/` and a plain `go.mod`/`go.sum` beside `ergo.mod` (q37; IG7).

*Prior art:* Kotlin/Native's cinterop writes a klib of stubs at build time; Swift caches imported modules; Borgo commits its generated `.brg` files. A committed binding keeps `ergo check` hermetic: it reads files, as `go.sum` is read.
