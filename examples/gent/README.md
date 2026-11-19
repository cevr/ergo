# gent

A small terminal agent, after the owner's harness gent (`~/Developer/personal/gent` at `aec1ba171`): a person types a message, the model's reply streams in, the model may call tools on the working directory, their results go back to it, and it is asked again until it answers. In E1 the model is a script (`--script FILE`): no network and no key; a live model is a later unit.

```sh
gent --script world.json                 # chat on the terminal
gent --script world.json -p 'hello'      # one turn, headless: prints the transcript
gent --script world.json -p 'hi' --record run.json
gent --replay run.json                   # folds a recorded run: no model, no tools, no clock
```

Flags: `-p TEXT` (one turn, no terminal), `--script FILE` (the scripted world: the model's replies and the files of the working directory, JSON of `memory.World`), `--record FILE` (write the run's signals), `--replay FILE`, `--max-steps N` (asks after one message, 8) and `--retries N` (asks again after a failure the model may recover from, 3). With no `--script` it exits 2 (no model); with standard input not a terminal and no `-p` it exits 2 and says what to pass.

| key | Idle | Asking, Waiting, Running |
| --- | --- | --- |
| a character, Backspace | edits the composer | edits the composer |
| Enter | sends the composer's line | ignored |
| Esc | ignored | stops the turn: the draft is kept as cut, each call that waited gets the result `interrupted` |
| Ctrl-C | leaves | stops the turn, as Esc |

The status line names the phase and the key that leaves it (law `EveryStateShowsItsExit`). The transcript marks each row's kind: `>` the person, `<` the model, `→` a tool call, `=` a result, `x` a failed result, `~` a cut reply, `!` a failed ask.

Tag: `default`. It is an original program after gent, not a port with recorded output.

## Layout

The program is a module of packages (`ergo.mod`: `module gent`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `agent/` | The domain. `message.ergo`: `Call`, `Row` (the transcript), `Attempts`, `WellFormed`. `model.ergo`: the operation type `Model` (`Complete`, `Stream`) with its laws, the failures, `Class` (retry or give up) and `Explain`. `tools.ergo`: the operation type `Tools` (`Read`, `List`) with its laws, `Request`, `Parse`, `Uses`, `Gate`, `Run`. `loop.ergo`: the machine `Agent` (`Step`, its work `ask` and its timer) and five laws. `run.ergo`: `Recorded`, `Replay`, and `Open`, `Say`, `Headless`, which run the machine as an actor over a journal. |
| `memory/` | The in-memory providers: `Scripted` (a `Model` that plays a script) and `Tree` (`Tools` over a map of files); the law `ReplayIsTheRun`. |
| `screen/` | `frame.ergo`: `Frame(View, term.Window) []string`, one pure function from the agent's state and the composer to the rows of the screen, and `Paint`; `keys.ergo`: the machine `Composer`, `Intent` (what a key means in a phase), `Exit`, `Status`. |
| `cmd/gent/` | The binary: `main.ergo` (flags, the world file, headless and replay) and `chat.ergo` (the one draw loop). |

## What it shows

- **The loop is a machine with a pure step.** `Step(State, Signal)` calls no capability (its row is empty). Asking the model is the work of the `Asking` phase and the retry wait is the `Waiting` phase's timer, so the machine's runtime cancels both when an interrupt leaves the phase. A recorded run is the list of signals the actor committed, and `Replay` folds it with `machines.Run`: the law `ReplayIsTheRun` checks that a live run on a virtual clock ends in the state its fold gives.
- **Laws on the contracts, for every provider.** `Model.StreamJoinsToComplete` (the deltas join to the reply's text, and a stream fails as `Complete` does), `Model.TruncatedIsNotATurn`, `Tools.ListedIsThere`, `Tools.LeavingEscapes`. A later provider (the live model of E3) passes the same laws.
- **Laws on the loop.** `TranscriptIsWellFormed` (every call has one result, after it and before the next reply), `StepsAreBounded` (always), `RetriesWait` (sometimes), `NoRetryAfterFirstDelta` (a failure after text was shown keeps the text as cut and never retries: gent and BAML refuse such a retry, the Vercel AI SDK allows it) and `InterruptReturnsToIdle`.
- **Exhaustive policy.** `Class`, `Gate`, `Intent` and `Lines` have no default arm: a new failure, request, key or row kind stops the build until its case is written.
- **Frame laws.** `FrameFitsWindow`, `TailIsVisible` (the transcript sticks to its end), `KindsLookDifferent`, `EveryStateShowsItsExit`.
- **One draw loop.** Keys and the model's deltas reach one loop through a queue; the test `DrawsOnlyStatesThatExisted` checks that each frame painted is the frame of a state the run committed, and the explore leg searches its schedules (`explored`).

Each law has a mutant it kills, in `testdata/mutants.json` (19 for E1); five are killed by a case instead (`max-steps`, `tools`, `cut`) or by the draw-loop test. Generated signals seldom reach a tool and generated scripts seldom stream text, so some laws steer their runs (`steered`) and the providers have generator constructors (`NewScripted`, `NewTree`, `NewCall`).

## Run it

```sh
CGO_ENABLED=0 go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/gent/cmd/gent -o /tmp/gent
for p in agent memory screen cmd/gent; do ./bin/ergo test examples/gent/$p; done
go run ./scripts/examples gent
```

## Cases

`answer` (one streamed reply), `tools` (a turn that lists and reads from the tree, then answers), `retry` (a rate limit before any delta, then a reply, on the virtual clock), `cut` (a stream that breaks: the draft kept and marked `~`), `max-steps` (a model that calls tools forever stops at the bound), `replay` (a recorded run folded) and `no-terminal` (no `-p`, standard input not a terminal: status 2).

## Limits found

- **GENT-G1: std `json.Decode` is not pure.** A machine's step that parses a tool call's arguments is refused by `journal.Start` ("requires its parameter m to be pure"), so the calls are parsed in the `Asking` work and the signal `Replied` holds the parsed `Use`s. Owner: std json (SQ).
- **GENT-G2: a generator constructor that calls `json.Encode`** fails with a nil dereference at its entry inside machine-law runs; `NewCall` builds its JSON by hand. Owner: lower/testgen; a smallest repro is still to be written.
- **GENT-G3: `duration` has no JSON form** in std json codecs, so `RateLimited` holds `AfterMs int`.
- **GENT-G4: a `sometimes` machine law cannot steer.** The explorer's random runs never reach `Running`, so "some run runs a tool" is the `tools` case, not a law.
- **One rune is one column.** No display-width function exists in std; wide text wraps wrongly (TEXT-WIDTH).
- E1 has no shell tool, no ask gate (every request is allowed) and no live model: E2 and E3.
