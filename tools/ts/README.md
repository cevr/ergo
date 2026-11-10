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
exposes its package's operation types and the exported types they reach;
other exported types (machines, records no operation carries) are not on
the wire and are left out. Imported wire types are included under
private names.
There is no runtime projection cache. For TypeScript incremental builds,
bump the package version when changing the mapper, or use `tsc --build
--force`: TypeScript keys mapped output by mapper package/version/options.
Restart watch after editing a different Ergo file in the same package or
an imported Ergo package (cross-file watch registration is not yet provided).

## Installing: never inside the checkout

Rule: dependencies of `tools/ts` are installed outside the Ergo checkout,
never as `tools/ts/node_modules`. Bun installs by hard-linking files from
its global cache, so an install inside the checkout gives checkout files
other names outside it. The closure sweep (`internal/closure`) refuses
such a checkout, because a test could read or change a checkout file
through a name the sweep cannot see. Skipping git-ignored dependency
folders in the sweep would make that blind spot permanent, so the sweep
keeps refusing and the install moves out.

* To warm Bun's cache for the conformance check, copy the reviewed pins to
  a scratch directory and install there:

  ```sh
  d=$(mktemp -d) && cp tools/ts/package.json tools/ts/bun.lock "$d" &&
    (cd "$d" && bun install --frozen-lockfile --ignore-scripts)
  ```

  Delete the scratch directory afterwards; the cache stays warm.
* To use the mapper in a project, add `ergo-ts` from its local path in
  that project (`bun add /path/to/ergo/tools/ts`); the install lands in the
  project's `node_modules`, outside the Ergo checkout.

If the sweep reports "a hard link outside the checkout is another name of
a checkout file" under `tools/ts/node_modules`, delete that folder.

## Opt-in conformance

The check copies the tools into its own scratch directory (`t.TempDir()`)
and installs there with `bun install --frozen-lockfile --offline`; it never
writes inside the checkout. Missing tools or dependencies are errors, never
a successful skip when the TS leg is selected.

`ERGO_TS_TEST=1 go test -count=1 -timeout 10m ./internal/tsproj`

The Go-only fast gate does not select this leg. The batch selects it before
landing TypeScript-related units. The schemas in the check are generated in
scratch space, never checked in.
