import { Effect, Option } from "effect"
import { RpcClient } from "effect/rpc"
import { API } from "./domain/contract.ergo"

export const program = Effect.gen(function* () {
  const client = yield* RpcClient.make(API)
  const reply = yield* client.Take({ key: "a", cost: 3 })
  const remaining: number = reply.remaining
  yield* client.Take({ key: "b", cost: 5 }).pipe(Effect.catchTag("Limited", e => Effect.succeed(e.retryAfterMs)))
  const level = yield* client.Peek({ key: "a" })
  const present: Option.Option<unknown> = level
  return [remaining, Option.isSome(present)]
})
