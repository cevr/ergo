# every-row

A machine whose `after` gives a `machines.Every` charges `time.Clock` to its
runner's row W, never to its step's R (FKG1, runtime.md): the runner reads
the clock when the timer fires, and a fold fires no timer.

Ported from the Go test `TestEveryRow` (TP-DIAG). Mutants (ledger rows,
killer `ergo:TestCheckerFixtures`):

- `fkg1-every-runner-no-clock`: the conversion leaves `time.Clock` out of W,
  so an actor without a clock is quiet (`runner-needs-clock.ergo`).
- `fkg1-every-clock-in-step`: the clock goes into R, so the fold's law is
  S102 (`fold-needs-no-clock.ergo`).
- `fkg1-every-after-row-admitted` and `fkg1-after-every-refused`: an Every
  after with a capability row is not refused, or a plain one is
  (`every-after-row-refused.ergo`, `fold-needs-no-clock.ergo`).
- `every-std-only`: an Every timer is admitted by its shape, wherever it is
  declared (check.IsEvery; std has no privileges), so a user package's record
  of the shape is one (`user-every-admitted.ergo`).
- `every-any-shape`: a record with another field is no timer
  (`user-every-other-shape-refused.ergo`).
