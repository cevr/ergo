# idle-timer

A shared screen, such as a kiosk in a lobby, that ends a session nobody uses any more. After a wait with no activity it asks "are you still there?", counts down, and abandons the session when nobody answers. A hidden gesture, five quick taps in a corner, opens an admin screen. The program runs a script of timed touches on a virtual clock and shows each change of state and each event, at the time of the signal it follows.

Tag: `default`. It is an original program, a neutral re-creation of a common pattern: a class B machine, whose timers the runner owns. Its goldens were made by running the program, and then checked line by line against the rules below by hand.

## Rules

- A kiosk has a policy, which is the machine's input:
  - `StartWait`: the wait on the home screen;
  - `ScreenWait`: the wait on any other screen;
  - `AssistFactor`: the factor of the wait while the assist mode is on;
  - `PromptFor`: how long the prompt shows;
  - `TapWindow`: how soon a corner tap must follow the one before;
  - `AdminFor`: how long an unused admin screen stays.

  `lobby` waits 30s, 60s, ×2, 10s, 600ms and 120s. `library` waits 45s, 90s, ×3, 15s, 600ms and 60s.
- With no session (`suspended`), any touch, screen or assist switch begins one (`! began`).
- In a session (`waiting`), activity restarts the wait: a touch, a screen opened, or the assist switch. The wait is the wait of the screen, times the factor while assist is on. When the wait passes, the prompt shows.
- The prompt (`prompt N`) counts down the seconds it still shows, one per tick of the screen's clock. The ticks never move its deadline. Any activity continues the session with a new wait. When `PromptFor` passes, the session is abandoned (`! abandoned`).
- Corner taps (`tapping N`): each tap within `TapWindow` of the one before counts. The fifth opens the admin screen (`! admin opened`). A slower tap, other activity, or the window closing goes back to the session. A tap is stepped before a window timer that is due at the same instant.
- The admin screen closes on `leave` or after `AdminFor` unused (`! admin closed`). `finish` ends a session (`! ended`).
- A signal at the instant a timer is due is stepped first. A timer whose state has been left, or re-entered, is dropped and never steps. So a touch exactly at the end of the wait means that no prompt shows.

Script: the first line is `kiosk NAME`. Each other line is `@MS WORD` (milliseconds from the start, never going back), and WORD is `touch`, `open home|menu|form`, `assist on|off`, `tap`, `finish` or `leave`. The last line is `@MS end`. Blank lines and lines that start with `#` are skipped.

## Layout

