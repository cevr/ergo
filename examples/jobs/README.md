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
- With `-replay`, the program serves on a loopback listener on port 0 and sends it, with std `http`'s client (`http.Do` through an `http.NewClient`), the `METHOD PATH [BODY]` lines of standard input; `+MS` lines move the manual clock MS milliseconds. It prints each response as the client received it: `> ` and the request, the status, the headers but `Date`, sorted by name, and the body. A `HEAD` response has no body. The task that reads the lines makes the manual clock, so it alone is the clock's domain: the server waits on the network, which `Advance` does not see (time 6), so a server inside the domain would keep `Advance` waiting.

## Layout

The program is a module of packages (`ergo.mod`: `module jobs`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `jobs/` | The domain package, with no HTTP and no JSON. `job.ergo`: `State`, `Signal`, `Job`, the failures `NotFound` and `Conflict`, `Step`, `Timer`, the machine `Lifecycle`, `Settle` and `Apply`. `store.ergo`: the contract `Store` with five laws, and the failure `Stale`. `queue.ergo`: `Submit`, `Find`, `List`, `CancelJob` and `Remove`, over the `Store` and the `time.Clock` in the context. `laws.ergo`: the lifecycle laws. |
| `memory/` | `Table`, a `jobs.Store` in a `sync.Ref`. `ergo test` runs the `Store` laws here. |
| `api/` | The HTTP layer: `routes.ergo` (`Jobs`, `Endpoint`, the constant route table and its coverage law), `endpoint/` (empty endpoint variants), `queue.ergo` (`Queue`, the served Jobs provider), `body.ergo` (the JSON records `Draft`, `View`, `Listing`, `Problem`), `handle.ergo` (`Handle`, the one catch from failure to status, and `WithSite`), `laws.ergo` (the model law), `tests.ergo` (fresh worlds for the model and provider laws). |
| `cmd/jobs/` | The binary. It binds `api.Jobs` to a Queue over a `memory.Table` and the host's clock (or, under `-replay`, a manual one and `http.Net` to a client of its own listener), the listener to `-addr` and the site's root to `-site`. It is the only place that chooses live providers. |

## What it shows

- **A handler with a capability row, served by std `http`.** `api.Handle(http.Request) http.Response needs api.Jobs` is a plain function. `http.Serve` runs it on a child task per request. The binary binds Jobs to `api.Bind(store, clock)`, which captures its chosen store and clock. Tests bind the same served contract with a `time.Manual` and no socket. `api.NewQueue()` builds each generated provider-law case's fresh test world; it is not called by the binary.
- **A lifecycle machine on a clock.** `Lifecycle` is `machine[State, Signal]` with `after: Timer`: a queued job starts after `QueueFor`, a running one finishes after `RunFor`. The server runs no actor per job: `Settle` fires each due timer at its own time when a job is read, so a job read late is the job read on time (law `SettleComposes`).
- **A store contract with laws, and no lost change.** `Store.Swap` and `Store.Remove` take the job the caller read and fail `Stale` when another request changed it first; `CancelJob` and `Remove` read again. `memory.Table` makes each change one `sync.Modify`.
- **Typed failures become statuses in one place.** `answer` fails with each refusal by its type; `Handle`'s one `catch` names each and gives its status, with no `case _`, so a new failure stops the build until it has a status (`TestJobsFailureNeedsAStatus` in `scripts/examples`).
- **Optional and nullable JSON fields.** `Draft.Priority` is a `json.Optional[int]`: a client may leave the key out. `View.Ended` is a `json.Nullable[string]`: the key is always there, and `null` until the job ends.
- **Exhaustive `match` and one route contract.** `Step` names every pair of state and signal; Handle names every endpoint. `Routes` is a constant literal table with required success statuses, used by `http.Match`, `http.Success` and the TypeScript mapper. `RoutesCoverEndpoints` and the mapper require each endpoint exactly once. Jobs is served, not a synthetic description-only operation type. The small `api/endpoint` package keeps route constructors apart from the provider's method namespace.
- **A stock typed HTTP client.** Import `Routes` from `api/routes.ergo` with [ergo-ts](../../tools/ts/README.md) and pass it to Effect's `HttpApiClient.make`: `client.GET.ShowJob({params: {id: 1}})` reads one job, and `client.POST.SubmitJob({payload: {name: "build"}})` queues one. Method groups make a method-only change fail at the call site. Success 201 and empty 204 are declared once in Routes. Failure statuses remain Handle's total catch; their TypeScript schemas are a follow-up.

## Laws

| law | where | rule | mutant it kills |
| --- | --- | --- | --- |
| `UntakenJobIsDoneOnTime` | jobs | a job no one cancels is queued, running and done at the right times, to the ms | a timer that fires 1 ms late |
| `SettleComposes` | jobs | settling twice is settling once to the later time | a timer that fires at the read time, not its own |
| `CancelBeforeTheEnd` | jobs | a cancel succeeds exactly before the end, and names the state when it fails | a done job that is not ended |
| `Store.AddIsQueued`, `AllByAscendingId`, `SwapFromCurrent`, `StaleSwapChangesNothing`, `RemoveForgets` | contract, run in `memory` | the store keeps, lists, swaps and forgets jobs, and a stale change changes nothing | a swap that compares ids, not jobs |
| `AgreesWithModel` | api, with `memory.Table` | generated request sequences, with clock moves between them, get the model's status, body, `Location` and `Allow` at each step | a done job that is not ended; `ended` never set; a missing priority that is not 0; an `Allow` of one method; a lost `Location`; a delete of a running job; 409 given as 400 |
| `RoutesCoverEndpoints` | api | every generated Endpoint has exactly one route | a missing or repeated route |
| `Jobs.EmptyNameIsBadBody` | contract, run against `Queue` | a direct SubmitJob call refuses an empty name, without the HTTP decoder | a provider that relies on DraftOf for name validation |

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

- **G-jobs-1: a failure of another package's value type.** `http.NotFound`, `http.MethodNotAllowed`, `http.BodyTooLarge` and `http.BodyReadFailed` are values in package `http` (variants of `MatchResult` and `Body`). A function of this package that returns one of them as a failure is accepted by the checker, and its run is a defect (`panic: MethodNotAllowed{}`) instead of the typed failure. The api keeps its own failures (`NoRoute`, `WrongMethod`, `TooLarge`, `Unreadable`) until the compiler either lowers such a failure or refuses it.
- **G-jobs-2: a route carries a name, not a value.** Closed by H2: `http.Route[Endpoint]` carries its endpoint, so `answer` matches `f.Route.Value`.
- **G-jobs-3: `MethodNotAllowed` carries no methods.** Closed by H2: `MethodNotAllowed.Allow` is the methods, with HEAD after GET, joined once for the `Allow` header.
- **G-jobs-4: no HTTP client in std.** Closed by H1: the cases send their requests with `http.Do` over a port-0 listener.
