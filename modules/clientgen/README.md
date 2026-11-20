# clientgen

An Ergo module of this repository (`modules/`). It generates provider clients from the providers' own API descriptions (wf10 DISTILLED, route C). It reads Smithy 2.0 JSON AST models: AWS's published models, and the patched models distilled's converter writes for other providers.

| Package | What |
| --- | --- |
| `clientgen/smithy` | `Read` a model into typed records (`Model`, `Shape`, `Kind`, `Member`, `Trait`); `Prune` it to named operations and the shapes they reach; `Write` it back as JSON; `Refusals` lists the trait ids and shape keys it does not read. |

Only pruned model slices for ergo's own infra drivers are committed (owner, 2026-11-20), each with a provenance line naming its source, commit, sha256 and licence.

## Laws

- `EveryTraitReadOrRefused`: every trait is read (`Known`) or kept by name (`Refused`), never dropped.
- `PruneKeepsClosure`: each kept operation reaches the same shapes in the pruned model as in the whole one, and nothing else is kept.
- `WriteReadRoundTrips`: `Read(Write(m)) == m`.

The mutant ledger is `testdata/mutants.json`.
