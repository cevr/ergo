// The case: start the Ergo limiter (ERGO_BIN), read the loopback address
// it prints first, serve the gateway on another loopback port, and send it
// a fixed script of requests, printing each status and body. The limiter
// runs in this process's group, so the runner's stop of the group ends it.

import { Effect, Layer } from "effect"
import { FetchHttpClient } from "effect/http"
import { RpcClient, RpcSerialization } from "effect/rpc"
import { spawn } from "node:child_process"
import { createInterface } from "node:readline"
import { ClientLive, handler } from "./gateway.ts"

const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built limiter")
const limiter = spawn(bin, [], { stdio: ["ignore", "pipe", "inherit"] })
// The limiter's lines, in order: its address first, then a line per note
// of a Hold. nextLine waits for the next one, so the case waits on the
// limiter's events, never on time.
const lines: string[] = []
const readers: Array<(line: string) => void> = []
createInterface({ input: limiter.stdout }).on("line", line => {
  const reader = readers.shift()
  if (reader) reader(line)
  else lines.push(line)
})
const nextLine = (): Promise<string> =>
  new Promise((resolve, reject) => {
    const line = lines.shift()
    if (line !== undefined) return resolve(line)
    readers.push(resolve)
    limiter.once("exit", code => reject(new Error(`limiter exited ${code} while the case waited for a line`)))
  })
limiter.once("error", error => { throw error })
const first = await nextLine()
const addr = first.match(/^listening (127\.0\.0\.1:\d+)$/)?.[1]
if (!addr) throw new Error(`limiter printed ${JSON.stringify(first)}, not its address`)

const protocol = RpcClient.layerProtocolHttp({ url: `http://${addr}/rpc` }).pipe(
  Layer.provide(RpcSerialization.layerNdjson),
  Layer.provide(FetchHttpClient.layer),
)
const app = handler(ClientLive.pipe(Layer.provide(protocol)))
// idleTimeout 0: a Hold waits on the limiter's events, never on a timer.
const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch: request => app.handler(request) })

const script: ReadonlyArray<readonly [string, string]> = [
  ["GET", "/fetch/api"],
  ["GET", "/fetch/api?cost=3"],
  ["GET", "/fetch/api"],
  ["GET", "/fetch/api"],
  ["GET", "/level/api"],
  ["POST", "/advance?ms=150"],
  ["GET", "/fetch/api"],
  ["POST", "/advance?ms=50"],
  ["GET", "/fetch/api"],
  ["GET", "/fetch/search?cost=4"],
  ["GET", "/fetch/search?cost=3"],
  ["GET", "/level/search"],
  ["GET", "/fetch/nope"],
  ["GET", "/level/nope"],
  ["POST", "/advance?ms=5000"],
  ["GET", "/level/api"],
  ["GET", "/level/search"],
]
const send = async (method: string, path: string, signal?: AbortSignal): Promise<string> => {
  const response = await fetch(`http://127.0.0.1:${server.port}${path}`, { method, signal })
  const retry = response.headers.get("retry-after")
  const body = await response.text()
  return `${method} ${path} -> ${response.status}${retry === null ? "" : ` retry-after ${retry}`}${body === "" ? "" : `: ${body}`}`
}
for (const [method, path] of script) {
  console.log(await send(method, path))
}

// Interruption: a Hold waits on an empty bucket; the client hangs up; the
// gateway's fiber is interrupted, which interrupts the RPC call, which
// stops the Ergo Hold, whose release runs. Then a Hold that the clock
// lets through.
console.log(await send("GET", "/fetch/search?cost=3"))
const hangUp = new AbortController()
const held = send("GET", "/hold/search", hangUp.signal).catch(() => "GET /hold/search -> the client hung up")
console.log(`limiter: ${await nextLine()}`)
hangUp.abort()
console.log(await held)
console.log(`limiter: ${await nextLine()}`)
const granted = send("GET", "/hold/search")
console.log(`limiter: ${await nextLine()}`)
console.log(await send("POST", "/advance?ms=1000"))
console.log(await granted)
console.log(`limiter: ${await nextLine()}`)
await server.stop(true)
await app.dispose()
limiter.kill()
