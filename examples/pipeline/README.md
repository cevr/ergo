# pipeline

A build pipeline. A **run** has stages; a **runner** (an external system) runs one try of a stage; a failed try waits a backoff and the stage is tried again, three tries at most; an operator submits, cancels and retries runs; a **board** shows the runs by column. The binary reads an operator's script on standard input, a command of the generated command line on each line, and prints what the pipeline does.

Tag: `default`. It is an original program, a neutral domain. It is the worked example of domain modeling in Ergo: an aggregate as a machine, a view as a fold, a saga with a timer, a policy, an Api as one operation type with laws, and a scene that plays the real program against a reference story. Its goldens were made by running the program, and then checked line by line against the rules below.

## Rules

- A run is `Unsubmitted`, `Queued`, `Running` at a stage, `Failing` at a stage, or `Done` with an outcome (`Passed`, `GaveUp` at a stage, `Cancelled` with a reason). A run has 1 to 9 stages.
- The context runs one stage at a time, in the order the runs were submitted. A try that fails (or whose runner is offline) waits 1 s, then 2 s; the third failed try gives the run up, and the next run starts.
- An operator's command may come at any time. `cancel` stops a queued, running or failing run. `retry` tries a failed stage now, without its wait. A command that the run refuses is answered with its typed rejection, and nothing changes.
- Each command has an ID (`-id`). A resend with the same ID gets the first receipt and changes nothing.
- A run that ends gives one notice (`!`).

## Script

A script is the command line's own language (std `api.Session`): each line is a command of `pipeline/pipelinecli`, split into words as a shell splits a simple command (quotes group: `-why="no longer needed"`), and `wait`, the binary's one word, lets an hour pass. A blank line or a line that starts with `#` runs nothing.

```
submit -id=ID -run=RUN -stages=N    a new run
cancel -id=ID -run=RUN -why=WHY     stop the run
retry -id=ID -run=RUN -stage=S      try the failed stage S (0 is the first) now
board                               the board
run-of -- RUN                       the state of the run
wait                                an hour passes
```

