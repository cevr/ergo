# stopwatch and its original

What `examples/stopwatch` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `stopwatch` example (`conformance/upstream/foldkit-d21db423/examples/stopwatch`, MIT), stage b of FK1: the model and its screen, on std ui's test host.
## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.

## Comparisons moved from the README

- Upstream keeps `{elapsedMs, isRunning, startTime}` and reads the clock in two commands (`DetermineStartTime`, `DetermineTickTime`) and a tick subscription keyed on `isRunning`.
- Upstream's Start and tick scenes also check the commands that read the clock.
