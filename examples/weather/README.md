# weather

A weather lookup: type a zip code, submit it, and see the current weather there, or why there is none. The program plays a script of form events on the screen and shows the screen after each event.

Tag: `default`.

## How it works

- Package `weather` is the domain:
  - The state `Lookup` is a record: the `Draft` typed so far and the `Panel`, which is `Blank | Loading{Zip} | Ready{Report} | Failed{Why}`.
  - The signal is `Edited{Text} | Submitted | Got{Report} | NotFound | Unavailable`.
  - The machine `Weather` has one piece of work: the `Loading` state's entry fetches its zip from the `Forecast` (state-owned work). Its answer comes back as a signal: `Got`, or the fetch's typed failure, which is itself a signal, so the work needs no map of its failures.
- An edit is a `Stay`: it changes the draft and keeps the entry, so a fetch under way goes on. A submit while a fetch is under way is ignored. A submit of an empty draft fails at once with `NoZip`, and no fetch starts. Any other submit enters `Loading` with the trimmed draft. An answer outside `Loading` is ignored, and the runner also drops an answer of an ended entry, so an answer only reaches the ask that started it.
- `weather` declares the contract it needs, `Forecast { Fetch(zip) Report fails NotFound | Unavailable }`, with two laws.
- Package `memory` has two Forecasts:
  - `Stations`, four fixed reports; the zip 99999 is a station that is down.
  - `Slow`, which answers as `Stations` does after a wait that `main` gives it: a sleep on the script's clock.
- Package `web` is `OpenMeteo`, a Forecast over one `http.Network` whose origin policy names the two open-meteo services: the geocoding service finds the place of the zip, and the forecast service gives the weather there. `Live` builds it with `http.NewNet`. Its tests give it an `http.InMemoryNetwork` that routes by origin and path, and answers with upstream's fixtures.
- Package `screen` is the view:
  - the zip field (placeholder `Enter a zip code`);
  - the button, which reads `Get Weather`, or `Loading...` in a disabled fieldset while a fetch is under way;
  - the panel. `Panel` reads the lookup's report with `try ui.Await` in a `ui.Boundary`, whose catch shows `Fetching weather...` for `ui.Pending` and the reason for the failure. The report is a region with the zip as its heading.
  - `PanelMatched` is the same panel as an exhaustive `match`, the other form. The law `BoundaryIsTheMatch` holds the two to one screen.
- Package `script` plays lines on `ui.NewTestHost`:
  - `type TEXT` fills the zip field;
  - `submit` clicks `Get Weather`;
  - `click LABEL` clicks a button;
  - `wait MS` moves the clock, so `Slow`'s fetch answers;
  - `stats` prints the work of the act before it.

  After each line it prints the screen: `<DRAFT>`, `[button]`, `Weather report{# ZIP …}` or the panel's text.
- `cmd/weather` chooses the Forecast:
  - `memory.Slow`, which waits 500 ms on the manual clock that is also the test host's clock and the actor's Scheduler;
  - or, with `-live`, `web.Live()` over the host network.

  Only the first runs in the cases.

The input text and the panel are a record, and `Loading`'s entry owns the fetch, so no command value and no "is this answer still wanted" check exists.

## Laws

- `Forecast.ReportIsForTheZip`, `Forecast.NotFoundNamesTheZip` (contract): a report is the weather at the zip asked, and a zip with no place is refused with that zip.
  - They run against `memory.Stations`.
  - The `web` property `ContractHoldsOverTheFixture` runs them against `OpenMeteo` over the fixture network, because the law runner cannot build an `http.Network`.
  - `memory.Slow` holds a function, which the runner cannot build either.
  - `expected-skips` lists the four skips.
- `EditOnlyChangesTheDraft`: an edit in any state changes the draft and nothing else, and keeps the entry.
- `ReadyShowsLatestAsk`: over generated edits, submits and answers, the lookup shows what a model with no machine says.
- `BoundaryIsTheMatch` (package `screen`): for any drawn panel, the boundary's panel and the exhaustive match's write the same nodes.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `ask`: the full flow, a second ask after typing, a submit while loading (no `Get Weather` to click), an empty draft, a zip with no place, and a station that is down.
- `trim`: a draft with a space before the zip asks the trimmed zip; a negative temperature.
- `scenes`: the six scene tests, in order on one screen.
- `badscript`: an unknown line; the program says which line and exits 1.
