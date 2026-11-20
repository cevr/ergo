# clientgen

An Ergo module of this repository (`modules/`). It generates provider clients from the providers' own API descriptions (wf10 DISTILLED, route C). It reads Smithy 2.0 JSON AST models: AWS's published models, and the patched models distilled's converter writes for other providers.

| Package | What |
| --- | --- |
| `clientgen/smithy` | `Read` a model into typed records (`Model`, `Shape`, `Kind`, `Member`, `Trait`); `Prune` it to named operations and the shapes they reach, leaving out a named list of trait ids; `Write` it back as JSON; `WithProvenance` records where a slice came from in its metadata; `Refusals` lists the trait ids and shape keys it does not read. |
| `clientgen/cmd/slice` | `slice <model.json> <out.json> <source> <note> [drop:<trait id>]... <operation id>...`: the one tool that cuts the committed slices. |

Only pruned model slices for ergo's own infra drivers are committed (owner, 2026-11-20). Each carries its provenance under `metadata["clientgen.provenance"]`: source address and commit, the sha256 of the whole model, the operations kept, the traits dropped, and a note.

## Laws

- `EveryTraitReadOrRefused`: every trait is read (`Known`) or kept by name (`Refused`), never dropped.
- `PruneKeepsClosure`: each kept operation reaches the same shapes in the pruned model as in the whole one, less the dropped traits. A dropped trait appears nowhere, nothing else is kept, and the slice round-trips through Write and Read.
- `WriteReadRoundTrips`: `Read(Write(m)) == m`.

The mutant ledger is `testdata/mutants.json`.
