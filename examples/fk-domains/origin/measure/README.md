# foldkit-domains: the original, measured (plan.md §20)

Everything here is **measured** unless a line says inferred. Planner run of 2026-11-13, scratch only.

## What was measured, and how

- Source: `https://github.com/foldkit/foldkit-domains` at `039c941ad42c9db46d1aa8014c770c9a841c75ad` (2026-11-07; MIT, (c) 2025 Devin Jameson), cloned to `/tmp/opencode/fk-domains` (1.5 GB with its packages; removed at hand-back).
- Build: `pnpm install --frozen-lockfile`, `pnpm build` (pnpm 12.18.0 through `npx`, Node 24.11.1, Vite 8.3.5). The **production** build: client bundle, prerendered `/`, worker bundle.
- Server: `wrangler dev --local --ip 127.0.0.1 --port 5391` (workerd on this host; the worker serves the assets, the SSR and `/rpc`). No network: the latency of a deployed worker is not in these numbers.
- Browser: the repository's own Playwright 1.64.1 with headless Chromium 156.0.8078.4, viewport 1280 x 720, device scale 1, one fresh context per run.
- Scripts (kept): `measure.mjs` (phases load, type, scroll, sort, heap), `show.mjs` (prints a result file), `burst.mjs` (four ways to type). Raw results: `results-{native,native-heap,k1,k10,k10-sort}.json`. `patch-catalog.ts` (a copy is kept in the same folder) made the 1,000 and 10,000 catalogs (below): it appends synthetic TLDs `x00001…` to `src/shared/tld.ts` of the clone and can restore the file. `wire.mjs` recorded the server's wire (section 6). The scripts load Playwright from the clone's `node_modules`, so a rerun needs the clone at `/tmp/opencode/fk-domains` again (clone at the pin, `pnpm install --frozen-lockfile`, `pnpm build`, then `wrangler dev --local --ip 127.0.0.1 --port 5391`).
- Each run was inside `systemd-run --user --scope -p MemoryMax=6G -p MemorySwapMax=0`.

**Counted quantities are exact** and do not depend on the host: requests, bytes, DOM elements, DOM mutations (a `MutationObserver` on the document that counts every node of each added and removed subtree), mounted rows, heap bytes after two forced collections (`HeapProfiler.collectGarbage`).

**CPU numbers are upper bounds.** They are `Performance.getMetrics` with `timeDomain: threadTicks`: CPU time of the renderer's main thread (`ScriptDuration`, `TaskDuration`, `LayoutDuration`), not wall time. The host was shared and loaded the whole time (load average 31 to 62 on 16 cores), so cache and frequency effects are in them. Wall times (first paint, hydration) are worse still and are given only for the record. No number here is a claim about the original's speed on a quiet machine; they are the bar the port's **counts** are set against, and the shape (what grows with what).

## 1. Bytes

| File | Raw | As served (gzip) |
| --- | --- | --- |
| client JS, one file (`app-*.js`) | 821,314 B | 240,611 B |
| CSS | 37,860 B | 7,805 B |
| `index.html` (prerendered `/`) | 30,422 B | 4,784 B |
| first load, 3 files, transfer with headers | 888,574 B decoded | 278,716 B |
| five woff2 fonts (loaded when used) | 76 kB | — |
| worker bundle `dist/server/fetch.js` | 1,355,652 B | 305 kB |

The client bundle includes Foldkit's DevTools overlay (a production dependency; "deployed DevTools"). With the 10,000 catalog the client JS is 887,422 B: the catalog is in the bundle.

## 2. Load and hydration (5 runs each, median)

| Page | First contentful paint | Hydrated (the `data-foldkit-app` mark is gone) | Script CPU to hydration | Heap after GC, settled | Elements (nodes) | Requests |
| --- | --- | --- | --- | --- | --- | --- |
| `/` (prerendered) | 244 ms | 1,084 ms | 337 ms | 9.74 MB | 375 (1,123) | 0 |
| `/?q=foldkit` (request-time SSR) | 356 ms | 1,275 ms | 392 ms | 11.21 MB | 780 (1,956) | 2 (`{0,4}` 209 B, `{4,56}` 680 B gzip) |
| `/?q=foldkit`, CPU throttled x4 | 572 ms | 3,047 ms | 1,719 ms | 11.11 MB | 780 | 2 |

