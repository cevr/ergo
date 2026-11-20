# canvas-scenes

One program with two scenes on a canvas:

- **balls**: a press drops a ball with a random size, speed, heading and colour, and the balls bounce off the edges.
- **flow**: about 55 particles drift on a field of noise that moves with time, swirl round the pointer and leave trails, and a press bursts 22 more outward.

This is stage b of FK12: the view, on std ui's test host.

The program plays a script of frames and the user's acts on the screen. After each, it shows what changed on the screen: the heading and prompt, the buttons and the count, the sliders, whether the screen asks for frames, and the canvas, read from its paint.

Tag: `default`.

```sh
canvas-scenes < cases/tour/stdin
canvas-scenes -stats < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `seed`, `frame`, `press`, `move`, `click`, `strength` and `scale`. With `-stats`, each line also shows its work (`ui.Counts`) and the frames the host sent. With `-last` it prints only the last screen. `canvas-scenes -gen N` prints the receipt's script: the flow filled to about 600 trails, then N frames.

## The shape

- Package `scene` is the machine `Machine`. Its state is `Scene = Balls{Field, Running} | Flow{Field}`, and its input is the stream's start.
  - A frame (`Ticked`) steps the shown scene with `Stay` while it runs. A paused scene `Ignore`s it: no frame arrives while paused.
  - An act the shown scene has no use for is `Ignore`d: a pointer move or a slider over the balls, or a switch to the scene already shown.
  - A switch `Enter`s the other scene afresh, drawing on from the same stream.
- Package `balls` is canvas-art's model. `Drop`, `Advance` and `Clear` are pure functions of the field.
- Package `flow` is generative-art's model: `Burst`, `Advance`, `PointAt`, `Reset`, `Toggle` and the two sliders. `noise.ergo` holds the Perlin noise and its fractal sum.
- Package `seed` is a seeded stream (SplitMix64) kept as a value in each field. Every number a ball or particle is born with is drawn from it. A step is therefore pure, and the same seed and acts replay the same scene.
- Package `app` is the program, the machine `Shown`: the scene and the time of the last frame. The screen's frames (`ui.Animate`) carry the time since the host started. The scene steps by the time since the frame before, so `app` keeps the last frame's time and steps the scene by the difference. The first frame after the frames begin (the start, or play after a pause) starts the count and moves nothing.
- Package `screen` is the view. Each scene has its heading and prompt, its canvas in an Area that takes the pointer, Pause or Play, Clear or Reset, and the count; the flow also has its two sliders. The canvas paints the scene as a shape list: a circle is four cubics, a trail a polyline stroked twice, and a colour is CSS's `hsla` worked out to bytes.
- Package `script` is the host. It runs `app.Shown` as an actor on `ui.NewTestHost`. A `frame MS xK` line sets the host's frame period to MS and moves the clock by K periods. The host sends a frame each period while the screen asks for them.

## The control and the receipt

canvas-scenes makes the one perf claim of these ports (plan §18.3 FK12): per frame at 600 paths, its allocations and instructions against a hand Go control.

- `testdata/control` is that control: the same model, paint and script host in Go, with no ui package. It prints what `canvas-scenes` prints.
- The workload (the run's `receipts/canvas-scenes.workload`, until the examples layout takes a `workload` file with scripts/opcount) has `scripts/opcount` play `canvas-scenes -gen N` with `-last` at two sizes, so the cost of a frame of about 600 trails is the slope between them, on both sides.
- `hosts/mygo/controls/canvas-scenes` is the same model (copied) on MyGo's widgets, its canvas painted with MyGo's `Painter`. `TestCanvasScenesControlAgrees` (hosts/mygo) holds it to `canvas-scenes` on seeded scripts, and holds the two copies of the model to one file.

## Laws

- `balls`:
  - `BallsStayInBounds`: after any frames, balls dropped anywhere are a radius inside the canvas, and a bounce keeps the size of each part of a ball's velocity.
  - `DropsDrawFromTheStream`: a ball lands where the canvas was pressed, with its size and speed in range. The next drop, or another seed, draws other numbers.
- `flow`:
  - `AFrameKeepsTheCrowd`: a frame keeps the living particles in order, inside the band, with trails of at most 36 points. It then adds up to 3, numbered on, while fewer than 55 live.
  - `ABurstFansOut`: a burst adds 22 particles near the press, heading out evenly round the circle.
  - `SlidersKeepTheirSteps`: a slider's value is in its range, on its step, and the nearest step to the one asked.
- `scene`:
  - `SeedReplays`: the same seed and acts give the same scene, and another seed gives another, through a switch.
  - `PausedTickIsIdentity`: a frame of a paused scene is ignored, and a frame of a running scene is a step in the same scene.
  - `ActsReachTheShownScene`: an act the shown scene has no use for is ignored, and a switch starts the other scene afresh.
- `app`:
  - `FramesAreTheTimeBetween`: over drawn frame times, a running scene is stepped by the time between frames, and the first frame moves nothing.
  - `APauseStopsTheCount`: a paused scene ignores frames, and the first frame after play moves nothing, however long the pause was.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`:
  - the balls: presses, frames, a bounce, pause and play, and clear;
  - the flow: the first frames, a pointer, a burst, both sliders clamped and snapped, six seconds of deaths and births, a long frame capped at 50 ms, and reset;
  - a switch back to the balls.
- `scenes`: the 8 scene tests, each a comment and the acts that show it, and a click on a button the screen does not show.
- `badscript`: a repeat of zero frames. The program says which line and exits 1.
