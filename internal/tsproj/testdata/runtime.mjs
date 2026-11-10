import { Cause, Effect, Exit, Layer, Option, Schema } from "effect"
import { FetchHttpClient } from "effect/http"
import { RpcClient, RpcSerialization } from "effect/rpc"
import { API, Bundle, Draft, View } from "./domain/contract.ergo"
import { readFileSync } from "node:fs"
import { Point, Status } from "./plain/point.ergo"

Schema.decodeUnknownSync(Point)({ x: 1, y: 2 })
Schema.decodeUnknownSync(Status)({ _tag: "Waiting" })
Schema.decodeUnknownSync(Status)({ _tag: "Ready", at: { x: 1, y: 2 } })

const [url, serialization, valuesFile] = process.argv.slice(2)
const values = JSON.parse(readFileSync(valuesFile, "utf8"))
const codec = Schema.toCodecJson(Bundle)
Schema.decodeUnknownSync(Draft)({ name: "a" })
Schema.decodeUnknownSync(View)({ ended: null })
let missingNullableRefused = false
try { Schema.decodeUnknownSync(View)({}) } catch { missingNullableRefused = true }
if (!missingNullableRefused) throw new Error("Nullable incorrectly made its key optional")
const canonical = value => JSON.stringify(value, (_key, value) =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)))
    : value)
// A derived Go codec produces these bytes. Decode and re-encode using the
// emitted schemas; the Go harness compares canonical JSON, not text order.
for (const value of values) {
  const decoded = Schema.decodeUnknownSync(codec)(value)
  const encoded = Schema.encodeSync(codec)(decoded)
  if (canonical(encoded) !== canonical(value)) {
    throw new Error(`schema round trip differs: Go ${canonical(value)} TS ${canonical(encoded)}`)
  }
  console.log(JSON.stringify(encoded))
}
// Schema.Int (not Number) must reject fractions on decode, independently of
// the Go codec which can only produce an integer at an int position.
const fractional = { ...values[0], count: 0.5 }
let refused = false
try { Schema.decodeUnknownSync(codec)(fractional) } catch { refused = true }
if (!refused) throw new Error("integer schema admitted a fraction")

const program = Effect.gen(function* () {
  const client = yield* RpcClient.make(API)
  console.log(JSON.stringify(yield* client.Take({ key: "a", cost: 3 })))
  console.log(JSON.stringify(yield* client.Take({ key: "b", cost: 5 }).pipe(
    Effect.catchTag("Limited", e => Effect.succeed({ _tag: e._tag, retryAfterMs: e.retryAfterMs })),
  )))
  console.log(JSON.stringify(yield* client.Take({ key: "missing", cost: 1 }).pipe(
    Effect.catchTag("Unknown", e => Effect.succeed({ _tag: e._tag, key: e.key })),
  )))
  const some = yield* client.Peek({ key: "a" })
  const none = yield* client.Peek({ key: "missing" })
  if (!Option.isSome(some) || !Option.isNone(none)) throw new Error("Option drift")
  console.log(JSON.stringify(some.value))
  yield* client.Reset({})
  console.log("null")
  const huge = yield* Effect.exit(client.Take({ key: "a", cost: 9007199254740992 }))
  if (!Exit.isFailure(huge) || !Cause.hasDies(huge.cause)) throw new Error("wide int did not die")
  for (const value of values) {
    const decoded = Schema.decodeUnknownSync(codec)(value)
    const echoed = yield* client.Echo({ value: decoded })
    const encoded = Schema.encodeSync(codec)(echoed)
    if (canonical(encoded) !== canonical(value)) {
      throw new Error(`RPC echo differs: Go ${canonical(value)} TS ${canonical(encoded)}`)
    }
    console.log(JSON.stringify(encoded))
  }
})
const layer = RpcClient.layerProtocolHttp({ url }).pipe(
  Layer.provide(serialization === "json" ? RpcSerialization.layerJson : RpcSerialization.layerNdjson),
  Layer.provide(FetchHttpClient.layer),
)
await Effect.runPromise(program.pipe(Effect.scoped, Effect.provide(layer)))
