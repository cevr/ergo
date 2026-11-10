import { spawnSync } from "node:child_process"
import { isAbsolute } from "node:path"

// No cache: an edited contract or one of its Ergo dependencies must be seen
// by the next load. Both the mapper and this runtime path use tsproj.Project.
export function project(fileName) {
  if (!isAbsolute(fileName) || !fileName.endsWith(".ergo")) {
    throw new Error(`ergo-ts: expected an absolute .ergo file: ${fileName}`)
  }
  const result = spawnSync(process.env.ERGO ?? "ergo", ["tsmap", "--emit-js", fileName], {
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
    detached: true,
  })
  if (result.error || result.status !== 0) {
    throw new Error(`ergo-ts: projection failed for ${fileName}: ${result.error?.message ?? result.stderr}`)
  }
  return result.stdout
}
