# fluid

An Ergo module of this repository (`modules/`, MK0): a port of the Fluid Functionalism UI library to Ergo. Each component is one model (a machine where it has state, with its laws), one look and one view. The view renders on the std `ui` hosts (the test host and the MyGo desktop window) and as server-rendered HTML through std `html` and module `web`.

The plan is the run's FLUID-UI spec. `origin/README.md` names the original, the reference port and the pins. `NOTICE` holds the original's licence and the credits.

## Packages

| Package | What | Unit |
| --- | --- | --- |
| `fluid` (this folder) | `Component` (the 34 components) and `Foundation` (the 5 foundation pages), each with its docs slug; laws `SlugParses`, `FoundationSlugParses`, `ListedOnce` | FL0 |
| `motion` | The one timing model: `Tier`, `Spring`, `Entrance`, `Exit`, `Curve` (`Sprung`, `Ease`, `Linear`), `Clip`, `At`, `Ended`, `Ticks`, `Progress`, `CSS`; laws `ExitIsQuicker`, `EndsAtTarget`, `ReducedIsStill`, `NoTickWhenSettled`, `SheetCurveMatchesSpring`, `SettlesByItsDuration`, `SpringSolvesItsEquation` (a fourth-order Runge-Kutta reference) | FL3 |

The packages the spec plans are added by their units: `token` and `look` (FL1), `part`, `a11y` and `web` (FL2), `gui` (FL4), `roving`, `overlay`, `hover`, `scroll` and `icon` (FL5), and one package per component (C1 to C22).

## Rules

- No text of the reference port is copied: it has no licence of its own. Values (colours, sizes, radii, shadows, spring timings) come from the original's MIT design.
- Untitled UI icons, PDF thumbnails, the eyedropper and people's photos are not ported.
- No package of this module names a Go package or uses `extern`; nothing in std or the compiler names this module.
