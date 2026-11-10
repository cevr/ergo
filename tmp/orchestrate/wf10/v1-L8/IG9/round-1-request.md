ready

Protocol: supplied orchestrate worker/implementor protocol (in the implementor prompt).
Spec: `/home/exedev/.cache/baton/wf10/v1-L8/plan.md`, IG9 (section "After the lanes", line 288) and rows Q9, Q10 of section 2; `/home/exedev/.cache/baton/wf10/import-go/plan.md` IG9 (line 258).
Head: `6cce69d6` (branch `wf10/ig9`, worktree `/home/exedev/Developer/personal/.worktrees/ergo-l8b`). No rebase, no push.
Base: `235bc8a8` (C1 head). `git diff --stat 235bc8a8..6cce69d6 | tail -1`: `18 files changed, 843 insertions(+), 19 deletions(-)`.

Owner answers used: q1 (no merge of main; IG5 is not in this base; IG9 uses G4's `go.Error`), q2 (checker rule only for Go package variables; `ergo bind go` keeps refusing non-error variables; admitting `os.Stdout`-like variables is a follow-up).

## What changed, by file (measured)

- `internal/sketch/check/hostres.go` (new): `hostClose`, `hostCloseFunc`, `hostCloseOp`, `hostRefClose`: a Go type whose method set has `Close()` or `Close() fails go.Error` (binding methods list no receiver parameter), `go.Ref[T]` of one, and a Go interface with such a `Close`; `Close.HostClose`, `Close.HostFallible` for the lowering; `hostLendArg`.
- `check/resource.go`: rule 6 of the crossing walk: a Go type with that Close does not cross (was: every Go type crosses).
- `check/drop.go`: the Drop of a Go value is Go's Close (a Func, or the Op of a Go interface).
- `check/close.go`: a call of a Go `Close` records a Close fact (so it is a take, R19); a Go interface's `Close() error` operation too.
- `check/ownership.go`: a qualified package let (`P.V`, an imported package's or a Go package variable) handed off is S108 (`qualifiedLet`, `pkgLetMove`); before, only an unqualified package let was refused. A Go function's argument converted to a Go interface parameter is a lend, not R23's move.
- `check/call.go`, `check/check.go`, `check/typeops.go`: `c.hostCall` while a Go call's arguments are checked; R22 does not refuse a resource lent to a Go interface parameter there (`io.Copy(w, f)` keeps checking).
- `lower/release.go`: `hostRelease`: `v1.Release(task, func() { v1.CloseFailed("<file:line:col of the acquisition>", (v).Close()) })` (the callerRelease route; no new release route), or the bare call for `Close()`.
- `lower/blocking.go`: binding `CloseFailed` (no wait, no release route).
- `lower/expr.go`: a Go package variable read lowers to `(T)(P.V)` (the old `T(P.V)` did not parse for `*T`).
- `runtime/v1/task.go`: `CloseFailed(at string, err error)`; `OwnErr` uses it (one text).
- `lower/support_test.go`: `lowerSourceWith` (an importer) and `lowered.extra` (Go files beside a program in the test module).
- `lower/host_resource_test.go` (new): the synthetic Go package `hostres` (source and binding) and two tests (below).
- `lower/testdata/mutants.json`: four IG9 mutants.
- `docs/spec/interop.md` (new section Resources), `docs/spec/resources.md` R1, `CHANGELOG.md`.

## Tests (AGENTS.md Tests rule)

- `TestHostResourceVerdicts` (in process, fast tier, 64 seeds): each generated body (open, use, lend to a Go interface, explicit `try h.Close()`, `keep(h)` that takes, the package variable's lend, nested blocks) checks clean; the same body with one injected violation is S108 at the injected line (a use after `Close` or the hand-off, a take of the package variable, a discarded handle). Mutants only it kills: "a Go type with Close() is no resource", "a Go package variable may be taken", "R22 refuses a lend to a Go interface parameter".
- `TestHostResourceRun` (full tier, `needsBuild`, 24 seeds, one dispatcher binary): the same bodies run against `hrModel` (LIFO per scope, failure and defect edges, R9); the package variable is never closed; each failing automatic Close is a defect whose report has `release of the value acquired at /ergo-test/main.ergo:<acquisition line>:`. It builds Go because the subject is Go's Close on the emitted exit edges with its error turned into a defect (doc comment says so). Mutant only it kills: "a Go Close error dropped at an automatic release".
- The synthetic package's import path is `hostres` (no dot): the lowering imports a non-standard Go package only after IG7, so the test module requires it through its own `replace` (`hresFiles`). Inferred: when IG7 lands, the path can become a module path.
- A failing seed prints the Ergo source, both outputs and `ERGO_SEED=n`.

## How to run

```sh
umask 022
systemd-run --user --scope -q -p MemoryMax=8G -p MemorySwapMax=0 -- nice -n 19 env GOMAXPROCS=2 GOMEMLIMIT=6GiB GOCACHE=<private> go test -p 2 -timeout 20m -count=1 -run '^TestHostResource' ./internal/sketch/lower
```

## Checks (measured)

- Round gate: `/home/exedev/.cache/baton/wf10/v1-L8/IG9/gate-1.log`, **`GATE_EXIT=0`** (gofmt of changed Go files; vet and `-short` tests of check, lower, runtime/v1; the acceptance tests; `./internal/residue`; `TestTestCommandsHaveCacheCoverage`). Run on the tree that became 6cce69d6 (same content; the commit followed).
- Acceptance: `-run '^TestHostResource' ./internal/sketch/lower` uncached at head: exit 0 (in gate-1.log).
- Red at base: `red-1.log`, the two tests copied onto an archive of 235bc8a8: **`BASE_TEST_EXIT=1`** (at base `takes h has no effect: go.Ref[hostres.Handle] is not a resource`).
- Mutants: `mutants-1.log`, `go run ./scripts/mutants -run '^IG9' lower`: 4 judged, 4 killed, **`MUTANTS_EXIT=0`**.
- Closure sweep: not run; the new tests read no files.
- Share C (`GOMAXPROCS=2`, `-p 2`), umask 022, GOMEMLIMIT=6GiB, one private GOCACHE, every ad hoc run in the 8G zero-swap scope (common.md "Departs from" the no-worker-scope rule; followed as told).

## Known gaps

- `lower:TestTimeoutDeadlineIsTheProgramScheduler` fails at base 235bc8a8 too (measured on an archive of the base; the mutants tool reports it fails on the unmutated tree). Not IG9's; it does not touch timeout lowering.
- A Go `Close` called by an automatic release charges no `go.Foreign` in the releasing function's row: the value came from a function that had it. Inferred acceptable (go.Foreign is an authority marker, not a runtime provider); a later unit may charge it.
- Go code that keeps a lent argument past the call (`bufio.NewReader(f)`) is outside what a binding states; the owner still releases at its scope end (interop.md Resources 5).
- `ergo bind go` still refuses non-error Go variables (q2): the borrowed-variable rule is reached by a module binding only.
- Generic Go functions copy each type parameter by A3's existing D-B rule (`copiesOf`); no new test, since A3's tests own that rule.

## Prior art

Rust `File` closes on drop and drops the close error; Swift `~Copyable` `FileDescriptor` needs a written close; Go's vet has no check for a leaked `*os.File`. Gleam, Elixir and ReScript have no ownership of host handles (a BEAM port closes with its process). Kotlin/Native cinterop maps C types with no release tracking.
