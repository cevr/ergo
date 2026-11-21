# gallery

A gallery site whose navigations carry data work: entering the gallery loads its catalog, opening a painting loads it, moving to another painting loads only the new one, and leaving the studio saves its draft. The program plays a script of timed acts on the site's screen and shows what changed after each one, with what the site asked for. The host answers a catalog load after 600 ms, a painting after 400 ms and a save after 300 ms.

Tag: `default`.

```sh
gallery < cases/tour/stdin
```

A script line is `@MS WORD ...`, and the words are these:

- `open URL` types an address: a cold load, a reload, or a path typed.
- `link LABEL` clicks a link of the screen.
- `away HREF` follows a link elsewhere (the screen has none).
- `type TEXT` types in the studio's field.
- `back` is the browser's Back.
- `end` ends the script.

With `-stats`, the program also prints the work of each step.

## The shape

- Package `gallery` is the domain.
  - `Route = Home | Gallery | Painting{ID} | Studio | NotFound{Path}`, with `Parse` (a URL or a path) and `Print`.
  - The paintings.
  - The machine `Gallerist` over `Site`. `Site` holds the route (`None` before the first URL), the log of the last 20 navigations, the catalog (`Idle | Loading | Ready`), the painting (`NoPainting | LoadingPainting{ID} | ReadyPainting{ID}`), the draft, the draft last saved as the studio's work read it, and the jobs asked for.
  - The loads (`LoadCatalog`, `LoadPainting{ID}`) are jobs in the state. The machine's work runs them: it dates each job on the clock (600 and 400 ms, sleeps to each in time order and answers it. A navigation opens a new entry, and the work starts again with the jobs left, each keeping its due time.
  - The step's output is what the page asks of the browser: `Push{URL}` or `Leave{Href}`. The host answers a push with `Arrived`.
  - **Leaving the studio saves the draft in the studio's exit action** (`exit: leave`). The runner runs it when the studio's entry ends, with the state the entry ends with, so the draft is the one last typed (a key is a `Stay`), once per visit: on the navigation away, or at the site's end. A stay in the studio and a click are no end of the entry (a click is a `Tell`). The contract `Drafts` (`Save`, `Last`, law `LastIsTheLatestSave`) is the domain's; the studio's work reads `Last` back as `Recalled`.
- Package `memory` holds `Drafts` in process; `cmd/gallery/main.ergo` binds it, the only place that chooses it.
- **What a navigation means is one `match` on the route before and the route after.** `(Home, Gallery)` is an entry, `(Gallery, Gallery)` a stay, `(Painting, Painting)` a stay that compares ids, and `(Studio, Home)` an exit. The match lists all 25 pairs, so a new route is a compile error until each of its pairs says what it means. A cold load comes from no page; as a path the site does not have, it enters every page.
- Package `screen` is the view:
  - the navigation;
  - each route's page: the catalog's paintings as links once it hangs, a painting with its Previous and Next, the studio's field and its last saved draft, the not-found pages;
  - the transition log with the badges (`Cold load`, `Entered X`, `Exited X`, `Stayed on Painting: A → B`, `Stayed within route`), read from the route before and the route after, as the step reads them.
  A link is a `ui.Semantic` with the `Link` role whose press sends `Clicked`; `ui.OnURL` gives `Arrived`.
- Package `script` is the host. It runs `Gallerist` as an actor on `ui.NewTestHost` over a manual clock.
  - It follows each push the site's sink queued at once with `ui.Navigate` (a push, then the address change), and shows the jobs the site asked for.
  - Between the lines it moves the clock to each job due, in time order, so the work answers it, and shows what the answer changed.

## Laws

- `CatalogueLoadsOncePerVisit`: drawn navigations and catalog answers, step by step against a reference of a visit and a flight. The catalog is asked for exactly when the site enters the gallery, from another page or cold, with no load in flight.
- `StayRefetchesOnlyOnIdChange`: on a painting, whether it is loaded or loading, a navigation to a painting loads it exactly when the id is another.
- `LeavingStudioAlwaysSaves`: the site runs as an actor on a manual clock over drawn keys, arrivals, link clicks and clock moves, then stops; the drafts its exit actions saved are, in order, the draft as last typed at each leave of the studio with a draft, then at the end if it ends in the studio with one. No other step saves. A reference model reads only the route and the draft.
- `Drafts.LastIsTheLatestSave` (in the contract): after any saves, `Last` is the last draft saved.
- `LogKeepsTheLastTwenty`: the log keeps the last 20 navigations, newest first.
- `RoutesRoundTrip`: `Parse(Print(r))` is `r` for every route of the site.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `tour`: a cold load home; the gallery; leaving and re-entering it while the catalog loads (one load); a painting and then another typed, where the first one's late answer is ignored; the same painting again (no load); its Next link; a draft written in the studio and saved by its exit action on the move to a painting the gallery does not have; Back to the studio, and an empty draft left unsaved; a path the site does not have; a link elsewhere; and the log.
- `scenes` (new): the screen through its own links: a cold load into the gallery, a painting's Next and Previous, a fresh gallery entry that loads again, a painting the gallery lacks, a missing path and its link home, and Back.
- `badscript`: an unknown word; the program says which line and exits 1.
