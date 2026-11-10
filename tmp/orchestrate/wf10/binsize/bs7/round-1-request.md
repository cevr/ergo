ready
Protocol: supplied orchestrate worker protocol, implementor; wf10/common.md.
Spec: /home/exedev/.cache/baton/wf10/binsize/followups.md, first section (machine graph metadata without init-time registration); brief /home/exedev/.cache/baton/wf10/binsize/brief-bs7.md.
Head: 33cbc7088920bfae5f4392749bd5990af83f22bd (code and acceptance head).

# BS7 round 1: machine graphs as static data

## Commits

- Branch `wf10/bs7`, worktree `/home/exedev/Developer/personal/.worktrees/ergo-bs`. Base BS6 `865614e5`; code head `33cbc708`. No rebase, amend or squash.
- `git diff --stat 865614e5..33cbc708 | tail -1`: `28 files changed, 929 insertions(+), 93 deletions(-)`.
- `fb311ac4` feat: runtime hidden graph argument and static graph lowering; `bafb5cf2` fix: state and signal variant names kept apart; `9d96cec4` test: the trace property; `be147c2e` test: generator reaches more forms, plus the mutant ledgers; `1cb21a86` docs(spec); `f476cf09` docs(lower) comments; `33cbc708` test(sizes): the pairing fixture and the registry facts.
- A later commit that only adds the request carries this file at `tmp/orchestrate/wf10/binsize/bs7/round-1-request.md`. It changes no checked source. The verifier replays at `33cbc708`.

## What changed, by file

- `runtime/v1/actor_trace.go`: `MachineGraph.Timers` is now `[]MachineTimer{State, Signal}` and no longer a map, so a graph is constant data the linker lays out and no initializer builds. New `GraphOf func() *MachineGraph`, the hidden graph argument. The actor calls it only in its trace helpers (`graphOf`). `RegisterMachine`, `RegisteredGraph` and `VariantOf` stay as the **reference** path only, and a default program never names them, so the linker keeps none of them (the sizes facts check this).
- `runtime/v1/actor.go`, `runtime/v1/files.go`: `MachineStart`, `MachineSupervise` and `MachineChild` take the hidden `Graph` argument after `Sleeper`. The actor stores it and does not read a global registry. Runtime tests pass `nil` or a literal graph.
- `internal/sketch/lower/machine.go`: by default each sourced machine's graph is `var ergoGraphX = ergo.MachineGraph{...}`, a constant composite literal. Variant names are computed at compile time (`variantName` and `runtimeSpelling` spell the Go runtime's type string: `main.T`, the import path in brackets, a bare comma between type arguments, then the `rt.TypeName` rule; a type it cannot spell is an ICE). The hidden argument is `ergoMachineGraph[S, E]`, a generic func that type-switches on `ergoMachinePair[S, E]` and returns `&ergoGraphX`. It is emitted only when a start names it (`l.graphsUsed`). When two machines share an S/E pair, that pair gets no case and yields nil, which keeps the old rule that an ambiguous pair has no arms. The reference lowering `graphLowering=registry` emits the old init `RegisterMachine` with `VariantOf` names and passes `ergo.RegisteredGraph[S, E]`.
- `internal/sketch/lower/std.go`, `module.go`, `naming.go`: the hidden argument kind `v1.Graph` (`fc.graphArg`), emitting the accessor, and name reservation. The naming doc lists the graph names.
- Bug fix (`bafb5cf2`), present before this unit: the state and signal variant Go names of one machine shared a map, so a signal variant that shared a state variant's type name could be misnamed. They now live in separate maps (`stateOf` and `signalOf`).
- `internal/sketch/lower/graph_lowering_test.go`: `TestGraphLoweringsTraceAlike` is a seeded property. It generates programs whose state and signal unions hold records and generic instances over every form `goType` writes (basics, duration, records, slices, arrays, Maps, Options, tuples, funcs, nested instances, a `queue.Queue` from an imported package, an empty struct). The steps have whole, partial and multi-pattern arms and timers, and sometimes a twin machine over the same types. Each program is built under both lowerings and each machine is run as a traced actor. The machine events must be equal under both lowerings and must name an arm for every decision and the timer signal for every armed timer, except for shared pairs, which name none. `ERGO_GRAPH_SEED` replays one seed. A failure prints the seed, the Ergo source and both traces. The test is full tier (`needsBuild`), and its doc comment says why it builds Go: the names under test are Go runtime type strings.
- `internal/sketch/lower/trace_run_test.go`, `internal/sketch/machinegraph/graph.go`: comments that described registration now describe the static graph.
- `runtime/v1/actor*_test.go`, `manual_settle_test.go`: call sites updated for the new argument. No assertions changed.
- `internal/sizes/cmd/sizes/main.go`, `internal/sizes/budgets.json`: new fixture `pairing` (`examples/pairing/cmd/pairing`, an actor program) with ceilings set to the measured value plus 2 %, and facts that forbid `runtime/v1.(RegisterMachine|RegisteredGraph|VariantOf)[` and `main.init.[1-9]`. The same two facts are added to `deploys`. No other ceiling changed.
- `docs/spec/runtime.md` (Machines and actors 15, item 4): the arm is read from the static graph the start is given, not from a registry. `docs/spec/stdlib.md` (Hidden arguments): new item 5 `GraphOf`, and `ContextPackage` moves to 6.
- Ledgers `internal/sketch/lower/testdata/mutants.json` and `runtime/v1/testdata/mutants.json`: 16 `bs7-*` rows.

