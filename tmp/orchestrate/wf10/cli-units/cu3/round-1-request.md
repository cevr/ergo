ready

Protocol: supplied implementor worker protocol (not protocol.md or safety.md).
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief-2.md`, C3, round 1 (the brief is the spec; no separate spec.md).
Head: `3111d27fe74dfedad412da9e37a20aaecd82d45b` (implementation and forward merge of CU2's accepted ioctl restriction).
Base: `a98b130a`. Branch: `wf10/cu3`. Workspace: `/home/exedev/Developer/personal/.worktrees/ergo-v1b`.
Measured diff: `28 files changed, 1301 insertions(+), 20 deletions(-)` (`git diff --stat a98b130a..3111d27f | tail -1`).

## Changes

- `internal/sketch/std/prompt/{prompt,machines,ask,laws}.ergo`: text, secret, select, multi-select and confirm prompts. Four machines step on the `term.Key` union; each has a pure frame. `Ask` runs a machine inside `term.Raw`, writes frames to stderr, restores terminal state on every exit, and names `NotInteractive`, `Cancelled` and `io.ReadFailed` in its row. Convenience functions construct the machines.
- `internal/sketch/lower/{naming,std}.go`: bundled machine names are package-qualified and machine metadata reaches ordinary std lowering. Without this fix a std machine has an unnamed Go type or an internal compiler error. No speed lowering is introduced.
- `internal/sketch/lower/std_prompt_test.go`: `TestPromptLaws` runs the five std laws, seeded independent reference models of all four machines, and Ask with a scripted terminal (masked frames, restoration, cancellation, non-interactive failure). Replay is `ergo test --seed N` on the probe printed by a failure.
- `examples/starter/`: neutral project-plan CLI, flags or an interactive interview, a plan machine and laws, scripted test providers. Domain contracts are in `plan`; terminal implementation is in `terminal`; `cmd/starter` alone chooses providers.
- `internal/sketch/std/std.go`, lower and examples mutant ledgers, `docs/spec/stdlib.md`: package listing, API, laws, intent and lowering limits.
- Forward-merged CU2 q1: its typed ioctl boundary and `tools/provenance/runtime-unsafe-allow.txt` record the accepted host-edge exception. CU2's own request covers that change. CU3's gate was refreshed on this merged head.

## Run and evidence (measured)

Share D: GOMAXPROCS=2, Go -p 2, GOMEMLIMIT=6GiB, umask 022, one private cache `/tmp/opencode/cu237/gocache`. Checks used the explicit common.md lines 42–44 bounded-scope departure. One heavy check ran at a time; process inspections found no owned child left.

Round gate at head: `sh /tmp/opencode/cu237/run.sh sh /tmp/opencode/cu237/gate.sh cu3 a98b130a '^TestPromptLaws$' 'TestTerm|TestPromptLaws|TestStdLawsRun|TestMachine|TestFlagTree|TestIO' starter tui keypad`.
Log: `/home/exedev/.cache/baton/wf10/cli-units/cu3/gate-1.log`. Exit lines: `ACCEPT_EXIT=0`, `GATE_EXIT=0`, `ROUND_GATE_EXIT=0`.
It checks changed Go/Ergo formatting, vets runtime/v1, lower and std, tests std, residue, runtime/v1 and runtime, tests gate cache coverage, runs the focused lower tests, and runs starter, tui and keypad recorded cases. No full or batch gate was run.

Acceptance at head: `go test -p 2 -count=1 -timeout 30m -run '^TestPromptLaws$' -v ./internal/sketch/lower` → `--- PASS: TestPromptLaws`, `ACCEPT_EXIT=0`.
Red: `cu3/red.log`: the probe on ec6f1be2 fails with `unknown package "prompt"` (and term, introduced by CU2). The prompt package is absent at the immediate base too.
Mutants: `cu3/mutants.log`, `MUTANTS_EXIT=0`: nine std prompt mutants, two starter mutants, two std-machine lowering mutants, all killed. New guards in `TestPromptLaws`: non-terminal mapped to Cancelled, select passing the end, secret shown, marker toggling all, default ignored, upper-case Y ignored, backspace dropping a byte, redraw omitted, and marked answers duplicated. Earlier attempts at backspace and reversed answers survived; strengthened input and the duplicated-answer mutant were rerun and killed (the log retains both attempts). The two lower mutants omit std-machine naming or metadata. Starter's cases kill git being ignored and a feature-file mapping being swapped. The ledgers name each replacement and its guard. No existing source test was deleted.

## Intent, prior art and choices

Effect cli/Prompt's purpose is to ask a person for a typed answer. Ergo takes text/password/select/multiSelect/confirm behavior, not Prompt instruction values interpreted by a runtime (W14), Terminal service tags (W4), tagged QuitException objects (W5), or Schema classes (W6). Machines, ordinary functions, checked rows, unions and exhaustive match replace these. No API shape is copied for its own sake.

Prior art: Charm huh models a field as a Bubble Tea state machine; Owl.IO and Gleam survey read lines, with validation in library code; Node readline supplies a callback interface. ReScript uses host libraries and direct uncurried JavaScript calls. Elixir lowers the library calls to BEAM; Gleam uses its selected host. Ergo lowers steps and frames to ordinary Go functions and Ask to a key loop with one terminal scope. Its hand-Go control is that same switch/key loop with deferred restore. No measured speed or parity claim; union/interface and generic call costs remain follow-ups.

## Known gaps

- The first gate attempt failed only on inherited CU2 request prose. CU2 corrected that prose and merged forward; the recorded rerun is green. This was not a test flake.
- Machine laws are tested, not proved. The generated test probe is needed because the compiled law/property runner is the public execution boundary.
- No macOS run here; the batch owns that check. CU2's terminal-host limits carry forward.
- CU7 is the next stacked unit and is not part of this request. No branch was rebased or pushed.
