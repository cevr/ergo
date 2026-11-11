# RPC: Effect RPC over HTTP

Std `rpc` serves and calls an operation type over Effect RPC's HTTP protocol, so a stock Effect `RpcClient` (TypeScript, `effect` 4.1.2) calls an Ergo server, and an Ergo program calls it, with no adapter. `ergo gen rpc` writes, from a checked operation type, the Ergo server and the Ergo client of that contract. TypeScript is a peer on the wire, not a target: Go stays the only target, and no build starts Node, Bun or a TypeScript checker ([decision](../decisions.md#owner-direction-2026-11-10)).

## Package rpc

| Name | Signature | Go body |
| --- | --- | --- |
| `Answer` | `Answer[R](r http.Request, mount string, handle escaping func(tag string, payload string) string fails Failed needs R) http.Response needs R \| http.Streaming` | Ergo over `RPCParse`, `RPCExits`, `RPCExitLines` |
| `Serve` | `Serve[R](l http.Listener, limits http.Limits, mount string, handle …) fails http.InvalidLimits needs R \| Scheduler` | Ergo: `http.Serve` with `Answer` |
| `Dial` | `Dial(url string) Conn needs Ambient` | Ergo |
| `Loopback` | `Loopback(handle escaping func(tag string, payload string) string fails Failed) Conn`: `handle` served in this process | Ergo: `Answer` under `http.NewRecorder()` |
| `Call` | `Call(c Conn, tag string, payload string) string fails Failed \| Unrepresentable \| Transport` | `RPCCall` (Dial); Ergo (Loopback) |
| `Failed` | `Failed { Text string }`: a typed failure's tagged JSON | `RPCFailed` |
| `Transport` | `Transport { Reason string }`: a call the wire did not carry | `RPCTransport` |
| `Malformed`, `Envelope` | a body that is not Effect RPC's; one parsed envelope | `RPCMalformed`, `RPCEnvelope` |
| `Unrepresentable` | `Unrepresentable { Path string, Value string }` | `RPCUnrepresentable` |
| `Encode`, `Decode` | `Encode[T wire](v T) string`; `Decode[T wire](what string, text string) T fails Transport` (a text that is not a `T`) | Ergo over `json` |
| `Tagged`, `TagOf` | `Tagged(tag, object string) string` adds `"_tag"`; `TagOf(text string) string` reads it | `RPCTagged`, `RPCTagOf` |
| `Exact`, `ExactUint`, `Under`, `Refuse` | the int rule (below) | `RPCExact`, `RPCExactUint`; Ergo; `RPCRefuse` |
| `Lost` | `Lost[T](t Transport) T`: the defect `RpcClientError` with `t.Reason` | Ergo |
| `Die` | `Die[T](name, message string) T`: a defect Effect reads as `{name, message}` | `RPCDie` |

