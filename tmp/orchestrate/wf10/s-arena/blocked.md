blocked

S-arena r2 has F1 and F4 fixed at ffd288eef1bede42566c7bcba1b678c37b3e3c45. The focused gate, correctness acceptance and selected mutants pass. F2 (generic value copies) and F3 (opaque callback ABI/inlining) remain unresolved; required speed acceptance is not green.

Full investigation, options and recommendation: `/home/exedev/.cache/baton/wf10/s-arena/blocked.md`. Receipts and file changes: `round-2-request.md` beside this file.

Recommendation: define a compiler optimization unit for result-alias summaries and known-callback variants, with selectable reference lowerings and seeded properties; then merge forward and measure the required inlining/allocation/BCE evidence and quiet-window ratios. Alternatively, authorize that wider compiler work inside this unit. Do not remove value or interruption guarantees or add an arena-name exception.
