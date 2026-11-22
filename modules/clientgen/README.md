# clientgen

An Ergo module of this repository (`modules/`). It generates provider clients from the providers' own API descriptions (wf10 DISTILLED, route C). It reads Smithy 2.0 JSON AST models: AWS's published models, and the patched models distilled's converter writes for other providers.

| Package | What |
| --- | --- |
| `clientgen/smithy` | `Read` a model into typed records (`Model`, `Shape`, `Kind`, `Member`, `Trait`); `Prune` it to named operations and the shapes they reach, leaving out a named list of trait ids; `Write` it back as JSON; `WithProvenance` records where a slice came from in its metadata; `Refusals` lists the trait ids and shape keys it does not read. |
| `clientgen/emit` | `Emit(model, provider)` writes a client package's source: request and output records (with `New<Shape>` constructors, every optional member `None`), an operation type with a law per operation from its HTTP method (GET and HEAD repeatable, PUT and DELETE idempotent), an `Api` over `http.Net` that hands each request to a signer just before it is sent, the model's errors as failure records and its enums as unions with an `Other` variant. `PlanOf` is the binding plan the text is written from. An operation it cannot write yet is a typed `Refused`, never a skip: an XML body (ListObjectsV2, until the XML wire rules), an intEnum, a list or structure member. |

The committed slices (`testdata/models`: S3, R2, DNS) are cut by `examples/clientgen` (`clientgen slice <model.json> <out.json> <source> <note> [drop:<trait id>]... <operation id>...`), the one tool for it; a module is a library and has no `cmd/`. The same binary writes the clients (`clientgen emit [--check] <provider> <slice.json> <dir>`): the S3 client in `modules/aws/s3` is its output, and its case `s3-current` fails when the committed file is not what emit writes (the drift guard until a gen line can run an Ergo program, GEN-TASK).

The emitter writes text from one names table (`write.ergo`), not through std `code`, which reads declarations and has no writer yet (CODE-WRITER). Every std and runtime name is written qualified through its import; `Option`, `Some` and `None` are written bare from that table until a std `option` package exists.

Only pruned model slices for ergo's own infra drivers are committed (owner, 2026-11-20). Each carries its provenance under `metadata["clientgen.provenance"]`: source address and commit, the sha256 of the whole model, the operations kept, the traits dropped, and a note.

## Laws

- `EveryTraitReadOrRefused`: every trait is read (`Known`) or kept by name (`Refused`), never dropped.
- `PruneKeepsClosure`: each kept operation reaches the same shapes in the pruned model as in the whole one, less the dropped traits. A dropped trait appears nowhere, nothing else is kept, and the slice round-trips through Write and Read.
- `WriteReadRoundTrips`: `Read(Write(m)) == m`.
- `EveryMemberIsBound`: each member of a drawn operation's input lands in exactly one place (label, query, header, prefix headers, payload), and each output member is read from exactly one.
- `EmitIsDeterministic`: a drawn model emits the same files twice.
- Tests: `ListObjectsIsRefused`, `RequiredIsPlain` (a required non-label member is its type, not an Option), `GreedyLabelIsGreedy`.

The mutant ledger is `testdata/mutants.json`.
