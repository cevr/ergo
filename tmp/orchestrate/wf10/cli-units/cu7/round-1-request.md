ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief-2.md`, C7, round 1 (the brief is the spec; no separate spec.md).
Head: `546311db6cfcbcd5f5b13b34088f4c932c9b3c15` (implementation; request commit follows).
Base: `2cd26ac7` (CU3 request head). Branch: `wf10/cu7`. Workspace: `/home/exedev/Developer/personal/.worktrees/ergo-v1b`.
Measured diff: `16 files changed, 8447 insertions(+), 4 deletions(-)` (`git diff --stat 2cd26ac7..546311db | tail -1`). Most added lines are the public corpus.

## Changes by file

- `internal/sketch/std/toml/toml.ergo`: `Decode(c json.Codec[T], text) T fails Malformed | wired[T]` and `Open`. Reuses the same codec, field mapping, Reader derivation, refinement checks and ParseX functions as json. Syntax/limit failures have `Malformed{Line, Key, Reason}`. Derived shape/refinement failures retain the shared codec's paths and typed rows. No encoding API.
- `runtime/v1/toml.go`: self-contained TOML 1.0 recursive-descent parser. Distinguishes implicit, dotted, explicit, inline and array tables; validates UTF-8, strings/escapes, numbers and dates; checks byte/depth limits. Syntax failures use an ordinary first-error state, never panic/recover. Writes validated JSON for the existing Reader. No external module, reflection, unsafe, package-init parser work or host timezone lookup. Date/time values are canonical strings; nonfinite floats use json's existing mapping. Finite float tokens stay floats, including 1.0.
- `internal/sketch/std/toml/laws.ergo`, `internal/sketch/lower/std_toml_test.go`: generated-record law and a refinement property through public Decode. Replay: `ERGO_TOML_SEED=N`; failure prints source, generated case and outputs. The test uses the one-binary helper and kernel-bounded runner, because a typecheck cannot observe record values.
- `runtime/v1/toml_test.go`: public parser boundary against the toml-test corpus; minimal diagnostic rows for line/key bugs; a declared parser/hand-Go-control benchmark (not run here).
- `runtime/v1/testdata/toml-test/{corpus.json,LICENSE,README.md}`: public toml-test at `96fd0884`, selected by its TOML 1.0 manifest. Sources are byte-exact base64, including invalid UTF-8. Valid rows retain upstream JSON values; invalid rows have no expected value. No count or digest is pinned by the test. No upstream parser code is copied.
- `runtime/embed.go`, `runtime/v1/files.go`, `internal/sketch/std/std.go`, `internal/sketch/lower/blocking.go`: source retention, the Malformed record binding, package embedding, and explicit no-wait runtime binding. No new checker rule or format-specific derivation branch.
- `docs/spec/stdlib.md`: API, mapping/limits, source law ledger, strengths row and costs. Both mutant ledgers name the new guards. `internal/residue/testdata/wire-residue.txt` keeps the existing embed-list exception current and admits the ordinary codec constraint line.

## Run and evidence

Share D: GOMAXPROCS=2, Go -p 2, GOMEMLIMIT=6GiB, umask 022. One private GOCACHE: `/tmp/opencode/cu237/gocache`, removed at final handoff. Ad hoc checks follow common.md lines 42–44's explicit bounded-scope departure. One heavy check ran at a time. Process inspections found no owned child left.

Round gate at head: `sh /tmp/opencode/cu237/run.sh sh /tmp/opencode/cu237/gate.sh cu7 2cd26ac7 '^TestTOMLDecode$' '^Test(TOMLDecode|StdJSONLawsRun|Runtime199(NoWaitBindings|TaskReaders|BlockingOwner|DefectOwner|DefectRecovery))$'`.
Log: `/home/exedev/.cache/baton/wf10/cli-units/cu7/gate-1.log`. Exit lines: `ACCEPT_EXIT=0`, `GATE_EXIT=0`, `ROUND_GATE_EXIT=0`.
The focused gate checks changed Go/Ergo formatting, vets runtime/v1, lower and std, tests std, residue, runtime/v1 and runtime, checks gate cache coverage, and runs the focused lower tests. No full or batch gate was run.

Acceptance at head: `go test -p 2 -count=1 -timeout 30m -run '^TestTOMLDecode$' -v ./internal/sketch/lower` → `--- PASS: TestTOMLDecode`, `ACCEPT_EXIT=0`. Runtime corpus/diagnostics run in the gate's `go test -p 2 -count=1 -timeout 30m ./internal/sketch/std ./internal/residue ./runtime/v1 ./runtime` → `STEP_EXIT=0` (includes TestTOMLCorpus and TestTOMLFailuresNameLineAndKey). All are measured at 546311db.
Red: `cu7/red.log`, `ergo check /tmp/opencode/cu237/toml-red.ergo` at CU3 implementation head `3111d27f` → `unknown package "toml"`, `BASE_CHECK_EXIT=1`. The final base adds only its request update.
Closure: `ERGO_CLOSURE_SWEEP=runtime/v1 go test -p 2 -count=1 -timeout 20m -run '^TestEveryPackageRecordsOnlyItsClosure$' ./internal/closure` → `CLOSURE_EXIT=0` (`cu7/closure.log`). Later diagnostic fixes add no file reads.

Mutants: `cu7/mutants.log`, `MUTANTS_EXIT=0`: all seven killed. `TestTOMLCorpus` uniquely kills duplicate keys accepted, nested array-table headers using the first element, invalid numeric underscores, and multiline closing quotes lost. `TestTOMLFailuresNameLineAndKey` uniquely kills missing error locations and the invalid newline attributed to the next line. `TestTOMLDecode` uniquely kills Decode discarding the parsed document (the runtime corpus cannot see the std wrapper). Both ledgers record their guard. No existing source test was deleted.

## Intent, prior art and decisions

Effect ConfigProvider's intent is typed configuration from external input; Effect core has no TOML parser. Ergo takes that purpose, not Config/Schema object builders (W6), service tags/layers (W4), tagged error trees (W5), or a runtime Effect instruction per decode (W14). Declared records, shared derivation, typed rows and ordinary functions replace these. Decided by derive-dont-sync and NORTH_STAR's one-inspectable-contract principle: reuse json.Codec, not a second schema. Hand scanners avoid retaining parser initialization in a program that never uses TOML.

Primary docs checked: Gleam tom 2.1.0 (`https://hexdocs.pm/tom/tom.html`): pure parser, explicit Toml alternatives, dynamic decoders for records; Elixir toml 0.7.0 (`https://hexdocs.pm/toml/Toml.html`): hand-written decoder, maps/date-time structs, bottom-up transformers, toml-test; BurntSushi/toml docs (`https://pkg.go.dev/github.com/BurntSushi/toml`): reflection into typed Go records and optional metadata/strictness. Ergo instead keeps its existing compile-derived decoder. Gleam lowers ordinary library calls to its selected host; Elixir uses BEAM; ReScript uses host TOML libraries and direct uncurried JS calls (standing PRIOR_ART comparison). MoonBit's derive is the standing comparison for record decoding; no TOML-specific compiler rule is needed here.

