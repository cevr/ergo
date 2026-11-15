# fk-weather

A weather lookup: type a zip code, submit it, and see the current weather there, or why there is none. It is a port of Foldkit's `weather` example (`conformance/upstream/foldkit-d21db423/examples/weather`, MIT), stage b of FK3: the model and its view, on std ui's test host. The program plays a script of form events on the screen and shows the screen after each event.

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
- Package `web` is `OpenMeteo`, a Forecast over two `http.Net`s, as upstream's `fetchWeatherEffect` does: the open-meteo geocoding service finds the place of the zip, and the forecast service gives the weather there. Its tests give it `http.InMemory` Nets that answer with upstream's fixtures.
- Package `screen` is the view, as upstream's:
  - the zip field (placeholder `Enter a zip code`);
  - the button, which reads `Get Weather`, or `Loading...` in a disabled fieldset while a fetch is under way;
  - the panel. `Panel` reads the lookup's report with `try ui.Await` in a `ui.Boundary`, whose catch shows `Fetching weather...` for `ui.Pending` and the reason for the failure. The report is a region (upstream's article) with the zip as its heading.
  - `PanelMatched` is the same panel as an exhaustive `match`, the other form. The law `BoundaryIsTheMatch` holds the two to one screen.
- Package `script` plays lines on `ui.NewTestHost`:
  - `type TEXT` fills the zip field;
  - `submit` clicks `Get Weather`;
  - `click LABEL` clicks a button;
  - `wait MS` moves the clock, so `Slow`'s fetch answers (upstream's `Command.resolve`);
  - `stats` prints the work of the act before it.

  After each line it prints the screen: `<DRAFT>`, `[button]`, `Weather report{# ZIP …}` or the panel's text.
- `cmd/fk-weather` chooses the Forecast:
  - `memory.Slow`, which waits 500 ms on the manual clock that is also the test host's clock and the actor's Scheduler;
  - or, with `-live`, `web.Live()` over the host network.

  Only the first runs in the cases.

Upstream keeps the panel as `AsyncData` (`Idle | Loading | Success | Failure`) beside the input text, and its `FetchWeather` command carries the zip. Here the same two parts are a record, and `Loading`'s entry owns the fetch, so no command value and no "is this answer still wanted" check exists. Upstream sends an empty zip to the command, which fails with "Zip code required"; here the step fails it with no fetch. Upstream's view matches `AsyncData` with `matchDataSplitEmpty`; here the panel is a boundary over `ui.Await`, and the match is the reference it equals.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. The six scene tests are the case `scenes`. Upstream's `label('Zip code')` is the field's placeholder here, because a `ui.Input` has no label of its own (gap FK3-g5).

## Laws

- `Forecast.ReportIsForTheZip`, `Forecast.NotFoundNamesTheZip` (contract): a report is the weather at the zip asked, and a zip with no place is refused with that zip.
  - They run against `memory.Stations`.
  - The `web` property `ContractHoldsOverTheFixture` runs them against `OpenMeteo` over the fixture Nets, because the law runner cannot build an `http.Net`.
  - `memory.Slow` holds a function, which the runner cannot build either.
  - `expected-skips` lists the four skips.
- `EditOnlyChangesTheDraft`: an edit in any state changes the draft and nothing else, and keeps the entry.
- `ReadyShowsLatestAsk`: over generated edits, submits and answers, the lookup shows what a model with no machine says.
- `BoundaryIsTheMatch` (package `screen`): for any drawn panel, the boundary's panel and the exhaustive match's write the same nodes.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `ask`: upstream's full flow, a second ask after typing, a submit while loading (no `Get Weather` to click), an empty draft, a zip with no place, and a station that is down.
- `trim`: a draft with a space before the zip asks the trimmed zip; a negative temperature.
- `scenes`: upstream's six scene tests, in order on one screen.
- `badscript`: an unknown line; the program says which line and exits 1.
