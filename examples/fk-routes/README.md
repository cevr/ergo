# fk-routes

Screens chosen by the address. There is a home screen, a list of people with a search, a person by number, a file tree walked by the rest of the path, a deeply nested route and a not-found screen. Four shortcuts navigate, and Back returns to the searches before. It is a port of Foldkit's `routing` example (`conformance/upstream/foldkit-d21db423/examples/routing`, MIT). This is stage b of FK8: the model and its view, on std ui's test host.

The program runs as an actor on a manual clock, with its screen on std ui's test host, whose history is the browser's. It plays a script of the user's acts on the screen. After each act it shows what changed: the address and its depth, and the page.

Tag: `default`.

```sh
fk-routes < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `start`, `visit`, `link`, `click`, `type`, `key`, `back`, `wait` and `stats`.

## The shape

- Package `route` reads and writes addresses.
  - `Route = Home | Nested | People{Search} | Person{ID} | FilesIndex | Files{Path} | NotFound{Path}`.
  - `Parse` reads a path and its query. It drops a trailing slash and skips empty segments. A person's id that is not a number, and any first segment no route has, are `NotFound`, which keeps the path.
  - `Print` writes a route's one path, escaping each segment and the search (`Escape`, `Unescape`), so the two round-trip.
  - Upstream builds the same from combinators (`literal`, `slash`, `int`, `rest`, `Route.query`, `Route.oneOf`). Here they are one `match` on the first segment.
- Package `app` is the program as the machine `Router`. Its input is the address it opens at.
  - **The address drives the screen.** `Navigated{Path}` sets the route. A People route also sets the search field, records the search and searches again. That is an `Enter`, so the search's timer starts anew. Any other route is a `Stay`, so a search under way still answers.
  - **Navigation goes through the host.** A link (`Followed{Path}`), a shortcut and Search `Emit` a `Go{Path}`. The host follows it as a browser follows a link: `ui.Navigate` pushes the path on its history (std ui's `Location`, H7), and the address change reaches the machine as `Navigated` through the screen's `ui.OnURL`, as upstream's `NavigateInternal` and `PushSearchUrl` commands push the URL and the router's `ChangedUrl` follows.
  - **The search answers on a timer.** A Loading page's timer gives `Fetched` after 300 ms, as upstream's `FetchPeople` sleeps.
- Package `people` is the list, `Search` and the history of searches. Package `files` is the tree, `Find` and `Size`.
- Package `screen` is upstream's view:
  - the navigation's four links on every route;
  - each route's page with upstream's headings and texts;
  - the People page's search field, Search, recent searches, status and the people as links;
  - the files' breadcrumb, entries, details and missing panel.
  A link is a `ui.Semantic` with the `Link` role whose press sends `Followed`.
- Package `script` is the host. It runs the program on `ui.NewTestHost` and follows each `Go`. The shortcuts are the host's, as upstream's key bindings are a subscription, not the view's.
- Stage a's own `Location` contract and its `memory.History` are gone: std ui's `Location` and `MemoryHistory` carry the same laws (`PushThenBackRestores`, `BackStopsAtTheStart`).

How the port differs from upstream:

- **The People page is not a submodel.** Upstream folds a `People` submodel with its own messages (`GotPeopleMessage`, `informRouteChanged`). Here the page is a field of the machine's state, and its signals are the machine's. The screen draws the page as a part of the one view (`peoplePage`), with no boundary of its own.
- **Search ends the entry.** A shortcut or Search is an `Emit`, which ends the entry, so a search under way restarts its timer. Upstream's fetch runs on. The prelude's `Tell` (MQ-tell, a decision that stays and gives an output) removes this once it lands on this branch's base.
- **No external links.** Upstream's `ClickedLink` tells internal links from external ones (`load(href)`). Every link here is internal; `visit` is the address bar.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 16 story tests (13 in `story.test.ts`, 3 in `page/people.story.test.ts`) and 13 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests are the case `scenes`.

## Laws

- `route`:
  - `ParsePrintRoundTrips`: Parse reads back every route Print writes, over generated routes.
  - `PrintParseRoundTrips`: a parsed path printed again parses the same, over generated paths.
  - `UnknownPathIsNotFound`: a path whose first segment no route has is NotFound and keeps the path.
  - `EscapeRoundTrips`.
- `app`:
  - `NavigationSetsTheRoute`, `ShortcutsNavigate`, `TypingOnlyEdits` and `SearchGoesToItsAddress`.
- `script`, over generated walks of typed addresses, navigation links and Backs on the test host:
  - `BackRestoresRoute`: the history is a stack of the routes shown, and a Back shows again the route shown there before.
- `people`: `SearchMatchesNameOrRole` and `HistoryKeepsTheNewestDistinct`.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: it opens at a search address. It then covers:
  - the four shortcuts;
  - a person, a missing person and an id that is not a number;
  - a directory, a file, a deep file and a missing path;
  - an unknown address;
  - a `+` in a query;
  - typing, which does not search, and Search, which does;
  - a search that answers while another screen shows;
  - Back to the searches before.
- `scenes`: upstream's 13 scene tests, on one screen in order.
- `badscript`: an unknown key. The program says which line and exits 1.
