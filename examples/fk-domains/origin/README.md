# The original: foldkit-domains

- **Path:** `https://github.com/foldkit/foldkit-domains`
- **Commit:** `039c941ad42c9db46d1aa8014c770c9a841c75ad` (2026-11-07)
- **License:** MIT, Copyright (c) 2025 Devin Jameson (`upstream/LICENSE`, copied from the commit).

`fk-domains` ports this program: a domain search page on Foldkit, Effect and Alchemy (plan §20). This folder holds what the port is checked against. Nothing in it runs in the examples gate.

| Path | What it is |
| --- | --- |
| `upstream/` | The story and scene test files of the commit, as they are, with the license: `story/application.story.test.ts`, `story/cachedDomains.story.test.ts`, `story/progressiveDomains.story.test.ts` (from `src/client/story/`) and `scene.test.ts` (from `src/client/`). Only these files are vendored. |
| `tests.txt` | One line for each `it.effect` call in those files: `FILE: TITLE -> RESTATEMENT` (FK0's form). An `it.effect.each` gets one line, with its title as written. Nothing is restated yet: FD3a restates the stories on the search machine, and FD4b restates the scenes as TestHost scripts. |
| `titles.ts` | `bun origin/titles.ts` checks `tests.txt` against the titles it reads from `upstream/`. Each title must have exactly one line with a restatement, and there must be no extra line. It exits 1 on a missing, extra, doubled or empty line. It stands in for FK0's check in `scripts/examples` until that lands. |
| `measure/` | The original, measured before any design. `README.md` gives the method, every number and what was not measured. It also holds the scripts (`measure.mjs`, `burst.mjs`, `show.mjs`, `patch-catalog.ts`) and the raw results (`results-*.json`). |
| `wire/` | The server's wire, recorded from the original at the commit by `wire.mjs`: 20 cases. Each case has its response body (`NN-name.response.txt`) and, when it sent one of at most 4 KB, its request body (`NN-name.request.json`); `index.json` gives each case's path, method, sizes, status, content type and cache control. There are 14 searches (among them the whole catalog, a page past the end, a taken name and four labels that do not decode) and 6 transport cases (a content type that is not JSON, a body of 1,024 and of 1,025 bytes, `GET /rpc`, a body that is not JSON, and one too large). FD1a's `WireAgreesWithOrigin` reads them. |

Counts. The plan counted 20 stories and 15 scenes by reading. `titles.ts` counts the `it.effect` calls instead: 26 in the story files and 16 in the scene file, 42 in all. Four story calls and one scene call are `it.effect.each` tables. The query, init and server tests (`domainsQuery.test.ts`, `init.test.ts`, `src/server/search.test.ts`) and the 55 e2e tests are not story or scene tests. Their rules are laws of §20.4 (`search/cache`, `domains.Registry`, the wire) and are not vendored.

The measuring scripts load Playwright from a clone of the commit at `/tmp/opencode/fk-domains`; `measure/README.md` says how to rebuild it. A rerun of `wire.mjs` writes into a `wire/` folder beside the script, so move its output up a level.
