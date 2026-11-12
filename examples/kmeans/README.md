# kmeans

k-means clustering in Ergo: Lloyd's algorithm with k-means++ seeding on points of eight integer coordinates, the assignment step in parallel chunks whose sums merge by an operation with laws. It is a high-performance program of the corpus wave (HP1); its hand Go control is the bar.

```sh
kmeans run FILE K ROUNDS WORKERS SEED   # cluster FILE's points into K
kmeans gen N SEED                       # N seeded points, one a line
```

A point is a line of eight decimal coordinates, 0 to 4095, separated by single spaces. `run` prints each round's inertia (the sum of each point's squared distance to its centroid), how the run ended (`converged in N rounds` when no centroid moved, or `stopped after N rounds`), and the centroids.

## What it shows

- **Laws justify the parallel step.** `cluster.Sums` merge by `Merge`, whose laws (`MergeAssociative`, `MergeCommutative`, `EmptyIsIdentity`, `AddIsMergeOfOne`) say the chunks may be merged in any grouping and order, and `SplitDoesNotMatter` says the Sums are the same for every number of workers.
- **Integers make the laws exact.** Coordinates, sums and means are integers. A mean rounds to the nearest integer point, which is at least as near to the points as the old centroid (an integer point too), so `InertiaNeverRises` holds exactly. The test `FloatSumsDependOnTheGrouping` shows the case that refutes `MergeAssociative` stated over `float64` sums: with floats, the centroids would depend on the split.
- **The run is a machine.** `Iteration` ends a run when a round moves no centroid or at the last round; `StopsAtMax` and `StillEndsTheRun` are its laws.
- **Typed failures.** A line that is not a point is `Malformed{Line, Reason}`, and `Reason` is a closed union that `Explain` matches exhaustively.
- **No copy per distance.** `Nearest` reads the centroids from one flat slice (`Flat`), eight coordinates a window, so the inner loop calls nothing and copies no centroid; the law `NearestIsTheClosest` ties it to `Dist`.

## Layout

- `cluster/`: the domain. `point.ergo` (points, `Dist`, `Nearest`), `sums.ergo` (Sums, Merge and their laws), `assign.ergo` (the parallel step and the update), `seed.ergo` (SplitMix64 and k-means++), `lloyd.ergo` (the machine, `Run`, `InertiaNeverRises`), `source.ergo` (the `Source` contract and the text of points).
- `corpus/`: the seeded points: 48 true centres, each coordinate a centre's plus a triangular noise.
- `mapped/`: `Source` over the host's files, mapped read-only. `memory/`: `Source` over texts in memory.
- `cmd/kmeans/main.ergo`: binds `mapped` over `fs.NewHost()` and the host's standard output.

## The Go control

`cmd/ergo/testdata/kmeans/control` is the same seeding, rounds and report in hand Go (the input mapped and parsed in place, the centroids flat, one goroutine per chunk). `TestKmeansAgreesWithControl` (cmd/ergo, full tier) checks that both print what a plain reference prints, on seeded inputs at 1 to 9 workers.

Gaps: no read-only borrow, so a `Point` passed to a function is a 32-byte copy (hence `Flat`); no `tasks.Reduce` justified by `Merge`'s laws (the parts are merged in order after `tasks.Transform`); no `simd`.
