# sign-in

A member sign-in screen: the person types a phone number, gets a one-time code and types it, or scans a member card. The flow is a machine run as an actor. Its states own their work (sending a code, checking a code or a card) and their timer (an error screen goes back to the start after 30 s). A script on standard input plays the person and the card reader.

Tag: `default`. It is an original program, a neutral re-creation of a common kiosk pattern (survey E5). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module signin`.
- `pad/`: the digit pad, a small pure machine (`Press`) that the flow keeps in a field of its state (the region pattern). Laws: `DigitsRegionTotal`, `EraseUndoesDigit`.
- `signin/`: the domain. `codes.ergo` declares the contract the flow needs, the operation type `Codes` with its laws `CheckIsStable` and `CardIsStable`. `flow.ergo` declares the states, the signals, the machine `SignIn` with `initial`, `step`, `after` and `work`, and the eventless `Settle`. `laws.ergo` states the flow's rules as laws over the step.
- `memory/`: `Directory`, an in-memory provider of `signin.Codes`, and properties that run the `Codes` laws against it.
- `screen/`: the line protocol. It parses the script, runs `SignIn` as an actor with `actor.StartMachine`, asks it each signal, waits for the screen with `actor.WaitFor`, and prints the end with `actor.ExitOf`.
- `cmd/sign-in/main.ergo`: the binary, the only place that chooses the providers (the directory, a `time.Manual` clock, the host's standard input).

## What it shows

- **A total step.** `Step` decides every (state, signal) pair. `case (_, Cancel)` ends the flow from every state; each state then lists the signals it takes and ignores the rest with `case (State, _): Ignore`.
- **Input-initial and an eventless step.** The machine's input is the methods on (`Methods{Phone, Card}`). `Settle` turns a state with nothing to choose into the state it would choose: with only the phone on, the flow starts on the phone pad. Every `Enter` goes through `Settle`, so no state waits on a choice it cannot make (effect-machine `.immediate`, XState `always`).
- **State-owned work with typed failures.** `SendingCode` and `Verifying` own a `machines.Work` against `Codes`. The runner starts it after the entry commits and interrupts it when the entry ends, so a `cancel` during a verify drops its answer by the entry's epoch. `WrongCode` and `UnknownCard` are failures of `Codes` and also signals, so their works need no failure map; `Unreachable` is not a signal, and the checker requires `OnFailure`, which maps it to `SendFailed`.
- **A state timer.** `after: timer` gives `Failed` a 30 s timer whose signal is `Back`. The runner owns it: a re-entry restarts it, and it never fires after its entry ended. The `timer` case moves the manual clock with `wait`.
- **Rows.** `work` needs `Codes`; the machine's row is the work's row, so `screen.Run` needs `signin.Codes`, and only `main` binds it.
- **Observers.** The screen waits for the actor with `actor.WaitFor` (MU12): the first state that waits for the person, after a sent code or a check has answered. A verify that signs in ends the actor; `WaitFor` then fails `actor.Stopped`, and `actor.ExitOf` gives the result.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants-signin.sh`) |
| --- | --- | --- |
| `CancelAlwaysEnds` | Cancel ends with `Cancelled` from every state | Cancel ignored |
| `CodeOnlyAfterSend` | the code pad opens only after a sent code, or again after a wrong one | Submit of a phone opens the code pad |
| `ScanSkipsPhone` | a scan in a waiting state of a card screen verifies the card | the scan guard reads the phone method |
| `ThreeStrikes` | three wrong codes lock the phone; two do not | lock at the fourth |
| `SettleSkipsOnlyNoChoice` | the flow skips the choice exactly when the phone is the only method | settle skips with the card on |
| `SettleIsStable` | `Settle(Settle(s)) == Settle(s)` | — (holds the eventless rule's shape) |
| `FailedReturns` | an error screen's timer is 30 s and its signal starts the flow again | timer of 20 s |
| `pad.DigitsRegionTotal`, `pad.EraseUndoesDigit` | the pad holds only digits, never more than it takes; Erase undoes a digit | — |

The work and the timer run in the actor. The laws check the step's side of them (which signal a work's result is, what the timer's signal does); the cases check that the runner delivers them.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/sign-in/cmd/sign-in < examples/sign-in/cases/code/stdin
./bin/ergo test examples/sign-in/signin    # 7 laws; the 2 Codes laws are skipped here (no provider)
./bin/ergo test examples/sign-in/pad
./bin/ergo test examples/sign-in/memory    # the Codes laws against Directory
go run ./scripts/examples sign-in
```

Cases: `code` (phone pad, a refused submit, a wrong code, a sign-in), `strikes` (three wrong codes lock), `scan` (a card-only screen), `timer` (an unreachable phone, the 30 s timer back to the start, an unknown card, cancel), `left` (the script ends mid-flow: `ExitOf` is `Halted`).

## effect-machine side by side

The same flow in effect-machine would use: `State`/`Event` schemas; `Machine.make` with an input; `.on` per (state, event); `.onAny(Event.Cancel)`; `.immediate(State.Options, …)` for the eventless step; `.task(State.SendingCode, …, { onSuccess, onFailure })` and `.task(State.Verifying, …)`; `.timeout(State.Failed, { duration: "30 seconds", event: Event.Back })`; `.background` for the card reader; `Machine.spawn`, `actor.call`, `actor.waitFor` and `actor.awaitExit`.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| every state × signal decided | an unhandled event is dropped at run time | `Step` is a total `match`; `(State, _): Ignore` is written | compiler |
| Cancel from any state | `.onAny(Event.Cancel)`, which a specific `.on` overrides silently | `case (_, Cancel)` is the first arm: no state can override it | compiler (arm order is visible) |
| eventless step | `.immediate`, run by the runtime before the next event | `Settle`, a plain function applied at every `Enter`, with the law `SettleIsStable` | law |
| work result → event | `onSuccess`/`onFailure` handlers chosen by overloads (TS2769 when one is missing) | failures that are signals need no map; `Unreachable` needs `OnFailure`, and the checker names the missing map | compiler |
| stale work result | a result sent after the state's exit may reach a later state (it is a plain `send`; inferred, em-parity §4.2) | dropped by the entry's epoch | runner (MU4 model and test) |
| timer | `.timeout` compiles to a task in the state scope | `after: timer`, one function over the state union, exhaustive | compiler + runner |
| sub-machine (pad) | a child actor, or fields updated in handlers | a pure function on a record field, with laws | law |
| required services | `R` in the type | `needs Codes` from the work's row | compiler (equal) |

Where Ergo could not improve:

- **Signatures and type arguments.** `Stay[Flow]{…}`, `Done[Result]{…}`, `Timeout[Signal]{…}`, `Some[Flow]{…}` are written where effect-machine infers (em-parity §4.5 item 1).
- **Ignore arms.** Seven `(State, _): Ignore` arms; effect-machine drops unhandled events with no line. The arms are the price of totality (q40 keeps them explicit).
- **The card reader** is the script: there is no actor-lifetime source yet (M49), so a scan is a `Send` from the driver, as effect-machine's `.background` would do from inside.
- **The driver's wait for the timer**: before `wait` moves the clock, the screen spins until the timer's task sleeps (finding F2).

## Benchmark

No em-parity §3 scenario is this program's own: the costs it exercises (an `Ask` per signal, a work start per entry, a timer per error entry) are measured by MU3's and MU18's benchmarks. The command for an end-to-end time of the recorded cases is in the lane's `bench-pending.md` (not run; it needs the quiet bench window).

## Limits found

- **F1** `machines.Run` of a machine with work needs the work's row (`Codes`) although a fold never runs work, so the laws fold the step by hand (`fold`).
- **F2** A state timer's sleep starts on its own task after the entry commits, so a driver on `time.Manual` can move the clock before the timer sleeps and the timer then waits 30 s more. The screen spins on `clock.Sleepers()` before `wait`.
- **F3** A type that is a failure and also a variant of the signal union cannot be a tail value: `WrongCode` at a tail is a failure (rows.md, Tail results). The laws write `let w Signal = WrongCode` first.
- **F4** A law of an imported operation type is not run against this package's providers, so `memory/tests.ergo` restates the `Codes` laws as properties.
- **F5** No actor-lifetime source (M49): the card reader is the script.
- **F6** `[]T` has `Append` only; the laws write a `then` helper to join two slices.
