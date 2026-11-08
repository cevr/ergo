# Conformance inputs

The selected Effect cases cite Effect 4.0.1, commit `460272d30457f4697d8b8c52cad41caccbcace08`. Ergo keeps no checkout or copy of upstream: each case records its upstream file, line and test label as provenance text. Ergo's gate never executes the upstream suite and does not claim to pass it.

Ergo owns these inputs:

| File | Contents |
| --- | --- |
| `effect-cases.json` | Selected upstream cases, each with a status, limits and the Ergo tests that are its evidence. The cases of the earlier compiler lost their evidence with it ([#205](https://github.com/cevr/ergo/issues/205)) and are `pending`; five std cases have a v1 test whose subtest of the case's name is their evidence. `go run ./scripts/conformance check` validates it. |
| `codecs/` | Codec policy vectors, with the outputs the pinned Effect runtime produced for them (`effect-*comparator.out.jsonl`), frozen on 2026-10-26 as golden files. No test reads them until [#49](https://github.com/cevr/ergo/issues/49): the codec tests that did went with the retired compiler. Nothing runs Effect. |
| `http/` | Shared HTTP wire vectors: exact status/body/content-type exchanges, the limits each program declares, and lifecycle cases (body timeout, disconnect, shutdown with active work). No test reads them until [#49](https://github.com/cevr/ergo/issues/49): `TestHTTPWireVectors`, which ran them over real sockets, went with the retired compiler. They name example programs removed with it, at commit `dbf135bf`. |
| `upstream/foldkit-d21db423/` | The pinned Foldkit workspace snapshot, a language-design reference corpus; `go run ./scripts/conformance foldkit --self-check` verifies it offline. |
| `size/` | The Go controls and recorded size receipts; the fixtures return with v1 item 5. See [size/README.md](size/README.md). |

[docs/conformance.md](../docs/conformance.md) explains the statuses, commands and pin-update procedure.
