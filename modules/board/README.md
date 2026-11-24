# board

Design models and library packages for the message board.

## Design models (`models/`)

Each model package declares a `Design` state machine whose `always` laws are
exhaustively proved over its finite scope (with `sometimes` witnesses so no
invariant holds vacuously), alongside mutant machines whose `Breaks` laws must
hold to confirm each invariant catches its breach.

| Package | Behaviour |
| --- | --- |
| `models/tasks` | Task lifecycle, claim races, lease expiry, token fencing, and prerequisites |
| `models/eventlog` | Event-log cursors, scan-before-subscribe wait, and timeout semantics |
| `models/syncflow` | Two-way thread sync delivery, reconciliation, deduplication, and echo prevention |
| `models/ask` | Questions, racing answers, mention notifications, withdrawal, expiry, and wait timeouts |
| `models/grants` | Grant changes interleaved with in-flight writes and active waits |
| `models/signin` | OAuth authorization codes, PKCE, refresh-token rotation with reuse detection, revocation, and offboarding |
| `models/retry` | Idempotent posts across lost answers and payload conflicts, and atomic conditional label writes |
| `models/digest` | Contiguous digest coverage across periods, crashes between read and write, idempotent reruns, and visibility filtering |
| `models/schedule` | Scheduled firings across crashes, idempotency keys, cancellation races, and lost maker access |
