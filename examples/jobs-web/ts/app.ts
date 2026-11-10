// The page's program: the jobs session against the server that served
// this page, at /rpc of the page's own origin, each line appended to the
// page's log. The case builds this file with Bun and the ergo-ts loader
// into the folder jobs-web serves.

import { Effect } from "effect"
import { protocol, session } from "./jobs.ts"

const log = document.getElementById("log")
if (log === null) throw new Error("the page has no #log")
const print = (line: string) => {
  log.append(`${line}\n`)
}

Effect.runFork(
  session(print).pipe(
    Effect.provide(protocol(new URL("/rpc", location.href).href)),
    Effect.catchCause(cause => Effect.sync(() => print(`the session failed: ${cause}`))),
  ),
)
