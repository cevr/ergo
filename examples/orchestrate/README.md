# orchestrate

The ledger of an orchestration run as one binary, used by OpenCode over
MCP stdio and by hand on the command line. Its surfaces are generated from
the operation type `orchestrate.Api` in `modules/orchestrate/orchestrate`
(`ergo gen api|cli|mcp`), so the tools, the commands and the domain cannot
drift apart.

A unit is keyed by `repo`, `run` and `id`. It opens with the receipt kinds
its verdict requires. A receipt is one run of a check: the command, the
head it ran at, its exit code and its exit lines. The verdicts are a
machine: open, accepted at a head, landed at the accepted head. Accept and
land hold only when the latest receipt of every required kind at the head
exited 0; otherwise they fail with a typed refusal, `Unverified` (the kinds
missing or red at the head) or `NotAcceptedHead`.

    orchestrate mcp [--store PATH]          MCP tools on stdin/stdout
    orchestrate [--store PATH] [--json] COMMAND ...

Commands cover units (`unit-open`, `receipt-record`, `unit-state`, `unit-list`, `unit-accept`, `unit-land`, `unit-close`), run summaries and heads (`run-summary`, `run-head-set`), structured decisions (`decision-record`, `decision-list`), integration landings (`landing-record`, `landing-list`), operational events (`event-record`, `event-log`), log import (`import-plan`), commitments (`commitment-add`, `commitment-assign`, `commitment-land`, `commitment-decide`, `commitment-list`), queue items (`queue-upsert`, `queue-remove`, `queue-list`), and async questions (`question-ask`, `question-answer`, `question-list`).

The store is one SQLite file per host, written only by this binary:
`--store PATH`, else `$XDG_STATE_HOME/orchestrate/ledger.db` (by default
`~/.local/state/orchestrate/ledger.db`). Opening a v1 or v2 database migrates
non-destructively to schema v3; future schema versions are refused. `:memory:` keeps a private store
for the life of the process. `ORCHESTRATE_NOW` (a Unix second) fixes the
clock.

The case `mcp-verdict` drives a whole MCP session: it opens a unit, records
a red receipt and is refused the verdict as `Unverified`, then records a
green one at the same head and is accepted. The case `mcp-ledger` exercises
`unit-close`, commitments, queue items, and async questions over MCP stdio.
The case `mcp-progressive` exercises progressive-disclosure queries (`import-plan`,
`run-summary`, filtered/paginated `unit-list`, `decision-list`, `landing-list`,
`event-log`, and `unit-state` drill-down).
