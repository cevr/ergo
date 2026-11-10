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
const first: string = await new Promise((resolve, reject) => {
  limiter.once("error", reject)
  limiter.once("exit", code => reject(new Error(`limiter exited ${code} before listening`)))
  createInterface({ input: limiter.stdout }).once("line", resolve)
})
const addr = first.match(/^listening (127\.0\.0\.1:\d+)$/)?.[1]
if (!addr) throw new Error(`limiter printed ${JSON.stringify(first)}, not its address`)

const protocol = RpcClient.layerProtocolHttp({ url: `http://${addr}/rpc` }).pipe(
  Layer.provide(RpcSerialization.layerNdjson),
  Layer.provide(FetchHttpClient.layer),
)
const app = handler(ClientLive.pipe(Layer.provide(protocol)))
const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: request => app.handler(request) })

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
for (const [method, path] of script) {
  const response = await fetch(`http://127.0.0.1:${server.port}${path}`, { method })
  const retry = response.headers.get("retry-after")
  const body = await response.text()
  console.log(`${method} ${path} -> ${response.status}${retry === null ? "" : ` retry-after ${retry}`}${body === "" ? "" : `: ${body}`}`)
}
await server.stop(true)
await app.dispose()
limiter.kill()
