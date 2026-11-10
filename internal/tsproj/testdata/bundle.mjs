import { build } from "vite"
import ergo from "./vite.mjs"
import { build as buildEsbuild } from "esbuild"
import ergoEsbuild from "./esbuild.mjs"

await build({
  configFile: false,
  logLevel: "error",
  plugins: [ergo()],
  build: {
    ssr: "runtime.mjs",
    outDir: "output",
    emptyOutDir: false,
    rollupOptions: {
      external: id => id === "effect" || id.startsWith("effect/") || id.startsWith("node:"),
      output: { entryFileNames: "client.mjs" },
    },
  },
})

await buildEsbuild({
  entryPoints: ["runtime.mjs"], bundle: true, platform: "node", format: "esm",
  outfile: "output/esbuild.mjs", plugins: [ergoEsbuild()],
  external: ["effect", "effect/*", "node:*"],
})
