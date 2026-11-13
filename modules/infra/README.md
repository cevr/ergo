# infra

An Ergo module of this repository (`modules/`, MK0): infrastructure as code in direct style (IA1; the design model is [`models/infra`](../../models/infra/infra.ergo)).

| Package | What |
| --- | --- |
| `infra` | `Driver[P, A]` with its four laws, `Kind[P, A]`, `State[P]` and `Lock` with their laws, `Engine` (a plan or an apply of one kind), `Step`, `Begin`, `Finish` (the sweep), `Destroy`; `Deploy`, `WriteOutputs`, `OutputsSpec` (IA4) |
| `infra/memory` | a cloud of directories and files with a driver per kind, a state store, a lock, and a `Fuse` that fails a chosen call as a crash would |

## The row's protocol

An apply of an object writes its row `Creating`, calls the driver's `Apply`, and writes the row `Live`. The sweep (at `Finish`) and a destroy write `Deleting`, call `Delete`, and remove the row, the newest object first. A run takes a row that is not live as work to do again (`Resume`; the driver's `RetrySafe` makes that safe). A plan records the same steps and calls no host. A failure between `Begin` and `Finish` leaves the lock held, as a crash does; an operator breaks it by naming the holder.

## Laws

- `Driver`: `AttrsIsApply`, `RetrySafe`, `ReadAfterApply`, `DeleteIsIdempotent`, run for `memory.Dirs` and `memory.Files`.
- `State`: `PutThenGet`, `RemoveThenGet`; `Lock`: `LockExcludes`, `BreakNamesTheHolder`.
- `memory.ApplyConverges`: the model-based test after `models/infra`, in a wider scope: six objects of two kinds, any props, two runners taking turns, a crash at any call to the cloud or a store (before or after its effect), then a destroy. After every run the rows and the cloud agree (NoOrphan, LiveIsTrue); a finished run leaves what its stack wants (Converged), and a second plan has no step; a plan calls no host; the destroy leaves the cloud empty. `memory.DestroyIsNewestFirst`: a destroy deletes in the reverse of the order of creation, across kinds.

## Outputs

A stack returns a record `O` of its own, one that has a codec. `Deploy` runs it: `Begin`, the stack, `Finish`, and only then, in an apply, `WriteOutputs` writes `O` as JSON at a path (the file is replaced whole). A program reads it with std `config`: `config.Load[O](infra.OutputsSpec(path))`. Laws: `memory.OutputsRoundTrip` (what infra writes, config reads back as the same record) and `memory.OutputsAfterTheApply` (after a crash at any call of a second apply, the file holds the first apply's outputs; after it ends, the second's).

The mutant ledger (`testdata/mutants.json`) plants the design model's six mutants in the code, and d02's driver whose `Read` gives `None`.

## Decided

- The state store is per kind (`State[P]`), so a row keeps its props typed and needs no codec; the lock is per stack (`Lock`). The plan's `State { …, Lock, Unlock }` is split in two.
- A plan's change for a row left creating or deleting with the same props is `Resume`.
