# TypeScript wire contracts

The source is an Ergo operation type. TypeScript reads its Effect RPC
schemas without a generated `.ts` file. Ergo still compiles only to Go.
`ergo build`, `check` and `test` never run these tools.

An exported constant `[]http.Route[E]` table also exposes a stock Effect
`HttpApi`, under the table's name. Its exported operation type names every
variant of `E` as an operation. The table names method, path and required
success `Status`; the operation names path parameter types, at most one
JSON payload parameter, and its success result. `E` is a closed union of
empty endpoint variants, each routed exactly once. Computed tables or
fields, repeated or missing endpoints, and ambiguous contracts are
projection diagnostics. Nothing evaluates the table's source in JavaScript.

For example, `examples/jobs/api/routes.ergo` maps to `Routes`:

```ts
import { HttpApiClient } from "effect/http-api"
import { Routes } from "./api/routes.ergo"
const client = yield* HttpApiClient.make(Routes, { baseUrl })
const job = yield* client.POST.SubmitJob({ payload: { name: "build" } })
const found = yield* client.GET.ShowJob({ params: { id: job.id } })
```

Groups are named by HTTP method. Changing ShowJob from GET to POST removes
`client.GET.ShowJob`, so even a method-only drift fails at its TypeScript
call site. Path parameter names bind operation parameters by name; one
remaining parameter is the JSON body itself, not a wrapper. A no-result
operation uses `HttpApiSchema.Empty(Status)`; results carry the table's
status annotation, including 201. A result with 204, 205 or HEAD is refused.
The handler uses `http.Success(found.Route, text)` so the server reads the
same status as the mapper. A package with failure rows exports one constant
literal `[]http.FailureStatus` table: `{Tag: "NotFound", Status: 404}` per
nominal failure tag. It covers the routed contracts' failures exactly once;
computed entries, missing/repeated/unknown tags, tag collisions between types
and statuses outside 400..599 are projection diagnostics. Several failure
types may share a status: Effect decodes their tagged bodies at that status.
Each endpoint projects only its own failure row, with per-schema statuses.
The handler encodes a closed union of its contract failures, then calls
`http.Failure(Failures, taggedJson)`; it never chooses those statuses by hand.
`Effect.catchTag("NotFound", ...)` receives typed fields on a stock client.
Transport refusals outside the operation row remain undeclared-status errors.
RPC projections retain their existing failures.

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
Schema.Int; the RPC boundary enforces the safe-number range. A float64 is
`Schema.Number`, whose JSON codec is std json's float form: a finite JSON
number, or the strings `"NaN"`, `"Infinity"` and `"-Infinity"`. Both
refuse other strings and numbers beyond the float64 range. A JavaScript
`-0` is written as `0`. A float32 (Go rounds it on decode), recursive
schemas, refinement predicates, by-name codec pairs, and open/generic
operation contracts are diagnostics, not `any`.

Std `json.Optional[T]` in a record field is `Schema.optionalKey(T)`:
an absent value has no key. `json.Nullable[T]` is `Schema.NullOr(T)`:
its empty value is null, but its field key is still required. The two
compose for a three-state field. Outside a record field, Optional keeps
the codec's tagged Absent/Present form. Instantiated generic fields follow
the same rule.

Std `json.Value` is `Schema.Json`: its wire form is the JSON value itself,
not the six-kind union. The schema admits only JSON values (null, booleans,
finite numbers, strings, arrays and string-keyed objects, at any depth). It
refuses undefined, functions, dates, maps and other host objects at decode
and at encode. A TypeScript peer sees parsed JSON, so it does not keep what
`json.Value` keeps in Go: a number's spelling (and digits beyond a
JavaScript number's precision), a repeated key (the last one stays), and
the order of integer-like keys. A number spelling beyond the float64 range
is a valid `json.Value` in Go, but JavaScript parses it as an infinity,
which `Schema.Json` refuses.

No source body runs in TypeScript. Laws stay in Ergo. Each mapped file
exposes its package's operation types and every exported wire record and
closed union, even when no operation reaches it. A non-wire type (a machine,
a record with a function field or an operation type as data) is left out
without a diagnostic. A wire type outside the supported TypeScript profile
still reports a projection diagnostic. Imported wire types are included
under private names.
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
* Inside a copy of `tools/ts` itself (its own `package.json` is the
  nearest one), `bun --preload ergo-ts/bun` and `node --import
  ergo-ts/node` resolve by package self-reference (measured, Bun 1.4.4,
  Node 24.11.1). TypeScript 7.1.0-beta's `contentMappers` does not:
  `"package": "ergo-ts"` fails with TS18075 there. Link the copy into its
  own `node_modules` (`node_modules/ergo-ts` -> the copy), as the
  conformance check does, or name the copy by absolute path in
  `"package"`.

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
