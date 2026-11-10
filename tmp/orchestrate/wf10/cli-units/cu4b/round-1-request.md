ready

Protocol: supplied orchestrate implementor worker protocol. Spec: /home/exedev/.cache/baton/wf10/cli-units/cu4b/spec.md.
Head: 9e43081e6477c2571ca484780932086ddb4228db. Base: b183e1a01c6907b72c906c3d6bf8c25cb70319c7. Branch wf10/cu4b.

Full request: /home/exedev/.cache/baton/wf10/cli-units/cu4b/round-1-request.md (run ledger).
Diff: 10 files changed, 138 insertions(+), 10 deletions(-).

Changed exec.ergo's consuming Stop signature; runtime/v1/exec_start.go shares Close's masked grace/end/reap path and returns its stored actual status; blocking.go and runtime199_mask_test.go register that release route; extern_test.go checks post-Stop ownership and updates the sealed fixture; exec_start_test.go extends the seeded lifecycle property; std_exec_test.go runs the timeout/catch caller and FIFO-blocked TERM trap returning 37; two mutant ledgers add five guards; stdlib.md documents it.

Run: GOMAXPROCS=1 go test -p 1 -count=1 -timeout 10m -run '^TestExecStartRuns$' ./internal/sketch/lower (exit 0). The native probe checks and returns `status 37`. Red at base: S104 Running has no Stop, RED EXIT: 1 (red.log).

Gate: <unit>/gate-1-final.log, GATE EXIT: 0; ACCEPT EXIT: 0; MUTANTS EXIT: 0. Script final.sh runs touched-package test/vet, focused lower tests, residue and gate cache guard, compiler build and probe, then mutants. Reduced share GOMAXPROCS=1/-p1, umask022, nice19, private GOCACHE, bounded scope as common.md42–44 explicitly departs from the protocol. Process inspection clean; private cache removed at READY.

Mutants: TestRunningMatchesModel kills cu4b-skips-reap, cu4b-invented-status, cu4b-skips-shutdown and cu4b-checks-stopped-caller. TestExecStopTakes uniquely kills cu4b-stop-loses-takes. All five killed by failures, not deadlines, in the final run. Native TestExecStartRuns guards the compiler composition and real trap, not only a fake. Initial fixture failures (missing takes on the sealed fake; a fork-after-TERM shell helper) were fixed without weakening assertions.

Known gaps: CU4's inherited lower mask/ownership failures remain outside this focused gate. ToCaller remains line-based; raw chunks are not this unit. No full gate, independent batch verdict or speed claim. Effect's scoped ChildProcess intent becomes owned resources/takes, not detached kill fibers; Go control is Close plus its stored status. Gleam shellout is buffered; ReScript binds Node exit events; Elixir Port reports exit_status. No push or real key/model/scheduler/link changes.
