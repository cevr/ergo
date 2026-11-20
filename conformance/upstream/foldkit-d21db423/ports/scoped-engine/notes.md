# scoped-engine and its original

What `examples/scoped-engine` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `managed-resource-layer` example (`conformance/upstream/foldkit-d21db423/examples/managed-resource-layer`, MIT), stage b of FK6: the model and its view, on std ui's test host.
What upstream needs and the port does not:

- **No lifecycle echoes.** Upstream has 8 Messages; 3 of them (`StartedEngine`, `StoppedEngine`, `FailedStartEngine`) come from the runtime's managed-resource entry, and `SkippedCompute` answers a compute that ran without the engine. Here `Started` and `Refused` are the boot's answers, sent by the work that owns the boot. `StoppedEngine` has no counterpart: the end of the entry is the release.
- **No `ResourceNotAvailable`.** A compute exists only as a step of `Ready`, which has the engine. In any other state `Compute` is `Ignore` in the step, so "compute without an engine" cannot be written.
- **The compute is a step.** Squaring is pure, so `Ready`'s step computes the result itself. The plan expected L1 and L2 here ("the step cannot emit and stay"). They do not bite, because there is no command to emit. A compute that had to run on the engine's task would hit them.

Deliberate difference from upstream: the count of computes and the last result belong to `Ready`, so a new engine starts at 0 with no result. Upstream keeps both across stops. A result belongs to the engine that computed it.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files.
