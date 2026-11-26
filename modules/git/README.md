# modules/git

Native Ergo Git engine module.

## Packages

- `oracle`: Hermetic C Git reference oracle (`G-ORACLE`) for differential testing across OID formats (`sha1`/`sha256`), repository layouts (`Normal`, `Bare`, `DotGitPointer`, `LinkedWorktree`, `Alternates`), `-z` NUL-delimited plumbing parsers, `std/gen`-driven semantic history generation, typed transcript comparison, and event-order history shrinking.
- `source`: the source control service contract. `Source` includes `Changes` (blobs, then changesets in normal form against the first parent; an append moves no ref), `Objects` (wants of blobs and trees by Git id, and a read count), `History` (merge base, ancestry, tree diff) and `Refs` (compare-and-set, protected names). Its 14 laws run over generated histories against a reference model: `AppendIsTheReference`, `ChangesAreTheDiff`, `AppendTwiceIsOnce`, `BlobsBeforeChanges`, `AppendMovesNoRef`, `LogOnlyGrows`, `WantBindsBytes`, `ClosureIsTheChange`, `WantNeedsNoHistory`, `WantIsOnlyWhatWasAsked`, `MergeBaseIsTheReference`, `DiffIsTheReference`, `SetIsCompareAndSet`, `ProtectedMovesOnlyByLand`.
- `source/memory`: the reference provider of `source.Source` in one `sync.Ref`; a change id is the Git commit id of its root tree, parents and meta. Its test file runs every law under SHA-1 and SHA-256, and each of its 14 ledger mutants turns exactly one law red.
