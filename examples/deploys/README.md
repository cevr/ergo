# deploys

An HTTP/1.1 server of deployments: create, list and show deploys, and move one through its lifecycle, with JSON bodies and each typed failure mapped to its status code.

| request | answer |
| --- | --- |
| `GET /deploys` | 200, every deploy in the order it was created |
| `POST /deploys` with `{"service":…,"units":[{"image":…,"replicas":…,"mem":…}]}` | 201 with a `Location`, or 400 (not JSON), 415 (another media type), 422 (a rule broken, with the field) |
| `GET /deploys/{id}` | 200, or 404 |
| `POST /deploys/{id}/approve`, `…/release`, `…/cancel` | 200, 404, or 409 when the lifecycle refuses the event |
| another method on a known path | 405 with `Allow` |
| any other path | 404 |

Ergo has no listener yet (G-ex-c-7), so the binary serves one connection on standard input and output, as a program under inetd does: `socat TCP-LISTEN:8080,reuseaddr,fork EXEC:'ergo run examples/deploys/cmd/deploys'` puts it on a port. Requests may be pipelined; `Connection: close`, a garbled request (400) or the end of input ends the connection.

Tag: `default`. It is an original program, not a port.

## Layout

The program is a module of packages (`ergo.mod`: `module deploys`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `deploys/` | The domain, with no HTTP or JSON in it. `deploy.ergo` (`Unit`, `Draft`, `Deploy`, `Validate`, `Total`, `Create`, `Apply`, the failure `Invalid`), `lifecycle.ergo` (`Status`, `Event`, `Step`, the machine `Lifecycle`, the failure `Conflict`), `store.ergo` (`Store`, the store contract with four laws, the failures `NotFound` and `Taken`), `laws.ergo` (the lifecycle and total laws), `tests.ergo`. |
| `memory/` | `Table`, the in-memory `deploys.Store`. |
| `jsontext/` | A JSON value (`Value`, a union), `Parse` with the byte offset of each failure, `Encode`, `Quote`, `Member`, `Int`, and four laws. |
| `jsonwire/` | The derived codec over JSON: `Text`, a `Writer` that writes JSON text, and `Reader`, which reads a parsed `Value` for `decode[T]` with each value's path (types.md, Codecs). |
| `api/` | The HTTP layer as plain functions over records: `message.ergo` (`Request`, `Response`, `Header`, `Problem`, `Reason`), `route.ergo` (`Route`, `RouteOf`), `codec.ergo` (`DraftOf`, `DeployJSON`, the failure `BadBody`), `handle.ergo` (`Handle`), `laws.ergo`. |
| `http1/` | The HTTP/1.1 text: `format.ergo` (`ReadRequest`, `WriteRequest`, `WriteResponse`, the failure `Garbled`), `serve.ergo` (`Serve`, one connection), `laws.ergo`. |
| `cmd/deploys/` | The binary: it binds the memory `Table` and the host's streams, and is the only place that chooses them. |

## What it shows

- **Typed failures become status codes in one place.** `deploys.Create` fails `Invalid`, `deploys.Apply` fails `NotFound | Conflict`, the decoder fails `Unsupported | jsontext.Malformed | BadBody`. `api.Handle` catches each by name and gives it its status (422, 404, 409, 415, 400); a new failure in the domain stops the build until `Handle` maps it. The law `EveryRequestHasAStatus` states that no request reaches a status outside the documented list.
- **The lifecycle as a machine with exhaustive transitions.** `Step` matches on `(Status, Event)` and names all twelve pairs; the laws `FinalTakesNoEvent`, `NoWayBack`, `ApproveIsIdempotent` and `LiveWasApproved` state its shape. A second approval is `Ignore`d, a release before approval is `Reject`ed and becomes 409.
- **Routes as a union.** `RouteOf` is a `Route = List | Show | Create | Act | Wrong | Nowhere`, and `Handle` matches every variant. `UnknownPathIs404` and `WrongMethodNamesTheRight` (each method `Allow` names reaches the path) are its laws.
- **A pure handler.** `api.Handle(Request) Response needs deploys.Store` has no I/O: the tests drive whole conversations with no socket, and a listener, when std has one, binds it unchanged.
- **A store contract with laws, checked on its provider.** `Store` states `FindAfterAdd`, `AddTwiceIsTaken`, `ReplaceKeepsTheCount` and `AllInAddOrder`; `ergo test` runs them in `memory` against `memory.Table`, built by `NewTable`. The API laws take a `Store` as a law parameter and bind it in a context; `ergo test` runs them once per provider of `Store` that their package sees.
- **A derived codec in two phases.** `api.DraftOf` is `decode[deploys.Draft](jsonwire.NewReader(v))` after `jsontext.Parse`: the text is checked whole first (`Malformed`, 400), then the shape (`WireMissingField`, `WireWrongKind`, 422 with the member's path, `units[0].replicas`). The reading is derived from the `Draft` type, so the body cannot drift from it, and the test client's body is `encode(d, jsonwire.NewText())`.
- **Format laws.** `RequestRoundTrip` (a request written is read back as it was), `PipelinedRequestsAreAnsweredInOrder` and `ContentLengthIsTheBody`.

`ergo test` runs each law that takes no `deploys.Store`; the tests run the others over a `memory.Table`. Each law has a mutant (`batch-13/retheme/RT1-A/deploys-mutants.json` in the run dir, translated from ex-c's `orders/mutants.ts`).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
printf 'GET /deploys HTTP/1.1\r\n\r\n' | ./bin/ergo run examples/deploys/cmd/deploys
./bin/ergo test examples/deploys/jsontext
./bin/ergo test examples/deploys/deploys
./bin/ergo test examples/deploys/memory
./bin/ergo test examples/deploys/api
./bin/ergo test examples/deploys/http1
./bin/ergo test examples/deploys/cmd/deploys
go run ./scripts/examples deploys
```

## Cases

`session`: one connection of eight requests: an empty list, a create, a show, a release before approval (409), an approval, a deploy with no units (422), a `DELETE` (405 with `Allow`) and a missing deploy with `Connection: close` (404, and the connection ends).

## Limits found

Each limit is recorded with a repro in the lane's gap file (`gaps-ex-c.md`, run dir).

- **G-ex-c-7: no std http, json or net, and no way to reach `net/http`.** `import go "net/http"` is S109 (#202), and a user `extern` may bind only pure Go packages. The program writes its own JSON (`jsontext`) and HTTP/1.1 text (`http1`), and serves one connection on stdin/stdout.
- **G-ex-c-8: `io.Stdin` has no read of a given length.** The server reads the whole connection with `ReadAll` before it answers, so a client that waits for each response before the next request gets no answer until it half-closes.
- Closed in batch 13: G-exup-1 and G-exup-3. `ergo test` runs `Store`'s laws in `memory` against `memory.Table`, and the `api` and `http1` laws that take a `deploys.Store` with `memory.Table` bound to the parameter, so `deploys/checks.ergo` and the tests that restated the laws are gone. The `api` properties that steer requests and drafts stay.
- **The machine has no live runner.** `Apply` steps one event with `Step` and moves with `machines.Next`; an `Emit` would move the deploy like an `Enter`, though `Step` never emits.
- Friction, no gap: `number` is a prelude function name and cannot be redeclared (q30 admits only types), so the parser's numeral reader is `numeral`.

Found at batch 12 (T4's wider generators), fixed here:
- `jsontext.Parse` could not read a `\u` surrogate pair, which `Quote` wrote for an astral character, so an astral character did not round-trip (`Malformed` "a bad escape in a string"). `Quote` now writes every character but a quote, a backslash and the controls as it is, and `Parse` decodes the escapes itself, a surrogate pair as its one character and a lone surrogate as U+FFFD, as Go's `encoding/json` does.
- The laws' `Sane` kept a generated string that is not UTF-8, which JSON text cannot carry; it now makes each string and member name valid UTF-8 (`strings.ToValidUTF8`).
- A unit's memory had no upper bound, so a valid draft could overflow `Total`. `Validate` now refuses memory above `MaxMem` (100,000,000) and a deploy of more than `MaxUnits` (1,000) units.

Closed in batch 12: G-ex-c-9 (S3 and L4: `Quote` is a `for r in s` loop with `utf8.RuneText`, and `ValidUTF8`'s user extern is std `utf8.ValidString`); hex literals (L1: `0xD800` and the other surrogate bounds); `jsontext.Equal` (W3: a `Value` has `==`); `createThenShow` (L3: `return` leaves the `with`); the hand `Next` match in `Apply` and the laws (V2 `machines.Next`); the aliasing `Table` (its maps are in one `sync.Ref`); the property tests that restated laws that `ergo test` runs (V1).
