# fk-flow

One program with two scenes on a canvas:

- **balls**: a press drops a ball with a random size, speed, heading and colour, and the balls bounce off the edges.
- **flow**: about 55 particles drift on a field of noise that moves with time, swirl round the pointer and leave trails, and a press bursts 22 more outward.

It is a port of Foldkit's `canvas-art` and `generative-art` examples (`conformance/upstream/foldkit-d21db423/examples/`, MIT). This is stage a of FK12: the model, with no view yet.

The program plays a script of frames and acts. After each, it shows the scene: every ball, or the flow's time, controls and particles.

Tag: `default`.

```sh
fk-flow < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `seed`, `frame`, `press`, `move`, `toggle`, `clear`, `strength`, `scale` and `show`.

## The shape

- Package `scene` is the machine `Machine`. Its state is `Scene = Balls{Field, Running} | Flow{Field}`, and its input is the stream's start.
  - A frame (`Ticked`) steps the shown scene with `Stay` while it runs. A paused scene `Ignore`s it, because upstream's frame subscription is off while paused.
  - An act the shown scene has no use for is `Ignore`d: a pointer move or a slider over the balls, or a switch to the scene already shown.
  - A switch `Enter`s the other scene afresh, drawing on from the same stream.
- Package `balls` is canvas-art's model. `Drop`, `Advance` and `Clear` are pure functions of the field.
- Package `flow` is generative-art's model: `Burst`, `Advance`, `PointAt`, `Reset`, `Toggle` and the two sliders. `noise.ergo` holds the Perlin noise and its fractal sum.
- Package `seed` is a seeded stream (SplitMix64) kept as a value in each field. Every number a ball or particle is born with is drawn from it, where upstream's commands draw from `Random`. A step is therefore pure, and the same seed and acts replay the same scene.
- Package `script` is the host. The scenes have no timer and no work, so the host steps them with `machines.Next`. A frame is a script line, not a clock.

How the port differs from upstream:

- **No commands.** Upstream's `GenerateBall`, `GenerateAmbientParticle` and `GenerateBurstParticle` are commands that draw from `Random` and answer with a `Completed…` message. Here the draw happens in the step, from the field's own stream, so a press adds the ball at once. The plan names `random` seeded: std `rand` is a context service and a machine step is pure, so the stream lives in the state.
- **One program, two scenes.** Upstream has two apps. Here `show balls` and `show flow` switch scenes, and the stream passes on through the switch.
- **Sliders are values.** Upstream's sliders are `@foldkit/ui` Slider children. Here `strength` and `scale` set the value, kept in the slider's range and on its step. The sliders themselves come in FK12b.
- **The noise hash wraps in 32 bits.** Upstream multiplies in doubles and truncates. The fields differ in detail but not in kind.
- **Floats.** The goldens show positions rounded to whole pixels, so the last bits that a fused multiply-add may change on another host (plan Q5) rarely show. Whether they show on the Mac check is recorded as a gap, not hidden.

## Two originals

`origin/source` names one folder, `generative-art`, because the layout check reads one. `origin/tests.txt` covers generative-art's tests, and `origin/canvas-art-tests.txt` covers canvas-art's in the same form. The layout check does not read the second file.

## Upstream tests

Upstream has 12 story tests and 8 scene tests. Each has a line in `origin/tests.txt` or `origin/canvas-art-tests.txt`. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests need a view and are stage b.

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

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`:
  - the balls: presses, frames, a bounce, pause and play, and clear;
  - the flow: the first frames, a pointer, a burst, both sliders clamped and snapped, six seconds of deaths and births, a long frame capped at 50 ms, and reset;
  - a switch back to the balls.
- `badscript`: a repeat of zero frames. The program says which line and exits 1.
