// Effect 4.1.5 on TypeScript 7.0. The same program as lookup.ergo.

import { Context, Effect, Schema } from "effect"

export interface Answer {
  readonly name: string
  readonly addr: string
}

export class NotFound extends Schema.TaggedError<NotFound>()("NotFound", {
  name: Schema.String,
}) {}

export class Resolver extends Context.Service<
  Resolver,
  { readonly resolve: (name: string) => Effect.Effect<Answer, NotFound> }
>()("hosts/Resolver") {}

// Effect<Answer, NotFound | TimeoutError, Resolver>, inferred
export const lookup = Effect.fn("hosts.lookup")(function* (name: string) {
  const resolver = yield* Resolver
  return yield* resolver.resolve(name).pipe(Effect.timeout("2 seconds"))
})

// Effect<string, never, Resolver>, inferred
export const address = Effect.fn("hosts.address")(function* (name: string) {
  const answer = yield* lookup(name).pipe(
    Effect.catchTags({
      NotFound: () => Effect.succeed({ name, addr: "unknown" }),
      TimeoutError: () => Effect.succeed({ name, addr: "timed-out" }),
    }),
  )
  return answer.addr
})
