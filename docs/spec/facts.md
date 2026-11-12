# Value-tied facts

Status: **designed**: no compiler support yet. The parser refuses the clauses `given`, `gives` and `keeps`, and a `where` after a record type, with S001.

A [fact](../../GLOSSARY.md#constructs-contracts-and-laws) ties a checked condition to the values it names, so a later call can rely on it with no second check. A function asks for a fact with `given`, a function that makes one says so with `gives`, and a function that writes a value and leaves a fact true says so with `keeps`. A record type states a fact about every one of its values with `where` ([record invariants](#record-invariants)). Each fact that is not checked in the body is a law that `ergo test` runs, the test build checks every fact where it is used, and the run build emits no code for a fact. The owner put value-tied facts in v1 ([decisions](../decisions.md#owner-direction-2026-11-11)).

Each rule below names the test that holds it. These tests do not exist yet; the [status ledger](status.json) lists none until they do. The examples are fenced as `ergo`, not `go`, because the parser does not accept these clauses yet, and every `go` fence of the specification must parse; the parser's change turns them to `go`.

```ergo
// slices
func InRange[T](xs []T, i int) bool { 0 <= i && i < len(xs) }
func At[T](xs []T, i int) T given InRange(xs, i) { xs[i] }
func BinarySearch[T ordered](a Ascending[T], x T) Option[int] gives InRange(a.xs, it)

// maps
func Has[K comparable, V](m Map[K, V], k K) bool { … }
func At[K comparable, V](m Map[K, V], k K) V given Has(m, k)

// arena
func Live[T](a Of[T], r Ref[T]) bool { live(a.slots, r) }
func At[T](a *Of[T], r Ref[T]) T given Live(*a, r)
func Update[T](a *Of[T], r Ref[T], f func(v *T)) fails Stale keeps Live(*a, _)
func Free[T](a *Of[T], r Ref[T]) T fails Stale keeps Live(*a, x) when x != r

// a cursor
func more(c *cursor) bool { c.pos < len(c.text) }
func peek(c *cursor) byte given more(c) { c.text[c.pos] }
for more(&c) && peek(&c) == ' ' { c.pos += 1 }   // the write kills more(c) at the loop head
```

## Facts

1. **What a fact is.** A fact is a call of a fact function whose arguments are places. A fact function is a function that returns `bool`, has no `fails` and no `needs` row, and is total and pure. A place is a `let`, a parameter, or a field path of one, as [Borrows](types.md#borrows) 2 defines it. A fact is not a value: it has no type, and it is never stored, returned, sent or captured by an escaping frame, by the same rule as a borrow ([Borrows](types.md#borrows) 1). A fact that must travel inside a value uses a type with construction authority instead ([types](types.md#structs)). No new declaration kind exists: a fact function is an ordinary function, and `given`, `gives`, `keeps` and `when` are clauses of a signature. Test: `TestFactsModel` (`internal/sketch/check`).
2. **Introduction.** A fact holds in the branch where its call was true: the body of an `if`, the right operand of `&&`, the body of a `for`, the code after `if !F(…) { return }` (or `break` or `continue`), and the else of `if !F(…)`. A call to a function whose signature says `gives F(…, it)` gives the fact for its success value, `it`, in the arm of a `match` or a `catch` that binds that value; for an `Option` result, `it` is the `Some` value. Test: `TestFactsModel`.
3. **Kill.** A fact names subject places. It stops at the first write to a place that overlaps a subject, by the prefix rule of [Borrows](types.md#borrows) 2: an assignment, a `&x` passed to a call whose signature has no `keeps` for that fact, or a write in a loop body, which kills the fact at the loop head. A write to `c` kills a fact on `c.pos`; a write to `c.other` keeps it. A `let mut` that a function literal captures and writes is written at each call of that literal. A `let` is never written ([Bindings](types.md#bindings) 1), so a fact whose subjects are all `let` bindings or parameters by value holds to the end of its scope. Test: `TestFactsModel`.
4. **Use.** A call to a function with `given F(…)` needs `F` over the caller's places at the call, after the callee's parameters are replaced by the call's arguments. The checker finds it among the facts in scope by syntactic match, or by rule 6. Otherwise the call is an error at the call: "slices.At needs InRange(xs, i); check it first: if slices.InRange(xs, i) { … }". A function passes a fact on to its callees only by its own `given`: the checker infers no `given`, `gives` or `keeps`, for an exported function or a private one. Test: `TestFactsModel`; the text of the diagnostic is a row of `TestFixtures` (`internal/sketch/check`).
5. **Conditional keeps.** `keeps F(…, x) when C` keeps each fact `F` whose free argument `x` satisfies `C`, where `C` is a fact or a comparison over the call's arguments and `x`. `keeps F(…, _)` keeps every fact `F` over the written value. A call with no `keeps` for a fact kills every fact over the places it writes. Test: `TestFactsModel`, and the derived law of rule 7 in `TestFactChecksCatchWrongAxioms` (`internal/sketch/lower`).
6. **The decidable part.** When a fact function's body stays inside the fragment of [Refinements](types.md#refinements) 2, widened to two subjects (comparisons, `len`, `&&`, `||`, `!` and integer literals over its parameters), the checker unfolds it, and a fact follows from the facts in scope when the fragment decides the implication: `i+2 < len(t)` gives `InRange(t, i+1)`. A fact function outside the fragment, such as `Has` or `Live`, is an atom: only a fact with the same name and the same places satisfies it. Test: `TestFactsModel` (its reference model decides the implication by listing small values).
7. **Axioms are laws.** Each `gives` and each `keeps` (with its `when`) derives a law, which `ergo test` runs with the package's other [laws](types.md#laws). The law of `gives` says that the fact holds of each success value; the law of `keeps` says, over generated states, that the fact before the call implies the fact after it, and quantifies over `x` for a `when`. A law whose values can all be listed is proved; any other law is tested and is reported as tested. A refuted law refuses the build, as for every law ([Admission](../../GLOSSARY.md#constructs-contracts-and-laws)). Test: `TestFactChecksCatchWrongAxioms`.
8. **Erasure and test-build checks.** The run build emits no Go for a fact, a `given`, a `gives`, a `keeps` or a `where`. The test build evaluates each `given` at the callee's entry and raises a defect that names the fact, the callee and the call when it is false. So a fact that a write should have killed fails `ergo test` on the first case that reaches it. Test: `TestFactChecksCatchWrongAxioms`; `TestFactRunBuildErases` (`internal/sketch/lower`): the run build of a program equals, byte for byte, the run build of the same program with its facts removed, except at the sites that consume a fact.

## Record invariants

1. **Syntax.** `type T { … } where P(it)` puts the fact `P` on every value of the record type `T`. `P` is a fact function ([Facts](#facts) 1) of `T`'s package, and `it` names the value. This is the `where` of [Refinements](types.md#refinements) widened from the decidable fragment over a scalar to a fact function over a record. Test: `TestInvariantModel` (`internal/sketch/check`).
2. **Authority.** Only a type with construction authority, an unexported field ([types](types.md#structs)), may have a `where`: outside its package no literal, update or zero value builds a `T` that breaks `P`. A `where` on another record type is an error: "type T has no unexported field, so code outside its package can build a T that breaks P; add an unexported field or drop the where". Test: `TestInvariantModel`; the text of the diagnostic is a row of `TestFixtures`.
3. **Boundaries.** The boundaries of `T` are the exported functions of its package that take a `T` (by value or as `*T`) or return one. When `T` is unexported, every function of its package that takes or returns a `T` is a boundary. Test: `TestInvariantModel`.
4. **Where it holds.** At a boundary's entry, `P(x)` is a fact for each `T` parameter `x`. Outside the package, `P` holds of every value of type `T`. Inside a function of the package that is not a boundary, `P` is not a fact: the function gets it only through its own `given`, so a private helper may break `P` for a time. A missing `given P(…)` in such a helper is the error of [Facts](#facts) 4. Test: `TestInvariantModel`.
5. **Who keeps it.** Each boundary that writes a `T`, by returning one or through a `*T`, must leave `P` true at every exit. A failure exit is included, since the writes through a borrow stay after a failure; a defect exit is not. The test build evaluates `P` on each written `T` at each boundary exit, and raises a defect that names `P`, the function and the exit. The run build erases these checks. Test: `TestFactChecksCatchWrongAxioms`.
6. **Derived facts.** A fact that follows from `P` is stated as a `gives` on a helper with `given P(c)`: `func linkFor(c Cache[K, V], k K) Option[Ref] given linked(c) gives Live(c.nodes, it)`. Its law runs over values that the package's own constructors and boundary sequences build, as [Laws](types.md#laws) 10 builds providers, so `P` holds in each case with no generator of `T`. Test: `TestFactChecksCatchWrongAxioms`.

## Evidence

1. **Labels.** Each fact at a use has one evidence label, and `ergo inspect` shows it with the facts in scope:
   - **checked**: from a dominating compare in this body, or unfolded in the decidable fragment ([Facts](#facts) 6);
   - **proved**: by a law whose values were all listed, or by the proof tier;
   - **tested**: by the `gives` or `keeps` laws, or by an invariant's exit checks and derived laws, which passed their generated cases;
   - **none**: the receipt of the law is missing, stale or skipped.

   Test: `TestFactBuildsAgree` (`internal/sketch/lower`); the inspect text is a row of `TestInspectAPIRoute` (`cmd/ergo`).
2. **The evidence rule.** A fact labelled tested is evidence only when the receipt of the package that states the law is current at that package's revision, holds every law, exit check and derived law that the fact rests on with none skipped, and has the use's constant arguments, if any, among its cases. The std receipt is embedded in the compiler; a user package's receipt is the one `ergo test --receipt` writes. This is the evidence rule that law rewrites use. Test: `TestFactBuildsAgree`.

## Lowering

1. **Direct forms.** A body that consumes a fact through `given` lowers to its direct form when the fact has any label but none: `maps.At` is Go's `m[k]` with no comma-ok and no `Option`, and arena `At` has no generation compare and no failure return. A wrong fact here gives a wrong value, never memory unsafety, since Go still checks the index. Test: `TestFactLoweringAgrees` (`internal/sketch/lower`).
2. **Reload elimination.** Ergo's inlining puts a guard and its use in one Go function. Inside a fact's live range, the lowering reads each subject place once into a Go local, since [Facts](#facts) 3 proves that no write comes between; Go cannot know this for a field read through a pointer. Go's prove pass then removes the bounds check. No `unsafe` code is used. Test: `TestFactLoweringAgrees`.
3. **Unchecked loads.** Where Go's prove pass still keeps a bounds check, an index under an `InRange` fact lowers, in the run build only, to a call of a reviewed body in `runtime/v1`: `v1.LoadAt[T](xs []T, i int) T`, `v1.AddrAt[T]` for `&xs[i]`, and `v1.ByteAt(s string, i int) byte`. The test build lowers the same use to Go's checked index. A use gets an unchecked load only when its fact is labelled checked or proved, or tested under the evidence rule ([Evidence](#evidence) 2). Otherwise the use keeps Go's checked index, and `ergo check` names the reason: "unchecked load at x.ergo:12 not used: Live rests on arena.FreeKeeps, no receipt at <rev>". The lowering chooses the body from the fact, never from the callee's name. Test: `TestFactBuildsAgree`: on generated programs whose facts hold, the test build, the run build and the reference build print the same output, and a counter shows that the unchecked loads ran; with a seeded false axiom, the law refutes it, or the receipt marks it skipped and the use keeps Go's check, or the test build raises the named defect.
4. **A false axiom.** A law that passed its cases and is false (`Free keeps Live(*a, r)` for the freed `r`) makes the run build read outside a slice: memory unsafety, not a defect. Four things bound it: only the axioms that a current receipt holds drive a load; each test build checks each `given` and each invariant on the paths the tests reach; the reference build keeps every check; and the bodies are few and reviewed (rule 6). Test: `TestFactBuildsAgree`.
5. **The reference lowering.** `-X lower.factLowering=checked` builds the run binary with every consumer's Go check kept and no unchecked load. It stays selectable, and every example's `ergo test` passes under both lowerings. Test: `TestFactLoweringAgrees` (the reference equals the new lowering on generated programs whose facts hold, each branch reached).
6. **The unsafe inventory.** The `unsafe` bodies are listed in `internal/residue/unsafe.txt`, each with its function, its invariant and a review line (reviewer and date). An `unsafe` import or call outside the list, or a listed body with no review line, fails the build of the repository's tests. The list grows only with a review line. Test: `TestUnsafeBodiesListed` (`internal/residue`).

## Prior art

- GDP, "Ghosts of Departed Proofs" (Noonan, Haskell 2018): a fresh name per value, phantom proofs, and library axioms that nothing checks. Ergo takes the named subjects and the library axioms; an Ergo axiom is a law that runs.
- Rust's `indexing` crate and GhostCell: a branded lifetime ties an index to one container, and access inside the brand is unchecked. Ergo gets "the fact dies with its subject" from its borrow model, with no lifetime parameters.
- Liquid Haskell: refinement types with measures, decided by SMT. Ergo takes a decidable fragment only.
- Dafny `requires`/`ensures` and ghost code; F* erased proofs; Idris quantity-0 erasure: specifications that erase at compilation. `given` and `gives` are `requires` and `ensures` restricted to named bool functions, discharged by flow, laws and the proof tier.
- Ada SPARK: contracts checked at run time when enabled and removed when proved (`Assertion_Policy`). The test-build checks follow it.
- TypeScript type predicates and `asserts`, Kotlin contracts: a check that gives a flow fact at its call. They are unsound when the subject changes; Ergo kills the fact on a write.
- Go's prove pass removes bounds checks from dominating branches inside one function. Ergo feeds it by inlining and reload elimination.
- Gleam (`dict.get` returns `Result`, `let assert` crashes), Elixir (`Map.fetch!`, guards checked at run time) and ReScript (`Belt.Map.getExn`, the trusted `Array.getUnsafe`, OCaml's global `-unsafe`) tie no fact to a value: they check again at run time or trust the programmer.
