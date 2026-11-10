ready
Protocol: supplied orchestrate worker protocol, implementor; wf10/common.md.
Spec: the orchestrator's BS7b start message (fix the two trace-naming follow-ups of /home/exedev/.cache/baton/wf10/binsize/bs7/round-1-request.md, Known gaps), plus the answer to q1 (fix the third bug the new oracle found).
Head: a8076b82dac9204e48bad504dc71fa440314726a (code and acceptance head).

# BS7b round 1: traced actors name the right variant and the right arm

## Commits

- Branch `wf10/bs7b`, worktree `/home/exedev/Developer/personal/.worktrees/ergo-bs`. Base BS7 `19db963f`; code head `a8076b82`. No rebase, amend or squash.
- `git diff --stat 19db963f..a8076b82 | tail -1`: `10 files changed, 360 insertions(+), 62 deletions(-)`.
- `4d2d6e92` fix(machinegraph): the three fixes and the spec text; `c0400a64` test(lower): the property's new oracle; `5b5e25ad` test(lower): the generator reaches a whole pattern after its partial one in one case; `a8076b82` test: ledger rows.
- A later commit that only adds this request carries it at `tmp/orchestrate/wf10/binsize/bs7b/round-1-request.md`. It changes no checked source. The verifier replays at `a8076b82`.

## The bugs (all three were present before BS7b, in both graph lowerings)

1. **Flat signal union named by its union** (measured, red at base). A flat union's Go type is one struct named after the union, so the runtime read `Sg1` and not `X1` from a signal value. Then no arm that names a signal matched.
2. **Two instances of one generic in one union had one name** (measured through mutant `bs7b-generic-name`). `machinegraph.Name` gave `Dup` for both `Dup[[]int]` and `Dup[float64]`. The lowering keys its Go-name maps by that name, so one instance took the other's Go name, and a decision named a wrong arm.
3. **A case of several tuple patterns was read as a product** (found by the new oracle; fixed in this unit as the orchestrator answered for q1). `case (Box, Wrap), (_, Xb):` was read as "any state × {Wrap, Xb}". So `(Ab, Wrap)` named this arm, but the match takes a later arm.

## What changed, by file

- `runtime/v1/actor_trace.go`: `variant` asks the value for `ErgoVariant() string` before it falls back to the Go type's name. `(*MachineGraph).arm` names a site once when consecutive runtime arms share it (the patterns of one case).
- `internal/sketch/lower/flatunion.go`: `flatDecl` writes `func (x Sig) ErgoVariant() string { return [...]string{"A", "B"}[x.tag] }`. The names come from `variantName` (the same rule as the static graph). This is static data with no init code. The method is linked only where an interface call of it can be reached (the trace path of an actor program). The `sizes` budgets are unchanged and pass.
- `internal/sketch/machinegraph/graph.go`: `Name` adds the type arguments in Ergo spelling (`Box[Int]`, from check's `Type.String`). New `Alt` and `Arm.Alts` (JSON `omitempty`) hold each pattern of a case of several tuple patterns. `Arm.States` and `Arm.Signals` stay the union, so `ergo inspect machine` text does not change. New `Arm.Takes` is used by `ArmOf` and `Reachable`.
- `internal/sketch/lower/machine.go`: `machineGraph` writes one `MachineArm` per pattern at the arm's site. Both lowerings (static and `graphLowering=registry`) share this code, so the fixes apply to both.
- `docs/spec/runtime.md`: Machines and actors 15, items 1 and 4.
- Tests: `internal/sketch/lower/graph_lowering_test.go` and `internal/sketch/machinegraph/graph_test.go` (see below).
- Ledgers: `internal/sketch/lower/testdata/mutants.json` (2 rows), `runtime/v1/testdata/mutants.json` (2 rows), and a new `internal/sketch/machinegraph/testdata/mutants.json` (3 rows).

## Tests and mutants

No new test function. Two existing properties are made stronger:

- `lower.TestGraphLoweringsTraceAlike` (seeded, `ERGO_GRAPH_SEED` replays a seed, and a failure prints the source and both traces). The generator now also writes signal unions with no generic variant, so they lower flat. It also writes two instances of one generic (`Dup[a] | Dup[b]`), and a whole pattern after its partial one in one case. New checks: no event names a union (`St<n>`/`Sg<n>`); the arm that a decision names gives that decision's kind (the generator records each case line's kind); and no site is named twice. The property stops at the first failure for each seed.
- `machinegraph.TestGraphMatchesTheModel`: a generated arm can have a second tuple pattern. The model checks `Alts`, `States`/`Signals` (the union) and `Reachable` (it takes a pair only when one pattern takes it). It must reach "two patterns".

