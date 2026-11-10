ready
Protocol: supplied orchestrate worker protocol (implementor).
Spec: /home/exedev/.cache/baton/wf10/mapper-gaps/E5/spec.md; ../brief.md E5.
Head: de71d175061e2be21c08c3c7edb3da24d1ea8b9c.
Base: a1c3dc06ad8816bda8d99544c9a7d39d6ce2a446 (final E2).
Diff stat: 10 files changed, 318 insertions(+), 4 deletions(-).

Changes: sketchfront/discover.go reuses syntax-only loader rules for recursive
package selection and dependency order. cmd/ergo/recursive.go aggregates normal
failures but preserves fatal statuses. main.go/test.go accept DIR/...;
format.go uses the same selection without typechecking. commands.go, README.md
and docs/testing.md document it. module_commands_test.go drives a real module's
failed law and fix, check/fmt, subtree, empty match, nested boundary and defect.
It uses one CLI and bounded smoke processes, in the full tier only. Its nine
distinct mutants are in cmd/ergo/testdata/mutants.json, all assertion-killed.

Run: GOMAXPROCS=1 go test -p 2 -count=1 -timeout 10m -run
'^TestModuleCommands$' ./cmd/ergo; exit 0. Red at base: red-1.log exit 1,
observing the CLI's exit 2 and source-extension refusal for ./....
Gate: /home/exedev/.cache/baton/wf10/mapper-gaps/E5/gate-1.log, `gate exit 0`
at merged head: gofmt/diff, vet, cmd/ergo + sketchfront suites, residue,
cache-coverage guard and both closure sweeps. mutants-1.log: `judge exit 0`.
The first nested-boundary mutant survived; a descendant fixture now kills it.
No assertion removed or flake. No own process remained. umask 022, A1, private
GOCACHE (removed at READY), 8G scope per common.md departure lines 42-44.
No full/batch gate. Existing fmt limits and per-package serial builds remain;
no general glob syntax, performance claim, publication or paid call.
Full cold-verifier handoff and prior-art sources: same unit dir's request/notes.