The program is a module of packages (`ergo.mod`: `module idletimer`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `kiosk/` | The domain package. It is pure. `policy.ergo`: `Policy`, `Standard`, `Check` (fails `BadPolicy`). `machine.ergo`: `Screen`, `Session`, the state `Kiosk`, `Signal`, `Event`, `Begin`, `WaitFor`, `Timer` and `Step`. `laws.ergo`: `type Idler machine[Kiosk, Signal] { initial: Begin, step: Step, after: Timer, … }` and its laws. `policies.ergo`: `Policies`, the contract of a store of policies, with two laws. Also `tests.ergo`. |
| `memory/` | `Fixed`, the in-memory provider of `kiosk.Policies`: `lobby` and `library`. |
| `script/` | `Read` (fails `BadLine`), `WithTicks`, `Upto` and `Run`, the only function with capabilities (`Console`, `io.Stdin`, `kiosk.Policies`). |
| `cmd/idle-timer/` | The binary. `main.ergo` binds `let Live = context { kiosk.Policies: memory.Fixed }` and the host's standard input. It is the only place that chooses them. |

## What it shows

- **Runner-owned timers with a wait computed from the state** (V5 `after`, M58). `Timer(k Kiosk) Option[Timeout[Signal]]` gives each state its timer. The wait of `Waiting` depends on its screen and its assist mode. The program never starts or stops a timer.
- **`Enter` against `Stay`.** Activity re-enters `Waiting`, so its timer restarts. A `Tick` on the prompt is `Stay`, so the prompt's deadline holds. Each corner tap re-enters `Tapping`, so the window restarts.
- **`Emit` (q28; MQ1).** Each end of a session moves to `Suspended` and tells its event in one decision.
- **No stale timer (M46).** The runner drops a timer whose entry has ended. The case `library` shows this with a touch at the very instant the wait ends.
- **Input-initial (V4).** The machine's input is the policy. `script.Run` checks it first, because a start function cannot fail, and then runs `machines.Start(kiosk.Idler{}, policy)`.
- **A virtual clock.** `machines.RunTimed` is the pure timed fold. The laws and the program run on it: no host clock and no sleep.
- **Exhaustive `match`.** Each state's step is a `match` that names all eleven signals. The only `case _` arms are over a script word or a kiosk name, which are strings.
- **Typed failures.** `BadPolicy`, `NoSuchKiosk` and `BadLine`. `Step` itself never refuses: a signal that means nothing in a state is `Ignore`.
- **Laws.** `Idler` declares ten laws after its entries (MU6, `laws.ergo`), run by `ergo test`.
  - **Proved** (types.md Laws 10): three laws over one step take a `Session`, a `Signal` and a `bool`, types that can be listed, so `ergo test` runs every value and reports `proved`:
    - `OnlyIdlePrompts`: in a session, only the `Idle` timer brings the prompt, on every screen and assist mode (`proved: all 84 cases`).
    - `EndsAreTold`: no state of a session ends it in silence; every move to `Suspended` is an `Emit` (`proved: all 168 cases`).
    - `OnlyTapsOpenAdmin`: only the last quick corner tap opens the admin screen (`proved: all 168 cases`).
  - **Proved by a witness:** `law Wakes path [Suspended, Waiting]`: a signal from outside begins a session. A generated run that is a witness proves it; the policy is the input and cannot be listed, so `ergo test` draws the runs.
  - **Sampled:** six timed laws run on `RunTimed` with times drawn near the deadlines, `(100 cases)` each, never proved, because a time is an integer:
    - `NoAbandonBeforeIdle`
    - `ActivityDelays`
    - `StayKeepsDeadline`
    - `AssistWaitsLonger`
    - `ContinueKeepsTheSession`
    - `AdminNeedsFiveQuickTaps`

  The contract `kiosk.Policies` has two member laws, `EveryPolicyChecks` and `UnlistedIsNoSuchKiosk`. The survey's `StaleTimerNeverSteps` is a rule of the runner, not of this machine: std `machines` holds it (V5, `TestMachineRunAgreesWithFold`), and no edit of this program can break it. `LiveEqualsFold` waits for the live runner (MU3).
- **Tests.** The laws are the tests. `kiosk/tests.ergo` has one timed run, `KioskRun`. `memory/tests.ergo` calls the two `Policies` laws (G-ma-2).

## Limits found

- **No live runner (MU3, #57).** The program runs the pure fold over the whole script, so it is a replay, not a live screen.
- **No observation of a running machine (M50).** `RunTimed` gives only the last state and the outputs. To show a timeline, `script.Run` replays each prefix of the trace (`Upto`). That costs time quadratic in the length of the script. A timer's event is shown at the time of the signal it follows, so the resolution is one tick.
- **No state-owned work and no ticker (M45).** The screen clock's one-second tick is a signal that `WithTicks` adds to the trace. A live program would need a task that sleeps.
- **No outside source (M49).** Touches come from the script, not from an input stream that feeds the machine.
- **A timer that is not positive fires at once.** `Check` refuses such a policy, because a state whose timer re-enters it at once never lets time pass (runtime.md, Machines 9.1; lane V follow-up).
- **The timed laws restate the `Standard` policy's numbers**, and the laws over one step fix the policy to `Standard`: a `Policy` holds durations, which cannot be listed, so a law over every policy is sampled, not proved.
- **G-ma-1 and G-ma-2**, as in keypad.
- **No duration × int (M24).** `WaitFor` writes `duration(int64(base) * int64(p.AssistFactor))`, and a millisecond is `duration(int64(n) * 1000000)`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/idle-timer/cmd/idle-timer < examples/idle-timer/cases/lobby/stdin
./bin/ergo test examples/idle-timer/kiosk
go run ./scripts/examples idle-timer
```

## Cases

- `lobby`: the menu wait, a continued prompt, an abandon on the home screen, the admin gesture and leave, assist, and a finish on the prompt.
- `library`: a touch at the instant the wait ends, the triple assist wait, and a corner tap on the prompt.
- `badscript`: a screen that does not exist (status 1).
- `nokiosk`: a kiosk the store does not hold (status 1).
