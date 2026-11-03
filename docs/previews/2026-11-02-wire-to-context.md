# 2026-11-02: `wire` is spelled `context`

Labels: **Tested candidate**. This preview renames the construct that [`needs` and `provides`](2026-10-27-needs-provides.md) and [Ergo with and without Go's syntax](2026-10-28-without-go-syntax.md) spell `wire`. Both stay unchanged as the record of their day. Ticket: [#196](https://github.com/cevr/ergo/issues/196), stage 196w. Normative text: [grammar.md, Keywords](../spec/grammar.md#keywords) and [rows.md, Context](../spec/rows.md#context).

- **Tested candidate** snippets depend on branch `wf10/ctx-196w`. Commit `ab7ce760` holds the renamed lexer, parser, checker, lowering, corpus and std sources. The commands ran there as `ergo check`, `ergo fmt --check` and `ergo run`, on a scratch file, and gave the results each snippet states. The `import go` snippet ran on the commit that adds `TestImportGoKeywordPath`.

## What changed

The owner, 2026-11-02: "Also, let's rename it from wire to context. I think that's just more clear".

| Before (2026-10-28) | Now |
| --- | --- |
| `let App = wire provides Account needs Config { … }` | `let App = context provides Account needs Config { … }` |
| `with wire { Store: Mem{} } { … }` | `with context { Store: Mem{} } { … }` |
| `wire` is a keyword | `context` is a keyword; `wire` is an ordinary identifier |
| `import go context` imports Go's package | `import go gocontext context`; `import go context` is S108 |
| Glossary: Wire, Wire exposure, Inline wire, Named wire | Context, Context exposure, Inline context, Named context |

One concept now has one name. A context is what `with` installs for a block: service bindings, from a context literal or a named context, and context values such as `trace.Span` and `log.Annotate` ([runtime.md](../spec/runtime.md) G1). The term *context value* keeps its name and now reads as "a value in the context". The keyword count stays 28: `context` takes `wire`'s place.

## Decision

- **`context` is reserved, not contextual.** As a contextual word, `with context { Shared }` would read two ways: a context literal that embeds `Shared`, or a block that yields the value `Shared`. A reserved word keeps one parse.
- **Go's `context` package is imported under another name.** A keyword cannot be an import's bound name, so the path's last element clashes, and the alias rule ([grammar.md, Declarations](../spec/grammar.md#declarations) rule 1) admits an alias for a clash. `import go context` is S108, with the fix in the message. This recognises a Go habit ([#220](https://github.com/cevr/ergo/issues/220)). It is not a compatibility path, because no Ergo spelling is kept.
- **The network sense stays.** "Wire format", "on the wire", the wire key of a codec field and the wire form of a codec ([#49](https://github.com/cevr/ergo/issues/49)) are not this construct, and keep their word.
- **Internal names follow.** Syntax nodes (`ContextLit`, `ContextDecl`, `ContextEntry`), checker and lowering names, generated Go names (`context__…`), test names and diagnostic text all use the new word. Where a new name would meet an existing context-value name, the two keep distinct roles: `TestRuntime199ContextBuildProperty` (building a context) beside `TestRuntime199ContextValueProperty` (a value in the context), and the checker's `with` kinds `services` and `value`.

## Tested candidate: a named context and an inline context

`ergo check` exits 0, `ergo fmt --check` reports "already formatted", and `ergo run` prints `hello, Ada` then `hi, Bob`.

```go
type Greeter { Greet(name string) string }
type Polite { Prefix string }

func Greet(p Polite, name string) string { return p.Prefix + name }

let Live = context { Greeter: Polite{Prefix: "hello, "} }

func main() needs Console {
	with Live {
		Console.Println(Greeter.Greet("Ada"))
	}
	with context { Greeter: Polite{Prefix: "hi, "} } {
		Console.Println(Greeter.Greet("Bob"))
	}
}
```

## Tested candidate: the old spelling is a plain error

There is no rename hint (owner, [#217](https://github.com/cevr/ergo/issues/217): no compatibility while pre-release). `wire` is an ordinary identifier, so `wire { … }` parses as a composite literal of a type named `wire`. `ergo check` reports S104 "unknown type wire" at 6:12, the diagnostic any stray type name gets. `ergo fmt` rewrites the literal as `wire{Greeter: Polite{Prefix: "hello, "}}`. `TestWireIsAnIdentifier` pins `let wire = 1`.

```text
type Greeter { Greet(name string) string }
type Polite { Prefix string }

func Greet(p Polite, name string) string { return p.Prefix + name }

let Live = wire { Greeter: Polite{Prefix: "hello, "} }
```

## Tested candidate: importing Go's `context` package

`ergo check` reports S108 "context is a keyword; import the package under another name: import go gocontext context" at 1:11. With the alias, `import go gocontext context`, the keyword diagnostic goes away, and the import meets the S109 that every `import go` meets until [#202](https://github.com/cevr/ergo/issues/202) checks Go imports.

```text
import go context

func main() {}
```

## Prior art

- **Effect**: a `Context` is the typed map of services that a program runs with; a `Layer` builds one and owns the cleanup. Ergo's context is that map, and its constructors play the Layer's part.
- **Go**: `context.Context` carries deadlines, cancellation and request-scoped values down a call tree. Ergo's context values cover the same ground, and the name clash is why `import go context` needs an alias.
- **React Context** provides values to a subtree, as `with` provides a context to its block.
- **Scala 3 and Kotlin**: context parameters (`using` and `given`; Kotlin's `context(…)`) pass a value implicitly through calls, as a `needs` row is discharged by an enclosing `with`.
- **Gleam, ReScript, MoonBit**: no ambient context. Dependencies are explicit arguments (and ReScript's module functors, MoonBit's traits); React bindings in ReScript use React Context by name. Ergo keeps the dependency explicit in the `needs` row and only the binding ambient.
- **Elixir**: the process dictionary and `Logger.metadata` hold per-process values, the nearest analogue of a context value; there is no service-binding construct.

## What this preview does not claim

- No checking or building of `import go gocontext context`; Go imports are #202's, and stay S109 until it lands.
- No change to the type #237 plans for a first-class context: it is written `Context[R]`, and is still designed only.
- The old-syntax translator (`internal/migrate196`) still reads `wire`, which is its input; it writes `context`.
- Historical records (receipts, earlier previews, plans, research notes) keep the spelling they were written with.
