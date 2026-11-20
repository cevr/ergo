# weather and its original

What `examples/weather` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `weather` example (`conformance/upstream/foldkit-d21db423/examples/weather`, MIT), stage b of FK3: the model and its view, on std ui's test host.
## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. The six scene tests are the case `scenes`. Upstream's `label('Zip code')` is the field's placeholder here, because a `ui.Input` has no label of its own (gap FK3-g5).

## Comparisons moved from the README

- Upstream keeps the panel as `AsyncData` (`Idle | Loading | Success | Failure`) beside the input text, and its `FetchWeather` command carries the zip.
- Upstream sends an empty zip to the command, which fails with "Zip code required"; here the step fails it with no fetch.
- Upstream's view matches `AsyncData` with `matchDataSplitEmpty`; here the panel is a boundary over `ui.Await`, and the match is the reference it equals.