## How to run

Use `umask 022`, `GOCACHE=<private>`, `GOMAXPROCS=1`, Go `-p 2`, and nice 19. Ad hoc runs use the 8G bounded scope from common.md, as BS6 did.

- Focused gate: `zsh /home/exedev/.cache/baton/wf10/binsize/bs7/gate.sh`. It runs gofmt of the changed files; vet and tests of `./internal/sketch/lower ./internal/sketch/machinegraph ./runtime/... ./cmd/ergo ./internal/sizes/...`; residue; `TestTestCommandsHaveCacheCoverage`; and sizes acceptance.
- Property (full tier): `go test -p 2 -count=1 -run TestGraphLoweringsTraceAlike ./internal/sketch/lower` (without `-short`; under `-short` `needsBuild` skips it). Replay one seed with `ERGO_GRAPH_SEED=N`.
- Reference CLI: `go build -p 2 -ldflags=-X=ergo.local/prototype/internal/sketch/lower.graphLowering=registry -o <out> ./cmd/ergo`.
- Examples in both modes, with `deploys` and `relay` emitted-module counts: `zsh /tmp/opencode/bs7/examples.sh` (a copy is kept as `bs7/examples.sh`).
- Measurements: `bs7/measure.sh <cli> <tag> pairing shards deploys relay`. It counts init functions and their bytes, registry symbols and graph symbols from `go tool nm`, plus `GODEBUG=inittrace=1` bytes and allocations for main. Everything is counted; nothing is timed.

## Gate and acceptance evidence

