# signup-form and its original

What `examples/signup-form` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `form` example (`conformance/upstream/foldkit-d21db423/examples/form`, MIT), stage b of FK5: the model and its view, on std ui's test host.
## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.

## Comparisons moved from the README

- Upstream's form is a waitlist subscription; the port keeps its fields, rules and flow, with neutral texts.
- Upstream carries the address in the answer and compares it with the field; here the old answer never arrives.
