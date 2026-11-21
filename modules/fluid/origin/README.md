# The original: Fluid Functionalism

This module is a new work. It ports the design and the behaviour of the library below, and it copies no text from either source.

## Sources and pins (read 2026-11-17)

| What | Where | Pin | Licence |
| --- | --- | --- | --- |
| The original: a shadcn/ui registry of React components (Radix and Base UI flavours, Tailwind v4, the `motion` library) | `https://github.com/mickadesign/fluid-functionalism` | `f8014098` | MIT, Copyright (c) 2026 Micka Touillaud (in `../NOTICE`) |
| The reference for behaviour and structure: the Foldkit port by Tim Hanlon | `https://github.com/timhanlon/fluid-functionalism-foldkit` | `a8fdcc03` (it tracks the original at `7e6ae29`) | none of its own: no `LICENSE` file, `"private": true`; it ships the original's licence as `LICENSE.original` |
| The Foldkit port's registry, as served | `https://fff.timhanlon.com/r/registry.json` | fetched 2026-11-17: 51 items, 221 files, 40,347 lines; `foldkit@0.170.0`, `@foldkit/ui@0.170.0`, `effect@4.1.3` | as above |
| The pixel target: the Foldkit port's live docs | `https://fff.timhanlon.com/docs/<page>` | as served 2026-11-17 | as above |
| Headless behaviour checklists | vendored `conformance/upstream/foldkit-d21db423/packages/ui` (`@foldkit/ui` 0.167.0); WAI-ARIA APG | — | MIT (Foldkit) |

Because the Foldkit port has no licence of its own, it is read and not vendored, and no CSS, TypeScript or comment of it enters this repository. Values (colours, sizes, radii, shadows, spring timings, icon path data of the ISC and MIT sets) come from the original's MIT design.

## Not ported

- Untitled UI icons, the original's default set: their licence forbids use in a UI kit or library. The port's default set is Lucide (ISC); Tabler, Phosphor and Hugeicons (MIT) are the others.
- PDF thumbnails (a PDF renderer is out of scope), the eyedropper (a browser-only API), the docs site's own chrome, prose and settings panel, the registry's starter, and people's photos in the demos.

## Fonts

The live docs render with one font face: Inter, the variable face over weights 100 to 900 (`document.fonts`, measured 2026-11-17 in agent-browser's Chromium 152). Geist Serif is offered by the docs' settings and is not used by default. Inter is under the SIL Open Font License 1.1.

## The pixel target, and how it is read

The run's harness (`fluid-ui/pixel.ts`, not in this repository) loads each docs page of the 34 components and the 5 foundation pages in headless Chromium at 390x844 and 1440x900, light and dark, with `prefers-reduced-motion: reduce` and the docs' icon preference set to Lucide. It names each page's demo stages `s0`, `s1`, ... in document order and compares each stage's crop at colour threshold 0. Forced rows add hover, focus, press and open states, reached the same way on both sides. The gallery (`examples/fluid-gallery`, unit FL6) is to serve the same paths and mark its stages with `data-ff-specimen`.

What the harness learned about the live pages (each is handled in the harness, and each is a fact about the original a port need not copy):

- The first load of a page in a browser session renders differently from later loads (a stage measured while the font loads is taller; a corner pixel differs). The harness loads each page once before it reads it.
- Demos run script after the page loads: highlights mount, and one demo streams its steps for 18 s after it scrolls into view. The harness waits until the DOM has not changed for 1 s, finishes finite animations, and pauses repeating ones (shimmers, spinners) at 0.
- An element screenshot of a stage below the fold paints only the part inside the viewport. The harness scrolls each stage into view and copies its rows from viewport screenshots.
- Fixed and sticky docs chrome (the menu button at 390 px, the side bars at 1440 px) is hidden, so that it does not cover a stage.
- With Chromium's threaded raster, a translucent rounded border rasterised two ways across loads (one level of grey at a stage's corner). The harness runs Chromium with raster on one thread.

## Probes of the pinned Chromium (agent-browser 0.39.1, Chromium 152.0.7977.64; `FL0/probes/` in the run dir)

1. **Anchored hover glide.** CSS anchor positioning is supported, and a highlight placed with `position-anchor` and `anchor()` transitions between the hovered items with no script: 120 ms into a 400 ms move from the first item (top 40) to the fourth (top 136) it was at 84. So the web host's fluid hover is L1 for items the pointer is inside; the nearest item in a gap still needs code (3b), as the spec says.
2. **`inert` modality.** With the rest of the page `inert` and the dialog in the page: Tab visits only the dialog's controls, then leaves to the browser and comes back to the dialog (`first, close, BODY, first, close, BODY`); a pointer click on an inert button does nothing; the accessibility tree leaves the inert part out. So a server-rendered open dialog is modal with no script. Focus does reach the browser's own UI between cycles, as a native modal `dialog` allows too; Escape still needs the applier (3a).
3. **`linear()` against a spring.** Chromium's `linear()` returns each stop exactly (error 0 at every stop). Between stops it interpolates in straight lines, so the error against a spring's closed form depends on the stop count. With evenly spaced stops printed to 4 decimals, the largest error over 401 samples was: critically damped 0.0026 at 60 stops, 0.0015 at 80, 0.0010 at 120; under-damped (bounce 0.12) 0.0039 at 60, 0.0022 at 80, 0.0010 at 120. So `SheetCurveMatchesSpring`'s 0.002 needs about 100 evenly spaced stops, or fewer stops placed where the curve bends (FL3 decides; the law samples between stops, not only at them).