A value prints as the script's text (`text`: a receipt, the board by column, a run's status). A refused command prints as `refused: ` and its typed rejection (the tag and the wire's JSON), and the script goes on: the script's policy (`code`) gives a refusal status 0, and `Unavailable` status 1. A line that the command line cannot read (an unknown command, a missing flag, an unclosed quote) ends the script with status 2, naming the line; a resend is the same command again with its ID.

`-fail STAGE:TRIES` (repeated) makes stage STAGE of every run fail its first TRIES tries; a negative TRIES makes the runner offline for them.

## Over HTTP

`pipeline-server` serves the same Api over HTTP: Effect RPC at `/rpc` (the generated server) and `ok` at `/health`, on one listener whose address it prints first. `pipeline --server URL COMMAND...` sends one command of the generated command line to it through the generated client and prints the answer: a receipt, the board or a run as JSON, or a rejection as its type and JSON with status 1. A server that does not answer is a line and status 1.

```sh
pipeline --server http://127.0.0.1:PORT/rpc submit -id=c1 -run=build -stages=2
pipeline --server http://127.0.0.1:PORT/rpc board
pipeline --server http://127.0.0.1:PORT/rpc run-of -- build
```

## On a machine

`pipeline-infra` deploys `pipeline-server` with module `infra`'s command line over the stack of `deploy/` and module `infra-local`'s drivers on the host. A stage (`-stage`, default `dev`) is the folder `$PIPELINE_HOME/STAGE` (default `./.pipeline/STAGE`) holding the server's folder `data` (its `README` and an `address` file) and the drivers' ledgers and rows; its lock and its outputs (`STAGE.outputs.json`: the server's URL, port and folder) sit beside it. The server is `$PIPELINE_SERVER` (default `pipeline-server`, found on `PATH`) on `$PIPELINE_PORT` (default 8787), and an apply ends once its `/health` answers.

```sh
PIPELINE_PORT=8787 pipeline-infra plan        # the four creates
pipeline-infra apply -yes                      # starts the server, writes .pipeline/dev.outputs.json
pipeline --server http://127.0.0.1:8787/rpc board
pipeline-infra plan                            # plan: no changes
pipeline-infra destroy -yes                    # ends the server, removes its folder
```

## Layout

The program is a module of packages (`ergo.mod`: `module pipeline`), in the Go layout. Its `go.mod` requires modules `domain`, `mcp`, `infra` and `infra-local` of this repository, each by a `replace` to its folder:

| folder | what it holds |
| --- | --- |
| `run/` | The aggregate. `run.ergo`: `Run` (a union of modes), `Command`, `Event`, the rejections `WrongState`, `WrongStage`, `Closed`, `BadLength`, the pure `Decide`, the total `Evolve`, `Fold`, and `Handle` (decide, then fold). `laws.ergo`: the machine `Life` with `ClosedIsFinal`, `Forward` (the command that moves a run on, for the laws of other packages), `StagesPassInOrder`. |
| `board/` | The view: `View`, `Apply` (a fold step over events), `Of` (the board of the runs' states); the law `BoardIsFoldOfEvents`. |
| `retry/` | The saga: `Policy`, `Attempt` (`Trying` or `Backoff`), the machine `Saga` with a timer on `Backoff`; laws `GivesUpAfterAttempts`, `WaitsItsBackoff` under `machines.RunTimed` on a virtual clock. |
| `notify/` | The policy: `React(id, event) []Notice`, pure; the law `OneNoticePerEnd`. |
| `pipeline/` | The contracts and the context. `api.ergo`: the Api `Pipeline` (`Submit`, `Cancel`, `Retry`, `Board`, `RunOf`) with the law `DuplicateIsNoop`. `runner.ergo`: the external system `Runner` with the law `Repeatable`. `desk.ergo`: the context's machine `Dispatch` (`Idle`, `Busy`, `Waiting`): it calls each run's `Handle`, runs the saga's step as a pure child, and owns the runner's try as the work of `Busy`. `model.ergo`: `Model`, the provider of `Pipeline` over a durable actor of `Dispatch`, event-sourced on a `domain.Log` of module `domain` by `domain.Sourced`: the actor's state is a `domain.Ledger` of the desk, and `Hosted`, its step, commits each step's entries as one record of the stream `pipeline`, with `domain.Commit` when the desk enters a state and `domain.Keep` when it stays, so a command does not restart a try or a wait (`log.ergo`: `Evolve`, the fold of one entry into the desk, which the recover runs). `projection.ergo`: the board as a `domain.Follow` follower of the stream, with its own cursor. `log_laws.ergo`: `RecoverAgrees`, `RestartGoesOn`, `RestartMidStage`, `CommandKeepsTheWait`, `BoardFollowsTheLog`. `scene.ergo`: the reference `Story` and the law `SceneAgreesWithStory`. `tests.ergo`: the context `overMemory`, which feeds `Pipeline`'s law at `Model`. |
| `pipeline/pipelineapi/` | The Api's hub, generated by `ergo gen api pipeline.Pipeline` (run first): `Request`, `Op`, `Reply`, `Failure`, `Outcome`, the one dispatch `Call`, a `Client`, and the laws `CallIsTheOperation` and `ClientIsCall`. |
| `pipeline/pipelinerpc/`, `pipeline/pipelinecli/` | Generated over the hub by `ergo gen rpc pipeline.Pipeline` and `ergo gen cli pipeline.Pipeline`: the wire's `Request` (the hub's), the server's `Handle`, the `Client` (a provider of `Pipeline` over a connection), and the command line (`Parse`, `Print`, `Run`, the law `ArgsRoundTrip`). Each transport package has `Wire`, its `api.Transport`, and the law `Agrees`: the wire carries every call and every outcome the hub's `Call` can have unchanged. The examples runner's `gen --check` leg fails on a stale file. |
| `pipeline/pipelinemcp/` | Generated by `ergo gen mcp pipeline.Pipeline`: a Model Context Protocol tool per operation of the Api (`submit`, `cancel`, `retry`, `board`, `run-of`), described by the operation's doc comment, with the JSON schema of its arguments; `Call` and `Arguments` between a tool call and the hub's `Request`, `Answer`, `Tools`, and the laws `ArgumentsRoundTrip` and `ArgumentsFitTheSchema`. |
| `surfaces/` | What every surface shares: `Text` (an outcome as text), `Through` (the Api over a generated transport's `Wire` to the `Pipeline` in force); the laws `SurfacesAgree` and `TransportsTakeEachCommandOnce`. |
| `screen/` | The screen, a machine of its own (`Life`): its state (`Showing` or `Asking`) holds the form, the notice of the last reply, and the board and the picked run as loads (`ui.Async`); a command is a signal that carries the hub's `Request` (`pipelineapi`) (`Ask`). |
| `view/` | The one view (`View`, `Board`), a function of the screen's state whose loads call the `pipeline.Pipeline` in force; the law `ReplyIsShown` and the two scenes on `ui.TestHost`. |
| `deploy/` | The stack: `Stack(Want) Outputs` over `infra.Kind` of module `infra-local`'s directory, file and program; `Place` (a stage's drivers, state stores and lock), `Bind`, `Stages` and `Deployment` (infra's command line over them); the laws `StackConverges` and `ServerListensOnTheOutputsPort` on `fstest.Memory` and `exec.Processes`. |
| `memory/` | `Flaky`, a `pipeline.Runner` whose stages fail their first tries. |
| `cmd/pipeline/` | The binary: the flags, the script's policy (`code`, `text`) for the generated command line, which `api.Session` runs line by line, and the context that binds `pipeline.Runner` to a `memory.Flaky`, the event log to module `domain`'s memory log, the notices' follower (`told`, which runs `notify.React` over the records after its cursor) to a memory cursor, and `Scheduler` to a manual clock that only `wait` moves. With `--server URL` it binds `pipeline.Pipeline` to the generated client instead. It is the only place that chooses them. |
| `cmd/pipeline-server/` | The server: the context of a memory runner whose stages pass, module `domain`'s memory log and the live clock, served by `rpc.Answer` beside a `/health` route. |
| `cmd/pipeline-infra/` | The stack's command line: binds `deploy.Place` to `infra-local`'s drivers, rows and file lock on the host, with the HTTP health check. |
| `cmd/pipeline-mcp/` | The MCP server: the same context as the server's, its tools served by module `mcp` over standard input and output (`mcp/stdio`), for an agent's host that starts it as a process. |

## What it shows

- **State is a union of modes.** No status field: `Run`, `Desk` and `Attempt` are unions, and every `match` over them names each variant.
- **`Decide` has no `needs` row.** It cannot read a clock, a store or the network, so a restart's fold of the log gives the same desk. The checker proves the context's step pure before `actor.StartDurable` takes it.
- **Typed rejections.** `Decide`'s `fails` row is the run's rejections; the Api adds `NoSuchRun` and `Unavailable`; the CLI's `catch` names each one.
- **Contexts are data.** A command travels in an `Envelope` with its ID; the receipt is in the state, so a resend is answered from it.
- **A saga with a timer, run as a pure child.** The desk keeps the saga's `Attempt` in its job, calls `retry.Step`, and arms `retry.Timer` as its own `Waiting` timer. The saga's laws run the same functions under `machines.RunTimed`.
- **Work owned by a state.** The runner's try is the work of `Busy`: a cancel of the running run ends the entry, which stops the try.
- **A UI is a view of the domain.** The screen's step is pure; its view asks the host for each call of the Api as a load, so an answer, a rejection included, comes back as a signal and is state the view shows. The provider of `Pipeline` is the caller's: the in-process model, the generated client, a `testing.Script`.
- **Scenes.** `SceneAgreesWithStory` plays generated acts (submit, cancel, retry, wait) on the real program, a durable actor whose runner is a `testing.Script[pipeline.Runner]` and whose clock is a `time.NewManual` that only the scene moves, and compares the desk at the end with the reference story, which applies the same rules over `run.Decide` and `run.Fold`. A failing seed prints the acts and the runner's table, and the replay command.

## Laws

| law | where | rule | mutant it kills |
| --- | --- | --- | --- |
| `Life.ClosedIsFinal` | run | a run that ended refuses every command with `Closed` | `Decide` takes a submit of a run that ended |
| `StagesPassInOrder` | run | stages pass one after the other from the first; a run finishes after its last stage | `Evolve` drops `StagePassed` |
| `HandleFoldsItsEvents` | run | the state `Handle` moves a run to is the fold of the events it gives, so the desk and a recovery, which fold the events, see the run the step committed | `Handle` keeps the old state |
| `BoardIsFoldOfEvents` | board | the board folded from the events is the board of the runs' states | `Apply` keeps a run that leaves `failing` in that column too (two columns); `Apply` leaves a retried run in `failing` |
| `GivesUpAfterAttempts` | retry | a stage that always fails is tried three times, then given up | the saga gives up one failure early; its third order names the wrong try |
| `WaitsItsBackoff` | retry | the next try is ordered exactly when its wait ends | the timer fires after half the wait |
| `OneNoticePerEnd` | notify | one notice when a run ends, none before | `React` gives a notice for a failed stage |
| `Runner.Repeatable` | contract (against `memory.Flaky`) | the same try asked twice gets the same answer | `Flaky` answers by the count of calls, not by the try |
| `Pipeline.DuplicateIsNoop` | contract (`overMemory` in `pipeline`; `overHub` in `pipelineapi/tests.ergo`; `overWire` in `pipelinerpc/tests.ergo`) | a resend gets the first receipt. The handwritten client contexts each build the generated `Client` over a fresh `Model` and close the model after the case. | the desk does not look up the command's ID |
| `SceneAgreesWithStory` | pipeline | the program ends in the story's desk | `Handle` emits the events but keeps the old state; the work counts an offline runner as a pass; the desk starts the last queued run first |
| `RecoverAgrees` | pipeline | the stream a scene wrote folds back (`domain.Sourced`'s recover with `Evolve`) into the desk it ended in, exactly (the step commits the desk `Evolve` folds): runs, log, queue, job and try | a recovered retry keeps the try it retries; a recovered failure stays busy; a recovered cancel keeps the waiting job |
| `CommandKeepsTheWait` | pipeline | a command taken while the desk waits out a backoff keeps the wait: the next try comes when the backoff from the failure ends | the desk commits a stay with `Commit` (a new entry, a new wait); `Keep` drops the receipt |
| `RestartGoesOn` | pipeline | a context stopped part-way and opened again over the same log plays the rest to the story's desk | (module `domain`'s `SourcedSurvivesCrashes` holds the save's position) |
| `RestartMidStage` | pipeline | `RestartGoesOn` for a run whose second stage waits at the cut, after the operator retried its first | a job counts the failures of other stages |
| `BoardFollowsTheLog` | pipeline | the board that follows the log is the fold of the desk's entries | the board skips a record its follower delivers |
| `domain.Cursor.LastSaveWins`, `CursorsAreApart` | contract (against `Projection`) | the board's cursor keeps the last save, apart for each follower and stream | (module `domain`'s laws, run for the example's provider) |
| `SurfacesAgree` | surfaces | one generated list of requests gets the same answers, rejections included, and the same board, through the model and the screen on `ui.TestHost` (each request typed into its form and asked by its button); the generated transports are held by their own law `Agrees` | the screen's cancel button asks a retry; the board's load is keyed by nothing, so an older board stays |
| `Agrees` | pipeline/pipelinerpc, pipeline/pipelinecli, pipeline/pipelinemcp (generated) | the transport's `Wire` carries every request and every outcome the hub's `Call` can have (drawn by `Given`) unchanged | the server answers `WrongState` as `Closed`; `Print` drops `-stage` |
| `ReplyIsShown` | view | the view of a reply shows it: the receipt's ID and position, or `refused: ` and the reason | a rejection is shown as `took …` |
| `HeldTryShowsRunningThenPassed` | view | a scene: the runner (a `testing.Script`) holds the try of a submitted run; the screen shows its receipt and the run running; after a `Release` and a refresh, passed | the board hides the running column |
| `HeldAskShowsSendingThenReceipt` | view | a scene: the Api (a `testing.Script[pipeline.Pipeline]`) holds the screen's submit; the screen shows it sending, then the receipt, and the Api got the form's command | the screen in flight shows the last notice |
| `TransportsTakeEachCommandOnce` | surfaces | `Pipeline.DuplicateIsNoop` holds through each generated transport's `Wire` (command line, MCP tools, the RPC wire) | (the member law's body, on a provider `ergo test` cannot build) |
| `StackConverges` | deploy | a plan of a new stage lists its four creates; an apply makes them and writes outputs that name the server; a second plan has no change; a destroy ends the server and leaves no row | the stack gives two files one id |
| `ServerListensOnTheOutputsPort` | deploy | the server is told (`-addr`) the port its outputs name | the stack tells the server port 0 |
| `ArgsRoundTrip` | pipeline/pipelinecli (generated) | `Parse` reads what `Print` writes as the same request | `Print` drops `-stage` |

On the host, `cmd/ergo`'s `TestPipelineInfraDeploysTheServer` (full tier) builds `pipeline-infra` and `pipeline-server`, applies a stage in a temporary folder, gets `ok` from `/health` at the outputs' URL, plans no change, and destroys it: the server no longer answers, and its folder and the outputs are gone.

The mutants are in `testdata/mutants.json`.

## Limits it meets

- **One actor per context.** An actor per run would need children that outlive the parent's other transitions (corpus L3), so one actor holds every run, and one stage runs at a time.
- **An output from a `Stay` has no form (L1).** A command taken while a try runs must `Stay`, or the try would stop; so the entries live in the state, and the save appends them to the `domain.Log` when the step commits; the notices and the board follow that log, not the actor's sink.
- **A provider cannot need another capability.** `Model` holds its actor; the runner and the clock are bound where `Open` runs.
- **`Model` is a resource (q266).** `Model implements Closer`: its `Close` stops the actor, and the `with` or the frame that holds it releases it on every path. Code that gives a `Pipeline` to a `with` while it goes on using the `Model` names the `Model` in the entry: the `with` lends it for the block, and the owner closes it after. The screen and the MCP tools take the `Pipeline` in force and hold it as a context (`InForce`), so they answer while that `with` runs; a screen played after it ended is the held-context defect (mutant `pl-late-view`).
- **A screen's signal must compare.** `ui`'s signal type is `comparable`, and a board's columns are slices, so the board's load gives `screen.Lines` (each column joined by `, `), not a `board.View`.
- **A screen's call is a load, not the machine's work.** A `ui` session renders after its own events only; a signal that an actor's work sends would change the state with no new pass. So the view asks for each call (`ui.Load`), and its answer arrives through the session.
- **The file lock is not exclusive.** std `fs` has no exclusive create, so two `pipeline-infra` runs that start at the same instant can both take a stage's lock; one that starts while another is on is kept out.
- **The server's providers.** `pipeline-server` has no `-fail` flag: its runner passes every stage. A case of `--server` would need a server running beside it; `SurfacesAgree` holds the client to the model over `rpc.Loopback` instead.

## Run it

```sh
CGO_ENABLED=0 go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/pipeline/cmd/pipeline -o /tmp/pipeline
/tmp/pipeline -fail 2:2 < examples/pipeline/cases/basic/stdin
./bin/ergo test examples/pipeline/pipeline      # and run, board, retry, notify, memory
go run ./scripts/examples pipeline
```

## Cases

| case | what it shows |
| --- | --- |
| `basic` | two runs, a stage that fails twice and passes on its third try, the board before and after, a resend |
| `giveup` | an offline runner counts as a failed try; a stage that fails three times gives the run up, and the next run starts |
| `operator` | a cancel of a queued run (a quoted reason) and a retry now during a backoff; each kind of refusal, after which the script goes on |
| `badline` | a command the command line does not know: the lines before it run, then status 2 |
| `badflag` | a `-fail` value that is not `STAGE:TRIES`: status 2 |
