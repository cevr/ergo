# Scheduler seam: live-build erasure (X5)

Measured on go1.27.0 linux/amd64: `go build -gcflags=-m ./runtime/rt`
reports every hook in `sched_live.go` as inlineable, and reports 22 inlined
hook call sites. `TestSchedHooksCostNothingLive` checks the embedded runtime
in process: each live hook is empty, `Exploring` is constant false, and each
call either has plain arguments or is inside `if Exploring`. Calls that
construct a closure are guarded, so the live build removes that work too.

This is a structural erasure receipt, not a timing claim. Matched base/head
benchmarks are pending the orchestrator's quiet window (X5 `bench-pending.md`).
The baton protocol and its safety mutants are in `models/baton/baton.ergo`.
