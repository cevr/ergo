# counter-list and its original

What `examples/counter-list` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `counters` example (`conformance/upstream/foldkit-d21db423/examples/counters`, MIT), stage b of FK2: the model and its view, on std ui's test host.
## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. The five scene tests are the case `scenes`. Upstream's `nth(all.role('button', {name: '+'}), 1)` becomes the `+` within the row's group, because std ui has `First` and `Within` but no `Nth`.

## Comparisons moved from the README

- Upstream joins the child with `Update.foldChild({update, read, write, toParentMessage})` and a hand-written `GotCounterMessage` variant (`main.ts` 30 to 57).