- `bs7/gate-1.log`: **`exit 0`** at `33cbc708` (gofmt clean; vet, tests, residue, cache coverage and acceptance all `check exit 0`).
- Acceptance at `33cbc708`: `go run ./internal/sizes/cmd/sizes` gives `bs7/accept.log` with **`sizes: 7 fixtures within budget`**, **`exit 0`**. Receipts are in `bs7/receipts.jsonl`.
- Red at base: the same budgets and fixture list run on the BS6 tree (`865614e5`) give `bs7/sizes-red-at-base.log` with **exit 1**: `pairing: fact "...(RegisterMachine|RegisteredGraph|VariantOf)\[": 12 linked symbols match` and `fact "^main\.init\.[1-9]": 1 linked symbols match, first main.init.1`.
- `bs7/examples-static.log` and `bs7/examples-registry.log`: each **`examples: passed; cases: 214`**, **`exit 0`**, at `33cbc708`. `bs7/examples.log`: deploys and relay emit 0 `RegisterMachine` lines and one `func init` in both modes. Neither reaches a machine in its run build (BS5 reachability already dropped deploys' machine at base), so `pairing` and `shards` are the programs where the difference shows.
- `bs7/mutants.log`: 16 of 16 killed, **`exit 0`** (a focused ledger replay; each edit was restored).
- Flakes: none.

## Tests and mutants

`TestGraphLoweringsTraceAlike` (new, the owner's lowering rule: the old path stays selectable, a seeded property compares the trace events, and every helper has a mutant). It catches:

- `bs7-graph-nil` (std.go passes nil for the graph);
- `bs7-main-qualifier`, `bs7-arg-comma` and `bs7-import-path` (`runtimeSpelling` gets the package qualifier, the type-argument separator or the bracketed import path wrong);
- `bs7-array-len`, `bs7-star`, `bs7-map`, `bs7-func-results`, `bs7-struct-fields` and `bs7-struct-empty` (`runtimeSpelling` misspells one form);
- `bs7-shared-case` (a shared pair gets a case) and `bs7-no-case` (an own pair gets none);
- `bs7-names-apart` (the bug fixed in `bafb5cf2`);
- `bs7-hidden-graph` (files.go drops the argument).

No existing law, property, example or test catches these. The examples run the static path only, and the trace tests before this unit compared against the graph that the same lowering wrote.

The existing runtime property `TestActorModelAgreesWithTimedFold` already catches `bs7-graph-ignored` (the actor ignores its graph) and `bs7-timer-lookup` (the slice timer lookup is wrong). These rows are new ledger entries only and add no test.

## Measured (linux/amd64, Go 1.27.0; interleaved base, head, registry, twice, identical both times: `bs7/measure.log`)

| Program | Base BS6 bytes / stripped | Head bytes / stripped | main.init funcs (B) | inittrace main |
| --- | ---: | ---: | --- | --- |
| pairing | 3297932 / 2166944 | 3284639 / 2158752 | 2 (6762) → 1 (188) | 7984 B 122 allocs → 0 B 0 allocs |
| shards | 3780764 / 2515104 | 3759033 / 2498720 | 4 (5962) → 1 (188) | 7376 B 116 allocs → 0 B 0 allocs |
| deploys | 2629336 / 1712288 | 2629336 / 1712288 | 1 → 1 | 0 → 0 |
| relay | 2336278 / 1507488 | 2336270 / 1507488 | 1 → 1 | 0 → 0 |

Head pairing links one graph (56 B) and shards three (168 B). The 2 "registry syms" that measure.sh counts at head are `sync.(*Map).Store` and `Delete`, linked by other runtime code. None of them is `RegisterMachine`, `RegisteredGraph` or `VariantOf` (that count is 0, checked by the sizes fact). The registry reference CLI reproduces the base cost (pairing 2 inits, 121 allocs).

## Prior art

Source comparison only; no new upstream experiment. Gleam (JS and Erlang) and ReScript emit module-level constant records, which the host lays out as static data. Neither registers metadata at load time, and ReScript's `js_dump` prints constant blocks inline. Elixir/BEAM keeps module metadata in the compiled beam's literal and attribute chunks (`__info__`, `module_info`), which are read on demand and never built by an initializer. This unit follows the same path in Go: the graph is a constant composite literal (no map, so the Go compiler emits no init code for it), and it reaches the runner through a function the start is given, so a program that never traces only carries the bytes. The type switch over a phantom pair type keeps Go type identity without `reflect` or a runtime table, as BS2 did for `typeID`.

## Known gaps and follow-ups (also in state.md)

- Flat signal unions (`flatunion.go`) trace the union's name, not the variant's, in both modes. This predates BS7, and the property avoids it by giving every signal union a generic variant. Follow-up: name the variant in the flat form's trace.
- `machinegraph`: two instances of one generic in one union get the same arm name. This predates BS7. Follow-up.
- The full tier, merge gate and Mac check belong to the batch. The property is full tier. It ran in this gate because the gate's package tests run without `-short` (`ok .../internal/sketch/lower 976.400s` in gate-1.log).
