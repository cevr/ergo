ready

Protocol: supplied implementor worker protocol.
Spec: `/home/exedev/.cache/baton/wf10/cli-units/brief.md`, CU1 round 1, and cli-units/decisions.md.
Head: `e011fc5059bb7c35bc74e9d0866816db5471883a` (implementation).
Base: `c590c1bb`; branch `wf10/cu1`.
Measured diff: `32 files changed, 1238 insertions(+), 143 deletions(-)`.

The full request and gate receipts are at the shared run path:
`/home/exedev/.cache/baton/wf10/cli-units/cu1/round-1-request.md`.

Changed: std flag aliases, optional and repeated handles, recursive command
trees, full-path help, a declared root version, laws, a seeded reference-parser
property, migrated command literals, a neutral routecli example and docs.
Inherited rpc formatting and mutant re-anchors are included, plus rpcgen's
shared ledger-format repair.

By file: `internal/sketch/std/flag/{flag,parse,usage,cardinality}.ergo`
owns typed handles and parse/render/help; `command.ergo` owns trees, Group,
Root and version; `tree_laws.ergo` and `internal/sketch/lower/std_flag_tree_test.go`
own the new laws and parser model; `std_flag_test.go` and the six migrated
example command files initialize empty child lists. `examples/routecli`
shows the three-level API. `docs/spec/stdlib.md` documents it and its laws;
the lower and examples mutant ledgers name the new guards. The inherited
follow-ups change std rpc formatting, eight lower ledger edits and rpcgen's
ledger format. No source test was deleted.

Run: `go test -p 2 -timeout 20m -count=1 -run '^TestFlag' ./internal/sketch/lower`.
Measured at head: `STEP_EXIT=0` in shared `cu1/gate-1.log`.
Round gate: `sh /tmp/opencode/cu1-gate.sh`, through common.md's bounded scope;
`ACCEPT_EXIT=0`, `GATE_EXIT=0`, `ROUND_GATE_EXIT=0`.
The gate also runs std tests, vet, formatting, residue, cache coverage and the
affected examples. Seven source mutants and the routecli mapping mutant are
killed. The API smoke source is red at exact base c590c1bb and green at head.

Known gaps: the orchestrator selected option 1, preserving the inherited
t5-9 canonical-order and t5-28 return-classification survivors. Type derivation,
inherited global flags and other S-cli built-ins are out of scope. No speed
claim; the quiet-window scalar benchmark is pending with the orchestrator.
CU5 is not part of this request. No rebase or push.
