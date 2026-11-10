// The gateway: an Effect HTTP server in front of the Ergo limiter. Its
// routes call the limiter through a stock Effect RpcClient built from the
// contract that `../limits/limits.ergo` declares; the content mapper gives
// TypeScript its types and the Bun loader its runtime schemas, so no
// generated .ts file exists. Each typed failure of the contract is a
// status: Limited is 429 with Retry-After, UnknownBucket 404, BadCost 400.

import { Context, Effect, Layer, Option } from "effect"
import { HttpRouter, HttpServer, HttpServerResponse } from "effect/http"
import { RpcClient } from "effect/rpc"
import { Limiter } from "../limits/limits.ergo"

const makeClient = RpcClient.make(Limiter)

// Client is the limiter as the gateway's routes see it.
export class Client extends Context.Service<Client, Effect.Success<typeof makeClient>>()("limiter/Client") {}

// ClientLive builds the client once, over the protocol layer it is given.
export const ClientLive = Layer.effect(Client, makeClient)

const query = (request: { readonly url: string }, name: string, fallback: number): number => {
  const value = new URL(request.url, "http://gateway").searchParams.get(name)
  return value === null ? fallback : Number(value)
}

const text = (body: string, status = 200, headers: Record<string, string> = {}) =>
  HttpServerResponse.text(body, { status, headers })

// Routes is the gateway's router: the client is read once, when the
// router is built, and each route closes over it.
export const Routes = HttpRouter.use(router =>
  Effect.gen(function* () {
    const limiter = yield* Client

    // GET /fetch/:key?cost=N: one take of the key's bucket.
    yield* router.add("GET", "/fetch/:key", request =>
      Effect.gen(function* () {
        const { key } = yield* HttpRouter.params
        const grant = yield* limiter.Take({ key: key ?? "", cost: query(request, "cost", 1) })
        return text(`granted, ${grant.remaining} left`)
      }).pipe(
        Effect.catchTag("Limited", e =>
          Effect.succeed(text(`limited for ${e.retryAfterMs} ms`, 429, {
            "retry-after": String(Math.ceil(e.retryAfterMs / 1000)),
          }))),
        Effect.catchTag("UnknownBucket", e => Effect.succeed(text(`no bucket ${e.key}`, 404))),
        Effect.catchTag("BadCost", e => Effect.succeed(text(`a take costs 1 to ${e.capacity}, not ${e.cost}`, 400))),
      ))

    // GET /level/:key: the bucket's level, or 404.
    yield* router.add("GET", "/level/:key", Effect.gen(function* () {
      const { key } = yield* HttpRouter.params
      const found = yield* limiter.Peek({ key: key ?? "" })
      if (Option.isNone(found)) {
        return text(`no bucket ${key}`, 404)
      }
      const l = found.value
      switch (l._tag) {
        case "Full":
          return text("full")
        case "Partial":
          return text(`${l.tokens} tokens`)
        case "Empty":
          return text(`empty for ${l.retryAfterMs} ms`)
      }
    }))

    // POST /advance?ms=N: the limiter's clock moves on.
    yield* router.add("POST", "/advance", request =>
      Effect.gen(function* () {
        yield* limiter.Advance({ ms: query(request, "ms", 0) })
        return HttpServerResponse.empty({ status: 204 })
      }))
  }))

// handler is the gateway as a web handler over a client layer.
export const handler = (client: Layer.Layer<Client>) =>
  HttpRouter.toWebHandler(Routes.pipe(Layer.provide(client), Layer.provide(HttpServer.layerServices)), { disableLogger: true })
