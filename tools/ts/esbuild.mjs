import { project } from "./project.mjs"
import { dirname } from "node:path"

export default function ergo() {
  return {
    name: "ergo-wire-contracts",
    setup(build) {
      build.onLoad({ filter: /\.ergo$/ }, ({ path }) => ({
        contents: project(path), loader: "js", resolveDir: dirname(path),
      }))
    },
  }
}