Runs, hydrated ms, for `/`: 1150, 1084, 1189, 955, 986. The page shows its server-rendered content at first paint; the search field takes keys before hydration through the inline bootstrap script (read in `src`, not timed here). On `/?q=foldkit` the settled page has 36 rows and 4 cards mounted.

## 3. Typing (the debounce, the requests, the work per key)

The debounce is 150 ms and applies to the first range of a query only (`src/client/domainsQuery.ts`).

| Script | Requests | Notes |
| --- | --- | --- |
| "foldkit", each prefix left to settle (400 ms and more between keys) | **14**: two per prefix (`{0,4}` then `{4,56}`), 207 to 211 B and 669 to 682 B gzip | no aborts |
| "foldkit", keys 100 to 300 ms apart (`press`, 60 ms pause, loaded host) | **2**: the last query's two | the minimum |
| "foldkit", keys about 140 ms apart (`type` with 30 ms) | 2 | |
| "foldkit", a 40 ms pause and four reads of the counters between keys | 8, of which 4 aborted | prefixes whose debounce ran out before the next key |
| then 3 backspaces and the same 3 keys, 400 ms apart, inside the minute | **1** more request (11 of 12 range reads came from the cache) | the cache works |

- **Per key, while typing inside the debounce:** 469 nodes removed and 469 added (the results region is rebuilt as a loading state), 96 mutation records, 3 attribute changes; script CPU 77 ms median (47 to 93).
- **Per key, each prefix settling:** 533 to 789 nodes added, 265 to 789 removed, 129 mutation records; script CPU 128 ms median (101 to 204); layout 2.8 ms.
- **Not deterministic at mid gaps (measured, `burst.mjs` and the `burst250` phase):** on the loaded host, four ways of typing the same seven keys with 390 to 1,180 ms between the input events made 8, 5, 13 and 2 requests, where every prefix settling gives 14. The chain from a key to the request (URL replace, route change, read the clock, sleep 150 ms, fetch) runs on the main thread between renders, so the request count for the same script depends on the machine's speed. The port's counts are taken on a virtual clock and cannot vary.

## 4. Scrolling the virtual list

The catalog has 556 TLDs. The 1,000 and 10,000 rows are a **patched original**: synthetic TLDs appended to `src/shared/tld.ts`, rebuilt; nothing else changed. Steps of 576 px (0.8 of the viewport), two animation frames and 30 ms after each. Relevance order unless marked.

| Catalog | Page height | Steps to the end | Mounted rows, max | Mounted groups, max | Elements, max | Nodes added / removed per step (median) | Script CPU per step: median, p95, max | Requests while scrolling | Heap after GC at the end |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 556 (native) | 11,099 px | 20 | 72 | 12 | 1,200 | 310 / 340 | 69 ms, 144 ms, 144 ms | 9 pages of 60 (579 to 620 B gzip each) | 13.5 MB |
| 1,000 (patched) | 19,535 px | 34 | 72 | 12 | 1,200 | 310 / 340 | 106 ms, 274 ms, 311 ms | 16 | 14.6 MB |
| 10,000 (patched) | 190,535 px | first 60 of about 330 | 72 | 12 | 1,200 | 340 / 340 | **519 ms, 1,083 ms, 1,715 ms** | 30 in 60 steps | 35.2 MB |
| 556, price order | 11,099 px | 20 | 72 | 12 | 1,200 | 340 / 340 | 55 ms, 83 ms, 83 ms | 0 (one request of 556 before: 4,213 B gzip) | 13.8 MB |
| 1,000, price order | 19,535 px | 34 | 72 | 12 | 1,200 | 340 / 340 | 74 ms, 124 ms, 135 ms | 0 (one of 1,000: 7,073 B gzip) | 15.0 MB |
| 10,000, price order | 190,535 px | first 30 | 72 | 12 | 1,200 | 340 / 340 | 393 ms, 546 ms, 549 ms | 0 (one of 10,000: 57,345 B gzip) | 36.6 MB |

