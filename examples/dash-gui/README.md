# dash-gui

An animated dashboard: twelve panels (four line charts over rings of 10,000 samples, three spark lines, two gauges that ease toward each new value, two heat strips and an event log), each fed by a seeded sensor source that runs as its own task and hands its samples over through a bounded queue, on a virtual clock of 120 frames a second. This is the first stage of the program (corpus wave GP4a): the model and a headless driver. The panels on `ui.TestHost` (stage b) and the desktop window with `ui.Tick` (stage c) come when the `ui` host units land.

```sh
dash-gui gen [-seed S] [-n N]   # write a script of N steps
dash-gui play [-seed S]         # play the script on standard input over the sensors of seed S
```

A script is one command a line: `run N` advances the clock N frames, `pause P` and `resume P` pause and resume panel P, `policy P oldest|newest` sets what source P's full queue drops, `show` writes every panel's line (its mode, the samples it took, the frame of the last one, and what it draws), and `drops` writes how many samples each queue dropped. While the clock runs, a panel whose source falls silent for more than half a second writes `F: P stale`, and `F: P live` when samples come back. A line that is no command writes `? "LINE": want ...`, and a pause or resume a panel cannot take writes `? "LINE": refused`.

## What it shows

- **A machine per panel.** `panel.Feed` is `machine[State, Event]`, `State = Live | Paused | Stale`. The step matches the mode and the event exhaustively: samples in `Live` stay live, a tick more than `StaleAfter` frames after the last sample emits `WentStale`, samples in `Stale` emit `CameBack`, a paused panel ignores samples and the clock, and a pause of a paused panel or a resume of a running one is `Reject`. Laws: `GoesStale`, `PauseAndBack` (a path) and `StaleAfterSilence` (a live panel is live at every frame up to `StaleAfter` after its last sample and stale at the next).
- **Tasks, queues and a structured stop.** `feed.Start` runs a task for each source in one `tasks.Set`. Each frame the board gives every source an order, waits for every report, then drains the queues in panel order, so what a queue drops never depends on the scheduler and a seed replays the run exactly. When the script ends, `feed.Stop` ends the order queues and the set's `Wait` is the stop.
- **A drop policy as a union.** `Policy = DropOldest | DropNewest`; `Offer` matches it when a queue is full. Laws `DropOldestKeepsTheLast` and `DropNewestKeepsTheFirst` hold it to what each policy promises, with the dropped count.
- **Laws on the drawing.** `RingKeepsLastN` (a ring holds the last N values pushed, oldest first), `DownsampleKeepsMinMax` (a chart's columns keep the samples' least and greatest), `EasingEndsAtTarget` (an ease starts at its origin, ends at its target and moves monotonically between them), `HeatCountsEverySample`.
- **Contracts and providers.** `feed.Sensors` (laws `ReadIsRepeatable` and `CountIsRepeatable`): `synth.Bank` hashes the seed, the source, the frame and the sample's place, with silent stretches and bursts past a queue's capacity. `cmd/dash-gui/main.ergo` alone chooses it.

## Layout

- `panel/`: a panel. `ring.ergo`, `draw.ergo` (downsampling, easing, heat bins), `feed.ergo` (the machine), `frame.ergo` (a panel's line).
- `feed/`: the plumbing. `source.ergo` (the `Sensors` contract, the drop policy, a source's task), `board.ergo` (the panels, the pipes and a frame's two phases).
- `synth/`: the `Sensors` provider.
- `script/`: the headless driver (`LineParsesBack`) and the seeded script generator.
- `cmd/dash-gui/main.ergo`: binds `synth.Bank` for the seed.

## The Go control

`cmd/ergo/testdata/dash-gui/control` is the same dashboard in hand-written Go: a goroutine and buffered channels per source, rings over fixed buffers. `TestDashGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded sensor banks and scripts and checks that each prints what a plain one-loop reference model in the test prints. The receipts per frame (`scripts/opcount`) wait for unit C0.
