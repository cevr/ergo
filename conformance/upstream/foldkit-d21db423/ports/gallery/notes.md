# gallery and its original

What `examples/gallery` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `route-transitions` example (`conformance/upstream/foldkit-d21db423/examples/route-transitions`, MIT), stage b of FK9: the model and its view, on std ui's test host.
What upstream needs and the port does not:

- **No transition helper.** Upstream's `Transition.make`, `entered`, `stayed`, `exited` and `coldLoad` (from `foldkit/route`) compute the same facts from a `{maybePreviousRoute, nextRoute}` record, five `Step` functions combine them, and `Update.combine` merges their commands. Here the match on the pair is the whole of it.
- **No completion echoes.** `CompletedNavigateInternal` and `CompletedLoadExternal` have no counterpart: a `Push` is answered by the `Arrived` it causes.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 10 story tests and what restates it. The examples runner checks the file against the vendored files. Upstream has no scene tests; the case `scenes` is new, and `tests.txt` names it in its header.

A story is an Ergo test only when the test kills a mutant that no law and no case kills. That leaves one test, `AStaleAnswerForAnotherIdIsIgnored`, which kills `fk9-stale-answer-stays`. Nine stories have no test of their own, and each is restated as follows:

- Cold load into the gallery: `CatalogueLoadsOncePerVisit` draws cold entries; the tour logs `#1 Cold load → Home [Cold load] [Entered Home]`, and the scenes a cold load into the gallery.
- Cold load home: `CatalogueLoadsOncePerVisit`; the tour's first line.
- Entering the gallery: `CatalogueLoadsOncePerVisit` and `LogKeepsTheLastTwenty`; the tour's catalog answer.
- Re-entering while loading: `CatalogueLoadsOncePerVisit` models the flight; the tour re-enters at 400.
- Entering a painting: the tour enters painting 3 from the gallery; `StayRefetchesOnlyOnIdChange` checks the id asked.
- Staying with a new id, and with the same id: `StayRefetchesOnlyOnIdChange`; the tour visits paintings 4 and 4 again.
- Leaving the studio, with a draft and with an empty draft: `LeavingStudioAlwaysSaves` draws both; the tour does both.

## Comparisons moved from the README

- Upstream's save sleeps 300 ms; the in-process store takes no time, so the exit action holds the site for no wait.
