# runtime-faults and its original

What `examples/runtime-faults` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `crash-view` and `slow-warnings` examples (`conformance/upstream/foldkit-d21db423/examples/`, MIT).
Differences from upstream:

- **The view work is counted in nodes.** Upstream burns CPU for some milliseconds; here the view work writes more nodes than the view's bound allows, and that bound is set above the 4,026 nodes the patch's 4,000 rows write, so each workload is over one bound only.
- **The update work needs a frame.** A step cannot report its own work, so the update work is a burst of signals in one frame.
- **The crash view shows where the defect was raised.** A defect's reason is the runtime's text, which starts with the position of the `Unreachable` (`unreachable at crash.ergo:19:2: This is a simulated crash!`). Upstream shows the thrown error's message only.
- **No browser report.** Upstream's report logs to the console. Here it adds a line to a log that the script prints.

## Upstream tests

Each upstream story and scene test, and what restates it, is a line of `origin/tests.txt`, which the examples runner checks against the vendored files. No story or scene is an Ergo test. The laws and the cases kill every mutant that such a test would kill.

## Comparisons moved from the README

- Upstream's workloads burn CPU for some milliseconds, and the runtime times each phase.
