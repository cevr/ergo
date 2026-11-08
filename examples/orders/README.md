# orders

An orders HTTP/1.1 server: create, list and show orders, and move one through its lifecycle, with JSON bodies and each typed failure mapped to its status code.

| request | answer |
| --- | --- |
| `GET /orders` | 200, every order in the order it was placed |
| `POST /orders` with `{"customer":…,"lines":[{"sku":…,"quantity":…,"cents":…}]}` | 201 with a `Location`, or 400 (not JSON), 415 (another media type), 422 (a rule broken, with the field) |
| `GET /orders/{id}` | 200, or 404 |
| `POST /orders/{id}/pay`, `…/ship`, `…/cancel` | 200, 404, or 409 when the lifecycle refuses the event |
| another method on a known path | 405 with `Allow` |
| any other path | 404 |

Ergo has no listener yet (G-ex-c-7), so the binary serves one connection on standard input and output, as a program under inetd does: `socat TCP-LISTEN:8080,reuseaddr,fork EXEC:'ergo run examples/orders/cmd/orders'` puts it on a port. Requests may be pipelined; `Connection: close`, a garbled request (400) or the end of input ends the connection.

Tag: `default`. It is an original program, not a port.

## Layout

The program is a module of packages (`ergo.mod`: `module orders`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `orders/` | The domain, with no HTTP or JSON in it. `order.ergo` (`Line`, `Draft`, `Order`, `Validate`, `Total`, `Place`, `Apply`, the failure `Invalid`), `lifecycle.ergo` (`Status`, `Event`, `Step`, the machine `Lifecycle`, the failure `Conflict`), `book.ergo` (`Book`, the store contract with four laws, the failures `NotFound` and `Taken`), `checks.ergo` (one function per `Book` law, which a provider's tests call), `laws.ergo` (the lifecycle and total laws), `tests.ergo`. |
| `memory/` | `Ledger`, the in-memory `orders.Book`. |
| `jsontext/` | A JSON value (`Value`, a union), `Parse` with the byte offset of each failure, `Encode`, `Quote`, `Equal`, and four laws. |
| `api/` | The HTTP layer as plain functions over records: `message.ergo` (`Request`, `Response`, `Header`, `Problem`, `Reason`), `route.ergo` (`Route`, `RouteOf`), `codec.ergo` (`DraftOf`, `OrderJSON`, the failure `BadBody`), `handle.ergo` (`Handle`), `laws.ergo`. |
| `http1/` | The HTTP/1.1 text: `format.ergo` (`ReadRequest`, `WriteRequest`, `WriteResponse`, the failure `Garbled`), `serve.ergo` (`Serve`, one connection), `laws.ergo`. |
| `cmd/orders/` | The binary: it binds the memory `Ledger` and the host's streams, and is the only place that chooses them. |

## What it shows

- **Typed failures become status codes in one place.** `orders.Place` fails `Invalid`, `orders.Apply` fails `NotFound | Conflict`, the decoder fails `Unsupported | jsontext.Malformed | BadBody`. `api.Handle` catches each by name and gives it its status (422, 404, 409, 415, 400); a new failure in the domain stops the build until `Handle` maps it. The law `EveryRequestHasAStatus` states that no request reaches a status outside the documented list.
- **The lifecycle as a machine with exhaustive transitions.** `Step` matches on `(Status, Event)` and names all twelve pairs; the laws `FinalTakesNoEvent`, `NoWayBack`, `PayIsIdempotent` and `ShippedWasPaid` state its shape. A second payment is `Ignore`d, a ship before payment is `Reject`ed and becomes 409.
- **Routes as a union.** `RouteOf` is a `Route = List | Show | Create | Act | Wrong | Nowhere`, and `Handle` matches every variant. `UnknownPathIs404` and `WrongMethodNamesTheRight` (each method `Allow` names reaches the path) are its laws.
- **A pure handler.** `api.Handle(Request) Response needs orders.Book` has no I/O: the tests drive whole conversations with no socket, and a listener, when std has one, binds it unchanged.
- **A store contract with laws, checked on its provider.** `Book` states `FindAfterAdd`, `AddTwiceIsTaken`, `ReplaceKeepsTheCount` and `AllInAddOrder`; `memory`'s tests run them through `orders/checks.ergo`. The API laws take a `Book` as a law parameter and bind it in a context.
- **Format laws.** `RequestRoundTrip` (a request written is read back as it was), `PipelinedRequestsAreAnsweredInOrder` and `ContentLengthIsTheBody`.

Each law is also a property test (laws are checked, not run yet: #162), and each has a mutant that the test kills (`ex-c/orders/mutants.ts` in the run dir, 20 of 20).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
printf 'GET /orders HTTP/1.1\r\n\r\n' | ./bin/ergo run examples/orders/cmd/orders
./bin/ergo test examples/orders/jsontext
./bin/ergo test examples/orders/orders
./bin/ergo test examples/orders/memory
./bin/ergo test examples/orders/api
./bin/ergo test examples/orders/http1
./bin/ergo test examples/orders/cmd/orders
go run ./scripts/examples orders
```

## Cases

`session`: one connection of eight requests: an empty list, a create, a show, a ship before payment (409), a payment, an order with no lines (422), a `DELETE` (405 with `Allow`) and a missing order with `Connection: close` (404, and the connection ends).

## Limits found

Each limit is recorded with a repro in the lane's gap file (`gaps-ex-c.md`, run dir).

- **G-ex-c-7: no std http, json or net, and no way to reach `net/http`.** `import go "net/http"` is S109 (#202), and a user `extern` may bind only pure Go packages. The program writes its own JSON (`jsontext`) and HTTP/1.1 text (`http1`), and serves one connection on stdin/stdout.
- **G-ex-c-8: `io.Stdin` has no read of a given length.** The server reads the whole connection with `ReadAll` before it answers, so a client that waits for each response before the next request gets no answer until it half-closes.
- **G-ex-c-9: no byte or rune to string, and no range over runes.** `jsontext.Quote` starts from `strconv.Quote` and rewrites Go's escapes into JSON's; `ValidUTF8` binds `unicode/utf8.ValidString` by a user extern.
- **G-ex-c-5 (from kvstore): a member law cannot share its check.** `Book`'s laws are written once as laws and once in `orders/checks.ergo`.
- **The machine has no runner.** `Lifecycle` declares the machine (runtime.md: plan values, no runner until #57/#101), so `Apply` calls `Step` itself and handles all five `Decision` variants.
- Friction, no gap: hex literals are not in v0 (#81), so `0x10000` is written `65536`; `Option[Value]` and `[]string` are not `==`-comparable (as in Go), so `jsontext.Equal` and `http1.SameRequest` compare by hand; `return` cannot leave a `with` block (grammar.md, Statements 5), so a check that returns early is a function the `with` calls (`createThenShow`); `number` is a prelude name and cannot be redeclared, so the parser's numeral reader is `numeral`.
