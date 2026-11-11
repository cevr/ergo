import { registerHooks } from "node:module"
import { fileURLToPath } from "node:url"
import { project } from "./project.mjs"

registerHooks({
  load(url, context, nextLoad) {
    if (url.startsWith("file:") && new URL(url).pathname.endsWith(".ergo")) {
      return { format: "module", source: project(fileURLToPath(url)), shortCircuit: true }
    }
    return nextLoad(url, context)
  },
})
