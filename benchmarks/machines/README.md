# Machine scenarios against plusthree

plusthree (a Rust port of XState v6, owner prior art 2026-11-18) publishes
instruction counts for five event scenarios (`crates/plusthree-bench`,
design §10). This folder runs the same scenarios as Ergo machines and as a
hand-written Go switch, so the three can be set side by side.

| Scenario | Machine | One event |
| --- | --- | --- |
| S1 | `Inactive` / `Active` | `Press` flips the state (`Enter`) |
| S2 | one record `Counter{Count, Limit}` | `Inc`: `Stay` with `Count + 1` while `Count < Limit` (2^53 - 1) |
| S3 | `A{MidA{A1x}}` / `B{MidB{B1x}}` | `Jump` crosses the tree (`Enter`) |
| S4 | `Both{R1: Ra/Rb, R2: Rc/Rd}` | `Flip` flips both regions (`Enter`) |
| S7 | `PA` / `PB`, `after` one second to the other | a timer fires; the old entry's timer is dropped, the new one armed |

Ergo has no hierarchy or parallel construct: S3's levels are nested
payloads and S4's regions are fields of one record, and in both a change
opens one new entry of the whole machine. The scenarios have no entry
action or per-level timer, so nothing observable differs.

- `ergo/` is the Ergo module (`charts` holds the machines; `cmd/p3bench`
  runs one scenario for N events). Modes: `fold` (`machines.From` over a
  reused slice of 1024 signals), `step` (the step and `machines.Next` in a
  loop), `timed` (S7: `machines.RunTimed` on a `testing.VirtualClock`) and
  `actor` (S1 asks the live runner per signal; S7 moves a `time.Manual`
  one second per timer).
- `testdata/control` is the Go reference: switch machines, and S7
  emulated as plusthree's statig and smlang drivers emulate it (a cancel
  and a schedule effect pushed into a reused buffer). `s1actor` and
  `s7actor` are the floor of the actor modes: a goroutine that owns the
  state, asked over one unbuffered channel and answering on another.
- `measure.ts` takes instructions per event from `perf stat -e
  instructions:u` at N and at 0, and heap bytes per event from the peak
  resident size with `GOGC=off` at N and at 0 (an Ergo program cannot read
  `runtime.MemStats` yet: `ReadMemStats` takes a borrow it may keep, and a
  local Go package is not bindable, #202). Each count is taken twice.

```sh
ergo build benchmarks/machines/ergo/cmd/p3bench -o /tmp/p3bench
go build -o /tmp/control ./benchmarks/machines/testdata/control
ERGO_COMMIT=$(git rev-parse --short HEAD) bun benchmarks/machines/measure.ts /tmp/p3bench /tmp/control /tmp/p3.json
```

Measured results and their reading are in the MX-P3 research spec, not
here: numbers move with the compiler.
