# Ports of Foldkit examples

This guide is for the ports of Foldkit's examples under `examples/` (the examples' layout: [examples/README.md](../../../../examples/README.md)). `scripts/examples` checks each folder here (`checkPorts`).

Programs of `examples/` port Foldkit's examples (this folder's parent, MIT) and `foldkit/foldkit-domains`. A port keeps the upstream behaviour and tests, not Foldkit's shapes: there is no Message union with completion variants, no Command or Subscription records, and no blocks.

**The provenance folder.** A port `examples/<name>` has its provenance here, in `ports/<name>/`, never in the example. Its original runs in a browser, so it has no record leg. The folder holds:

- `README.md`: the upstream path, the commit and the license.
- `source`: a line per vendored upstream folder, its path from the repository root (for example `conformance/upstream/foldkit-d21db423/examples/stopwatch`). A port of two examples names both (`canvas-scenes`).
- `tests.txt`: one line per upstream story or scene test, `FILE: TITLE -> RESTATEMENT`. FILE is the test file's path under its folder (each `*story.test.ts` and `*scene.test.ts` in it); with more than one folder, it starts with the folder's last element and a slash (`canvas-art/src/story.test.ts`), and those last elements differ. TITLE is the test's title as written, with its spaces collapsed; an `.each` table is one test. RESTATEMENT names the Ergo test, law or script that restates the test, or says `not restated: REASON`. Lines that start with `#` are comments.
- Anything else the port keeps of its original, such as measurements or recorded server answers.

The layout check reads the titles from the vendored files (`test(`, `it(`, `it.effect(` and their `.each`, `.only` and `.skip` forms), so nothing is pinned. A title that `tests.txt` lacks, a title it has more often than upstream does, a line for a test that upstream does not have, and a line with no restatement are each a layout error.

**How a Foldkit program maps** (corpus plan §18.1):

| Foldkit | Ergo |
| --- | --- |
| `Model` and `Message` as Schema values and types | `type` records and unions, declared once. A status field becomes a union of modes |
| `update` returning `{ model, commands }` | the machine's step returning a `Decision` |
| `Command.define` | work that a state owns (`machines.Work`): the state is the command's data and the end of the entry cancels it. Or an `Emit` of a command union, run by the actor's sink |
| `Subscription` | the state's timer (`after`), a Listen work, `actor.Feed`, or `ui.Tick` |
| `ManagedResource` | a resource owned by the entry of the state that needs it: a Listen work acquires it, and the end of the entry releases it |
| Submodel, `Got*Message`, OutMessage | a pure child whose step the parent calls, or `actor.Child`; the output arrives through `Emit`/`Done` and `toParent` |
| `view`, `h.keyed`, a load the view asks for | a view with `needs ui.Slot[Signal]`, `ui.Each` with a typed key, `ui.Load` and `ui.Async` |
| Routing | a route union with parse and print (a round-trip law), and `ui.Location` |
| Story test | an Ergo `test` over the step, `machines.Run` or `RunTimed`, or a live actor on a `time.Manual`. `Command.resolve` is "feed the result signal" or "move the clock" |
| Scene test | a `ui.Script` run on `ui.TestHost` |

Three limits apply, and each port's ledger records where it meets them. L1: an `Emit` opens a new entry. L2: a state has one work. L3: children live in one entry. A port that finds a form around a limit says so in its README.

**Lifetimes, per kind of work:**

| Owner | What it owns, and when it ends |
| --- | --- |
| The program | the providers of `main`'s `with context`, released when `main` returns |
| One entry of a machine state | its work, its timer, its children and the resources its work holds, all ended when the entry ends (an `Enter`, `Done`, or the actor's stop) |
| One `ui.Load` epoch | its fetch, cancelled when a pass no longer writes the load, with a late answer dropped |
| One mounted view instance | its `ui.State`, dropped when the instance leaves the tree |
