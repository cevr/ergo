import { Cause, Effect, Exit, Layer, Option, Schema } from "effect"
import { FetchHttpClient } from "effect/http"
import { RpcClient, RpcSerialization } from "effect/rpc"
import { API, Bundle, Reply, Limited, Level } from "./domain/contract.ergo"
import { readFileSync } from "node:fs"

const [url, serialization, valuesFile] = process.argv.slice(2)
const values = JSON.parse(readFileSync(valuesFile, "utf8"))
const codec = Schema.toCodecJson(Bundle)
const canonical = value => JSON.stringify(value, (_key, value) =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)))
    : value)
// A derived Go codec produces these bytes. Decode and re-encode using the
// emitted schemas; the Go harness compares canonical JSON, not text order.
for (const value of values) {
  const decoded = Schema.decodeUnknownSync(codec)(value)
  if (value.first_id !== 1 || value.second_id !== 2 || decoded.ID !== 1 || decoded.Id !== 2) throw new Error("colliding defaults lost a field")
  if (value.label.label_text !== "tag" || decoded.label.text !== "tag") throw new Error("imported field key drift")
  const partial = value.levels.find(level => level._tag === "Partial")
  if (partial.token_count === undefined || "tokens" in partial) throw new Error("variant key drift")
  // The generated Extra properties keep their decoded names, while their
  // seed-selected wire names are independent of the projection and Go codec.
  for (const [name, text] of Object.entries(decoded)) {
    if (!/^extra\d+$/.test(name)) continue
    const [, seed, index] = text.split("-")
    const key = `wire_${seed}_${index} 🧭"\\\u0007\u{e0001}`
    if (value[key] !== text || name in value) throw new Error(`renamed key drift: ${key}`)
  }
  if (!Object.keys(decoded).some(name => /^extra\d+$/.test(name))) throw new Error("renamed fields lost")
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

// Schema.Defect does not retain the path/value members. Read the HTTP
// envelope independently to check the generator's integer walker path.
for (const [wide, path] of [
  [{ ...values[0], first_id: 9007199254740992 }, "value.first_id"],
  [{ ...values[0], levels: [{ _tag: "Partial", token_count: 9007199254740992 }] }, "value.levels.0.token_count"],
]) {
  const response = await fetch(url, {
    method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ _tag: "Request", id: 0, tag: "Echo", payload: { value: wide }, headers: [] }),
  })
  const [answer] = await response.json()
  const defect = answer.exit?.cause?.find(reason => reason._tag === "Die")?.defect
  if (defect?.name !== "Unrepresentable" || defect?.path !== path) throw new Error(`wide int path drift: ${JSON.stringify(answer)}`)
}

const program = Effect.gen(function* () {
  const client = yield* RpcClient.make(API)
  console.log(JSON.stringify(Schema.encodeSync(Schema.toCodecJson(Reply))(yield* client.Take({ key: "a", cost: 3 }))))
  console.log(JSON.stringify(yield* client.Take({ key: "b", cost: 5 }).pipe(
    Effect.catchTag("Limited", e => Effect.succeed(Schema.encodeSync(Schema.toCodecJson(Limited))(e))),
  )))
  console.log(JSON.stringify(yield* client.Take({ key: "missing", cost: 1 }).pipe(
    Effect.catchTag("Unknown", e => Effect.succeed({ _tag: e._tag, key: e.key })),
  )))
  const some = yield* client.Peek({ key: "a" })
  const none = yield* client.Peek({ key: "missing" })
  if (!Option.isSome(some) || !Option.isNone(none)) throw new Error("Option drift")
  console.log(JSON.stringify(Schema.encodeSync(Schema.toCodecJson(Level))(some.value)))
  yield* client.Reset({})
  console.log("null")
  const huge = yield* Effect.exit(client.Take({ key: "a", cost: 9007199254740992 }))
  if (!Exit.isFailure(huge) || !Cause.hasDies(huge.cause)) throw new Error("wide int did not die")
  const wide = Schema.decodeUnknownSync(codec)(values[0])
  const refused = yield* Effect.exit(client.Echo({ value: { ...wide, ID: 9007199254740992 } }))
  if (!Exit.isFailure(refused) || !Cause.hasDies(refused.cause)) throw new Error("wide renamed field did not die")
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
