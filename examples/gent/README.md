# gent

A small terminal agent, after the owner's harness gent (`~/Developer/personal/gent` at `aec1ba171`): a person types a message, the model's reply streams in, the model may call tools on the working directory, their results go back to it, and it is asked again until it answers. In E1 the model is a script (`--script FILE`): no network and no key; a live model is a later unit.

```sh
gent --script world.json                 # chat on the terminal
gent --script world.json -p 'hello'      # one turn, headless: prints the transcript
gent --script world.json -p 'hi' --record run.json
gent --replay run.json                   # folds a recorded run: no model, no tools, no clock
gent --script world.json --session s.log  # keeps the signals in s.log; run it again to go on from them
gent --script world.json --here          # the tools act on this directory; shell commands run here, each asked first
```

Flags: `-p TEXT` (one turn, no terminal), `--script FILE` (the scripted world: the model's replies and the files of the working directory, JSON of `memory.World`), `--record FILE` (write the run's signals), `--replay FILE`, `--max-steps N` (asks after one message, 8), `--retries N` (asks again after a failure the model may recover from, 3), `--session FILE` (keep the session's signals in FILE as they commit, one JSON per line through `journal/file`, and on a later start go on from them), `--here` (the tools act on the real working directory, a confined `fs.Root`, and the shell runs on this machine) and `--allow-shell` (with `-p`: approve every shell command; without it each is declined, since no one is there to answer). With no `--script` it exits 2 (no model); with standard input not a terminal and no `-p` it exits 2 and says what to pass.

| key | Idle | Asking, Waiting, Running |
| --- | --- | --- |
| a character, a paste, Backspace, Delete | edits the composer at its cursor | edits the composer at its cursor |
| Left, Right, Home, End | moves the composer's cursor | moves the composer's cursor |
| Enter | sends the composer's line | ignored |
| Esc | ignored | stops the turn: the draft is kept as cut, each call that waited gets the result `interrupted` |
| Ctrl-C | leaves | stops the turn, as Esc |
| PageUp, PageDown | scrolls the transcript by a screen; a page down to the end follows new output again | the same |
| Tab | opens or folds the detail of finished calls | the same |

While a shell command waits for approval (Approving) the screen shows `? $ COMMAND  allow? y / n`: `y` runs it, `n` (or Esc) declines it and the model is told so. The tools: `read`, `list`, and `shell` (`sh -c` in the working directory, empty standard input, stopped after 60 s; its output read while it runs and stopped at 8,000 characters). Every result the model is told holds at most 8,000 characters, then a line that counts the rest.

The status line names the phase and the key that leaves it (law `EveryStateShowsItsExit`); while the agent works a spinner turns before it: its mark is a pure function of the time since the work began, a mark per 120 ms (law `SpinIsTheClock`; the draw loop's `Began` keeps the start, and the test `SpinnerFollowsTheClock` advances a virtual clock 240 ms and sees the third mark). The draw loop reads the clock; a timer of its own only asks it to draw again, and that request carries no state, so no spinner turn reaches the agent or its journal (test `JournalIgnoresElapsedTime`: a read answered at once and one answered 5 s later commit the same signals). The transcript wraps between words to the window (law `WrapLosesNothing`), sticks to its end unless scrolled up (laws `StuckToEndFollows`, `ScrolledUpStays`, `ScrollStaysInRange`), and shows a finished call as one line, `→ NAME ARGS · ok · N lines`, until Tab opens it. The headless transcript (`-p`, `--replay`) shows every call in full. A frame is written as the lines that changed since the last one (law `ChangedLinesRedrawTheFrame`). The transcript marks each row's kind: `>` the person, `<` the model, `→` a tool call, `=` a result, `x` a failed result, `~` a cut reply, `!` a failed ask.

Tag: `default`. It is an original program after gent, not a port with recorded output.

## Layout

The program is a module of packages (`ergo.mod`: `module gent`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `agent/` | The domain. `message.ergo`: `Call`, `Row` (the transcript), `Attempts`, `WellFormed`. `model.ergo`: the operation type `Model` (`Complete`, `Stream`) with its laws, the failures, `Class` (retry or give up) and `Explain`. `tools.ergo`: the operation type `Tools` (`Read`, `List`) with its laws, `Request` (`ReadFile`, `ListDir`, `RunShell`), `Parse` (laws `ParseReadsWhatSchemaNames`, `UnknownNameIsTyped`), `Uses`, `Gate`, `Run`. `shell.ergo`: `Shell` over `exec.Runner`, `Bound` (law `ResultsAreBounded`). `loop.ergo`: the machine `Agent` (`Step`, its work `ask` and its timer), `Recover` and its laws. `run.ergo`: `Recorded`, `Replay`, and `Open`, `Resume`, `Say`, `Headless`, which run the machine as an actor over a journal. |
| `host/` | `Workdir`: `Tools` over a confined `fs.Root` (`--here`); a path that climbs out fails `Escapes` before any lookup. Its laws run over an `fstest` tree (`NewWorkdir`). |
| `memory/` | The in-memory providers: `Scripted` (a `Model` that plays a script), `Tree` (`Tools` over a map of files) and `Commands` (an `exec.Script` of the world's shell results); the law `ReplayIsTheRun`. |
| `screen/` | `frame.ergo`: `Frame(View, term.Window) []string`, one pure function from the agent's state, the composer, the scroll and the spinner's turn to the rows of the screen, `Wrap`, `Paged`, and `Changes` (the lines to write); `keys.ergo`: the machine `Composer`, `Intent` (what a key means in a phase), `Exit`, `Status`. |
| `cmd/gent/` | The binary: `main.ergo` (flags, the world file, headless and replay), `session.ergo` (the session file's codecs and `openSession`) and `chat.ergo` (the one draw loop). |

## What it shows

- **The loop is a machine with a pure step.** `Step(State, Signal)` calls no capability (its row is empty). Asking the model is the work of the `Asking` phase and the retry wait is the `Waiting` phase's timer, so the machine's runtime cancels both when an interrupt leaves the phase. A recorded run is the list of signals the actor committed, and `Replay` folds it with `machines.Run`: the law `ReplayIsTheRun` checks that a live run on a virtual clock ends in the state its fold gives.
- **Laws on the contracts, for every provider.** `Model.StreamJoinsToComplete` (the deltas join to the reply's text, and a stream fails as `Complete` does), `Model.TruncatedIsNotATurn`, `Tools.ListedIsThere`, `Tools.LeavingEscapes`. A later provider (the live model of E3) passes the same laws.
- **Laws on the loop.** `TranscriptIsWellFormed` (every call has one result, after it and before the next reply), `StepsAreBounded` (always), `RetriesWait` (sometimes), `ShellRunsOnlyApproved` (a shell command is entered only by Approve of that call), `DenyIsToldNotRun`, `NoRetryAfterFirstDelta` (a failure after text was shown keeps the text as cut and never retries: gent and BAML refuse such a retry, the Vercel AI SDK allows it) and `InterruptReturnsToIdle`.
- **Approval is a state.** `Gate(RunShell)` is `Ask`, so the machine waits in `Approving`; the terminal's `y`/`n` or, headless, `--allow-shell` answers it, and the record holds the answer. A replay runs no work, so it never runs a command.
- **Exhaustive policy.** `Class`, `Gate`, `Asked`, `Intent` and `Lines` have no default arm: a new failure, request, key or row kind stops the build until its case is written.
- **Frame laws.** `FrameFitsWindow`, `TailIsVisible` (the transcript sticks to its end), `KindsLookDifferent`, `EveryStateShowsItsExit`.
- **Resume runs nothing twice.** `journal.Start` replays a file and then enters the state it ends in, so a session cut while a call ran would run the call again. `Recover` replays the file first; when the turn is still open it appends `Resumed`, which closes the turn as an interrupt does, so the call is told it was interrupted and nothing runs (law `ResumeRunsNothingTwice`, case `resume`). A file the loop no longer takes fails `Diverged` and the message names the signal (case `resume-diverged`).
- **Composer laws.** `CursorStaysOnTheText`, `HomeThenTypeIsFirst`, `EndThenTypeIsLast`, `TypeThenBackspaceIsNoChange`, `PasteIsTyping`.
- **One draw loop.** Keys and the model's deltas reach one loop through a queue; the test `DrawsOnlyStatesThatExisted` checks, replaying the lines each paint writes, that each screen shown is the frame of a state the run committed, and the explore leg searches its schedules (`explored`).

Each law has a mutant it kills, in `testdata/mutants.json` (39); seven are killed by a case instead (`max-steps`, `tools`, `cut`, `shell-approved`, `big-output`) or by the draw-loop test. Generated signals seldom reach a tool and generated scripts seldom stream text, so some laws steer their runs (`steered`) and the providers have generator constructors (`NewScripted`, `NewTree`, `NewCall`).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/gent/cmd/gent -o /tmp/gent
for p in agent memory screen cmd/gent; do ./bin/ergo test examples/gent/$p; done
go run ./scripts/examples gent
```

## Cases

`answer` (one streamed reply), `tools` (a turn that lists and reads from the tree, then answers), `retry` (a rate limit before any delta, then a reply, on the virtual clock), `cut` (a stream that breaks: the draft kept and marked `~`), `max-steps` (a model that calls tools forever stops at the bound), `replay` (a recorded run folded) `no-terminal` (no `-p`, standard input not a terminal: status 2), `shell-approved` (`--allow-shell`), `shell-denied` (no `--allow-shell`: declined and told), `escape` (`../secret.txt` refused and told), `bad-arguments`, `unknown-tool`, `big-output` (a scripted 9,000-character output cut at the bound) `resume` (a session file cut while an approved shell call ran: the call is told it was interrupted and the shell is not run), `resume-diverged` (a file whose signal the loop does not take: status 1, the signal named) and `host-shell` (`--here`: a real `sh -c` whose output passes the bound and which then sleeps: the read stops at the bound and the stop ends its process group, status 143).

## Limits found

- **GENT-G1: std `json.Decode` is not pure.** A machine's step that parses a tool call's arguments is refused by `journal.Start` ("requires its parameter m to be pure"), so the calls are parsed in the `Asking` work and the signal `Replied` holds the parsed `Use`s. Owner: std json (SQ).
- **GENT-G2: a generator constructor that calls `json.Encode`** fails with a nil dereference at its entry inside machine-law runs; `NewCall` builds its JSON by hand. Owner: lower/testgen; a smallest repro is still to be written.
- **GENT-G3: `duration` has no JSON form** in std json codecs, so `RateLimited` holds `AfterMs int` and a wait (`RetryAfter`, `Waiting`) holds `Ms int`: the session file keeps states and signals as JSON.
- **GENT-G4: a `sometimes` machine law cannot steer.** The explorer's random runs never reach `Running`, so "some run runs a tool" is the `tools` case, not a law.
- **One rune is one column.** No display-width function exists in std; wide text wraps wrongly (TEXT-WIDTH).
- **Standard output and error interleave arbitrarily.** `exec.Running.Chunks` keeps each stream's order, not their order between each other, so the shell's result may put an error line before output written earlier. The cases write one stream.
- **No case interrupts a running command.** The draw loop's Esc stops `Running`, whose work's end stops the process group; a terminal case needs the `tty` leg, which is not built.
- No live model yet.
