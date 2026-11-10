// The case: build the page (app.ts, with the ergo-ts loader, so the
// contract's schemas come from the same projection tsc reads) into a
// fresh folder beside index.html; start the Ergo job server (ERGO_BIN)
// on that folder; check that it serves the page and the built program;
// then run the page's own session against the same origin under Bun.
// The server runs in this process's group, so the runner's stop of the group
// ends it.

import { Effect } from "effect"
import ergo from "ergo-ts/esbuild"
import { spawn } from "node:child_process"
import { copyFileSync, mkdtempSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { createInterface } from "node:readline"
import { protocol, session } from "./jobs.ts"

const bin = process.env.ERGO_BIN
if (!bin) throw new Error("ERGO_BIN names the built jobs-web")

const web = mkdtempSync(join(tmpdir(), "jobs-web-"))
const built = await Bun.build({
  entrypoints: [join(import.meta.dir, "app.ts")],
  outdir: web,
  naming: "app.js",
  target: "browser",
  plugins: [ergo()],
})
if (!built.success) throw new AggregateError(built.logs, "bun build of app.ts failed")
copyFileSync(join(import.meta.dir, "index.html"), join(web, "index.html"))
const app = readFileSync(join(web, "app.js"), "utf8")

const server = spawn(bin, [web], { stdio: ["ignore", "pipe", "inherit"] })
server.once("error", error => { throw error })
const first = await new Promise<string>((resolve, reject) => {
  createInterface({ input: server.stdout }).once("line", resolve)
  server.once("exit", code => reject(new Error(`jobs-web exited ${code} before its address`)))
})
const addr = first.match(/^listening (127\.0\.0\.1:\d+)$/)?.[1]
if (!addr) throw new Error(`jobs-web printed ${JSON.stringify(first)}, not its address`)
const origin = `http://${addr}`

// The page: what a browser asks the origin for.
const get = async (method: string, path: string): Promise<[Response, string]> => {
  const response = await fetch(origin + path, { method })
  return [response, await response.text()]
}
const [page, html] = await get("GET", "/")
console.log(`GET / -> ${page.status} ${page.headers.get("content-type")}, ${html.includes(`<script type="module" src="/app.js">`) ? "loads /app.js" : "does not load /app.js"}`)
const [script, js] = await get("GET", "/app.js")
console.log(`GET /app.js -> ${script.status} ${script.headers.get("content-type")}, ${js === app ? "the built program" : "not the built program"}`)
for (const [method, path] of [["GET", "/missing.js"], ["POST", "/index.html"], ["GET", "/rpc"]] as const) {
  const [r] = await get(method, path)
  console.log(`${method} ${path} -> ${r.status}${r.headers.has("allow") ? ` allow ${r.headers.get("allow")}` : ""}`)
}

// The session, as the page runs it, against /rpc of the same origin.
await Effect.runPromise(session(line => console.log(line)).pipe(Effect.provide(protocol(`${origin}/rpc`))))

server.kill()
rmSync(web, { recursive: true })
