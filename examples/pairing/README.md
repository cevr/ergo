# pairing

A device pairing screen: the operator types a serial number, gets a one-time code and types it, or taps a device tag. The flow is a machine run as an actor. Its states own their work (sending a code, checking a code or a tag) and their timer (an error screen goes back to the start after 30 s). A script on standard input plays the operator and puts tags on the tag reader; the reader is a source that the actor owns.

Tag: `default`. It is an original program on the shape of survey E5 (a total step with state-owned work, a timer and a fed source). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module pairing`.
- `pad/`: the digit pad, a small pure machine (`Press`) that the flow keeps in a field of its state (the region pattern). Laws: `DigitsRegionTotal`, `EraseUndoesDigit`.
- `pair/`: the domain. `devices.ergo` declares the contract the flow needs, the operation type `Devices` with its laws `CheckIsStable` and `TagIsStable`. `flow.ergo` declares the states, the signals, the machine `Pair` with `initial`, `step`, `after` and `work`, and the eventless `Settle`. `laws.ergo` states the flow's rules as laws over the step.
- `memory/`: `Devices`, an in-memory provider of `pair.Devices`, and properties that run the `Devices` laws against it.
- `screen/`: the line protocol. It parses the script, runs `Pair` as an actor with `actor.StartMachine`, asks it each touch and key, feeds it the tag reader with `actor.Feed`, waits for the screen with `actor.WaitFor`, and prints the end with `actor.ExitOf`.
- `cmd/pairing/main.ergo`: the binary, the only place that chooses the providers (the directory, a `time.Manual` clock, the host's standard input).

## What it shows

- **A total step.** `Step` decides every (state, signal) pair. `case (_, Cancel)` ends the flow from every state; each state then lists the signals it takes and ignores the rest with `case (State, _): Ignore`.
- **Input-initial and an eventless step.** The machine's input is the methods on (`Methods{Serial, Tag}`). `Settle` turns a state with nothing to choose into the state it would choose: with only the serial on, the flow starts on the serial pad. Every `Enter` goes through `Settle`, so no state waits on a choice it cannot make (effect-machine `.immediate`, XState `always`).
- **State-owned work with typed failures.** `SendingCode` and `Verifying` own a `machines.Work` against `Devices`. The runner starts it after the entry commits and interrupts it when the entry ends, so a `cancel` during a verify drops its answer by the entry's epoch. `WrongCode` and `UnknownTag` are failures of `Devices` and also signals, so their works need no failure map; `DeviceUnreachable` is not a signal, and the checker requires `OnFailure`, which maps it to `SendFailed`.
- **A state timer.** `after: timer` gives `Failed` a 30 s timer whose signal is `Back`. The runner owns it: a re-entry restarts it, and it never fires after its entry ended. The `timer` case moves the manual clock with `wait`.
- **Rows.** `work` needs `Devices`; the machine's row is the work's row, so `screen.Run` needs `pair.Devices`, and only `main` binds it.
- **A source the actor owns.** The tag reader (`screen.Reader`) is a queue of tags; `Taps` is it as a `Seq[pair.Signal]`, and `actor.Feed(a, Taps(reader))` makes it a source of the actor (MU10). The reader's task belongs to the actor, not to the screen: it stops when the flow ends, so a tag put on the reader after a pairing is never stepped. A tap line puts a tag on the reader, waits for the reader's beep (the tap is in the inbox), and reads `State`, which waits until the flow has stepped it; `actor.Latest` then says whether the flow refused it.
- **Observers.** The screen waits for the actor with `actor.WaitFor` (MU12): the first state that waits for the operator, after a sent code or a check has answered. A verify that pairs ends the actor; `WaitFor` then fails `actor.Stopped`, and `actor.ExitOf` gives the result.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants-pair.sh`) |
| --- | --- | --- |
| `CancelAlwaysEnds` | Cancel ends with `Cancelled` from every state | Cancel ignored |
| `CodeOnlyAfterSend` | the code pad opens only after a sent code, or again after a wrong one | Submit of a serial opens the code pad |
| `TagSkipsSerial` | a tap in a waiting state of a tag screen verifies the tag | the tap guard reads the serial method |
| `ThreeStrikes` | three wrong codes lock the serial; two do not | lock at the fourth |
| `SettleSkipsOnlyNoChoice` | the flow skips the choice exactly when the serial is the only method | settle skips with the tag on |
| `SettleIsStable` | `Settle(Settle(s)) == Settle(s)` | — (holds the eventless rule's shape) |
| `FailedReturns` | an error screen's timer is 30 s and its signal starts the flow again | timer of 20 s |
| `pad.DigitsRegionTotal`, `pad.EraseUndoesDigit` | the pad holds only digits, never more than it takes; Erase undoes a digit | — |

The work and the timer run in the actor. The laws check the step's side of them (which signal a work's result is, what the timer's signal does); the cases check that the runner delivers them.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/pairing/cmd/pairing < examples/pairing/cases/code/stdin
./bin/ergo test examples/pairing/pair    # 7 laws; the 2 Devices laws are skipped here (no provider)
./bin/ergo test examples/pairing/pad
./bin/ergo test examples/pairing/memory    # the Devices laws against Devices
go run ./scripts/examples pairing
```

Cases: `code` (serial pad, a refused submit, a wrong code, a tap the serial-only screen refuses, a pairing), `strikes` (three wrong codes lock), `tap` (a tag-only screen), `timer` (an unreachable serial, the 30 s timer back to the start, an unknown tag, cancel), `left` (the script ends mid-flow: `ExitOf` is `Halted`).

## effect-machine side by side

The same flow in effect-machine would use: `State`/`Event` schemas; `Machine.make` with an input; `.on` per (state, event); `.onAny(Event.Cancel)`; `.immediate(State.Options, …)` for the eventless step; `.task(State.SendingCode, …, { onSuccess, onFailure })` and `.task(State.Verifying, …)`; `.timeout(State.Failed, { duration: "30 seconds", event: Event.Back })`; `.background` for the tag reader; `Machine.spawn`, `actor.call`, `actor.waitFor` and `actor.awaitExit`.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| every state × signal decided | an unhandled event is dropped at run time | `Step` is a total `match`; `(State, _): Ignore` is written | compiler |
| Cancel from any state | `.onAny(Event.Cancel)`, which a specific `.on` overrides silently | `case (_, Cancel)` is the first arm: no state can override it | compiler (arm order is visible) |
| eventless step | `.immediate`, run by the runtime before the next event | `Settle`, a plain function applied at every `Enter`, with the law `SettleIsStable` | law |
| work result → event | `onSuccess`/`onFailure` handlers chosen by overloads (TS2769 when one is missing) | failures that are signals need no map; `DeviceUnreachable` needs `OnFailure`, and the checker names the missing map | compiler |
| stale work result | a result sent after the state's exit may reach a later state (it is a plain `send`; inferred, em-parity §4.2) | dropped by the entry's epoch | runner (MU4 model and test) |
| timer | `.timeout` compiles to a task in the state scope | `after: timer`, one function over the state union, exhaustive | compiler + runner |
| sub-machine (pad) | a child actor, or fields updated in handlers | a pure function on a record field, with laws | law |
| required services | `R` in the type | `needs Devices` from the work's row | compiler (equal) |
| tag reader | `.background(effect)`, a device of the machine that runs in every state, with its own `send` | `actor.Feed(a, Taps(reader))`: a plain `Seq` of signals that the actor owns and stops; the machine stays a pure step, and the source is a value a test can read without an actor | runner (`TestActorFeedSourcesEndWithTheActor`) |

Where Ergo could not improve:

- **Signatures and type arguments.** `Stay[Flow]{…}`, `Done[Result]{…}`, `Timeout[Signal]{…}`, `Some[Flow]{…}` are written where effect-machine infers (em-parity §4.5 item 1).
- **Ignore arms.** Seven `(State, _): Ignore` arms; effect-machine drops unhandled events with no line. The arms are the cost of totality (q40 keeps them explicit).
- **The reader's beep.** A source's value is a `Send`: it returns when the value is queued, and nothing tells the screen when the flow has stepped it. The reader pushes a beep after each queued tag so the script can wait for it (finding MB2-G1). effect-machine's `.background` has no such answer either (inferred).

## Benchmark

No em-parity §3 scenario is this program's own: the costs it exercises (an `Ask` per signal, a work start per entry, a timer per error entry) are measured by MU3's and MU18's benchmarks. The command for an end-to-end time of the recorded cases is in the lane's `bench-pending.md` (not run; it needs the quiet bench window).

## Limits found

- **F3** (a rule, not a gap) A type that is a failure and also a variant of the signal union fails when its value ends a function (rows.md, Tail results 3). The checker now reports that at the value, with both spellings (S101); the laws write `let w Signal = WrongCode` first, the value spelling.
- **F4** A law of an imported operation type is not run against this package's providers, so `memory/tests.ergo` restates the `Devices` laws as properties.
- **F5** (closed by MU10) The tag reader is an `actor.Feed` source now.
- **MB2-G1** A fed value has no answer: a driver that must see its effect waits on a beep the source gives after each `send`, then on `State`.
- **F6** (fixed, fu-mb2) `[]T` had `Append` only, so the laws wrote a `then` helper to join two slices. They now call `slices.Concat`, which never shares an array with its arguments.
