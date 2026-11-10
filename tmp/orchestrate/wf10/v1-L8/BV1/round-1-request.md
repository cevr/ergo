ready

Protocol: supplied orchestrate worker/implementor protocol (in the implementor prompt).
Spec: `/home/exedev/.cache/baton/wf10/v1-L8/brief-BV1.md` (the unit spec; supersedes the misnamed brief-IG11.md).
Head: `a8140bac2649736016741e9db93621fc625b895f` (implementation head, branch `wf10/bind-rvars`, worktree `/home/exedev/Developer/personal/.worktrees/ergo-bs`). A request-only commit follows; no rebase or push.
Base: `00c4325c84d155ce9942f915542c9f5448643748` (X-L8).
`git diff --stat 00c4325c..a8140bac | tail -1`: `12 files changed, 222 insertions(+), 40 deletions(-)`.

## What changed (measured)

- `internal/gobind/map.go`: admits variables whose actual Go method set has `Close()` or `Close() error`; ordinary host state stays refused, and the normal type/refusal closure still applies. Package pointers use `Option[go.Ref[T]]`, not a non-nil promise.
- `internal/gobind/bind_test.go`: extends `TestBindMappingProperty` with os.Stdout, generated value/pointer method sets, valid and wrong Close signatures, and named interface variables. The reference is the interop table, not the mapper.
- `internal/gobind/testdata/mutants.json`: four admission/type-mapping mutants.
- `internal/sketch/check/expr.go`: reading a borrowed resource variable charges `go.Foreign`, even when no method is called. Ownership still uses IG9's existing borrowed-package-let rule.
- `internal/sketch/lower/expr.go`: snapshots a borrowed pointer variable once and uses the existing Go result mapper to produce None or Some. No release is registered.
- `internal/sketch/lower/host_resource_test.go`: replaces the hand-written binding with `gobind.File` over the fixture's declarations, in process. The existing seeded verdict/runtime properties now match and lend both nil and non-nil bound variables; hand-offs and Close of borrowed match bindings are refused. A bare read without authority is S102.
- `internal/sketch/lower/static_release_test.go`: passes t to the updated importer; its existing property still exercises both release lowerings with the host-resource generator.
- `internal/sketch/lower/testdata/mutants.json`: authority-read and borrowed-pointer-lowering mutants.
- `internal/sketch/std/gostd/src/os/os.ergo`: regenerated Stdin, Stdout and Stderr bindings.
- `internal/sketch/std/gostd/src/net/http/http.ergo`: regenerated NoBody refusal (its host type is unexported).
- `docs/spec/interop.md`, `CHANGELOG.md`: borrowed admission, nullable pointers, authority and unchanged non-resource refusal.

## How to run

Use umask 022, A2 (`GOMAXPROCS=1`, Go `-p 2`), nice 19, `GOMEMLIMIT=6GiB`, and one private GOCACHE. All ad hoc checks ran in an 8G, zero-swap systemd scope. This follows common.md:44's explicit Departs-from line for the worker no-scope rule.

Acceptance (run uncached at the implementation head, measured):

```sh
go test -p 2 -timeout 20m -count=1 -run '^(TestHostResourceVerdicts|TestHostResourceRun|TestStaticReleaseAgrees)$' ./internal/sketch/lower
go test -p 2 -timeout 20m -count=1 ./internal/gobind
```

Both acceptance commands: `STEP_EXIT=0` in `gate-1.log`'s final run at a8140bac. `TestHostResourceRun` uses the existing one-binary helper and kernel memory bound: execution is needed to observe Go Close and nil branches, not merely Go type correctness.

## Gate and red evidence (measured)

- Round gate: `/home/exedev/.cache/baton/wf10/v1-L8/BV1/gate-1.log`, final **`GATE_EXIT=0`**. Script: `gate-1.sh`. Includes diff/gofmt, vet of gobind/check/lower, all gobind tests, checker/lower short suites, the acceptance above, residue, gate cache coverage, and gobind's closure sweep (`ERGO_CLOSURE_JOBS=1`).
- The first gate found an unregenerated net/http refusal. Commit a8140bac fixes it; the entire focused gate then passed. No flake or assertion weakening.
- `red-1.log`: `go run -p 2 ./cmd/ergo bind go os` at base emits `ergo:refused Stdout: a Go variable is host state; call a function` (the CLI correctly exits 0 for a binding containing refusals).
- `red-property-1.log`: the expanded `TestBindMappingProperty` on base production code: **`RED_EXIT=1`**, including Stdout refused instead of `Option[go.Ref[os.File]]`.
- `red-authority-1.log`: the extended `TestHostResourceVerdicts` before the authority fix: **`RED_EXIT=1`**, seed 1 sees no diagnostic on a bare variable read where S102 is required.
- `mutants-1.log`: **`MUTANTS_EXIT=0`**, six judged, six killed. This base's official runner has no gobind suite, so `bun <unit dir>/mutants.ts` runs the named ledger killers serially on a private git archive, restores each edit, and drains each private process group. No production file is mutated.
- After the process-starting checks, ps showed no children or remaining process group owned by the completed job. Private cache and archive are removed by `cleanup.ts` before READY.

## Test value and mutants

No new top-level test. The mapping property newly kills refusal of resource variables, wrong Close results being admitted, and nullable variables being mapped as non-null handles; it retains the existing negative control for ordinary state. The verdict property newly kills a resource variable read that needs no authority. The runtime property newly kills a raw pointer cast to Option. Existing IG9 ownership guards now run through generated bindings instead of a fixture that bypasses admission. No production-only test seam was added.

## Known gaps and map update

The full/merge/macOS gates remain the batch's work. No performance claim. Unmappable resource host types remain refused (for example net/http.NoBody's unexported type). No new mutable-variable API or ownership exception is introduced. BV1's admission, borrowed-use, refusal and generated-binding acceptance clauses are covered; no HITL or tracker state was changed.

## Prior art (inferred from the indexed sources, not a new upstream audit)

Gleam and ReScript lower written host externals directly and leave host handle lifetimes to host/library conventions. Elixir/Erlang ports use process-owned cleanup. MoonBit's FFI uses written signatures and its Option layout can use a null sentinel. Ergo reuses Go declarations and the existing Option boundary conversion, but keeps the borrowed lifetime in the checker. No new wrapper/runtime release path is needed.
