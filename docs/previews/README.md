# Language evolution previews

A preview is a dated, reviewable snapshot of how an Ergo construct or syntax looks at one point in the language's history. Read in date order, previews show how the language evolves. The owner asked for them on 2026-10-08 ("also use sideshow to proviide language construct and syrnax sketfhes as we gonso we can see how things evolve") and listed them in the 2026-10-09 map goal ("and language evolution previews").

This directory is the record. A rendered copy, such as a Sideshow post, is a view of a preview file and may be published from it, but the Markdown file here is what reviewers read and what later previews compare against.

## Rules

- One file per preview, named `YYYY-MM-DD-<slug>.md`, added to the index below.
- `.ef` text is authoritative. Every snippet carries one label:
  - **Checked**: the snippet checks with `ergo check` at a recorded commit, and the preview names the command and the commit.
  - **Checked (prototype)**: the snippet is in the Go-shaped language of [`docs/spec/`](../spec/README.md), which `ergo check` does not implement yet, and it checks with the merged prototype compiler (`go run ./cmd/sketchc check <file>` in `prototypes/sketch9`) at a recorded commit. The preview names the command and the commit. The label becomes **Checked** when `ergo check` implements the language.
  - **Tested candidate**: the snippet depends on an unmerged branch under review; name the branch and its head commit.
  - **Proposed**: not admitted. Say so, and name the spec, research record or map item that would admit it.
- Each preview states what it does not claim, such as a benchmark result, framework port or construct that has not been admitted.
- A preview is a snapshot. When the language moves, add a new preview that names the one it supersedes, and leave the old one unchanged.
- A syntax spike or construct decision that changes how source reads adds a preview when it lands, including when the result is a rejection.
- Keep private hosts, local paths and session transcripts out of this directory.

## Index

| Date | Preview | Labels |
| --- | --- | --- |
| 2026-10-08 | [Explicit failure and service contracts](2026-10-08-effect-contracts.md) | Checked |
| 2026-10-12 | [Laws, satisfactions and evidence](2026-10-12-laws.md) | Tested candidate, Proposed |
| 2026-10-27 | [The Go-shaped language ("Effect for Go")](2026-10-27-go-sketch.md) | Checked (prototype), Proposed |
| 2026-10-27 | [Actors as an ordinary library](2026-10-27-actor-library.md) | Proposed |
| 2026-10-27 | [Why did this request time out? An agent reads a trace](2026-10-27-traceability.md) | Checked (prototype), Proposed |
| 2026-10-27 | [`needs` and `provides`](2026-10-27-needs-provides.md) | Tested candidate, Proposed |
| 2026-10-27 | [The language is Ergo](2026-10-27-ergo.md) | Tested candidate |
| 2026-10-28 | [Ergo with and without Go's syntax](2026-10-28-without-go-syntax.md) | Checked, Proposed |
| 2026-11-02 | [`wire` is spelled `context`](2026-11-02-wire-to-context.md) | Tested candidate |

## Earlier sketches

The 2026-10-08 sketches (language evolution, generalized `run` and progress) were rendered as HTML outside the repository. They carry a private host address and status claims tied to that day, so they are not copied here. The first index entry records the Ergo snippet from the language-evolution sketch again, checked against the current compiler.