1. **Answer and Serve** (Rm). RPC is an ordinary handler of the std http core ([stdlib http](stdlib.md#http)): `Answer(r, mount, handle)` answers `POST mount` and `POST mount/` (Effect's `RpcClient` posts to its URL with a trailing slash), so a program that serves a page or other routes beside RPC calls `Answer` from its own `http.Serve` handler on one listener. `Serve(l, limits, mount, handle)` is `http.Serve` with `Answer` as its only handler. Before any call starts, another path is 404, another method 405 with `Allow: POST`, a body over `limits.MaxBodyBytes` 413 (`http.Limits` is required; R1's fixed 16 MiB cap is gone), a failed read or a body that is not Effect RPC's (`Malformed`) 400. Each `Request` envelope runs `handle(tag, payload)` on a task of its own below the request's task, inside the caller's `needs R`, so the providers a `context` binds around `Serve` serve every call. A client that goes away stops the request's task, which stops every call left, and the request ends after their releases ran. A stop of `Serve`'s caller is `http.Serve`'s: the listener closes, every request and call stops, and each is joined.
2. **Serializations.** `Content-Type: application/json` is a body of one envelope or an array of them, answered with an array of `Exit` envelopes, each carrying its request's id (not its position), once every call ended; `application/x-ndjson` is a line per envelope, answered through `http.Stream` with one flushed `Exit` line per call as it ends, so a fast call's Exit reaches the client while another call of the batch is held (`TestRpcFramedExitFlowsWhileHeld`). The stream is http's streamed body ([stdlib http](stdlib.md#http) 9): status and header are fixed before the first line, and a failed write stops the calls left.
3. **Exit.** A value is `{"_tag":"Success","value":V}`, `null` for an operation with no value; `handle`'s `Failed{Text}` is `{"_tag":"Failure","cause":[{"_tag":"Fail","error":Text}]}`; a defect is `{"_tag":"Failure","cause":[{"_tag":"Die","defect":{"name":N,"message":M}}]}`. `requestId` is the request's id token as sent, a number or a string.
4. **Call.** `Call` posts one `Request` (`application/json`, ids counted from 0) and reads its `Exit`: a value's text, `Failed` with a `Fail`'s error text, `Unrepresentable` for a `Die` named `Unrepresentable` (rule 5), and `Transport` for a failure of the wire: a server that cannot be reached or drops the connection, a status other than 200 (a URL with no RPC mount), a `Die` named `RpcError` (the server's rpc layer could not read the call: the generated `Handle` raises it for a request that is not the contract's), an `Interrupt` cause, a server-wide `Defect` or `ClientProtocolError` message, and a body that is not Effect RPC's. Any other `Die` is the server's own defect (its provider's) and is raised again as a defect: no client row names it, as Effect's `RpcClient` dies with it. A stop of the calling task aborts the request, which stops the call on the server.
5. **The int rule** (orchestrator 2026-11-10, Q3). A JavaScript number holds an integer exactly only within ±(2^53−1), so the Effect RPC profile refuses a larger one at encode and at decode: `Exact` and `ExactUint` fail with `Unrepresentable{Path, Value}`, and `Under` prefixes its path as a walker descends. The `json` codec itself writes every int's exact digits. A field that needs the full range uses a string type. `Refuse(u)` raises the defect a server answers for an int it will not send: the `Die` `{"name":"Unrepresentable","message":…,"path":P,"value":V}`. Effect's `Schema.Defect` reads its name and message; `Call` reads all four back as `Unrepresentable{Path: P, Value: V}`.
6. **Loopback.** `Loopback(handle)` is a `Conn` to `handle` served in this process: each `Call` is an `http.Request` that `Answer`, `Serve`'s own handler, answers (its envelopes, its json answer) under `http.NewRecorder()`, in memory, with no socket and no connection, so it reaches no host and needs no `Ambient`, and `handle` runs on a task below the caller, as a call of `Serve` runs. A stop of the caller stops that task, and `Call` ends after its releases ran (`TestRPCCallsStopWithTheCaller`). A row-free constructor can build a generated `Client` over it, so `ergo test` runs a contract's laws through the wire: `examples/limiter/loopback`.

## ergo gen rpc

`ergo gen rpc [--check] DIR.OPTYPE` reads the checked package in `DIR` and writes the package `DIR/<pkg>rpc` with three files, each headed `// Code generated by ergo gen rpc; DO NOT EDIT.` and a line naming the contract by its import path (never by how the command spelled the folder) with its digest:

- `wire.ergo`: `Request`, a union with a variant `<Op>Request` per operation whose fields are the operation's parameters (the field `Key` for the parameter `key`, so the wire key is `key`, as Effect's payload struct names it), and the int walkers `exact<Type>` of every type the contract carries.
- `server.ergo`: `Handle(tag string, payload string) string fails rpc.Failed needs <pkg>.<OpType>`, which decodes the `Request`, calls the bound provider, and encodes the value, or the failure tagged with its type's name.
- `client.ergo`: `Client`, `NewClient(conn rpc.Conn) Client`, and two functions per operation (owner q90, R-chk):
  - `<Op>Checked`, the call itself, whose row is the operation's row plus the wire's failures: `rpc.Unrepresentable` when the operation carries an int (in an argument, its value or a failure), then `rpc.Transport`. For `Take(key string, cost int) Grant fails Limited | UnknownBucket | BadCost` it is `TakeChecked(client Client, key string, cost int) limits.Grant fails limits.Limited | limits.UnknownBucket | limits.BadCost | rpc.Unrepresentable | rpc.Transport`. An argument the wire cannot carry fails before it is sent; a value or a failure read back fails after; a value the server refused to send fails with the server's path and value. A failure the row does not name, a value or failure that is not the contract's, and every `Transport` of `rpc.Call` fail `rpc.Transport`. An operation that carries no int has no `Unrepresentable` in its row, and a server that refuses one anyway is a `Transport`.
  - `<Op>`, with the operation's own signature, so `Client` provides the operation type: `with context { limits.Limiter: limitsrpc.NewClient(conn) } { … }` runs the same code against a remote provider, and the operation type's laws run against it too. It is `<Op>Checked` with the two wire failures raised as defects: `rpc.Refuse(u)` (Die `Unrepresentable`) and `rpc.Lost(t)` (Die `RpcClientError`). One generated path writes the call; the provider is a catch over it.

  A defect of the server's provider is a defect in both forms: it is not a failure of the wire. A caller that must report a lost server or a refused call (a CLI, a gateway with a fallback) calls the checked form and matches its row; code written against the operation type gets the provider. `examples/limiter`'s `limiter check URL` is such a caller (`BucketsChecked`).

`--check` writes nothing and fails when a file differs from what the generator would write. On the server the int rule surfaces as a defect (`rpc.Refuse`) for a value or failure before it is answered, since `Handle` answers a contract's row; a client reads that `Die` back as `Unrepresentable` (Call, rule 4).

The TypeScript mapper and loaders (below) project wire data and operation types, not function bodies. The checked functions live in the generated `<pkg>rpc` package and are not projected. The wire of a call is unchanged; only the `Die` of a refused int gains `path` and `value` members, which Effect's `Schema.Defect` does not read.

Prior art: Effect's `RpcGroup` and `RpcClient` (`effect/src/rpc`, `RpcMessage.ts` for the envelopes, `RpcSerialization.ts` for json and ndjson); Go's `net/rpc` and gRPC's generated stubs; Elixir's `:erpc` over Erlang distribution (a call is a process; a dead node is an error, not a typed failure); Gleam and ReScript generate no RPC layer: their users write JSON encoders and decoders by hand (`gleam/json`) or bind a TypeScript client (tRPC). Ergo's contract is the operation type itself, so the server and the client are derived, not declared twice.

## TypeScript content mapper and loaders

`ergo tsmap` serves TypeScript 7.1.0-beta's content mapper protocol on
stdio. It reads the checked package with the editor buffer as a read-only
overlay. The mapper and `ergo tsmap --emit-js FILE.ergo` share
`internal/tsproj`: one walk of the checked types emits Effect RPC groups
and schemas, with TS declarations for checking or JS values for loading.
No Ergo function body is translated to JavaScript. No generated `.ts`
file is checked in.

The private local package [tools/ts](../../tools/ts/README.md) provides
Bun, Node and Vite loaders (and an esbuild adapter), the pinned TypeScript
and Effect versions, and setup instructions. Run `tsc --runExternalCode`;
without that explicit permission TypeScript refuses to start the mapper.
Content-mapped files are not emitted as JS by TypeScript, so a runtime
loader is required. Source diagnostics use `ergo`; positions support
UTF-8 and UTF-16. Renamed wire fields use Alias spans, not Verbatim spans.

Field keys follow [Codecs 2](types.md#codecs), including explicit literals.
Decoded TS properties keep the default projected names (`NodeID` as `nodeID`);
Effect 4's `Schema.encodeKeys` maps them to their declared encoded keys
(`"node_id"`). This is the pinned API's equivalent of Effect 3's `fromKey`.
When default names collide (`ID` and `Id`) but explicit keys distinguish them,
the colliding decoded properties keep their source spelling, so neither is
lost. Tagged variants, failure schemas and imported/instantiated records use
the same mapping. The seeded mapper/RPC property includes renamed keys,
non-ASCII and escaped keys, imported fields, and colliding default names.

Supported wire shapes are listed in the tool README. An unsupported
shape is a projection diagnostic, never an unchecked `any`. Each mapped
file exports its package's operation types and every exported wire record
and closed union, including data no operation reaches. Non-wire declarations
(machines, records with function fields or operation types as values) are
left out without a diagnostic. A wire type outside the supported TypeScript
profile still reports a projection diagnostic. Reachable imported types
use private schema names. Cross-file watch registration is not provided
yet: restart TypeScript watch after changing another Ergo file in the
package or an imported package. Runtime loads do not cache the projection.

The schemas follow std json's field and value forms: a record field of
`json.Optional[T]` uses `Schema.optionalKey(T)`, `json.Nullable[T]` uses
`Schema.NullOr(T)` in every position, and nesting composes these rules.
Optional outside a record field stays the tagged Absent/Present union.
The instantiated field type selects its form, including a generic field.
Std `json.Value` uses `Schema.Json`, its untagged JSON form; the tool
README lists what a JavaScript peer does not keep of it. A float64 uses
`Schema.Number`, whose JSON codec writes and reads the same non-finite
strings as std json; a float32 is a projection diagnostic.

Measured acceptance: `ERGO_TS_TEST=1 go test -count=1 -timeout 10m
./internal/tsproj` checks a seeded codec round trip, stock RpcClient
against an Ergo server under Bun/Node/Vite with json and ndjson, and four
signature mutations reported at TypeScript call sites. It is opt-in;
the Go-only gate starts no TypeScript tool.

Prior art: Gleam emits TypeScript declarations beside JS modules;
ReScript genType emits type adapters, with codecs supplied separately.
Ergo emits only the checked wire view: Go remains the target and the
schemas are for the peer. Elixir/Phoenix sends JSON events without a
shared static client contract. The TypeScript 7.1 mapper adds virtual
source and source spans; the loaders supply its missing runtime emit.

## Not yet

- Streams (`RpcSchema.Stream`, `Chunk` envelopes) are not served and a stream operation is not generated. Envelopes other than `Request` (`Ack`, `Ping`, `Eof`, `Interrupt`) are skipped: over HTTP a client's abort is the request's end. Request headers are not read.
