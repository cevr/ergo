# Ergo

Claude orchestrates. Opus native subagents implement, one writer per isolated lane, running in parallel as far as the work DAG allows. After each implementation, one verifier from the other model family verifies the round in a clean checkout, through the codex mod: `mcp__codex__start` with model `sol`, the verifier role (the bridge still accepts the old name `reviewer`), and "verify directly" in the prompt; follow up with `mcp__codex__send`. It proves that the change does what the spec says and that it follows the written rules, and files nothing else (orchestrate, 2026-11-03). Continue past two rounds while findings remain. Older handoffs naming `okra counsel`, other models, Herdr panes or Codex panes are superseded. Every verify includes a prior-art section: how Gleam, ReScript, Elixir and the other compile-to-host languages in PRIOR_ART.md handle the same problem, and which optimizations and lowering paths they use (owner, 2026-10-07: "remember to look at lang prior arts as well as part of the review, gleam, rescript, elixir - they all compile downto another language, what optimizations and paths have they used?").

Work runs in waves (owner, 2026-11-03: "batching the implementation work and then batching the verification work"). A lane builds up to four units back to back on stacked branches. Each unit still has its red acceptance check, its mutants and the gate. At the end of the wave, one verifier per unit starts at the same time. Nothing reaches main unverified. A wave ends with non-trivial programs in `examples/`, one per folder, that use its features: ports of the owner's CLIs, a TUI, a GUI and a small game (owner, 2026-11-03: "as kind of a verification round as well"; "in an example within the repo"). A gap a program finds becomes a unit.

Read README.md, docs/design.md, and docs/wayfinder/README.md before changing the language.

For language architecture, runtime/interop contracts, or terminology changes, read NORTH_STAR.md, PRIOR_ART.md, and GLOSSARY.md first. Direction and tiebreaks live in NORTH_STAR.md; source comparisons and open research live in PRIOR_ART.md; domain definitions live only in GLOSSARY.md. Loop setup and drafted owner questions are recorded in plans/architecture-loop-2026-10-05.md.

- `.ergo` source is authoritative. CLI and MCP share the compiler semantic model.
- Public effect contracts are explicit. Do not silently admit missing services or undeclared errors.
- Keep unsupported syntax a diagnostic, never an unchecked passthrough to the host.
- Build a small Go compiler. Go is the only target (owner, 2026-10-26); any other target is a diagnostic. Effect is prior art for the guarantees, not a runtime. Preserve the shared conformance corpus.
- Use Rift for isolated repository changes. Implementers never push; the orchestrator integrates accepted, reviewed, gated heads into local `main` and may fast-forward `origin/main` to them (owner, 2026-10-07). Other publishing requires an explicit owner request.
- On the exe workbox, transferred `.rift` markers may be unregistered and the transferred source is on ext4. If `workrift` cannot resolve it, use an isolated Git worktree as the loop skill permits. Verify registry membership before workspace cleanup.
- Run `./scripts/gate.sh` before handoff. Performance claims require measured evidence.
- Wayfinder execution is explicitly authorized for this effort; do not close HITL tickets without user feedback.
