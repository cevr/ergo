# raytrace

A path tracer in Ergo: spheres and triangles in a bounding-volume tree, four materials as a union, a seeded stream per pixel, the image rendered in parallel tiles and written as a binary PPM. It is a high-performance program of the corpus wave (HP3); its hand Go control is the bar.

```sh
raytrace W H SPP SEED OBJECTS WORKERS TILE > image.ppm
```

`W`×`H` pixels, `SPP` samples a pixel, the scene of `SEED` with `OBJECTS` objects on a ground under a light, tiles of `TILE`×`TILE` pixels rendered by `WORKERS` tasks at once. The image is the same for every `WORKERS` and `TILE`.

## What it shows

- **Unions and exhaustive `match` in the hottest loop.** `geom.Shape` is `Sphere | Triangle`, `geom.Node` is `Leaf | Branch`, `render.Material` is `Diffuse | Metal | Glass | Light`, and the tree's answer is an `Option[Hit]`; every `match` names every case.
- **A law ties the fast path to the plain one.** `TreeHitEqualsBruteForce` says the tree walk finds the same shape at the same distance as testing every shape in order, on generated shapes (repeated ones included) and rays.
- **The path is a machine.** `render.Bounces` steps a path on each bounce's `Event`: the sky, a light or an absorbing surface ends it, a scattering surface goes on up to `MaxBounces`. `Trace` drives the same `PathStep`, and `EndsByMaxBounces` and `LightEndsThePath` are its laws.
- **Seeds make the parallel step exact.** Each pixel draws from its own SplitMix64 stream, so `TileSplitDoesNotMatter` holds byte for byte for every tile size and number of workers. `NoNaNPixel` says no sample's light is NaN, infinite or negative.

## Layout

- `geom/`: vectors and rays (`vec.ergo`), the shapes, where a ray meets them and their boxes (`shape.ergo`), the tree (`tree.ergo`) and its law (`laws.ergo`).
- `render/`: the stream (`rng.ergo`), materials, the path machine and `Trace` (`path.ergo`), the camera, pixels and tiles (`image.ergo`).
- `world/`: the seeded scenes, and the image's laws.
- `cmd/raytrace/main.ergo`: parses the job and writes the PPM to the host's standard output.

## The Go control

`cmd/ergo/testdata/raytrace/control` is the same scene, tree, materials and sampling in hand Go (shapes and materials as tagged structs, no interfaces; a goroutine per worker taking tiles from a counter), every float operation in the Ergo program's order. `TestRaytraceAgreesWithControl` (cmd/ergo, full tier) checks that both write the same bytes on seeded jobs. The bytes are the same on every host: Ergo rounds every float operation, so Go fuses no multiply and add on arm64 (types.md, Operators 5), and the control writes each product as `float64(a*b)` for the same reason.

Gaps: small records (`Vec`, `Ray`) and the `Scene` are passed by value through every call; the path's state, a machine's, is a Go interface.
