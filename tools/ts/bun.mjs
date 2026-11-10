import { plugin } from "bun"
import { project } from "./project.mjs"

plugin({
  name: "ergo-wire-contracts",
  setup(build) {
    build.onLoad({ filter: /\.ergo$/ }, ({ path }) => ({ contents: project(path), loader: "js" }))
  },
})
