# address-routes

Screens chosen by the address. There is a home screen, a list of people with a search, a person by number, a file tree walked by the rest of the path, a deeply nested route and a not-found screen. Four shortcuts navigate, and Back returns to the searches before. This is stage b of FK8: the model and its view, on std ui's test host.

The program runs as an actor on a manual clock, with its screen on std ui's test host, whose history is the browser's. It plays a script of the user's acts on the screen. After each act it shows what changed: the address and its depth, and the page.

Tag: `default`.

```sh
address-routes < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `start`, `visit`, `link`, `click`, `type`, `key`, `back`, `wait` and `stats`.

## The shape

- Package `route` reads and writes addresses.
  - `Route = Home | Nested | People{Search} | Person{ID} | FilesIndex | Files{Path} | NotFound{Path}`.
  - `Parse` reads a path and its query. It drops a trailing slash and skips empty segments. A person's id that is not a number, and any first segment no route has, are `NotFound`, which keeps the path.
  - `Print` writes a route's one path, escaping each segment and the search (`Escape`, `Unescape`), so the two round-trip.
  - A path is parsed by one `match` on its first segment.
- Package `app` is the program as the machine `Router`. Its input is the address it opens at.
  - **The address drives the screen.** `Navigated{Path}` sets the route. A People route also sets the search field, records the search and searches again. That is an `Enter`, so the search's timer starts anew. Any other route is a `Stay`, so a search under way still answers.
  - **Navigation goes through the host.** A link (`Followed{Path}`), a shortcut and Search `Emit` a `Go{Path}`. The host follows it as a browser follows a link: `ui.Navigate` pushes the path on its history (std ui's `Location`, H7), and the address change reaches the machine as `Navigated` through the screen's `ui.OnURL`.
  - **The search answers on a timer.** A Loading page's timer gives `Fetched` after 300 ms.
- Package `people` is the list, `Search` and the history of searches. Package `files` is the tree, `Find` and `Size`.
- Package `screen` is the view:
  - the navigation's four links on every route;
  - each route's page with its headings and texts;
  - the People page's search field, Search, recent searches, status and the people as links;
  - the files' breadcrumb, entries, details and missing panel.
  A link is a `ui.Semantic` with the `Link` role whose press sends `Followed`.
- Package `script` is the host. It runs the program on `ui.NewTestHost` and follows each `Go`. The shortcuts are the host's, not the view's.
- Stage a's own `Location` contract and its `memory.History` are gone: std ui's `Location` and `MemoryHistory` carry the same laws (`PushThenBackRestores`, `BackStopsAtTheStart`).

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
- `scenes`: the 13 scene tests, on one screen in order.
- `badscript`: an unknown key. The program says which line and exits 1.
