# transfers and its original

What `examples/transfers` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `interrupting-commands` example (`conformance/upstream/foldkit-d21db423/examples/interrupting-commands`, MIT), stage b of FK4: the model and its view, on std ui's test host.
## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. The four scene tests are the case `scenes`.