Choosing the price order: script CPU 266 ms (556), 358 ms (1,000), 1,020 ms (10,000); 587 nodes added each time.

What the table shows:

- **The DOM is bounded and does not grow with the catalog**: at most 72 rows in 12 groups of 6, at most 1,200 elements. The virtual list works.
- **The work per scroll step is not bounded**: it grows with the catalog, 69 ms at 556 and 519 ms at 10,000, for the same 340 nodes changed. So a part of each update is proportional to the catalog, not to the window (inferred cause: the list of candidate names is `label + each TLD` over the whole catalog, `src/shared/domain.ts`, and the order, the filters and the per-row view data are derived over that whole list on an update, before the virtual list takes its window; not profiled). At 10,000 a scroll step takes about 2.3 s of wall time on the loaded host.
- **Every step replaces about a quarter of the mounted nodes** (340 of 1,200): leaving groups are removed and entering ones are built; a row's subtree is not reused.
- **The heap grows with the catalog before any row is asked for**: 11.2 MB at 556, 12.1 MB at 1,000, 31.9 MB at 10,000, on the settled first page.

## 5. A long session

400 distinct queries (`session1` to `session400`), each left to settle (350 ms without a request), two forced collections before each reading.

| Queries | 0 | 50 | 100 | 150 | 200 | 250 | 300 | 350 | 400 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Heap after GC, MB | 9.70 | 16.45 | 18.16 | 19.45 | 20.72 | 21.45 | 22.83 | 23.97 | 25.10 |
| DOM nodes | 1,086 | 1,351 | 1,351 | 1,351 | 1,351 | 1,351 | 1,351 | 1,351 | 1,351 |
| Event listeners | 44 | 103 | 103 | 103 | 103 | 103 | 103 | 103 | 103 |

- 2.00 requests per query (800 in all); script CPU 167 ms per query.
- **The DOM is flat. The heap is not**: after the first 50 queries it grows by about 24.7 kB per query and has not levelled at 400 (8.65 MB over the last 350). The query cache is bounded (100 settled entries), so the cache does not explain growth past query 100 (inferred: the DevTools history, which is part of the production bundle, or the router's history state; not separated here).

## 6. The server's wire (recorded; `../wire/`, by `wire.mjs`)

20 cases, each with its request body and its response text; `wire/index.json` has the status, the content type, `Cache-Control` and the sizes.

- A success is `[{"_tag":"Exit","requestId":n,"exit":{"_tag":"Success","value":[…listings…]}}]`; a listing is `{"_tag":"Unavailable","name","tld"}` or `{"_tag":"Available","name","tld","price","renewalPrice","years","isPremium"}`. Sizes: 4 listings 409 B, 56 listings 6,777 B, the whole catalog 67,639 B (raw).
- With no offset and no limit the answer is the whole catalog (66,081 B for `acme`).
- A payload that does not decode is **200** with `"exit":{"_tag":"Failure","cause":[{"_tag":"Die","defect":"…"}]}`: an empty label, an upper-case label, a label with a dot (the pattern is `^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`), an offset of -1 or of 556 ("between 0 and 555"), a limit of 0 ("between 1 and 556"). A body that is not JSON is 200 with a `Defect` frame.
- Transport: a content type that is not JSON is 415; a body of 1,024 bytes is taken, one of 1,025 is 413; `GET /rpc` is 404; every `/rpc` answer has `Cache-Control: no-store`; an unknown page path is 404.

## 7. What was not measured

- A deployed worker (network, cold start, edge cache). The first paint of a remote page.
- A quiet host. Every CPU number above is an upper bound.
- Frame rate while scrolling by wheel or touch (the steps are programmatic jumps); the mobile layout; the dialogs and the cart; accessibility (the repository's 11 a11y e2e tests were read, not run).
- The cause of the heap growth and of the per-step growth (both marked inferred).
- The original's own test suites were not run (20 stories, 15 scenes, 7 query tests, 5 server tests, 55 e2e tests; counted by reading).
