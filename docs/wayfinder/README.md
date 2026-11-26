# Wayfinder

The Wayfinder map lives on GitHub, and only there: issue [#1](https://github.com/cevr/ergo/issues/1) (label `wayfinder:map`) and its native sub-issues in `cevr/ergo`. The repository keeps a derived, read-only [snapshot](snapshot.json) for offline reading and the offline gate. Nothing in the repository is a second source of map state. The local Markdown tracker and migration records that preceded this are described in the [archive](ARCHIVE.md).

## How the map is represented

- **Map.** The single open issue labelled `wayfinder:map`. Its body holds the destination, standing owner direction and the index of decisions.
- **Tickets.** Every transitive native sub-issue of the map. Each carries exactly one type label: `wayfinder:task` (agent-implementable), or a human-in-the-loop (HITL) label `wayfinder:grilling`, `wayfinder:prototype` or `wayfinder:research`. Secondary labels such as `implementation:spec` or `implementation:task` describe a task's kind.
- **Tracks and tiers.** Optional secondary labels place a ticket in the Go-only roadmap. `track:compiler` marks the compiler track ([#192](https://github.com/cevr/ergo/issues/192)); `track:stdlib` marks the standard-library track ([#206](https://github.com/cevr/ergo/issues/206)). A standard-library ticket also carries every tier its work spans, matching the tier column of #206: `tier:kernel` (a runtime or checker rule the module needs), `tier:library` (ordinary library code over the kernel) and `tier:host` (a Go host package). `check` counts only the `wayfinder:*` type labels, so these labels never change its result. A new secondary label is created with `gh label create` before a ticket uses it.
- **Parent and child.** Native sub-issues. A parent with open children is a rollup; it is done when its children are.
- **Dependencies.** Native "blocked by" relationships. A ticket is blocked while any blocker is open.
- **Claims.** A GitHub assignee.
- **Progress, decisions and resolutions.** Issue comments. The body states the question or contract; it is edited only when the contract itself changes.
- **HITL tickets** are resolved by owner feedback only. Agents never close them, even when the related implementation is complete.

## Reading the map

`cmd/wayfinder` reads GitHub through `gh api graphql` (Go standard library, read-only):

```sh
go run ./cmd/wayfinder frontier          # open, unassigned, unblocked, non-rollup tickets, HITL excluded
go run ./cmd/wayfinder frontier --all    # also HITL tickets that are otherwise ready
go run ./cmd/wayfinder list              # every ticket with its derived status
go run ./cmd/wayfinder show 41           # one ticket: relationships, body and comments
go run ./cmd/wayfinder check             # structural check of the live map
go run ./cmd/wayfinder snapshot          # regenerate docs/wayfinder/snapshot.json
go run ./cmd/wayfinder snapshot --check  # exit 1 if the snapshot differs from GitHub
```

Run it from the repository; the default snapshot path is resolved from the nearest `go.mod`. Add `--snapshot` to `frontier`, `list`, `show` or `check` to read the committed snapshot without network access, and `--json` to the read commands for machine output. Each ticket has one derived status: `closed`, `claimed`, `blocked`, `rollup`, `hitl` or `ready`; the frontier is `ready`.

`check` reports errors for a blocked-by or parent cycle, an open ticket under a closed parent, a ticket without exactly one type label, a parent or blocker outside the map, a map body reference to an issue outside the map, and anything other than one open map. Blockers are checked on every issue, the map included. It warns about a closed ticket still blocked by an open one (a stale edge) and, live, about open Wayfinder-labelled issues outside the map. `snapshot` refuses to write a map with errors.

The map holds one repository. Every relationship endpoint (parent, blocker and sub-issue) is read with its repository, and an endpoint in another repository ("blocked by other/repo#6") is a `check` error, never a bare number that would alias this repository's #6. An open foreign blocker keeps its ticket `blocked`, an open foreign sub-issue keeps its parent a `rollup`, and a foreign parent does not make an issue a map member. Each member's sub-issue connection must be complete and name the same local children as their parent links, or the read fails. The snapshot records GitHub's spelling of the repository name, whatever spelling `--repo` used. Because `snapshot` refuses a map with errors, the snapshot's integer `parent` and `blockedBy` always name issues in its `repository`. Cross-repository edges would need a snapshot schema that records repository identity; that is out of scope until the map needs one.

The snapshot records, per ticket, number, title, state and reason, labels, assignees, parent, blocked-by, URL, `updatedAt` and a SHA-256 of the body; it records the issue numbers the map body references. It is deterministic: the same GitHub state always produces the same bytes. The gate runs `go run ./cmd/wayfinder check --snapshot`, and `go test ./...` runs the command's fixture tests against a fake `gh`; it never contacts GitHub, so a stale snapshot does not fail the gate. GitHub wins over the snapshot.

## Workflow

1. The orchestrator picks work from `frontier` and assigns the ticket on GitHub.
2. Implementation lanes do not edit GitHub or the snapshot. A lane's final report carries a "Map update" section: acceptance clauses satisfied with test evidence, what remains, and suggested new or split tickets.
3. The orchestrator applies the update on GitHub: a progress or resolution comment, body edits when the contract changes, new tickets as sub-issues with blocked-by edges, and closure of satisfied non-HITL tickets. A ticket merged into another gets a "Merged into #N" comment, the target gets a "Merged from #M" comment, and the merged ticket is closed as "not planned".
4. The orchestrator runs `go run ./cmd/wayfinder check`, then `snapshot`, and commits `docs/wayfinder/snapshot.json` with the work it describes.

## Research and spike acceptance

New spike or comparison records carry a compact evidence packet: immutable primary-source URLs or repository pins and inspected paths; the mechanism observed; the Ergo choice and ordinary Go idiom; an Effect comparison (prior art for the guarantee) when relevant; the named North Star fit and tradeoff; the pillars served (P1–P7); rejected alternatives; counterevidence or limits that could overturn the choice; and finite execution with two unrelated callers plus causal negative controls before a support claim. A spike is design/source evidence until the required execution, receipts and independent review exist. Historical notes and current executable evidence remain separate.

For a zero-cost abstraction record, the packet also names a semantically equivalent, optimized hand-written Go baseline with the same validation, ownership, cancellation and completed cleanup; Effect semantics are the reference, not a control. It states whether the abstraction is erased, directly lowered or retained, and measures residual allocation, dispatch, checks, retained modules, executable bytes and runtime cost under matched source and toolchain conditions. Do not trade away a guardrail or turn one benchmark into a universal claim; these measurements join the benchmark-last ledger.

The native Go baseline is optimized idiomatic Go with the same contract; a native result below that baseline remains unresolved. The packet records every material measured lowering or specialization candidate, including static `match` dispatch and a generated effect runtime when applicable. Machine-written generated code is allowed as an implementation detail but must preserve typed errors, service rows, ownership, cancellation, scopes and completed cleanup. Possible speedups remain ambitions to measure, not current claims or universal multipliers. The packet records cold and warm evidence and rejected or losing alternatives.

A spike or construct decision that changes how source reads also adds a dated [language evolution preview](../previews/README.md), including when the result is a rejection. Previews are the reviewable record of how Ergo source looks over time, which the owner asked for on 2026-10-08 and named in the 2026-10-09 goal.

The language specification is [`docs/spec/`](../spec/README.md), v0 from [#193](https://github.com/cevr/ergo/issues/193), with its status per construct and its Gaps list. The dated [2026-10-27 preview](../previews/2026-10-27-go-sketch.md) shows the language at that version. The historical sketch-9 prototype implemented the core that the spec marks "implemented in prototype".

Durable records include [portable i64 arithmetic](../research/numeric-arithmetic.md), [user-defined JSX pragmas and target-qualified host views](../research/jsx-pragmas-and-runtime-selection.md) (historical: the JavaScript target was removed on 2026-10-26), [the lawful runtime contract and evidence categories](../research/lawful-runtime-contract.md), and [generalized `run` binding](../research/generalized-run-binding.md) (its GET-backed hosted reconciliation is [archived](ARCHIVE.md)).
