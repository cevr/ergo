# deps

A package dependency resolver. It reads a manifest (a project's own needs and the registry of releases it may choose from), chooses the newest version of each package that keeps every constraint on it, and prints the packages in the order to build them (`deps resolve`), the shortest chain of needs that pulls one package in (`deps why PACKAGE`), or each need between the chosen packages (`deps graph`). `-f FILE` names the manifest (default `deps.txt`).

```text
# a comment
project site
  needs web ^2.1
  needs log >=1.2 <2

package web 2.3.1
  needs log ^1.4
```

Constraints are semver-lite: `=V`, `>=V`, `>V`, `<=V`, `<V`, `^V` (Cargo's and npm's caret), `~V` (up to the next minor), a bare `V` (exact) and `*`, several in one want meaning all of them. A version is `MAJOR.MINOR.PATCH`, with missing parts 0.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked by hand against the manifest in `testdata/`.

## Layout

The program is a module of packages (`ergo.mod`: `module deps`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `semver/` | Versions and constraints. `version.ergo` (`Version`, `ParseVersion`, `Format`, `Compare`), `constraint.ergo` (the union `Bound`, `Constraint`, `ParseConstraint`, `Holds`, `Satisfies`, `CaretLimit`, `And`), `laws.ergo`, `tests.ergo`. Pure. |
| `graph/` | A directed graph over any ordered node type. `graph.ergo` (`Graph[N ordered]`, `Of`, `Add`, `Link`, `Order` failing `Cycle[N]` with the cycle's path, `Path`), `laws.ergo`, `tests.ergo`. Pure. |
| `resolve/` | The resolver and its contract. `index.ergo` (the contract `Index`, an operation type with two laws, and its failures), `resolve.ergo` (`Resolve`, its failures `NoVersion`, `NoPlan`, `TooHard`, `Circular`, and `Why`), `table.ergo` (`Table`, an in-package `Index`, and the generators the laws run over), `laws.ergo`, `tests.ergo`. Pure but for its `Index`. |
| `manifest/` | The file format. `manifest.ergo` (`Manifest`, `BadLine`, the machine `Reader` whose state is the block being read, `Step`, `Read`), `laws.ergo` (`Format` and two laws), `tests.ergo`. Pure. |
| `memory/` | `Registry`, the `Index` of a manifest's releases (`NewRegistry`), with tests that it keeps `Index`'s laws and resolves as `resolve.Table` does. |
| `cmd/deps/` | The binary. `main.ergo` binds the host's `os.Process`, `fs.Files` and `io.Stdout`, and a `memory.Registry` of the manifest, and is the only place that chooses them. Its tests run the commands over an `fstest.Memory`. |

## What it shows

- **A typed failure that carries the evidence.** `graph.Order` fails `Cycle[N]{Path}`, the path of the cycle; `resolve.Resolve` fails `NoVersion{Name, Wants}` with every want on the package and who wrote it, `Circular{Path}` with the pinned versions in the ring, `NoPlan`, `TooHard{Steps}`, `UnknownPackage` or `UnknownVersion`. `cmd/deps` catches each by name, with no `case _`, and prints it (`deps: no version of log keeps every want:` and each want with its source).
- **A union and exhaustive `match`.** `Bound` has eight variants, and `Holds` names every one; the manifest reader matches the pair `(first word, indented)`.
- **A machine.** `manifest.Reader` is `machine[Block, Signal]`: its state is `Top | InProject | InRelease`, a line `Enter`s a block, a needs line `Stay`s with one more need, a comment is `Ignore`d, and the end is `Done` with the manifest. `Read` runs it with `machines.Run`.
- **A contract in the domain, providers named after their dependency.** `resolve` declares `Index` with the laws `VersionsAreNewestFirst` and `EveryVersionHasNeeds`; `resolve.Table` and `memory.Registry` provide it; `main` chooses `memory.Registry`.
- **A backtracking search over a pointer.** `solve` is a Go programmer's depth-first search with a step budget in a `*search`, which also records the first dead end that one package is to blame for.
- **Laws, each killed by its own mutant.** `semver` states six (`FormatRoundTrips`, `CompareIsAntisymmetric`, `CompareIsTransitive`, `CaretKeepsTheLeadingPart`, `AndIsBoth`, `TextRoundTrips`), `graph` five (`OrderRespectsEdges`, `OrderHasEveryNodeOnce`, `OrderIsDeterministic`, `CycleIsAClosedWalk`, `PathFollowsEdges`), `resolve` three (`ResolutionIsSound`, `ResolutionIsDeterministic`, `NoVersionIsHonest`) and two on `Index`, `manifest` two (`ReadInvertsFormat`, `NoiseChangesNothing`). `ergo test` runs each, and `cmd/deps` checks that a resolved plan, pinned exactly, resolves to itself. For each law there is a mutant of the program that breaks it, and the law itself fails on it (18 of 18, `mutants.json` in the batch-12 ex-upgrade run dir).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/deps/cmd/deps resolve -f examples/deps/testdata/deps.txt
./bin/ergo test examples/deps/resolve
go run ./scripts/examples deps
```

## Cases

`resolve`, `why`, `graph` (the three commands over `testdata/deps.txt`), `conflict` (`NoVersion` with its wants), `ring` (`Circular`), `why-not-needed`, `bad-line`, `missing-file`, `usage` and `help`.

## Limits found

Each is written up with a repro in the lane's gap file (`gaps-ex-d.md`).

- **G-ex-d-9** (still open after T4) Generated ints rarely meet, so the laws' generators are steered by hand (`resolve.NameOf`, `Few`, `RootOf`, `graph.Crowded`, `Few`, `Downhill`). Measured at batch 12 with T4's wider generators, seeds 1 to 5: with the graph steering removed, mutants g1 and g5 survive their own laws; with the names and versions not folded mod 3, r1 survives and r3 is killed by one seed of five. So the steering stays.
- **G-ex-d-12** `needs` is a keyword, and a parameter named `needs` gets "expected a type".
- **G-ex-d-13** A law cannot be generic, so the graph laws are stated at `int` nodes.
- **G-exup-1** (closed in batch 13) `ergo test` runs the laws of `resolve.Index` against `memory.Registry` too, in package `memory`, building each provider with `NewRegistry`; `memory/tests.ergo` checks them again over the steered names.
- Known from other lanes: a required positional argument's `Get` is an `Option` (std-strengths F1).

Closed in batch 12: **G-ex-d-10** (L3: `return` leaves a `with`, so `report`, `SoundOrFailed`, `Honest` and `ringOf` are gone); **G-ex-d-11** (V2: `Read` runs `Reader{}` with `machines.Run`; its two impossible outcomes are `Unreachable`); **G-ex-d-14** and G-ex-b-15 (the value rule: `appended`, `appendedNeed`, `copiedWants` and `graph.copied` are gone, and `pinned` copies by assignment); **G-ex-d-5** (W3: `Manifest` has `==`, so `manifest.Same` is gone); G-ex-b-16 (K3: `let b = if …` needs no annotation); the property tests that restated the laws (V1 runs the laws, and the 18 mutants of `mutants.json` are now killed by the laws themselves, seeds 1 to 3); the hand `fs.Files` of `cmd/deps`'s tests (U1: `fstest.NewMemory`).
