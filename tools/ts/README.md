# TypeScript wire contracts

The source is an Ergo operation type. TypeScript reads its Effect RPC
schemas without a generated `.ts` file. Ergo still compiles only to Go.
`ergo build`, `check` and `test` never run these tools.

Use the pins in `package.json`: Effect 4.1.2 and TypeScript 7.1.0-beta.
Install this private package from its local path; publishing needs an owner
request. Put the matching `ergo` executable on PATH. Runtime loaders also
accept `ERGO=/absolute/path/to/ergo`.

```json
{
  "compilerOptions": {
    "strict": true, "noEmit": true, "target": "es2022",
    "module": "preserve", "moduleResolution": "bundler", "skipLibCheck": true
  },
  "contentMappers": [{ "package": "ergo-ts", "extensions": [".ergo"] }],
  "include": ["*.ts"]
}
```

Run `tsc --runExternalCode -p .`. This permission is required. In an editor,
enable the mapper only for a trusted workspace. Import the contract as
`import { Routes } from "./routes.ergo"` and pass it to stock `RpcClient.make`.

TypeScript does not emit mapped files. Choose a runtime loader:

* Bun: `bun --preload ergo-ts/bun client.ts`
* Node 24+: `node --import ergo-ts/node client.ts`
* Vite: `import ergo from "ergo-ts/vite"`; set `plugins: [ergo()]`.
* esbuild: import the default plugin from `ergo-ts/esbuild` and add it to
  `plugins`. It uses the same emitter, not a separate implementation.

The loaders call `ergo tsmap --emit-js FILE.ergo`. They pass arguments
directly, not through a shell. A source or projection failure stops the
load. The mapper checks unsaved source as a read-only overlay. Diagnostics
use source `ergo` at the Ergo span; mapped wire names use Alias spans.
UTF-8 and UTF-16 positions are supported.

Supported: exported operation types with named parameters, records, closed
unions, Option, slices, string-key Maps, tuples, strings, booleans and
integers. Unit results use Schema.Void. Tags and field keys follow the
derived codec. Fail rows become tagged error schemas. Integers use
Schema.Int; the RPC boundary enforces the safe-number range. Non-finite
float forms, recursive schemas, refinement predicates, by-name codec pairs,
and open/generic operation contracts are diagnostics, not `any`.

No source body runs in TypeScript. Laws stay in Ergo. Each mapped file
exposes its package; imported wire types are included under private names.
There is no runtime projection cache. For TypeScript incremental builds,
bump the package version when changing the mapper, or use `tsc --build
--force`: TypeScript keys mapped output by mapper package/version/options.
Restart watch after editing a different Ergo file in the same package or
an imported Ergo package (cross-file watch registration is not yet provided).

## Opt-in conformance

Warm the Bun cache once from the reviewed lockfile. The check then installs
with `bun install --frozen-lockfile --offline`; missing tools or dependencies
are errors, never a successful skip when the TS leg is selected.

`ERGO_TS_TEST=1 go test -count=1 -timeout 10m ./internal/tsproj`

The Go-only fast gate does not select this leg. The batch selects it before
landing TypeScript-related units. The schemas in the check are generated in
scratch space, never checked in.
