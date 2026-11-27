# atlas

Control-plane runtime, SQLite persistence, Streamable HTTP `/mcp` server with
SHA-256-hashed per-thread bearer auth (D11-D13), non-blocking host telemetry
(`HostCapacity`), and non-spawning periodic `WellnessReport` checks (q210)
projected from `modules/atlas/api.Api` (`ergo gen api|rpc|cli|mcp`).

    atlas [--store PATH|:memory:] mcp
    atlas [--store PATH|:memory:] serve [--http 127.0.0.1:8787]
    atlas [--store PATH|:memory:] wellness
    atlas [--store PATH] work JOB
    atlas --store PATH restart-drill JOB
    atlas [--store PATH|:memory:] [--json] [--yes] COMMAND ...

The store defaults to `$XDG_STATE_HOME/atlas/state.db` (or
`~/.local/state/atlas/state.db`). Passing `--store :memory:` uses an isolated
in-memory SQLite store for process lifetime without writing disk files.

Environment overrides for deterministic cases:
- `ATLAS_NOW`: fixed Unix millisecond timestamp
- `ATLAS_HOST_STATIC`: JSON `host.Status` (or `{"status": host.Status}`)
  snapshot backing `api.NewStaticHostCapacity`

HTTP endpoints (`serve`):
- `GET /healthz`, `GET /wellness`: non-spawning `WellnessReport` JSON
- `GET /host/capacity`: non-blocking `HostCapacityReport` JSON
- `POST /rpc/<Operation>`: generated `apirpc` dispatch
- `GET|POST|DELETE /threads/<threadID>/mcp`, `/mcp`: per-thread `/mcp`
  endpoint enforcing SHA-256 bearer auth (`hmac.Equal`), 24h turn-only idle
  expiry, close/rotate revocation, and D13 outside-audience handler refusals

Durable work (`modules/atlas/serve`):
- `work JOB`: one daemon incarnation: `BootReconcile`, the RestForOne
  supervision tree (store-owner, pool-hold-scheduler, handle and turn
  supervisors), then it starts or resumes JOB, prints `working JOB`, and
  finishes it when a `finish` line arrives on stdin; on end of input it shuts
  the tree down and leaves JOB open for the next boot.
- `restart-drill JOB`: starts `work JOB`, kills it with SIGKILL once it is
  mid-work, starts it again (the boot resumes JOB and it finishes), starts it a
  third time (nothing runs twice), and prints how many times JOB was settled.

Cases exercise `host-capacity`, `wellness-report`, `thread-turn-cli`,
`cli-not-found`, `mcp-thread-and-wellness` and `restart-kill9-resume`
deterministically.
