# jobs

A job queue served over HTTP. A client submits a job, lists the jobs, reads one, cancels one and deletes one. A job that no client cancels runs by itself on the clock: it is queued, then running, then done.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a small JSON service over std `http`, with a store contract, a lifecycle machine on a clock, and each typed failure given its status in one place. Its goldens were made by running the program, and then checked line by line against the rules below.

## Rules

- `POST /jobs` with `{"name": "...", "priority": n}` queues a job and answers `201`, the job, and `Location: /jobs/ID`. Ids are 1, 2, 3, ... `priority` may be left out (it is `0` then); `name` may not, and may not be empty.
- `GET /jobs` lists every job by id (`{"jobs": [...]}`); `GET /jobs/ID` is one job.
- A job is `queued` for 1 s after it was submitted, then `running` for 2 s, then `done`, on the Clock. `ended` is `null` until the job is done or cancelled, then the time it ended.
- `POST /jobs/ID/cancel` cancels a queued or running job (`200`); a job that ended is `409`.
- `DELETE /jobs/ID` removes a job that ended (`204`); a queued or running job is `409`: cancel it first.
- Refusals are `{"error": "..."}`: `400` a body that is not JSON or could not be read, `404` no route or no job (an id that is not a whole number above 0 is no job), `405` a known path with another method, with `Allow`, `409` as above, `413` a body over 64 KiB, `422` JSON of the wrong shape or an empty name. `HEAD` gets `GET`'s headers and no body.
- With `-site DIR`, every path outside `/jobs` is a file of `DIR` (`/` is `/index.html`), served by `http.Files` through a root opened once: a path or a link that leads outside `DIR` is `403`, a missing file `404`, a method other than `GET` and `HEAD` `405`.
- With `-replay`, the program opens no socket: it reads `METHOD PATH [BODY]` lines and `+MS` lines (the manual clock moves MS milliseconds) from standard input, and prints each response: `> ` and the request, the status, the headers and the body. A `HEAD` prints no body.

## Layout

The program is a module of packages (`ergo.mod`: `module jobs`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `jobs/` | The domain package, with no HTTP and no JSON. `job.ergo`: `State`, `Signal`, `Job`, the failures `NotFound` and `Conflict`, `Step`, `Timer`, the machine `Lifecycle`, `Settle` and `Apply`. `store.ergo`: the contract `Store` with five laws, and the failure `Stale`. `queue.ergo`: `Submit`, `Find`, `List`, `CancelJob` and `Remove`, over the `Store` and the `time.Clock` in the context. `laws.ergo`: the lifecycle laws. |
| `memory/` | `Table`, a `jobs.Store` in a `sync.Ref`. `ergo test` runs the `Store` laws here. |
| `api/` | The HTTP layer: `routes.ergo` (`Endpoint`, the route table for `http.Match`), `body.ergo` (the JSON records `Draft`, `View`, `Listing`, `Problem`), `handle.ergo` (`Handle`, the one catch from failure to status, and `WithSite`), `laws.ergo` (the model law), `tests.ergo` (imports `memory`, so the law runs against `memory.Table`). |
| `cmd/jobs/` | The binary. It binds `jobs.Store` to a `memory.Table`, `time.Clock` to the host's clock (or a manual one under `-replay`), the listener to `-addr` and the site's root to `-site`. It is the only place that chooses them. |

## What it shows

- **A handler with a capability row, served by std `http`.** `api.Handle(http.Request) http.Response needs jobs.Store | time.Clock` is a plain function. `http.Serve` runs it on a child task per request, and its row reaches `Serve`'s caller, `main`, which binds the store and the clock. The tests call it as a function, with a `time.Manual` and no socket.
- **A lifecycle machine on a clock.** `Lifecycle` is `machine[State, Signal]` with `after: Timer`: a queued job starts after `QueueFor`, a running one finishes after `RunFor`. The server runs no actor per job: `Settle` fires each due timer at its own time when a job is read, so a job read late is the job read on time (law `SettleComposes`).
- **A store contract with laws, and no lost change.** `Store.Swap` and `Store.Remove` take the job the caller read and fail `Stale` when another request changed it first; `CancelJob` and `Remove` read again. `memory.Table` makes each change one `sync.Modify`.
- **Typed failures become statuses in one place.** `answer` fails with each refusal by its type; `Handle`'s one `catch` names each and gives its status, with no `case _`, so a new failure stops the build until it has a status (`TestJobsFailureNeedsAStatus` in `scripts/examples`).
- **Optional and nullable JSON fields.** `Draft.Priority` is a `json.Optional[int]`: a client may leave the key out. `View.Ended` is a `json.Nullable[string]`: the key is always there, and `null` until the job ends.
- **Exhaustive `match`.** `Step` names every pair of state and signal; `RouteOf` names every endpoint, and the route table is built from it.

## Laws

| law | where | rule | mutant it kills |
| --- | --- | --- | --- |
| `UntakenJobIsDoneOnTime` | jobs | a job no one cancels is queued, running and done at the right times, to the ms | a timer that fires 1 ms late |
| `SettleComposes` | jobs | settling twice is settling once to the later time | a timer that fires at the read time, not its own |
| `CancelBeforeTheEnd` | jobs | a cancel succeeds exactly before the end, and names the state when it fails | a done job that is not ended |
| `Store.AddIsQueued`, `AllByAscendingId`, `SwapFromCurrent`, `StaleSwapChangesNothing`, `RemoveForgets` | contract, run in `memory` | the store keeps, lists, swaps and forgets jobs, and a stale change changes nothing | a swap that compares ids, not jobs |
| `AgreesWithModel` | api, with `memory.Table` | generated request sequences, with clock moves between them, get the model's status, body, `Location` and `Allow` at each step | a done job that is not ended; `ended` never set; a missing priority that is not 0; an `Allow` of one method; a lost `Location`; a delete of a running job; 409 given as 400 |

Two mutants no law sees are caught elsewhere: a `case _` in `Handle`'s catch by `TestJobsFailureNeedsAStatus`, and an id with a leading zero (`/jobs/01`) taken as job 1 by the case `refusals`. Each mutant was applied once and killed by an assertion (measured).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/jobs/cmd/jobs -o /tmp/jobs
/tmp/jobs -addr 127.0.0.1:8080 -site some/dir &
curl -s -XPOST localhost:8080/jobs -d '{"name":"build"}'
printf 'POST /jobs {"name":"build"}\n+3000\nGET /jobs/1\n' | /tmp/jobs -replay
./bin/ergo test examples/jobs/api
go run ./scripts/examples jobs
```

## Gaps

- **G-jobs-1 (closed): a failure of another package's value type.** `http.NotFound`, `http.BodyTooLarge` and `http.BodyReadFailed` are values in package `http` (variants of `MatchResult` and `Body`). The api's rows name them, so in the api they are failures of their own types (types.md, Error types 1), while `http.Match` still returns them as values. Before the fix, the lowering read one package's view for every body, and such a run was a defect (`panic: MethodNotAllowed{}`).
- **G-jobs-2: a route carries a name, not a value.** `http.Route.Name` is a string, so finding the endpoint of a `Found` route is a lookup (`endpointOf`) with a fallback that cannot happen. A route that carries a typed value (`Route[T]`) would make it a field read.
- **G-jobs-3: `MethodNotAllowed` carries no methods.** `Allowed` matches the path once per method to build the `Allow` header that RFC 9110 requires with a `405`.
- **G-jobs-4: no HTTP client in std.** The cases use `-replay`; the socket path is checked by std `http`'s own tests and by hand (Run it). Brief G9 adds a client.
