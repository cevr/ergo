# domain-search and its original

What `examples/domain-search` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

A domain search, ported from `foldkit/foldkit-domains` at `039c941a` (MIT, (c) 2025 Devin Jameson; `origin/`).
## How it differs from the original, and why

- **One operation, named for the wire.** The operation is `SearchDomains`, the original's RPC tag, so its wire name is the original's. Its payload is `{label, offset?, limit?}`: the range parameters are `json.Optional`. A search with no range lists the whole catalog, as the original's defaults do.
- **The failure is `Down`.** The original's `SearchError` has one variant, `Unavailable`. Here `Listing` already has a variant of that name, so the failure is `Down`. No recorded case fails this way.
- **The bounds are types.** A label, an offset or a limit out of range does not decode. As in the original, it is a 200 whose frame is a `Die`. The original's defect text is Effect's schema message, and the port's is `rpc.Decode`'s, so the texts differ.
- **A body that is not JSON** is a 200 whose one frame is a `Defect` named `SyntaxError`, as the original's Effect server answers it; std `rpc.Answer` gives it. Its message is Go's JSON error, not JavaScript's.
- **No stale prices.** The original shows a stale answer while it fetches the range again, and keeps it if that fetch fails. The port follows the model's `FreshBound`, so a stale entry is a miss. A failed refresh shows its failure and a retry, as the original's scene "failed searches expose a retriable alert without stale prices" asks.
- **A navigation starts its search at once.** A link or a step back or forward is not typing, so it does not wait for the debounce. A URL the page opens at does wait, as the original's first range does.
- **A retry reloads only what the window shows.** Every load that failed is marked not loaded, and the planner asks for the first one in the window, or for the whole catalog when the criteria need it.
- **Typing back to fresh prefixes makes no request.** The original made 1 request for three backspaces and the same three keys, and its records do not say which read missed. The port makes 0, and that count does not depend on the host (receipt P1).
- **A GET of `/rpc` is 404** with no body, as the original's router has one `POST` route; std `rpc.Answer` answers it so, before the endpoint's own refusals.

## The wire, checked against the original

`server/recorded/` holds 20 cases that FD0 recorded from the original's worker with `wire.mjs`, with `index.json` (status, content type, `Cache-Control`). The `server` test `WireAgreesWithOrigin` reads them through `embedded`. For each case, `server.Answer` must give the original's status, `Content-Type` and `Cache-Control`. For a 200 it must also give the same frames: a success has the same listings, field for field, and a failure or a defect must be the same kind.

The case's body reaches `Answer` as `http.Serve` hands it over under `server.Limits()`: a body over `MaxBodyBytes` is `http.BodyTooLarge`. So the 1,024-byte and 1,025-byte cases check the limit that the binary serves with.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files in `origin/upstream/` (`origin/source`). FD3a restated the stories, in `search/stories.ergo`. Two of them are not restated: the links story is the view's, and the early-handoff story belongs to the server-session and wasm forms. FD4b restates the scenes.
