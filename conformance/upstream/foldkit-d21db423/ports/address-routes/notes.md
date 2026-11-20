# address-routes and its original

What `examples/address-routes` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `routing` example (`conformance/upstream/foldkit-d21db423/examples/routing`, MIT).
How the port differs from upstream:

- **The People page is not a submodel.** Upstream folds a `People` submodel with its own messages (`GotPeopleMessage`, `informRouteChanged`). Here the page is a field of the machine's state, and its signals are the machine's. The screen draws the page as a part of the one view (`peoplePage`), with no boundary of its own.
- **Search ends the entry.** A shortcut or Search is an `Emit`, which ends the entry, so a search under way restarts its timer. Upstream's fetch runs on. The prelude's `Tell` (MQ-tell, a decision that stays and gives an output) removes this once it lands on this branch's base.
- **No external links.** Upstream's `ClickedLink` tells internal links from external ones (`load(href)`). Every link here is internal; `visit` is the address bar.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 16 story tests (13 in `story.test.ts`, 3 in `page/people.story.test.ts`) and 13 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests are the case `scenes`.

## Comparisons moved from the README

- Upstream builds the same from combinators (`literal`, `slash`, `int`, `rest`, `Route.query`, `Route.oneOf`).