Hand-Go control: the same parser followed by encoding/json into an equivalent config record (`BenchmarkTOMLParse/GoControl`). No benchmark ran on the shared host and no speed or parity claim is made. The parsed tree plus JSON transcode and generic Reader adapters are an inferred cost gap; direct Reader events are the follow-up. Quiet-window command: `cu7/bench-pending.md`.

## Known gaps and map update

- TOML 1.1 additions and encoding are outside C7. Date/time values use canonical strings, not new date/time types; this is a documented representation policy.
- Source line/key is on syntax/limit failures; the existing derived shape/refinement failures use codec paths. No undeclared failure or missing service is admitted.
- The law renderer initially emitted literal DEL, legal JSON but not legal TOML. The renderer was fixed to escape it; the parser kept rejecting it. These were deterministic development failures, not flakes.
- The initial gate (`gate-1-initial.log`) caught the parser's private syntax panic/recover as a defect-owner violation. Fixed in 546311db by explicit error state/returns, not an exemption. Both failing guards remain in the final focused gate; unrelated generated runtime199 process properties are not part of that narrower rerun.
- No macOS run here; it is the batch's check. No branch was rebased or pushed. CU2/CU3 have their own requests and passed focused gates.
- C7's corpus and generated-record acceptance clauses are tested. Suggest marking the decoder portion ready, with encode, TOML 1.1 and a direct Reader/performance follow-up left separate.
