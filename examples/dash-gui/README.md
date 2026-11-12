# dash-gui

An animated dashboard: twelve panels (four line charts over rings of 10,000 samples, three spark lines, two gauges that ease toward each new value, two heat strips and an event log), each fed by a seeded sensor source that runs as its own task and hands its samples over through a bounded queue, on a virtual clock of 120 frames a second. The model and its headless driver are stage a (corpus wave GP4a); the view on `ui.TestHost` is stage b (GP4b). The desktop window comes with the MyGo host (stage c).

```sh
dash-gui gen [-seed S] [-n N]   # write a script of N steps
dash-gui play [-seed S]         # play the script on standard input over the sensors of seed S
dash-gui scene [-seed S] [-frames N]  # move the view on ui.TestHost N frames, then play a scene script on it
```

A scene script drives the view as an agent drives a window: `frames N` moves the host's clock N frames of a 120th of a second (the view's `ui.Animate` turns each into a `Next` for the app), `tap LABEL` presses a node by its label (`toggle P` pauses or resumes panel P, `policy P` swaps its queue's policy), `show` writes the frame's text nodes (the frame's number, each panel's line, the drop counts: the same lines `play`'s `show` and `drops` write), and `shot` writes the whole screen with each canvas's paint list.

A script is one command a line: `run N` advances the clock N frames, `pause P` and `resume P` pause and resume panel P, `policy P oldest|newest` sets what source P's full queue drops, `show` writes every panel's line (its mode, the samples it took, the frame of the last one, and what it draws), and `drops` writes how many samples each queue dropped. While the clock runs, a panel whose source falls silent for more than half a second writes `F: P stale`, and `F: P live` when samples come back. A line that is no command writes `? "LINE": want ...`, and a pause or resume a panel cannot take writes `? "LINE": refused`.

## What it shows

- **A machine per panel.** `panel.Feed` is `machine[State, Event]`, `State = Live | Paused | Stale`. The step matches the mode and the event exhaustively: samples in `Live` stay live, a tick more than `StaleAfter` frames after the last sample emits `WentStale`, samples in `Stale` emit `CameBack`, a paused panel ignores samples and the clock, and a pause of a paused panel or a resume of a running one is `Reject`. Laws: `GoesStale`, `PauseAndBack` (a path) and `StaleAfterSilence` (a live panel is live at every frame up to `StaleAfter` after its last sample and stale at the next).
- **Tasks, queues and a structured stop.** `feed.Start` runs a task for each source in one `tasks.Set`. Each frame the board gives every source an order, waits for every report, then drains the queues in panel order, so what a queue drops never depends on the scheduler and a seed replays the run exactly. When the script ends, `feed.Stop` ends the order queues and the set's `Wait` is the stop.
- **A drop policy as a union.** `Policy = DropOldest | DropNewest`; `Offer` matches it when a queue is full. Laws `DropOldestKeepsTheLast` and `DropNewestKeepsTheFirst` hold it to what each policy promises, with the dropped count.
- **Laws on the drawing.** `RingKeepsLastN` (a ring holds the last N values pushed, oldest first), `DownsampleKeepsMinMax` (a chart's columns keep the samples' least and greatest), `EasingEndsAtTarget` (an ease starts at its origin, ends at its target and moves monotonically between them), `HeatCountsEverySample`.
- **Contracts and providers.** `feed.Sensors` (laws `ReadIsRepeatable` and `CountIsRepeatable`): `synth.Bank` hashes the seed, the source, the frame and the sample's place, with silent stretches and bursts past a queue's capacity. `cmd/dash-gui/main.ergo` alone chooses it.

- **One view for every host.** `screen.View` is a `func(f *ui.Frame[Signal], d Dash)`: a keyed `ui.Each` of the panels, each with its line, a pause button and a policy button (`ui.Pressable` with a `Signal` value), and a `ui.Canvas` whose paint list draws the panel: a chart's columns as one stroke, a subpath a column from its least to its greatest sample, read from the ring in place (`panel.RingColumns`, law `RingColumnsReadInOrder`), a gauge as a bar to its eased value, a heat strip as bins whose alpha is their share. `screen.App` is the machine an actor runs under it; its step runs `feed.Frame`, the board's frame with no tasks (a step starts none), which offers each source's samples to a queue of its own under its policy exactly as the source's task does in `play`.

## Layout

- `panel/`: a panel. `ring.ergo`, `draw.ergo` (downsampling, easing, heat bins), `feed.ergo` (the machine), `frame.ergo` (a panel's line).
- `feed/`: the plumbing. `source.ergo` (the `Sensors` contract, the drop policy, a source's task), `board.ergo` (the panels, the pipes and a frame's two phases).
- `synth/`: the `Sensors` provider.
- `screen/`: the app machine (`app.ergo`) and the view (`view.ergo`).
- `script/`: the headless driver (`LineParsesBack`), the seeded script generator, and the scene on `ui.TestHost` (`scene.ergo`).
- `cmd/dash-gui/main.ergo`: binds `synth.Bank` for the seed.

## The Go control

`cmd/ergo/testdata/dash-gui/control` is the same dashboard in hand-written Go: a goroutine and buffered channels per source, rings over fixed buffers. `TestDashGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded sensor banks and scripts and checks that each prints what a plain one-loop reference model in the test prints; every fifth seed also plays a scene on the view and holds its text to the same reference. The scene case's `shot` holds the paint lists. The receipts per frame (`scripts/opcount`) wait for unit C0.
