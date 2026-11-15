# fk-weather

A weather lookup: type a zip code, submit it, and see the current weather there, or why there is none. It is a port of Foldkit's `weather` example (`conformance/upstream/foldkit-d21db423/examples/weather`, MIT), stage a of FK3: the model, with no view yet. The program plays a script of form events against the lookup, run as an actor, and shows what the lookup shows after each event.

Tag: `default`.

## How it works

- Package `weather` is the domain. The state `Lookup` is a record: the `Draft` typed so far and the `Panel`, which is `Blank | Loading{Zip} | Ready{Report} | Failed{Why}`. The signal is `Edited{Text} | Submitted | Got{Report} | NotFound | Unavailable`. The machine `Weather` has one piece of work: the `Loading` state's entry fetches its zip from the `Forecast` (state-owned work). Its answer comes back as a signal: `Got`, or the fetch's typed failure, which is itself a signal, so the work needs no map of its failures.
- An edit is a `Stay`: it changes the draft and keeps the entry, so a fetch under way goes on. A submit while a fetch is under way is ignored. A submit of an empty draft fails at once with `NoZip`, and no fetch starts. Any other submit enters `Loading` with the trimmed draft. An answer outside `Loading` is ignored; the runner also drops an answer of an ended entry, so an answer only reaches the ask that started it.
- `weather` declares the contract it needs, `Forecast { Fetch(zip) Report fails NotFound | Unavailable }`, with two laws.
- Package `memory` is `Stations`, a Forecast of four fixed reports; the zip 99999 is a station that is down.
- Package `web` is `OpenMeteo`, a Forecast over two `http.Net`s: the open-meteo geocoding service finds the place of the zip, and the forecast service gives the weather there, as upstream's `fetchWeatherEffect` does. Its tests give it `http.InMemory` Nets that answer with upstream's fixtures.
- Package `script` reads lines `type TEXT` and `submit` and plays them against the actor. After each event it prints `> EVENT` and then `[DRAFT] PANEL`; when the event started a fetch, it also prints the panel once the fetch has answered.
- `cmd/fk-weather` chooses the Forecast: `memory.Stations` (the `Stations` context), or, with `-live`, `web.Live()` over the host network. Only the first runs in the cases.

Upstream keeps the panel as `AsyncData` (`Idle | Loading | Success | Failure`) beside the input text, and its `FetchWeather` command carries the zip. Here the same two parts are a record, and `Loading`'s entry owns the fetch, so no command value and no "is this answer still wanted" check exists. Upstream sends an empty zip to the command, which fails with "Zip code required"; here the step fails it with no fetch.

## Upstream tests

Each title of upstream's `src/story.test.ts` and `src/scene.test.ts`, and what restates it. (FK0 moves this list to `origin/tests.txt`, which the examples runner checks.)

| Upstream title | Restated by |
| --- | --- |
| submitting the weather form fetches weather and shows result | covered by law `ReadyShowsLatestAsk` (a submit enters Loading, and a resolved Got shows the report) and the `ask` case |
| failed fetch shows failure state | covered by law `ReadyShowsLatestAsk` (a resolved NotFound or Unavailable shows Failed with that reason) and the `ask` case |
| fetchWeather returns SucceededFetchWeather with data on success | `web` test `FetchGivesTheReport` |
| fetchWeather returns FailedFetchWeather on HTTP failure | `web` test `HTTPFailureFails` |
| fetchWeather returns FailedFetchWeather when no results found | `web` test `NoPlaceIsNotFound` |
| initial view shows empty form with Get Weather button | not restated: stage a has no view (FK3b, after H4 and H4r) |
| typing a zip code updates the input value | not restated: stage a has no view (FK3b); the law `EditOnlyChangesTheDraft` holds the model half |
| submitting the form shows loading state | not restated: stage a has no view (FK3b) |
| successful fetch renders weather card | not restated: stage a has no view (FK3b) |
| failed fetch renders error message | not restated: stage a has no view (FK3b) |
| full flow: type zip code, click get weather, see results | not restated: stage a has no view (FK3b); the `ask` case plays the same flow on the model |

## Laws

- `Forecast.ReportIsForTheZip`, `Forecast.NotFoundNamesTheZip` (contract): a report is the weather at the zip asked, and a zip with no place is refused with that zip. They run against `memory.Stations`, and the `web` property `ContractHoldsOverTheFixture` runs them against `OpenMeteo` over the fixture Nets (the law runner cannot build an `http.Net`; `expected-skips` lists the two skips).
- `EditOnlyChangesTheDraft`: an edit in any state changes the draft and nothing else, and keeps the entry.
- `ReadyShowsLatestAsk`: over generated edits, submits and answers, the lookup shows what a model with no machine says.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `ask`: upstream's full flow, a second ask after typing, an empty draft, a zip with no place, and a station that is down.
- `trim`: a draft with a space before the zip asks the trimmed zip; a negative temperature.
- `badscript`: an unknown line; the program says which line and exits 1.
