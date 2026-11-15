# Compile speed receipts

## Package split

Compile-speed unit CS4: a program of several Ergo packages lowers to one Go package per Ergo package, plus one shared package below them (`internal/sketch/lower/pkgsplit.go`). `ERGO_LOWER=single` selects the reference, the one-package module. Measured on 2026-11-13 at the CS4 head, on the 16-core workbox, with a 1-minute load of 38 to 60. The reference is the base's lowering: `lower.Lower` is unchanged at the CS4 head, so the reference columns are base against head. The load is high, so the wall times are not a speed verdict. The counts of compile actions do not depend on the load.

### What the go command compiles (measured, `go build -x`)

The `calc` example has 7 Ergo packages. Each mode has its own fresh `GOCACHE`, warmed by a one-line program (so the Go standard library and the Ergo runtime are compiled). Each row is one `ergo build ./cmd/calc` with `GOFLAGS=-p=4`.

| Step | Reference: packages compiled | Split: packages compiled | Reference wall / user / sys (s) | Split wall / user / sys (s) |
| --- | --- | --- | --- | --- |
| First build | `main` (the whole program) | shared, ast, eval, syntax, typing, opt, repl, main | 3.72 / 1.93 / 0.42 | 3.13 / 1.78 / 0.61 |
| Edit a string literal in `ast` (the lowest package) | `main` | ast and every package that imports it: eval, typing, syntax, opt, repl, main | 2.60 / 1.81 / 0.53 | 2.82 / 1.88 / 0.54 |
| Edit an integer literal in `opt` | `main` | opt, main | 2.80 / 1.86 / 0.47 | 2.15 / 1.31 / 0.42 |

- Measured: the go command compiles an edited package and every package that imports it, and no other package. It keys each import by the content ID of the import's whole archive (`cmd/go/internal/work/exec.go`, `buildActionID`: `import <path> <contentID>`), not by its export data. So a body edit that keeps the export data still compiles the importers again. `TestBodyEditCompilesOnePackage` checks this rule: after a body edit of the lowest package `l0`, the compiled packages are exactly `l0`, `mid` and the root. It does not check plan §3.2's "1 Go compile". One compile per body edit needs a driver that keys a package on its imports' export data (`compile -linkobj`), which is the direct path (BUILD-direct, CS10). This base does not have it.
- Measured: `TestColdCompilesInParallel` blocks each leaf compile in a `-toolexec` wrapper until all three leaves are in flight. The go command then has the three leaves in flight at the same time.
- Measured, not controlled: `go run ./scripts/examples` with all 239 cases passed under both lowerings. The split run took 7 min 4 s, and the reference run took 13 min 36 s, each with an empty emission cache and the same warm Go cache, at a load of 40 to 60. Load differences can explain this gap. It is not a claim.

### Run time (measured, `calc -fold` on 200,000 lines)

The same input goes to both binaries in 5 alternating runs. The outputs are byte-equal. CPU seconds are user / sys:

| Run | Reference | Split |
| --- | --- | --- |
| 1 | 2.03 / 0.70 | 1.78 / 0.68 |
| 2 | 1.95 / 0.60 | 1.74 / 0.65 |
| 3 | 1.76 / 0.72 | 1.71 / 0.59 |
| 4 | 1.75 / 0.54 | 1.96 / 0.67 |
| 5 | 1.99 / 0.66 | 1.78 / 0.60 |

The mean user time is 1.90 s for the reference and 1.79 s for the split. The runs show no loss at this load (measured). The binaries are 6,025,020 and 6,033,435 bytes.

- The corpus (measured): the checker's corpus has 4,980 candidates, and 768 of them check clean and lower. `Split` left all 768 Go texts byte-equal (0 changed), so their builds and runs at head are the base's. The check was a scratch test at the CS4 head over `corpusDomain`, not committed. The conformance programs are also one Ergo package each, and the same rule covers them (inferred from `splitMain`: with one user package, no group leaves the root). They were not run separately.
- Cross-package inlining: the Ergo inliner still sees the whole program, so the split does not change which bodies Ergo inlines. Go's inliner inlines across packages through export data. A function that Go inlined inside one package and does not inline across packages would show here as a loss; none showed.
