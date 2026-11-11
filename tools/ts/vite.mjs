import { project } from "./project.mjs"

export default function ergo() {
  return {
    name: "ergo-wire-contracts",
    enforce: "pre",
    load(id) {
      const fileName = id.split("?", 1)[0]
      if (fileName.endsWith(".ergo")) return { code: project(fileName), map: null }
    },
    // A changed imported contract can change any mapped module's schemas.
    // Let Vite invalidate its graph rather than retain stale projections.
    handleHotUpdate(context) {
      if (context.file.endsWith(".ergo")) {
        context.server.moduleGraph.invalidateAll()
        context.server.ws.send({ type: "full-reload" })
        return []
      }
    },
  }
}
