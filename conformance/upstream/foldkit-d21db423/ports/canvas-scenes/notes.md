# canvas-scenes and its original

What `examples/canvas-scenes` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `canvas-art` and `generative-art` examples (`conformance/upstream/foldkit-d21db423/examples/`, MIT).
How the port differs from upstream:

- **No commands.** Upstream's `GenerateBall`, `GenerateAmbientParticle` and `GenerateBurstParticle` are commands that draw from `Random` and answer with a `Completed…` message. Here the draw happens in the step, from the field's own stream, so a press adds the ball at once. The plan names `random` seeded: std `rand` is a context service and a machine step is pure, so the stream lives in the state.
- **One program, two scenes.** Upstream has two apps. Here the Balls and Flow buttons switch scenes, and the stream passes on through the switch.
- **The first frame moves nothing.** Upstream's first frame moves the scene by the few milliseconds since its subscription began. Here it only starts the count (`app` law `FramesAreTheTimeBetween`).
- **Sliders take a press.** Upstream's sliders are `@foldkit/ui` Slider children, dragged or keyed. Here each slider is an Area named by its label that shows its value; a press at x on its 200-pixel track asks for the value at x, kept in range and on its step. An Area cannot tell a drag from a hover, so a move sets nothing (gap FK12-g1).
- **The canvas is read from its paint.** The script shows the canvas's fills, strokes and path points, and each filled circle under 30 pixels (a ball, or a particle's head) by its centre, radius and colour, the first six of them, and the pointer's ring.
- **The noise hash wraps in 32 bits.** Upstream multiplies in doubles and truncates. The fields differ in detail but not in kind.
- **Floats.** The goldens show positions rounded to whole pixels, so the last bits that a fused multiply-add may change on another host (plan Q5) rarely show. Whether they show on the Mac check is recorded as a gap, not hidden.

## Two originals

`origin/source` names both upstream folders, one a line. `origin/tests.txt` has a line for each test of both, and each FILE starts with its folder's name (`canvas-art/src/story.test.ts`).

## Upstream tests

Upstream has 12 story tests and 8 scene tests. Each has a line in `origin/tests.txt`. No story or scene is an Ergo test, because a law or a case already kills every mutant that a test of it would. The scenes are case `scenes`, on the test host, one comment each.
