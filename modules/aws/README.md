# aws

An Ergo module of this repository (`modules/`): clients of AWS services, generated from AWS's published Smithy models by `modules/clientgen` (wf10 GEN-EMIT).

| Package | What |
| --- | --- |
| `aws/aws` | The protocol runtime the generated clients call, written by hand: the `Signer` contract (`Unsigned` is its one provider here; SigV4 is AWS-CORE's), the shared failures `Failed` and `Malformed`, `ErrorOf` (S3's `<Error>` body over std `xml`'s Parser, or the status's name for a body-less HEAD), and the wire rules of Smithy's HTTP bindings: labels (`Label`, greedy `{Key+}`), header numbers, flags and timestamps (http-date, date-time, epoch seconds), prefix headers. Law `LabelDecodes`. |
| `aws/s3` | **Generated; do not edit.** The S3 client for HeadObject, GetObject (its body streamed to a sink), PutObject (a string body) and DeleteObject: the `S3` operation type with its method laws, `Api` over `http.Net` and a `Signer`, the records, errors and enums. Regenerate with `clientgen emit s3 modules/clientgen/testdata/models/s3.json modules/aws/s3` (examples/clientgen). |
| `aws/memory` | The fake: `Memory` implements `s3.S3` over buckets in memory; `Handler` serves S3's REST answers from one for `http.InMemory`; `Seen` is a test signer that marks and keeps each request it was given. Its contexts `lawMemory` and `lawApi` run s3's laws over the fake and over the Api in front of it. |

A static key (an API key, a bearer token) is an `http.Credential` the Net holds (std http's `ForOrigin`, over a `config.Redacted[string]`): `Api` has no field for one, so no request it builds carries it. `Signer` is for what is computed from each request (SigV4, AWS-CORE).

## Laws

- `S3.<Op>IsRepeatable` (HEAD, GET) and `S3.<Op>IsIdempotent` (PUT, DELETE), generated: the second call's outcome is the first's. The S3 model marks none of these operations readonly or idempotent, so the laws come from RFC 9110's method semantics. They run over `Memory` and, in package memory, under `lawApi`.
- `ApiIsTheFake`: the Api over `http.InMemory(Handler(fake))` answers each drawn call as the fake does: output, failure (NotFound, NoSuchKey, InvalidObjectState) and body.
- `PutThenGetIsThePayload`: what PutObject stores GetObject gives back, metadata by lower-cased name, over the fake and over the Api.
- `KeyOnlyInTheNet`: a key bound to the Net reaches the server on every request and is in none the Api or its signer hands on; each request the server gets went through the signer once.
- `SlashesStayInThePath`: a key's "/" is sent as a separator, each segment escaped once.

`expected-skips` lists the generated laws skipped for `Api` in package s3 (no `http.Net` is built there); they run in package memory. The mutant ledger is `testdata/mutants.json`.
