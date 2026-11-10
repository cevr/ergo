# E3b: HTTP failure statuses

Round 1. Authority: orchestrator assignment, mapper-gaps/decisions.md Q4,
brief.md and wf10/common.md. Base 47eb057a; workspace ergo-e12, branch wf10/e3b.
Share A1: GOMAXPROCS=1, go -p 2. Never rebase or push.

Owns: internal/tsproj HTTP projection and tests/ledger; std http ordinary
failure response functions, HTTP lowering law harness and ledger; examples/jobs
HTTP contract, handler, laws, program expectations and README; tools/ts docs and
mapper version; docs/spec/stdlib.md; this unit's run files.
Do not touch: other units, shared caches, main, publishing, full/batch gates.

Export one constant top-level failure-status table. Each failure type of the
served operation rows has one status. Mapper and example law refuse missing
and duplicate entries. The server and projection read this same table. Emit
stock Effect HttpApi per-status error schemas with tagged bodies. Stock client
must decode jobs not-found and invalid-draft failures; a renamed failure makes
the stale call site fail tsc. Red acceptance first. Laws, named mutants and
focused gate required. On compaction stop, with state current.

Round gate: changed-package tests and vet; changed-file formatting; HTTP std
laws, jobs laws and program cases; stock HTTP acceptance; residue, gate cache
coverage and tsproj closure sweep; unit mutants. Full gate belongs to batch.

Seams: stock HttpApiClient sees status-directed tagged errors (RPC tests miss
HTTP statuses); projection diagnostics reject invalid table shapes (runtime
example uses a valid table); example law checks complete unique failure table
(routing laws know only endpoints); std law checks lookup and response data
(success law never reads failure tables).