These mutants were all killed, and no test before BS7b killed any of them (`bs7b/mutants.log`, **7 of 7 killed**, `exit 0`; each mutant was applied alone, and a mutant counts as killed only when every killer package reports `--- FAIL:`):

| Mutant | Edit | Killers |
| --- | --- | --- |
| `bs7b-flat-name` | the flat union's method is not `ErgoVariant` | lower property |
| `bs7b-variant-self` | the runtime ignores `ErgoVariant` | lower property |
| `bs7b-generic-name` | `Name` drops the type arguments | lower property |
| `bs7b-alts-dropped` | machinegraph keeps no `Alts` | machinegraph model, lower property |
| `bs7b-alts-lowered` | the lowering writes the product arm | lower property |
| `bs7b-takes-alts` | `Arm.Takes` ignores `Alts` | machinegraph model |
| `bs7b-arm-once` | the runtime names a shared site twice | lower property |

## How to run

Use `umask 022`, a private `GOCACHE`, `GOMAXPROCS=1`, Go `-p 2` and nice 19, in the 8G bounded scope from common.md. The protocol's rule against worker-made scopes is overridden by common.md's memory rule, which this unit follows.

- Focused gate: `zsh /home/exedev/.cache/baton/wf10/binsize/bs7b/gate.sh`.
- Property (full tier, built Go): `go test -p 2 -count=1 -run TestGraphLoweringsTraceAlike ./internal/sketch/lower`. Under `-short`, `needsBuild` skips it.
- Model: `go test -count=1 -run TestGraphMatchesTheModel ./internal/sketch/machinegraph`.
- Mutants: `bun /home/exedev/.cache/baton/wf10/binsize/bs7b/mutants.ts`.

## Gate and acceptance evidence

- `bs7b/gate-1.log`: **`exit 0`** at `a8076b82`. gofmt is clean. vet passes. Tests of `./internal/sketch/lower` (845 s, property included), `./internal/sketch/machinegraph`, `./runtime/...`, `./cmd/ergo` and `./internal/sizes/...` pass. `./internal/residue` and `TestTestCommandsHaveCacheCoverage` pass.
- Acceptance: the stronger property is green at the head (inside gate-1). Red at base: `bs7b/red-at-base.log`, **`exit 1`**. That run is the final test file on the BS7 code (`19db963f`), and in each of the six seeds an actor names a union (`signal=Sg1`). Bugs 2 and 3 are red at base through `bs7b-generic-name` and `bs7b-alts-dropped`, which restore the base code of each.
- `bs7b/accept.log`: `sizes: 7 fixtures within budget`, `exit 0`.
- `bs7b/examples.log`: **`examples: passed; cases: 214`**, `exit 0`, run with the head CLI (static graphs).
- Flakes: none.
- After each run, `ps` showed none of my processes left.

## Prior art

This is a source comparison only. Gleam and ReScript lower a variant to a tagged value and keep the constructor name as data: ReScript's `TAG` field and Gleam's record class on JS. So their debug output names the constructor, not the type. This unit gives the flat struct the same thing, a name per tag held in a constant array, and does not use runtime reflection. Elixir/Erlang `case` clauses with several patterns, or with guards, are separate clauses in the compiled core, and a clause is a whole pattern. They are never a product of positions. `Arm.Alts` keeps the same unit. OCaml's or-patterns `(A, X) | (B, Y)` have the same semantics, and its pattern-match compiler expands them before it builds the decision tree.

## Known gaps

- `Arm.Takes`'s partial result for a multi-pattern case (a pair that only partial patterns take) has no killing test, because the machinegraph model generates no partial patterns. It is used only by `Reachable` (inspect coverage) and is not ledgered.
- The full tier, the merge gate and the Mac check belong to the batch.
- Only the static mode of the examples ran in this round. The registry mode shares the changed code (`machineGraph`, `flatDecl`), and the property runs both modes.
