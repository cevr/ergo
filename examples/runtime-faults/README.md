# runtime-faults

Two small apps that show what the runtime does when a program goes wrong or is slow:

- **crash**: one button whose step defects. The window then shows the app's crash view, the app's report logs the crash, and Reload starts the app afresh.
- **lab**: four buttons. Each makes one phase of a pass do more counted work than its budget allows: the step, the view, the patch, or the subscriptions' dependencies. The session sends a warning back for each slow pass, and the lab logs it.

This is stage b of FK13. Both apps already have their views, because the crash view (H6a) and the slow warnings (H6b) are features of a window's session, and a session needs a view. The program plays a scene script on the test host (`ui.TestHost`), which steps the same session a window steps, and prints what the screen shows.

Tag: `default`.

```sh
runtime-faults < cases/lab/stdin
```

## How it works

- Package `crash` is the crash app: the machine `App`, whose step is `Unreachable` for every signal, its `View` (the Crash button), its `CrashView` (a title, the defect's reason, and Reload), and `Crashes`, which pairs the crash view with a report that logs each crash with the signal it came around, the state and the reason. `ui.StartApp` (H6a) keeps the window open on the crash view. It runs the report once per crash, and takes the crash view's signal as the reload, which starts a fresh actor from the machine's initial state.
- Package `lab` is the lab: the machine `Bench`, whose state `Lab` holds the busy workload, the next warning id, the log (newest first, at most eight) and the patch surface's rows. Its `Screen` asks the session to judge each pass against a `ui.Budget` with one bound per phase (`ui.OnSlow`, H6b). The session sends each counter over its bound to the step as a `Recorded` signal, which the step logs. A pass that the session's own signals cause (showing the log) is not judged, so a warning never warns about itself.
- Package `script` reads a scene script (`app crash` or `app lab`, then `tap LABEL`, `frame`, `settle`, `screen`, `find TEXT`, `stats`, `reports`, `crashed`, `warnings`) and plays it on the test host on a `time.Manual`. A frame moves the clock by one frame period.
- `cmd/runtime-faults` chooses the clock.

Every phase's work is counted (`ui.Stats`), so a warning is the same on every run and every host, and the laws need no timing:

| Workload | Its work | Counter, bound | Count |
| --- | --- | --- | --- |
| Run update work | 8 animations, so one frame steps a burst of 8 signals | `Signals`, 4 | 8 |
| Run view work | 5,250 nodes in a group that then stops (`ui.Boundary`), so they reach no patch | `Written`, 5,000 | 5,272 |
| Run patch work | mounts 4,000 keyed rows, kept from pass to pass by a `ui.Same` keyed by the mount (lazy rows) | `Ops`, 100 | 4,002 |
| Run dependency extraction | 8 loads, one per dependency | `Fetches`, 4 | 8 |

Each workload is over one bound only. The patch's rows fit the view's bound, and the view work's nodes make no ops.

## Laws

- `crash` `CrashShowsTheCrashView`: over cycles of Crash and Reload presses, each Crash press defects. The screen is then the crash view with the defect's reason, and the report has one line per crash so far, naming the press, the state and the reason. Each Reload shows the first screen again, with no crash.
- `lab` `StepFollowsTheModel`: drawn runs, clears, warnings of any counter, and the work's own signals are folded over the step. After every step, the lab matches a model of its workload, log, ids and rows. The log is cut to eight entries, and a counter that no phase bounds changes nothing.
- `lab` `EachWorkloadWarnsItsPhase`: on the test host, drawn presses of the four workloads and of Clear give one warning per run, of that run's own phase. Each warning is over its bound and names the workload's button as its trigger. The screen shows the model's log. A Clear empties the log, and the screen says so. The rows stay mounted after the first patch run.

The std laws `CrashShowsTheCrashView`, `ReloadIsInitial` (H6a) and `StatsCountEveryPhase` (H6b) in `internal/sketch/std/ui/laws_host.ergo` check the session itself.

The mutants these kill are in `testdata/mutants.json`.

## Cases

- `crash`: the first screen, a crash with its crash view and report, a reload, and a second crash.
- `lab`: the controls, then each workload in turn with the pass's counted work and the log, an idle frame that warns of nothing, and a Clear that keeps the rows.
- `badscript`: a tap of a label that no node has. The program says which line, and exits 1.
