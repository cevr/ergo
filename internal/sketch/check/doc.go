// Package check is the checker of the language (#196). It takes the
// parsed files of one package (internal/sketch/syntax) and an Importer, and
// returns the checked package (scopes, objects, the type of every checked
// expression, the declared or inferred rows of every function and literal)
// and its diagnostics, sorted by position, with UTF-8 byte spans.
//
// docs/spec/ is normative: types.md, rows.md, grammar.md, runtime.md
// (machines and tasks) and diagnostics.md, with
// docs/research/rows-and-constraints.md §2, §3, §4 and §7. Where the spec is
// silent, the checker follows the Go specification's typing as go/types
// implements it, minus what the spec removes (no nil, no const, no pointer
// values outside parameters and receivers, no generic methods).
//
// Phases: collect the package's declarations; solve the kinds of the
// generic type declarations together (kinds.go); resolve type declarations
// lazily with cycle colouring; resolve signatures; reject instantiation
// cycles on the instantiation graph (mono.go); check bodies one strongly
// connected component of the reference graph at a time, so the inferred
// rows of a callee are final before its callers (a lexical walk of every body
// gives the graph's edges before any typing, refs.go; the initialization
// cycles are found first, discoverInit; class G pins the unwritten
// rows of a function used as a value inside its own component; inside a cyclic component a silent pass
// finds the instantiation cycles, then a worklist joins the rows to a
// fixpoint from empty rows before any is published); then the
// rules that read inferred rows (machine declarations, named contexts, context
// cycles), Error methods and unused imports. An invalid operand or type suppresses every diagnostic that
// would follow from it: one mistake, one diagnostic.
//
// The prelude (internal/sketch/std/prelude.ergo) and the standard library are
// Ergo source checked by this checker, so no library item is a checker
// branch (timeout.After is an ordinary bodiless declaration).
//
// Prior art:
//   - Go types2 (cmd/compile/internal/types2): the phase order, the Importer
//     seam, the invalid type that stops cascades, object colouring for
//     declaration cycles, keyed method sets, and function argument type
//     inference by unification (infer.go).
//   - Gleam: module-wide inference with every error reported, and its
//     exhaustiveness checker for case expressions.
//   - ReScript and OCaml: constructors disambiguated by the expected type
//     (generic literals and case variants without type arguments); OCaml's
//     Parmatch usefulness algorithm (Maranget 2007) for totality over a case
//     matrix, never the product of the subjects, with Parmatch's pruning:
//     subsumed rows dropped, a wildcard row answering at once, equal
//     submatrices answered once. The column to split is chosen by
//     Maranget's necessity heuristic, ties broken by source column, so
//     the search is deterministic for a given source, and a fixed step
//     budget counts every superlinear pass, as rustc's
//     pattern_complexity_limit and GHC's max-pmcheck-models do: over
//     budget, the switch is refused (S103), never accepted unproven.
//     Like theirs, the budget depends on the order of the subjects; a
//     proved verdict does not (D16 as amended in round 5).
//   - go/types initorder.go and its "instantiation cycle" check: package
//     var initialization cycles and expanding generic instantiations are
//     rejected on their dependency graphs, with no depth limit.
//   - Koka and Effekt: effect rows inferred for unannotated functions and
//     discharged by handlers; here a with block subtracts the capabilities
//     its context binds, and a catch subtracts the caught error types.
//   - Effect: Effect<A, E, R>, where catchTag removes a tag from E and
//     provide removes a service from R; a function value widens to a type
//     whose rows are supersets, never the reverse.
//   - Elixir: its gradual set-theoretic types infer across modules without
//     requiring written signatures, so it does not impose explicit exported
//     rows; Ergo's written exported rows (types.md, Generics 4) follow
//     MoonBit and Go's export data instead, and Elixir is the contrast.
//   - MoonBit: top-level signatures are annotated and locals inferred, and
//     its raise rows are part of the function type.
//   - Laws (laws.go): Elixir StreamData's `check all` and QuickCheck state
//     properties as code; MoonBit, Gleam and ReScript have no laws, and
//     Haskell's class laws are documentation only. Ergo checks a law as code
//     and keeps it out of the binary, as Zig keeps its `test` blocks out of
//     non-test builds.
package check
