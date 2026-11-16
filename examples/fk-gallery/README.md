# fk-gallery

A gallery site whose navigations carry data work: entering the gallery loads its catalog, opening a painting loads it, moving to another painting loads only the new one, and leaving the studio saves its draft. It is a port of Foldkit's `route-transitions` example (`conformance/upstream/foldkit-d21db423/examples/route-transitions`, MIT), stage a of FK9: the model, with no view yet. The program plays a script of timed visits and shows the page after each one, with what the site asked for. The host answers a catalog load after 600 ms, a painting after 400 ms and a save after 300 ms, as upstream's sleeps do.

Tag: `default`.

```sh
fk-gallery < cases/tour/stdin
```

A script line is `@MS WORD ...`, and the words are these:

- `open URL` is a cold load or a reload.
- `go PATH` follows a link of the site.
- `away HREF` follows a link elsewhere.
- `type TEXT` sets the studio's field.
- `back PATH` moves the history.
- `end` ends the script.

## The shape

- Package `gallery` is the domain.
  - `Route = Home | Gallery | Painting{ID} | Studio | NotFound{Path}`, with `Parse` (a URL or a path) and `Print`.
  - The paintings.
  - The machine `Gallerist` over `Site`. `Site` holds the route (`None` before the first URL), the log of the last 20 navigations, the catalog (`Idle | Loading | Ready`), the painting (`NoPainting | LoadingPainting{ID} | ReadyPainting{ID}`), the draft and the draft saved.
  - The step's output is a list of `Command`s: `LoadCatalog`, `LoadPainting{ID}`, `SaveDraft{Draft}`, `Push{URL}` and `Leave{Href}`. The host answers a load or a save with its signal, and a push with `Arrived`.
- **What a navigation means is one `match` on the route before and the route after.** `(Home, Gallery)` is an entry, `(Gallery, Gallery)` a stay, `(Painting, Painting)` a stay that compares ids, and `(Studio, Home)` an exit. The match lists all 25 pairs, so a new route is a compile error until each of its pairs says what it means. A cold load comes from no page; as a path the site does not have, it enters every page.
- Package `script` is the host of stage a. It reads the script, steps the site, owes each command's answer at its time, and delivers the answers in time order between the lines. Time is a count of milliseconds, so nothing waits.

What upstream needs and the port does not:

- **No transition helper.** Upstream's `Transition.make`, `entered`, `stayed`, `exited` and `coldLoad` (from `foldkit/route`) compute the same facts from a `{maybePreviousRoute, nextRoute}` record, five `Step` functions combine them, and `Update.combine` merges their commands. Here the match on the pair is the whole of it.
- **No completion echoes.** `CompletedNavigateInternal` and `CompletedLoadExternal` have no counterpart: a `Push` is answered by the `Arrived` it causes.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 10 story tests and what restates it. The examples runner checks the file against the vendored files. Upstream has no scene tests; FK9b writes new scene scripts.

A story is an Ergo test only when the test kills a mutant that no law and no case kills. That leaves one test, `AStaleAnswerForAnotherIdIsIgnored`, which kills `fk9-stale-answer-stays`. Nine stories have no test of their own, and each is restated as follows:

- Cold load into the gallery: `CatalogueLoadsOncePerVisit` draws cold entries; the tour logs `1 cold->Home`.
- Cold load home: `CatalogueLoadsOncePerVisit`; the tour's first line.
- Entering the gallery: `CatalogueLoadsOncePerVisit` and `LogKeepsTheLastTwenty`; the tour's catalog answer.
- Re-entering while loading: `CatalogueLoadsOncePerVisit` models the flight; the tour re-enters at 400.
- Entering a painting: the tour enters painting 3 from the gallery; `StayRefetchesOnlyOnIdChange` checks the id asked.
- Staying with a new id, and with the same id: `StayRefetchesOnlyOnIdChange`; the tour visits paintings 4 and 4 again.
- Leaving the studio, with a draft and with an empty draft: `LeavingStudioSavesDraft` draws both; the tour does both.

## Laws

- `CatalogueLoadsOncePerVisit`: drawn navigations and catalog answers, step by step against a reference of a visit and a flight. The catalog is asked for exactly when the site enters the gallery, from another page or cold, with no load in flight.
- `StayRefetchesOnlyOnIdChange`: on a painting, whether it is loaded or loading, a navigation to a painting loads it exactly when the id is another.
- `LeavingStudioSavesDraft`: only leaving the studio saves, and only a draft that is not empty.
- `LogKeepsTheLastTwenty`: the log keeps the last 20 navigations, newest first.
- `RoutesRoundTrip`: `Parse(Print(r))` is `r` for every route of the site.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `tour`: a cold load home; the gallery; leaving and re-entering it while the catalog loads (one load); a painting and then the next one, where the first one's late answer is ignored; the same painting again (no load); a draft written in the studio and saved on a history move to a painting the gallery does not have; an empty draft left unsaved; a path the site does not have; a link elsewhere; and the log.
- `badscript`: an unknown word; the program says which line and exits 1.
