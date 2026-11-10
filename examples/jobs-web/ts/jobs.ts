// The jobs session: one program that the page runs in the browser and
// the case runs under Bun, against the Ergo job server at a URL. It calls
// the server through a stock Effect RpcClient built from the contract
// that `../jobs/jobs.ergo` declares; the content mapper gives TypeScript
// its types and the loader its runtime schemas, so no generated .ts file
// exists. Each typed failure of the contract is a line the page shows.

import { Effect, Layer, Option } from "effect"
import { FetchHttpClient } from "effect/http"
import { RpcClient, RpcSerialization } from "effect/rpc"
import { type Job, Jobs, type State } from "../jobs/jobs.ergo"

// describe is a state as the page shows it: the union's tags, matched
// exhaustively (a new state in the contract fails tsc here).
export const describe = (s: State): string => {
  switch (s._tag) {
    case "Queued":
      return "queued"
    case "Running":
      return `running step ${s.step + 1} of ${s.of}`
    case "Finished":
      return "finished"
    case "Failed":
      return `failed: ${s.why}`
    case "Cancelled":
      return "cancelled"
  }
}

const line = (j: Job): string => `#${j.number} ${j.name}: ${describe(j.state)}`

// protocol is Effect RPC over HTTP at url, ndjson framed: the server
// streams each call's Exit as it ends.
export const protocol = (url: string) =>
  RpcClient.layerProtocolHttp({ url }).pipe(
    Layer.provide(RpcSerialization.layerNdjson),
    Layer.provide(FetchHttpClient.layer),
  )

// session submits three jobs and a bad one, works them, cancels one
// while it runs and one after it ended, and asks for a job that is not
// there, printing a line for each answer.
export const session = (print: (line: string) => void) =>
  Effect.gen(function* () {
    const jobs = yield* RpcClient.make(Jobs)
    const list = Effect.gen(function* () {
      for (const j of yield* jobs.List({})) print(line(j))
    })
    const submit = (name: string, steps: number, failAt = 0) =>
      jobs.Submit({ spec: { name, steps, failAt } }).pipe(
        Effect.tap(id => Effect.sync(() => print(`submitted ${name} as #${id}`))),
        Effect.catchTag("Rejected", e => Effect.sync(() => print(`rejected ${JSON.stringify(name)}: ${e.reason}`))),
      )
    const cancel = (id: number) =>
      jobs.Cancel({ id }).pipe(
        Effect.tap(() => Effect.sync(() => print(`cancelled #${id}`))),
        Effect.catchTag("AlreadyDone", e => Effect.sync(() => print(`#${e.number} is already ${describe(e.state)}`))),
        Effect.catchTag("NoSuchJob", e => Effect.sync(() => print(`no job #${e.number}`))),
      )
    const work = (ticks: number) =>
      jobs.Work({ ticks }).pipe(Effect.tap(() => Effect.sync(() => print(`worked ${ticks}`))))

    yield* submit("build", 3)
    yield* submit("flaky", 4, 2)
    yield* submit("backup", 5)
    yield* submit("", 1)
    yield* submit("forever", 0)
    yield* list
    yield* work(2)
    yield* list
    yield* cancel(3)
    yield* work(3)
    yield* list
    yield* cancel(1)
    yield* cancel(9)
    const missing = yield* jobs.Status({ id: 9 })
    print(Option.isNone(missing) ? "no status for #9" : line(missing.value))
  }).pipe(Effect.scoped)
