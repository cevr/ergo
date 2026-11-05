# Decisions

This file is the dated log of the owner's decisions for Ergo, newest first. A decision applies the principles to one feature, construct, stage or tool; [NORTH_STAR.md](../NORTH_STAR.md) holds the principles: what Ergo is for, the pillars, the north stars, the tiebreaks, the owner rules and the rejected directions. Each entry keeps the owner's words, the decision, and its tickets and links as NORTH_STAR.md recorded them; only relative link targets changed, so that they resolve from `docs/`. An entry with two or more dates sorts by its latest date, and entries with the same date keep their order from NORTH_STAR.md. Entries with no date are in the [Undated](#undated) group at the end. Where a moved passage says "above" or "below" and the new order changes what it means, a bracketed note names the entry.

## Decided by principle, 2026-11-05

- **Record update is `T{..base, F: v}` (2026-11-05).** Decided by principle under the owner's "dont block on me" (2026-10-28), for the sketch gap "no record update syntax" (examples/tidewalk-replay rebuilt five fields of `Walk` in four arms to change one; a full rebuild also resets a field added later). Recorded on [#196](https://github.com/cevr/ergo/issues/196). Rules: [grammar.md](spec/grammar.md#expressions) Expressions 11 and [types.md](spec/types.md#structs) Structs 3.
  - Form: a composite literal of a record type whose first element is `..` and the base, then one or more keyed fields: `Walk{..w, Turn: w.Turn + 1}`.
  - The base has exactly the literal's type, with its type arguments. A union that has the type as a variant is S108 at the base: match it first. `Walk{..w}` is S108: the copy is `w` itself (one form per job).
  - The fields follow the keyed-literal rules. A positional element, a second base, a base that is not first, a base in a slice or Map literal and `..` in a pattern are S001.
  - The base is evaluated once, first, then the field values in source order. The result is a copy; the base is unchanged. A slice or Map field shares its backing with the base's, as every record copy does in v1 (#222, #197).
  - The update is a composite literal of its type, so every construction rule for that type applies to it unchanged.
  - Laws: `T{..b, F: x}.F == x`; every other field equals `b`'s; `T{..T{..b, F: x}, G: y} == T{..b, F: x, G: y}` for distinct fields; for one field the later update wins.
  - It lowers to a copy and one store per field, with no allocation.

  Principles: algebraic data (a machine steps to a new state by an expression); Go idioms beat novel notation, with the departure recorded (Go has no update expression because its values are mutable by default, and Ergo's `let` is immutable by default, 196v); visually distinct constructs (literal syntax, no keyword, no dot form); composable primitives (an update nests, by the laws). Rejected: `{ m with x }` (OCaml, F#) reuses `with` with a new meaning; `{ m | x }` (Elm) overloads `|`, which Ergo uses for rows and unions; `m.copy(x: …)` (Kotlin) is a dot form and needs named arguments; base-last `T{x: 1, ..m}` (Rust) reads against the order of evaluation. Prior art: Gleam `Walk(..w, turn: 1)` and MoonBit `{ ..w, turn: 1 }` put the base first with `..`; ReScript `{...w, turn: 1}`; Elixir `%Walk{w | turn: 1}`.

## Owner direction, 2026-11-04

- **A defect stops the test run.** Orchestrator ruling for lower-v1 stage 4 (Q-S4-3), on the owner's decided-by-principle delegation: the written test rule ([grammar](spec/grammar.md) Declarations 11) stands, and it overrides the sentence of lower-v1 spec D8 that a test's defect is caught at the test boundary so that another test can run. A failed assert, a typed failure that escapes a test and a refuted property fail their test, and the run goes on. A defect fails its test and stops the run with exit 2. Principle: after a defect, the state of the shared process is not trusted. `ergo test` exits 0 when every test passes, 1 when a test fails, and 2 for a defect or an invalid invocation; the output tells the last two apart (a defect prints its test's `FAIL` line and the runtime's report). Prior art: Go's test binary stops at a panic; Elixir's ExUnit goes on, because each test has its own process, which Ergo tests do not have.
- **Extensions are Luau first, after v1.** Owner, asked how a program like Gent would get extensions in Ergo: "What I'd like would be something like luau - typed". Then: "the extensions also meant to be dynamic, they're meant to kind of be self-updating, meant to be durable, they're meant to be, you know, hot, reloadable. They're meant to have UI component or TUI components as well as back-end capabilities". And: "we can do gent after v1 mainly just because of its you know Lua requirements". Design, decided by principle ([#241](https://github.com/cevr/ergo/issues/241)):
  - Each extension gets one in-process Luau VM, loaded through the C binding generator (bind-c) and purego.
  - The host API is declared once as Ergo operation types. Ergo generates the `.d.luau` declarations from them, and Luau's own checker checks each extension at load. A failed load keeps the last good version.
  - An extension's `needs` row is exactly the set of host globals in its VM.
  - Durable state lives in a store that the host keeps, so a hot reload or a self-update keeps it.
  - UI is view data that the host renders.

  Prior art: Roblox's Luau embedding, Neovim's Lua plugins with generated API types, Erlang and Elixir hot code loading.
- **Laws stand at top level.** Owner: "Why do we have certain types just for laws? ergonomic failure?" The answer: yes, a gap. 196l made laws members of operation types only, and a record cannot hold a law, so every probe that stated a law about plain functions made an empty wrapper type (`OrderRules`, `ViewLaws`, `BoardLaws`), and one sketch fell back to an unrun `func LawMoveConservesMoney`. Decided by principle (one construct, and its placement sets the quantifier; constructs are visually distinct; no compatibility while pre-release), and built in check-v1 law-top:
  - `law X(params) { … }` is also a top-level declaration, beside `test`. It is quantified over its parameters only. It has no type parameters (S001): a generic law, such as log replay or delta equals recompute, belongs once on the library protocol, as a member of its operation type.
  - A law in an operation type stays, quantified over every provider of its type. A law in a machine stays designed (2026-11-03 (b), [#57](https://github.com/cevr/ergo/issues/57)), quantified over the step graph.
  - A type with laws and no operations is S108 once, at its name: "T has laws and no operations; write it as a top-level law: law X(…) { … }". Its laws are still checked, so their errors show. No shim keeps the old form.
  - A top-level law takes the checker rules of a member law, minus the operation scope: its scope is its parameters, then the package. Top-level laws and tests share one namespace, because a report names each by its name alone. A top-level law has no owning type in the checker's data (`Law.Owner` is nil).
  - It is checked, not run: [#162](https://github.com/cevr/ergo/issues/162) runs a top-level law as a fuzz target over its parameters, and a member law against every provider. No build contains Go from a law ([types](spec/types.md#laws) rule 11).

  Prior art: Haskell QuickCheck states `prop_x` at top level and class laws beside the class; Lean, Coq/Rocq, Idris and Dafny state `theorem` and `lemma` at top level and put protocol laws in a class or structure; Elixir StreamData's `property` sits beside ExUnit's `test` at the top of a test module, compiled only under `mix test`; MoonBit uses top-level `test` blocks with a quickcheck library; Gleam (`qcheck`) and ReScript (fast-check) have property tests as library functions and no law construct; Fai checks `example:` contracts beside definitions. Zig, Go and Rust keep their tests out of the normal build, as Ergo keeps laws out of both builds.
- **Self-hosting after v1 is the speed test.** Owner, asked what self-hosting would take: "we can do that post-V1. I think self-hosting is probably a good litmus test to see how fast we are. The goal is to be 1x or below handwritten optimized Go code". The compiler (about 42,000 lines of Go today) is ported in stages: `format`, then `syntax`, then `check` and `lower`. Each stage goes on only when its output is byte-identical to the Go compiler's on the conformance corpus and its benchmark ratio against the Go version is at most 1.0. A three-stage build closes the port, and the compiler's own generated Go is the bootstrap seed. Prerequisites: packages, Go interop ([#202](https://github.com/cevr/ergo/issues/202)), predictable map and slice aliasing ([#222](https://github.com/cevr/ergo/issues/222)), record update and `let … else`. Ticket: [#242](https://github.com/cevr/ergo/issues/242). Prior art: Go 1.5 translated its C compiler to Go; TypeScript left self-hosting for Go for about a 10x speedup; Elixir bootstraps from Erlang; Gleam, ReScript, Elm and PureScript stay on their host language.
- **Speed levers keep the guarantees.** Owner: "Ideally, we would be able to kind of take advantage of compiler tricks and hints that we can give to make certain operations even faster - And for the rest, we should be able to kind of provide levers that increase speed while maintaining the simplicity and the capabilities and the guarantees that Ergo's North Stars provide". Decided by principle (pillar 7 and the tiebreak **Contract guardrails** beat **zero-cost slogans**): a lever changes speed, never meaning. It is admitted in one of three forms: proved by the checker from facts it already has (exhaustive unions, unique ownership, purity, refinements, as the proof tier of 2026-11-02 (b) removes a proved check); licensed by a law the program states and the test tier checks; or a hint whose removal never changes a result (layout, inlining, a profile). Unsafe code stays at the FFI edge. A research spike measures each candidate trick against matched optimized Go, keeps the losers visible, and sends the questions that shape v1 lowering (union layout, generic specialization, the lowering of code without effects) to [#196](https://github.com/cevr/ergo/issues/196) first. Ticket: [#243](https://github.com/cevr/ergo/issues/243). Prior art: GHC `RULES` and `SPECIALIZE`, Koka Perceus, Rust monomorphization, Go PGO and bounds-check elimination, ReScript's lowering of variants and strings.

## Owner direction, 2026-11-03

The owner asked whether Ergo has branded types ("a kind of parse, don't validate perspective that certain types must be insured") and whether machines and laws both have a place, as type laws do. The orchestrator answered with two sketches; the owner replied "Yes, I like both."

- **(a) Brands are construction authority.** A defined type (`type Celsius float64`) is a label: anything can convert into it. A guarantee is a type only its package can construct, so its parse function is the only way in: no literal, no implicit zero value and no conversion into it outside the package ([types.md](spec/types.md#zero-values-and-definite-initialisation) rule 6, designed). Its invariant is a law beside it, and the proof tier proves a decidable one (an integer range) and removes the check it proves. Spelling (owner, 2026-11-03: "Okay, let's go with your recommendation"): a guarantee checked by a function is a record with an unexported field, with no new syntax. A decidable invariant (an integer range, a length bound, non-empty) may be written as a refinement, `type Port int where 1 <= it && it <= 65535`, which derives the parser and the law and is discharged by the proof tier. The derived parser is `ParsePort`, so codecs ([#49](https://github.com/cevr/ergo/issues/49)) find it by name like a hand-written one: a decoded `Port` passes its predicate, and a brand encodes as its base type. A codec never builds a type with construction authority structurally; it decodes through the type's `ParseX`. A law parameter takes the same refinement as its generated domain, `law DivMod(a int, b int where b != 0)`, in laws v1 ([#162](https://github.com/cevr/ergo/issues/162); owner, 2026-11-03: "I think it makes a lot of sense in laws. We could probably make that as part of V1 of the laws"). A refined type also gives laws its generator: a `Port` argument is generated inside its range. Refinements are in v1 (owner, 2026-11-03: "let's make the type refinement. Let's also put the type refinements in V1 as well"; [#196](https://github.com/cevr/ergo/issues/196) done-when 17): the derived `ParseX`, a tested law, constants checked by constant evaluation at check time, a non-constant conversion into a refined type refused (S108), generators and codecs. Proving a refinement and removing its downstream checks waits for the proof tier. A `where` predicate outside the decidable fragment is S108, so a refinement never hides a runtime check. A library brand type is rejected: the compiler would check it secretly ([design](design.md#language-design-principles)). Prior art: ReScript `private` types, Gleam `opaque` types, Rust's private-field newtype, MoonBit abstract types, Elixir's Dialyzer `@opaque`, Effect `Brand`. Ghosts of Departed Proofs evidence at opaque ingress is the law client named on 2026-10-12.
- **(b) Machines carry laws.** A `machine` type admits `law` members beside `initial` and `step`, as an operation type does: a law over one transition, over a generated signal sequence run from `initial`, or over reachability. A law that reads only which variant follows which is checked exhaustively on the finite graph `ergo inspect` already builds, and is reported proved; one that reads payloads or an effectful helper runs as a fuzz target and is reported tested. Every runner of a machine plan, the actor library included, passes the provider laws (Reject keeps state, Done is final, steps apply in send order). Tickets: [#57](https://github.com/cevr/ergo/issues/57) (machines), [#162](https://github.com/cevr/ergo/issues/162) (running and proving laws).
- **(c) One spelling for a row: `|`.** Owner, 2026-11-03: "Let's make it so that the union after needs also uses the pipe syntax instead of commas". A capability row is a union, as an error row is, so it is written the same way: `fails NotFound | Declined needs Orders | Payments`. `provides` takes the same spelling (`context provides Orders | Payments`), because it is the same kind of row (orchestrator, by principle: one rule per invariant). A comma between names in `needs` or `provides` is S001, and its message gives the fix; no old spelling is kept (no compatibility while pre-release, owner 2026-10-27). Inside a parameter list, `needs A | B` no longer needs parentheses, because `|` does not end a parameter, so grammar.md rule 9 gets simpler. `with E, F` keeps its comma: it lists context values, not a row. This is stage 196n, a sweep that runs alone after check-v1 E4 merges and before E5 ([#196](https://github.com/cevr/ergo/issues/196)). Prior art: Effect writes its requirement channel as a type union (`R1 | R2`); Koka writes effect rows `<exn, div>` and Unison writes abilities `{IO, Exception}`, both with commas. Ergo picks the union spelling so the two rows of a signature read alike.

## Owner direction, 2026-11-02

The orchestrator proposed four north-star changes after the sketch gap review. The owner first answered "Yes, to all three", then clarified that all four were approved: "So, yeah, let's answer. I'll answer yes to all of your North Star changes, including the fast feedback". The owner asked which languages align most closely with Ergo's thinking on effectful code, using the phrase "like FAI and Roc", and named the source: "https://github.com/Krzysztof-Cieslak/fai is the lang". The prior-art rows are in [PRIOR_ART](../PRIOR_ART.md#owner-direction-references-2026-11-02).

NORTH_STAR.md's North-star pillars section recorded the stage of the proof tier; the pillar itself stays there:

The proof tier is now designed in outline (change (b) of the 2026-11-02 direction below): an SMT solver at check time, within a fixed time budget, proves only decidable facts, and reports "proved" only when it discharges the obligation. Everything outside that tier, including a passing finite law suite, is still reported as tested, never as proved.

- **(a) Host-driven programs.** A host may own the control loop. A game engine, a GUI window or a TUI screen calls the program each frame or event, on the host's thread, through a declared entry signature. Capabilities stay separate services provided by a context, and one platform owning every capability stays rejected. Prior art: Roc platforms, Elm `Browser.element`, Ebiten `Game`. Tickets: [#202](https://github.com/cevr/ergo/issues/202) ([stage comment](https://github.com/cevr/ergo/issues/202#issuecomment-6589909579)) and [#240](https://github.com/cevr/ergo/issues/240).
- **(b) The pillar 4 proof tier.** Prove only decidable facts, with an SMT solver at check time and a fixed time budget: index ranges, length relations, integer bounds and unreachable arms. Report "proved" only when the solver discharges the obligation; a timeout is "unknown". A proved fact lowers as a removed check (pillar 7). Everything else stays tested, never proved. Prior art: Liquid Haskell, F*, SPARK. Ticket: [#162](https://github.com/cevr/ergo/issues/162) ([comment](https://github.com/cevr/ergo/issues/162#issuecomment-6589909948)).
- **(c) Fixed arrays `[N]T` return.** Stage 196v removed them by principle; this reconsiders that prior under the tiebreak **Reconsidering a prior** beats **consistency with it**. The grammar change is [#196](https://github.com/cevr/ergo/issues/196) stage 196y ([comment](https://github.com/cevr/ergo/issues/196#issuecomment-6589910304)) and lands with its code; until then the spec keeps its current text.
- **(d) Fast feedback.** The north star "Exceptionally fast compilation" widens to "Exceptionally fast feedback". Measure the edit -> check -> test loop an agent runs, not only the build: check-time examples run with no Go build ([#162](https://github.com/cevr/ergo/issues/162)), and one warm core serves CLI and MCP ([#203](https://github.com/cevr/ergo/issues/203), [comment](https://github.com/cevr/ergo/issues/203#issuecomment-6589911016)). Prior art: Fai, where `fai check` evaluates `example:` contracts, a JIT serves run and test, and a per-workspace daemon keeps a warm Salsa database.

On SIMD and unsafe, owner, 2026-11-02 (voice-transcribed; "interrupt" means "interop"): "can we make SIMD something we can use - I don't like as long as we can interrupt [interop] with C or you know FFI, you know, it's probably all right to leave unsafe at the edge with that FFI layer". SIMD is therefore usable from Ergo through a std `simd` module over Go's portable `simd` package (`GOEXPERIMENT=simd`, Go 1.27). A build without the experiment lowers the same module to a scalar loop. The Go package is experimental, so receipts record the toolchain. `unsafe` stays at the FFI edge: `import go` packages, cgo or purego. Ergo gets no `unsafe` block. Ticket: [#190](https://github.com/cevr/ergo/issues/190) ([comment](https://github.com/cevr/ergo/issues/190#issuecomment-6589910676)).

### Owner rule, 2026-11-02: SIMD and unsafe

NORTH_STAR.md recorded this decision as an owner rule too:

- SIMD is usable through a std `simd` module over Go's portable `simd` package (`GOEXPERIMENT=simd`, Go 1.27), lowered to a scalar loop without the experiment, with the toolchain recorded in every receipt. `unsafe` stays at the FFI edge (`import go` packages, cgo or purego), and Ergo gets no `unsafe` block (owner, 2026-11-02: "it's probably all right to leave unsafe at the edge with that FFI layer"; [#190](https://github.com/cevr/ergo/issues/190)).

## Exports follow Go's capitalisation, 2026-10-27

From the tiebreak **Go idioms** beat **novel notation** in NORTH_STAR.md, which keeps the principle:

The owner may override a Go spelling decision by decision; explicit `pub` replacing Go's capitalized exports is one such override (owner, 2026-10-08). **Superseded 2026-10-27:** asked again with the recommendation to export by Go's capitalisation, the owner answered "Yeah, let's go with your [recommendations]", so exports follow Go's capitalisation and `pub` is withdrawn ([spec grammar](spec/grammar.md#visibility)).

## The `.ef` extension, 2026-10-27

From the owner rule on the language name in NORTH_STAR.md, which keeps the rule:

The `.ef` extension stays until [#205](https://github.com/cevr/ergo/issues/205) decides `.ergo` ([preview](previews/2026-10-27-ergo.md)).

## Go is the only target (owner, 2026-10-26)

NORTH_STAR.md keeps the principle in its section [Go is the only target](../NORTH_STAR.md#go-is-the-only-target-owner-2026-10-26). These parts of that section record decisions:

It is no longer a runtime, an ABI or a conformance dependency: the JavaScript emitter, the pinned Effect checkout and the Node toolchain are removed, and `--target js` is the EF110 diagnostic.

The same day the owner dropped the exact Bun version check and the pinned Effect upstream requirement ("and lets remove this requirement"; "also the effect requirement as well").

This supersedes every earlier direction in this file that names a JavaScript or TypeScript target, front-end code through that target, the pinned Effect runtime or ABI, or TypeScript/Effect baselines. Each such passage is marked "superseded 2026-10-26" and kept as a record of the earlier direction.

From the owner rule on Effect's behavior in NORTH_STAR.md, which keeps the rule:

The pinned Effect checkout is removed (owner, 2026-10-26: "also the effect requirement as well").

### Owner rules superseded 2026-10-26

NORTH_STAR.md kept these owner rules, superseded on 2026-10-26, in its Owner rules:

- **Superseded 2026-10-26**: Native Go executable is the default product path; JavaScript may emit Effect code (owner, 2026-10-05). JavaScript keeps emitting pinned Effect as the default and reference provider, and a lighter provider joins only through the RS1 laws ([#124](https://github.com/cevr/ergo/issues/124)) together with the Ergo conformance corpus; Effect's own suite ([#123](https://github.com/cevr/ergo/issues/123)) is secondary evidence (owner, 2026-10-12).
- **Superseded 2026-10-26** in part (no JavaScript target): Build application code, server and front-end, for anything JavaScript can run, with I/O as checked effects (owner, 2026-10-11, superseding the 2026-10-05 server-only rule; [#117](https://github.com/cevr/ergo/issues/117)). Kernels, hard real-time, and freestanding/no-GC execution remain outside the destination (owner, 2026-10-05).

## Owner direction before 2026-10-26

The directions below keep their dates. Passages marked "superseded 2026-10-26" yield to the two sections above.

The two sections are NORTH_STAR.md's [Go is the only target](../NORTH_STAR.md#go-is-the-only-target-owner-2026-10-26) and [North-star pillars](../NORTH_STAR.md#north-star-pillars-owner-2026-10-26).

### Owner runtime direction, 2026-10-08 and 2026-10-12

Owner runtime direction, 2026-10-08 and 2026-10-12: userland should be able to provide a superior effect runtime, and Ergo intends to supply one: possibly a generated runtime that no one would write by hand, keeping the same guarantees while running many times faster. The language exposes checked constructs and a lawful runtime contract; a runtime is an ordinary provider of that contract, selected visibly, in the same way JSX notation selects a user-defined pragma instead of a framework adapter. Until a scoped, reviewed provider decision admits another representation, the JS ABI stays pinned and Effect-compatible and pinned Effect remains the default and reference provider. Provider laws and evidence categories live in the [lawful runtime contract](research/lawful-runtime-contract.md). The speedup is a measured ambition under the performance direction above, not a current result. **Superseded 2026-10-26** in part: there is no JS ABI and no pinned Effect provider. Ergo's own Go runtime is the first provider, and a lighter or generated provider joins through the same lawful contract.

[Note: "the performance direction above" is [Owner performance direction, 2026-10-08](#owner-performance-direction-2026-10-08), now below this entry.]

### Provider admission bar, 2026-10-12

On 2026-10-12 the owner set the admission bar: "it should probably downlevel to effect code, but if we can make a lighter runtime that has the same guarantees as effect, that might be an interesting avenue, but it would have to [pass] the same test suite as [Effect]." Then: "it doesn't have to have the same contract … it would be the same laws, I suppose, I would have to follow. So it would be an approximation. … we should put [it] up the queue because it's part of the language constructs." The JS target therefore keeps emitting pinned Effect as the default and reference provider. A lighter provider is admitted by the same laws, the RS1 suite over P1–P12 and R1–R15 ([#124](https://github.com/cevr/ergo/issues/124)), together with the Ergo conformance corpus. Those laws define what scope, fork, join, interrupt, timeout, recover, provide and layers mean, so they belong to the language-construct tier. A provider's representation and API may differ from Effect's. Pinned Effect's own test suite ([#123](https://github.com/cevr/ergo/issues/123)) is secondary evidence that approximates the laws. A provider that claims an Effect surface keeps that suite's compatibility evidence for the surface, but the suite never admits a provider alone. The RS1 suite does not run yet, nothing runs the Effect suite against a second provider, and a provider's performance is measured only after admission. **Superseded 2026-10-26** in part: Ergo no longer downlevels to Effect code, and no target emits pinned Effect. The laws stay the admission bar for every provider, including Ergo's own Go runtime; Effect's suite stays approximation evidence for the guarantees only.

### Owner law direction, 2026-10-12

Owner law direction, 2026-10-12: "laws are not just for runtimes … they also help to kind of formalize and prove behaviour more than types can … kind of like ghosts of departed proofs and bend … we should consider that." And: "laws is for sure a construct." A law is therefore one general construct that states and checks behavior types cannot express. It has many clients: runtime providers (R1–R15, [#124](https://github.com/cevr/ergo/issues/124)), equality and ordering dictionaries, codec round-trips, service operations, machine invariants and Ghosts of Departed Proofs evidence at opaque ingress. This confirms the 2026-10-08 `law`/`contract` decision in the [design principles](design.md#language-design-principles) and settles the placement that the [lawful runtime contract](research/lawful-runtime-contract.md) left open. The [#125](https://github.com/cevr/ergo/issues/125) spike decides the construct's shape, discharge and inspection, not whether laws are a construct. A passing finite law suite is reported as tested, never as proved. No law construct is implemented.

NORTH_STAR.md recorded the queue decision as an owner rule:

- Laws move up the queue as part of the language-construct tier (owner, 2026-10-12: "we should put [it] up the queue because it's part of the language constructs"; [#124](https://github.com/cevr/ergo/issues/124), [#125](https://github.com/cevr/ergo/issues/125)).

### Owner construct bar, 2026-10-12

Owner construct bar, 2026-10-12: "if we're gonna have language constructs, they should be distinct from other already possible constructs. Like normal dot chaining and whatnot." On keywords and primitives: "I think the else block is overloading if else, since that's already a well-known programming concept. … scope and all these things are actually just normal methods in typescript effect. Time out, I look at it as just part of a schedule and whatnot. So I'm wondering why they need to be Language constructs. I mean, if we can justify it, it makes sense, but we'd have to think about a good way to make them primitive rather than blocks." And: "the goal is to provide a language that has enough guardrails so that bad code or illegal states cannot be represented. And that's really where machine as a language construct earns its keep … layers … So if we can make these language constructs and they make sense, then I think we should. But they need to be designed in a way that really makes sense and that are portable across different domains. So [userland] implementations of various different things that may not necessarily be effect runtimes, but are effectual … may not be the default standard library that we provide, but it could have a [userland] implementation that is superior in whatever domain they're in."

The recorded synthesis of these three directions, not owner words, follows. A construct earns its place by passing the guardrail test and the portability test, both defined in the [glossary](../GLOSSARY.md); its protocol's laws come from [#125](https://github.com/cevr/ergo/issues/125). Where the guardrail test fails, the behavior is a primitive function with honest rows plus library, as in Effect. A construct that passes both tests uses a keyword or block form distinct from dot and pipe. A dot or pipe form is always ordinary library, and no well-known keyword is reused with a new meaning. Machines already follow this through provider-independent plans. [#134](https://github.com/cevr/ergo/issues/134) decides, for `.provide`, `.timeout` and `.orFail`, between a keyword or block construct and ordinary library, and it replaces [#95](https://github.com/cevr/ergo/issues/95)'s plan to reserve postfix words. Until #134 decides, those forms remain postfix constructs. Of the compile-to-host peers in the [compiler-known construct comparison](../PRIOR_ART.md#compiler-known-construct-comparison-2026-10-09), Elixir's fixed special forms over library functions are the closest match for this bar.

[Note: in NORTH_STAR.md the three paragraphs before this synthesis were [Owner application direction, 2026-10-11](#owner-application-direction-2026-10-11) (now below), [Owner law direction, 2026-10-12](#owner-law-direction-2026-10-12) and the owner construct bar above.]

### Owner readability direction, 2026-10-12

Owner readability direction, 2026-10-12: "The language should feel easy to read, easy to understand, should not feel rust like, or Haskell like, and kind of like, obtuse, abstract language, but expressive, declarative, and explicit." On generics: "I feel like the language design is leading more typescript than go, especially with the generics … they look very expressive. I'm not sure if they look simple … It could be that that's the best way, but just to put some thoughts on there." Together with the 2026-10-11 goal above, signatures should read with Go's simplicity. Simplify what users write, not what the compiler checks: checked rows, ownership and explicit public contracts all stay (the fail-safe TypeScript projection went with the JavaScript target on 2026-10-26). [#135](https://github.com/cevr/ergo/issues/135) measures implicit kinds, pass-through row elision, named constraints in the type-parameter list and a worded row difference against a rewritten corpus. Elision may apply to pass-through rows only, never to declared requirements. Until #135 decides, the current generic and row spellings, and the surface syntax of [#116](https://github.com/cevr/ergo/issues/116), are provisional.

[Note: "the 2026-10-11 goal above" is [Owner application direction, 2026-10-11](#owner-application-direction-2026-10-11), now below this entry.]

### Owner composability direction, 2026-10-12

Owner composability direction, 2026-10-12: "very important to think about composability … the primitives have to be able to be stacked on top of each other and grow together … effect does that pretty well in typescript … well organized that they can build into greater and greater sums that are gooder than its parts." The recorded elaboration of this direction, not owner words, is that every construct and primitive decision runs the [composability checks](../GLOSSARY.md) with evidence: lossless wrapping, nesting, first-class recipes, orthogonality and composition laws ([#125](https://github.com/cevr/ergo/issues/125)). A visually distinct construct must still pass these checks. In the [effect rows note](research/effect-rows-as-types.md#verdict), a prototype of the recipe-parameter contract checks user wrappers of catch, provide and timeout like the direct forms on nearly all of the corpus. A wrapper that closes its own scope around the wrapped work is not yet shown, and the contract is not implemented ([#128](https://github.com/cevr/ergo/issues/128)).

### Owner application direction, 2026-10-11

Owner application direction, 2026-10-11: "I definitely say [Go's] simplicity is the goal. We want to compile down [to] typescript." And: "I think a goal is that we should be able to swap out the provided effect runtime for user [land] implementation that could potentially be better, for example, or other kind of monadic abstractions for different use cases … And something that can really be fast. … I'd say, for now, it's definitely not just a server programming language. This could be app, basically application code, whether server or front-end, because we can compile the typescript. So anything JavaScript can handle, we should be able to handle, too. So, this is, like, rendering [three.js], all these kinds of things, canvas, [WebGL] … But just with more guardrails. and kind of IO being handled by effects rather than promise based run times." Ergo is therefore a general application language, for servers and front-ends, and this widens the 2026-10-05 server scope. Native Go stays the default executable target. The TypeScript/JavaScript target on Effect carries front-end code, with I/O handled by checked effects instead of promises. Go's simplicity stays the reading standard even where the output is TypeScript. A userland provider may replace the bundled effect runtime under the runtime direction above, and other monadic abstractions are tracked as generalized `run` binding ([#73](https://github.com/cevr/ergo/issues/73)). [#117](https://github.com/cevr/ergo/issues/117) tracks the gaps between today's server-focused profile and real-time client code: floating-point numbers, loops and local mutation, typed numeric buffers and a render-loop spike. None of them is built, so front-end support is direction, not a current claim. **Superseded 2026-10-26** in part: the TypeScript/JavaScript target, and front-end code through it, are removed; Go is the only target. Go's simplicity as the reading standard and replaceable userland providers stand.

### Owner machine direction, 2026-10-07 to 2026-10-09

Owner machine direction, 2026-10-07, 2026-10-08 and 2026-10-09: "if we're adding a special compile check for machines, then it should be a language construct rather than just api"; on 2026-10-09: "one thing about the machine spec, Step is a parser intrinsic? how can we make it so users can provide their own machine runtimes?". `machine` is therefore a visible construct, and it is provider-independent: a construct that any userland runtime can provide. The compiler checks the declaration and lowers it to a provider-independent machine plan. An ordinary userland provider owns execution, admission, scopes, cleanup and observation, and the compiler never dispatches to a built-in machine engine. Ergo ships its own userland machine runtime as the first provider; it uses only the public provider boundary that any second provider could use. See the [machine provider contract](specs/machine-provider-contract.md). This is authorized implementation direction, not current provider support.

### Owner construct direction, 2026-10-09

Owner construct direction, 2026-10-09: "i think in the case of machines, syntax is preferred over special cased library constructs" — "and generally i should say all of our langauge constructs". Whenever the compiler must know a construct's meaning, for checking, inference, plan facts, inspection or special lowering, that construct is syntax: a keyword, operator, declaration form, postfix construct or predeclared language identifier defined by the language (Go's predeclared `len` and `error` model). It is never a bundled or prelude library item that the compiler recognises by identity or name to grant language semantics, which is the Rust "lang item" pattern. Library code the compiler treats exactly like user code stays library; an item becomes library by losing every compiler branch, not by moving its declaration. A library intrinsic, which swaps the implementation of an ordinary declared signature without changing diagnostics, types, rows or ownership, is a separate permitted category and must be documented; so is the implementation of a syntax construct. A language protocol, a row label whose charging a construct's own rule defines (such as `Foreign` for host calls), is language; an ordinary capability service stays library. Foreign identities that `import go` adapts, such as `context.Context`, form that construct's finite host-protocol table. This sharpens the 2026-10-07 [visible-construct refinement](design.md#language-design-principles) and supersedes the earlier plan to turn `.catch` and `.provide` into ordinary library operations: they are dedicated parser forms today, and they stay syntax. Two parts of this paragraph are superseded by later direction. First, "they stay syntax" no longer holds as stated. The [effect rows note](research/effect-rows-as-types.md#verdict) ([#115](https://github.com/cevr/ergo/issues/115)) finds `.catch<E>` and `.provide<S>` GO as ordinary library through general row features, each gated on its unit ([#128](https://github.com/cevr/ergo/issues/128), [#131](https://github.com/cevr/ergo/issues/131), [#132](https://github.com/cevr/ergo/issues/132)). It finds `.recover<E>` conditional ([#133](https://github.com/cevr/ergo/issues/133)) and `.timeout` and `.provide(Layer)` NO-GO as library. Second, the 2026-10-12 construct bar below no longer admits a postfix construct as a construct spelling. [#134](https://github.com/cevr/ergo/issues/134) is re-deciding provide and timeout, including whether one provide block absorbs `.provide<S>`. Until those units land, every form named here remains a dedicated parser form. The [compiler-known constructs audit](research/compiler-known-constructs-audit.md) inventories current and planned violations and their migrations; it is direction, not a claim that they are fixed.

[Note: "the 2026-10-12 construct bar below" is [Owner construct bar, 2026-10-12](#owner-construct-bar-2026-10-12), now above this entry.]

### Strong abstractions and zero-cost implementations, 2026-10-08

Owner direction added 2026-10-08: strong abstractions and zero-cost implementations
are a joint destination. A higher-level Ergo construct must make the illegal
states and invalid operations covered by its checked contract unrepresentable in
checked Ergo source, while foreign and trusted behavior remains explicitly
qualified. It must preserve the same validation, ownership, cancellation and
cleanup contracts as an explicit implementation. Its admitted uses should erase
or lower directly enough that abstraction-induced executable size, allocation,
dispatch and residual-check costs are visible and measured against semantically
equivalent explicit Go and TypeScript/Effect baselines. This is an acceptance
obligation, not a claim that current lowering has zero overhead or that every
abstraction can be universally free. **Superseded 2026-10-26** in part: the
TypeScript/Effect baseline went with the JavaScript target; the baseline is
semantically equivalent explicit Go.

### Owner performance direction, 2026-10-08

Owner performance direction added 2026-10-08: a compiled native Go application
must meet or exceed an optimized idiomatic Go application on a matched workload
when both preserve the same validation, failure/service rows, ownership,
cancellation and completed cleanup. This is a destination acceptance target, not
a current result. The JS target must pursue every measurable lowering and
specialization opportunity in its admitted hot paths, including static `match`
dispatch to `if`/`switch` forms when the generated result is better. A generated
or specialized effect runtime may be machine-written in a form a human would
not choose, and a many-times speedup may be a useful hypothesis to test, while
the pinned default Effect-compatible JS ABI and userland runtime contract remain
intact. These are measured ambitions, never a universal multiplier or a reason
to weaken errors, scopes, cancellation or completed cleanup. **Superseded 2026-10-26** in
part: the JS target and its pinned Effect-compatible ABI are removed. The native
Go clause stands, and the lowering and specialization ambition, including a
generated runtime, now applies to Go.

### State machines, codec derivation and host compilation, 2026-10-06

Additional owner direction, 2026-10-06: state machines are a vital primitive and should be straightforward to express and inspect at language level. Compare Effect Machine and XState before fixing the supported transition/lifecycle contract. Rust-style derivation should remove structural codec boilerplate while retaining Effect-style transformations between distinct wire and domain types, including explicit failures and service requirements in each direction. Compare Gleam, ReScript and TypeScript for host-language compilation and interop. These are implementation requirements under research, not claims of current support.

### Go-like priority and machine prior art, 2026-10-06

The owner sharpened the priority on the same date: keep Ergo as Go-like as possible—simple and explicit, adding the capabilities Go forgoes. XState v6's active PR/branch is the main machine prior art; v5 is historical comparison only. New primitives reuse ordinary data, functions, effect rows and scopes. Common source should not require a framework vocabulary, a separate schema declaration for every type, or a tuple of type-level bookkeeping parameters just to call a library.

### Owner loop direction, 2026-10-06

Owner loop direction, 2026-10-06: actively seek useful primitives that make writing Effect explicit, declarative and delightful. Every pass traces recurring production ceremony, identifies the underlying contract, and compares a small regular primitive against ordinary library composition. Prove adopted improvements with two unrelated runnable callers and negative controls: shorter wiring must retain visible execution, failures, services and owners. Readability and inspectability are acceptance criteria; line-count reduction alone is not evidence. Research sketches remain proposals until they execute on their advertised targets.

### Owner layer direction, 2026-10-06

Owner layer direction, 2026-10-06: make `layer` a first-class declaration composing `service` operations and `impl` behavior/construction. The language assembles a checked lazy acquisition graph; the runtime owns shared acquisition, rollback and completed cleanup. Infer exposed `provides`, construction `raises` and remaining `uses`; checked annotations may narrow outputs or bound rows. Bindings, rather than graph leaves, determine an unannotated layer's exposed services. Merge by node identity, diagnose ordinary duplicates, replace deliberately before acquisition, and provide once at an execution boundary. Domain functions retain requirements. See [layer contracts](specs/layers.md); this is authorized implementation work, not current layer support.

### Small binaries, 2026-10-06

Small binaries are another owner-established north star, 2026-10-06. Keep unused standard-library and runtime facilities out of application executables, including when a fluent API is used. Library availability in the distribution must remain separate from application retention, while a user-declared foreign Go import is a required package-initialization dependency rather than an unused facility; module selection, initialization and direct-call lowering need measured size and reachability evidence.

### Owner interop constraint, 2026-10-06

Owner interop constraint, 2026-10-06: preserve the standard protocols that make Go libraries compose. Automatically import native type identities, methods and structural interfaces such as readers, writers, closers, filesystems and contexts, while retaining native values and useful optional method sets. Signature compatibility is distinct from resource ownership, cancellation, shared mutation and behavioral trust. The [language/library partition](research/effect-language-boundary.md) keeps checked Effect contracts in regular language mechanisms and facility policies in modules; broader host objects and methods remain implementation work.

### Owner data constraint, 2026-10-06

Owner data constraint, 2026-10-06: Ergo has no `nil` or `null` language value, nullable ordinary type or observable uninitialized field. Absence is an explicit closed alternative, with bundled `Option<T>` supplied by the generic-data contract. Nullable host results adapt at the boundary; a native nil pointer cannot become `Some` of an ordinary host reference. Preserve Go's error and partial-result semantics, including a nonnil error interface with a nil dynamic payload. Native runtime internals and wire protocols may contain null representations without exposing them as an unchecked Ergo value. See [absence and host boundaries](specs/absence-and-host-boundaries.md). Wider host adaptation remains implementation work.

### Actors, 2026-10-06

Actor direction under consideration, owner question 2026-10-06: compare BEAM/OTP supervision and hot code loading, Effect Cluster, celld, Rivet and Cloudflare Durable Objects. The [research](research/actor-model-and-durability.md) recommends checked local actor ownership and explicit supervision, with durable entities and workflow replay supplied by optional providers/libraries. Preserve native Go builds and exclude unused actor/storage/deployment modules from application retention. Shared mutable host values, process failure, ambiguous external effects and code upgrade boundaries must remain explicit. This comparison does not expand the first machine profile into distributed execution or certify those guarantees; a later finite contract and executable conformance are required.

Owner refinement, 2026-10-06: actors are owned addressable behavior, not necessarily machines. Actor execution primarily belongs to runtime/stdlib and reuses ordinary service/impl/effect/uses/raises/scope contracts. Typed handlers and restricted receive loops are useful without a new declaration. Machines add transitions and entry lifetimes; message-free effects remain fibers. Supervision, durable addressing and persistence compose with either behavior independently. The [actor contract](specs/actors.md) resolves the proposed initial payload/host-alias, acknowledgement and inspection boundaries; syntax must earn a guarantee common mechanisms cannot express. Source review is input, not local/distributed runtime proof.

### Failure rows spelled `raises`, 2026-10-06

Syntax naming follows the owner's 2026-10-06 clarification: expected failure rows use `raises`, alongside service `uses` rows. Replace the earlier `throws` keyword consistently in admitted source, examples, diagnostics and tooling documentation. Keep naming idiomatic and regular; this spelling change does not alter typed failures, defects or cancellation semantics.

### Tooling diagnostics and types, 2026-10-06

Tooling must expose editor-grade diagnostics and full types through the CLI and MCP as well as the language server (owner, 2026-10-06). Errors, warnings and advice retain their severity, source ranges and checked revision across surfaces. Full types include data structure, callable contracts, dependencies and available ownership evidence; a display name alone is insufficient. Bounded references preserve complete type identities without recursively expanding entire programs. See the [shared tooling contract](specs/semantic-tooling.md); this is a requirement, not a claim that the editor adapter is implemented.

### User-defined lint rules, 2026-10-06

The owner also requires user-defined lint rules (2026-10-06). Ship a versioned, inspectable extension API over the same compiler facts, with configurable rule packs, meaningful source-based tests and shared CLI/MCP/LSP reporting. Project policy may add advice or fail a lint gate; it cannot suppress compiler correctness diagnostics or alter checked types. Optional rule execution must not enlarge application binaries or become a mandatory build dependency. See [custom lint rules](specs/custom-lint.md).

### Built-in formatter, 2026-10-06

The owner requires a built-in formatter (2026-10-06). One canonical `.ef` style and shared printer serve `ergo fmt`, CI checks, MCP previews and editor formatting. Preserve comments and directive meaning; formatting needs syntax, not successful typechecking or backend execution. See the [formatting contract](specs/formatting.md). The pure formatter and shared traversal are integrated at `45b43d2`; CLI/MCP adapters are integrated and exercised through real processes, and LSP document formatting uses the same formatter over versioned buffers, pending independent review.

### Foundation integration update, 2026-10-06

NORTH_STAR.md recorded this receipt at the top of its North stars section:

Foundation integration update (2026-10-06; `a9da03a`): bounded File/Fiber provenance, causal scheduler/testing and shared diagnostic surfaces have passed their reviewed integration gates. Ownership follows deferred results and captures through supported data/helper operations and conservatively refuses unresolved potential escape. Ordinary callback contracts, complete type snapshots and matched native framework benchmarks remain following work.

### Owner performance direction, 2026-10-06

NORTH_STAR.md recorded this direction after its Rejected table:

Owner performance direction, 2026-10-06: measure native compilation/build time and executable footprint against explicit controls: a straightforward idiomatic Go implementation, an optimized Go implementation, and actual TypeScript/Effect cohorts (the TypeScript/Effect cohorts are superseded 2026-10-26). Separate frontend/check/import, source generation, backend compilation, linking and execution; include cold and warm runs plus private-source, public-source and dependency edits. Match observable work, outputs, errors, cancellation and completed cleanup. Retain raw repeated samples, latency distributions, CPU and allocations; for binaries retain generated modules, dependency closure, unstripped symbols, exact bytes and build flags. Make no faster-or-smaller claim without reproducible matched receipts and independent equivalence review.

### Foundation and receipts, 2026-10-05

Established 2026-10-05 from the owner's conversation, [design sketch](design.md), [interop direction](interop.md), and the checked prototype. Receipts below refer to baseline `4c497ac`; they identify evidence and gaps, not permanent line numbers. Wider language capabilities remain proposals until a runnable example and a guard establish them.

Current implementation receipt (2026-10-05; managed runtime foundation `5c8c31f`, import/lifecycle update `8b092fc`): `runtime/ergo` and [`examples/lifecycle.ef`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/examples/lifecycle.ef) establish managed Go ownership, cooperative cancellation, cleanup, and bounded current-scope inspection. `examples/go-interop` establishes the native Go adapter seam, retaining partial results; primitive Go package functions now import automatically with explicit Foreign capability and reviewed behavior metadata. CLI/MCP inspection still describes a single source file, and shared Go/JS lifecycle conformance covers ownership, unobserved failures and timeout cleanup defects; complete provider parity remains open. See [runtime contracts](runtime.md).

Production foundation receipt (2026-10-05; reviewed integration `e5777f7`): records, payload enums, exhaustive interpretation, typed failure payloads and canonical declaration inspection execute on Go/JS. Reasoned lint suppression cannot disable compiler diagnostics. Provider construction checks configuration and captures dependency values while preserving the invocation owner; recipe/materialization origins and incoming dependents share the compiler graph. Full gate and Go race checks passed. Causal scheduling and bounded ownership checking remain under implementation review, and matched server performance remains unmeasured.

[Note: in NORTH_STAR.md the [Foundation integration update, 2026-10-06](#foundation-integration-update-2026-10-06) followed these receipts; it is now above this entry.]

## Undated

These entries carry no date in NORTH_STAR.md.

### Effectful machine steps

[In NORTH_STAR.md this paragraph followed the owner layer direction of 2026-10-06.]

The owner further clarified that machine steps must be allowed to be effectful. Admit both ordinary pure and effect functions with explicit contracts; serialize their execution under the actor's owner, preserve failure/service rows, and commit state only after successful evaluation and cleanup. Local state commit does not imply rollback of external effects.

### The no-value contract `void`

The canonical no-value contract is `void`: the `void` type and bare `void` expression describe successful completion without an information-bearing result. Empty parameter and argument lists remain `()`, while `()` is rejected as a type or expression so no-value syntax cannot be confused with grouping or calls. `void` is not absence, `never`, nil, null, or a backend sentinel; `Option<void>` retains its `Some`/`None` distinction. The Go target may use a zero-size carrier in value positions, but that representation does not change the source contract.

### Status and pending-ticket clauses moved from NORTH_STAR

NORTH_STAR.md held these clauses inside principle sections. Each is a status, a receipt or a pending ticket for one feature; the principle around it stays in NORTH_STAR.md.

- From the north star **General application language**: The [#117](https://github.com/cevr/ergo/issues/117) numeric, loop and buffer work serves Go applications.
- From the north star **Operability and agent introspection**: Process-wide task inspection remains a gap.
- From the north star **Exceptionally fast feedback**: Current single-file timings do not establish incremental, end-to-end or edit -> check -> test performance.
- From the north star **Native Go executables** (NORTH_STAR.md keeps the sentence up to "the same contract"): Matched application acceptance targets performance at least as good as optimized idiomatic Go with the same contract; current comparative performance remains unmeasured.
- From the north star **Adoption through host interop**: Receipt: [`internal/compiler/imports.go`](https://github.com/cevr/ergo/blob/dbf135bff09901e85385945593113f35b300199c/internal/compiler/imports.go) and `host_types.go` consume native signatures as canonical host types with Option absence adaptation and complete results, plus Go method sets, direct native interface assignment, checked assertions, filesystem/optional-method dispatch, checked I/O reads and writes, and method behavior contracts; owned resources and callbacks remain gaps.
- From the north star **Owned lifetimes**: Receipt: `runtime/ergo` implements owned shutdown with conformance tests.
- From the tiebreak **A primitive function plus library** beats **a construct that fails the guardrail or portability test**: [#134](https://github.com/cevr/ergo/issues/134) decides provide and timeout by this rule.
- From the tiebreak **Visually distinct constructs** beat **method-shaped syntax** (NORTH_STAR.md keeps the sentence up to "construct spelling"): Postfix constructs are therefore no longer an admissible construct spelling; existing ones stay until [#134](https://github.com/cevr/ergo/issues/134) decides, per form, between a keyword or block construct and ordinary library.
- From the sweep **Feedback latency** (NORTH_STAR.md keeps the method from "For changed build paths"): The front end has no compile benchmark yet (the retired compiler's `BenchmarkCompile10KLines` went with it; [record](receipts/retire-ef.md)); for changed build paths compare matched Ergo/Go cold, warm, private-edit, public-edit, and dependency-edit fixtures, recording toolchain/hardware/cache regime and stage timings.
- From the sweep **Zero-cost abstractions** (NORTH_STAR.md keeps the rest of the method, and the sweep **Matched server performance** keeps "Functional/lifecycle equivalence precedes scoring"): Benchmark only after conformance and the final measurement-ledger phase.
- From the rejected row **Reusing a well-known keyword, such as `else`, with a new construct meaning**: [#134](https://github.com/cevr/ergo/issues/134) decides the replacement.
- From the rejected row **A language construct that looks like ordinary dot chaining**: Today's postfix constructs remain until [#134](https://github.com/cevr/ergo/issues/134) decides each form; this replaces [#95](https://github.com/cevr/ergo/issues/95)'s reservation plan.

### Live check

NORTH_STAR.md had this section, a recipe and the state of the live check:

- Drive: `./scripts/gate.sh` builds `bin/ergo` and exercises native Go execution through the CLI process tests (`TestSketchExampleBuildsAndRuns`, `cmd/ergo/sketch_process_test.go`).
- State: `./bin/ergo check examples/editions/sketch/main.ef` and `./bin/ergo diagnostics --json examples/editions/sketch/main.ef`. `ergo inspect`, `ergo explain` and MCP are unavailable until [#203](https://github.com/cevr/ergo/issues/203); there is no out-of-process runtime endpoint yet.
- Stop: the process tests wait for their processes to exit. Longer-lived server checks must record shutdown and scratch cleanup.
