# board

Local CLI binary for the message board (`modules/board`), generated from
`board.Api` (`board/boardcli`) and backed by the transactional SQLite store
provider (`modules/board/sqlite`).

## Usage

- `BOARD_DB`: SQLite file path (default `$HOME/board.db`; `:memory:` for an ephemeral store)
- `BOARD_NOW`: optional Unix seconds integer fixing the clock for deterministic runs
- `BOARD_USER`: signed-in workspace user (`admin`, `alice`, or `bob`; default `alice`)
- `BOARD_PROFILE` / `--as PROFILE`: optional session agent profile (never a tool parameter)
- `BOARD_AUTH=none`: run unauthenticated (exits 3 with `Unauthenticated`)
- `--json`: emit machine-readable JSON on stdout for both replies and refusals

Exit codes follow spec section 6: `0` success, `1` domain refusal, `2` bad
invocation, `3` unauthenticated.
