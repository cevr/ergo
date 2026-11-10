import { Effect, Option } from "effect"
import { RpcClient } from "effect/rpc"
import { API, Draft, Snapshot, Snapshots, View } from "./domain/contract.ergo"

export const program = Effect.gen(function* () {
  const client = yield* RpcClient.make(API)
  const reply = yield* client.Take({ key: "a", cost: 3 })
  const remaining: number = reply.remaining
  yield* client.Take({ key: "b", cost: 5 }).pipe(Effect.catchTag("Limited", e => Effect.succeed(e.retryAfterMs)))
  const level = yield* client.Peek({ key: "a" })
  const present: Option.Option<unknown> = level
  return [remaining, Option.isSome(present)]
})

const draft: Draft = { name: "a" }
const view: View = { ended: null }
export const wireForms = [draft, view]
const snapshots: Snapshot[] = [{}, { body: null }, { body: { k: [1.5, "s", true, null, {}] } }]
// @ts-expect-error a function is not a JSON value
const hostValue: Snapshot = { body: () => 1 }
export const values = [snapshots, hostValue, Snapshots]
import { Point, Status, Écho } from "./plain/point.ergo"
const point: Point = { x: 1, y: 2 }
const status: Status = { _tag: "Ready", at: point }
const echo: Écho = { text: "unicode" }
export const standalone = [point, status, echo]
