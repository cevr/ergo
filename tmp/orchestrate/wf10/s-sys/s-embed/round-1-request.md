ready

Protocol: orchestrate implementor worker protocol carried in the prompt. Spec: `/home/exedev/.cache/baton/wf10/s-sys/brief.md`, item 2, no separate spec.md.
Head: `bb0f4cf849a4c76f6fd914ca1e23f50c47ee2f07`, branch `wf10/s-embed`.
Base: `1e63da6cec0ea40dac14664f4806be84954a1828`.
`git diff --stat 1e63da6c..HEAD | tail -1`: `34 files changed, 802 insertions(+), 10 deletions(-)` (measured).

## Changes by file

- `internal/sketch/check/embed.go`: predeclared embedded[T](pattern), a whole immutable package initializer, for string, []byte or embed.FS. `call.go`, `check.go`, `expr.go`, `info.go`, `wirecall.go`, `footprint.go` integrate checked facts. `extern_test.go` extends the existing sealed-protocol guard.
- `internal/sketchfront/embed.go`, `load.go`: package-relative patterns, positioned errors, link/escape/portable-path rejection, exact asset snapshots and root/imported build identity. `embed_test.go`, `testdata/mutants.json` guard both build modes and all three forms with a seeded property and diagnostic rows.
- `internal/sketch/lower/embed.go`, `decls.go`, `module.go`, `naming.go`, `tests.go`: native //go:embed plus asset files, no Go payload literals; private tree prefixes removed by io/fs.Sub. `blocking.go` records nonwaiting task ABI; `runtime199_mask_test.go` accounts for the new non-Close kind. `std_laws_test.go` runs the two embed laws.
- `internal/sketch/std/embed/embed.ergo`, `std.go`, `runtime/v1/embed.go`, `runtime/embed.go`: sealed read-only provider of fs.Files, empty tree and native read methods. The existing context-residue inventory in `internal/residue/testdata` updates its exact runtime source-list row.
- `examples/router/cmd/router/main.ergo`, `help/usage.txt`, `cases/help/{stdin,expected.out}`, `README.md`: help in the binary, same consumer under embedded/memory fs.Files, byte value semantics and tree metadata. `scripts/examples/main.go` accepts the loader's selected assets while rejecting stray files.
- `docs/spec/stdlib.md`, `GLOSSARY.md`, `docs/previews/2026-11-10-embedded-assets.md`: public syntax, laws, strengths and Go lowering facts.

## Checks, run and receipts

Gate log `/home/exedev/.cache/baton/wf10/s-sys/s-embed/gate-1.log`: final `EXIT 0`. Scripts under `/tmp/opencode/s-sys`: embed-gate.sh, embed-gate-resume.sh, embed-gate-finish.sh, embed-portable-gate.sh. Full reproduction is embed-gate.sh <log>, with ERGO_CLOSURE_JOBS=1 and a 30m watchdog for the optional lower sweep. The continuations retain completed results after a server restart and resolver-only follow-up.

- Touched-package short tests and vet, residue, TestTestCommandsHaveCacheCoverage, all three closure sweeps, TestStdLawsRun and router cases pass. The lower closure first exhausted an overall 10m watchdog; one retry at one observed worker/30m passed in 1639.679s. No assertion changed. Residue inventory and help newline failures were fixed in 1b287c14 and 7a05910f. Resolver guard fix is bb0f4cf8.
- Acceptance at head: `go test -p 2 -timeout 180s ./internal/sketchfront`, `SECTION EXIT 0`; `/tmp/opencode/s-sys/ergo test examples/router/cmd/router`, `PASS (1 tests)`, `SECTION EXIT 0`. Build CLI with `go build -p 2 -o /tmp/opencode/s-sys/ergo ./cmd/ergo`. Replay property with ERGO_EMBED_SEED=71.
- Eight mutants killed in the final gate: embed-missing-directive, embed-payload-not-keyed, embed-asset-dropped, embed-parent-escape, embed-local-initializer, embed-multiple-string-files, embed-tree-prefix-retained, embed-invalid-element. Initial seven also in s-sys/s-embed/mutants.log; final eight `EXIT 0` in gate-1.log. One initially invalid mutation was repaired and rerun, not counted as killed.
- Red at base 1e63da6c: copied TestEmbeddedAssetsSnapshot fails with unknown embedded / unknown embed package, not a Go compile error. `/home/exedev/.cache/baton/wf10/s-sys/s-embed/red-base.log`: `BASE TEST EXIT 1 (expected 1: embedded is absent)`. Portable-path diagnostic rows red at 7a05910f: portable-red.log, `RED TEST EXIT 1`.
- In-process binary facts at head: `bun /tmp/opencode/s-sys/size/receipt.ts`, size.log, `EXIT 0`. It reads executables, asserts payload presence and payload-sized growth within linker alignment. Payload 1024 -> 131072 bytes; Ergo executable 3013321 -> 3144409 bytes; Go control 2358348 -> 2489460 bytes. The Go floor omits Ergo's task/error entry; no absolute-size parity or speed claim.
- umask 022, nice 19, GOMAXPROCS=1, go -p 2, one private GOCACHE, GOMEMLIMIT=6GiB and inherited kernel RLIMIT_DATA 8GiB. Process checks show no lane child remains. Cache retained through S-atomic per brief.

## Test value and prior art

Loader/lowerer seam catches lost payloads, directives, stale revisions and positioned refusals; it misses native storage and ABI, covered by the binary receipt and example. The fs.Files example catches provider bypass, retained prefixes and slice aliasing. No production test-only hook was added; no tests were deleted.

Effect FileSystem's intent is replaceable reads; it has no build-time embedding module. fs.Files, typed failures, laws and context replace service tags/layers (W4/W5) and call-chain encodings (W14). The Go control is //go:embed, embed.FS and io/fs.Sub. Inferred from standing sources: Gleam/ReScript use host asset tooling and BEAM/JS lowering; Elixir external resources track compile dependencies but release assets often stay separate. Ergo keys bytes as checked inputs and lets Go link them. No displaced speed lowering or optimization claim.

## Known gaps

Pass a package directory. A raw source-text/single-file check has no asset loader and explicitly refuses embedding. The portable subset rejects bracket filenames rather than re-globbing selected files. Full/merge/conformance and Darwin belong to the batch. No verify-app feature map exists here. common.md lines 42–44 ask for worker-created scopes, contrary to the carried protocol: used kernel data limits without a launcher instead. Immutable tree failures are NotFound; the shared fs.Files signature retains its full typed row.

Extended request and state: `/home/exedev/.cache/baton/wf10/s-sys/s-embed/round-1-request.md`, `/home/exedev/.cache/baton/wf10/s-sys/s-embed/state.md`.
