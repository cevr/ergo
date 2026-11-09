# stack-builder

A stack builder: a hub browses a library by pages, searches it, and opens a part editor as a child actor. A composite's editor opens a nested editor of the same machine for each of its slots, so editors nest as deep as the library does (a twin of two nodes, each a compute and a storage part, is three deep). Each nested editor's unit goes into its parent's unit. When an editor ends, the hub goes back to the page or the search it was opened from. The plan goes to a cluster at deploy. A script on standard input plays the operator.

Tag: `default`. It is an original program on the shape of survey E7 (recursive child editors with history). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module stackbuilder`.
- `stack/`: the domain.
  - `editor.ergo`: the `Editor` machine (`Choosing | Picking | Nested`); its `Nested` state's work starts a child `Editor` for the chosen part.
  - `hub.ergo`: the `Builder` machine. Its state is the record `Hub{Library, Units, Screen}`, whose `Screen` (`Browsing | Searching | Editing`) is a region of the state; `Editing`'s work starts the part's `Editor`.
  - `cluster.ergo`: the contract `Cluster` with its law `PlanCountsEveryUnit`.
  - `laws.ergo`: the hub's and the editor's rules as laws.
- `parts/`: the parts library, as data.
- `memory/`: `Cluster`, an in-memory provider of `stack.Cluster`, and a property that runs its law.
- `screen/`: the line protocol. It runs the hub with `actor.StartMachine`, finds the open editors through the hosts kept in the states (`host.Get()`), and asks the innermost one. An entry is shown only after its work filled its host, so the screen waits for no editor to start.
- `cmd/stack-builder/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Recursive children (MU5).** `Editor`'s work starts an `Editor`: one machine is a child of the hub and of itself. Each child belongs to the entry that started it, so when a hub or an editor leaves `Editing` or `Nested`, its editor and that editor's nested editors stop first, innermost first.
- **A child's host kept in the parent's state.** The step that enters `Editing` or `Nested` puts an empty `actor.NewHost[…]()` in the state; the entry's work fills it with `actor.ChildIn(send, host, …)`, and the screen reads it with `host.Get()` to reach the innermost editor. The runner shows the entry only after the fill, so no signal carries the address and no reader waits for it. No registry, no names: a host is a value (MQ10), and it fails `Stopped` once its entry ended.
- **Typed output routed by the parent.** An editor ends with `Added{L} | WentBack`; `toHub` and `toSub` map it to the parent's signal. A `Back` in any editor leaves the plan as it was.
- **History as data.** `Editing.From` is `FromList{Page} | FromSearch{Query, Page}`, so the hub returns to the page or the search after an editor, and a search's `close` returns to its page.
- **An eventless step.** A composite with a slot left starts in `Picking` (`settle`).
- **A contract with a typed failure.** `Cluster.Submit` fails `Full`; the screen tells the operator.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants.sh`) |
| --- | --- | --- |
| `HistoryRestores` | an editor from a page or a search returns the hub there, after Back or Add | the hub returns to page 1 |
| `BackChangesNothing` | Back leaves the plan equal; Add adds exactly the unit | a unit added twice |
| `NestedPartsInsideParent` | a composite's unit holds one sub-unit per slot, each the nested editor's unit, at the size of the part and its subs | a sub-unit rebuilt with count 1; the subs' size dropped |
| `CountStaysPositive` | Less at count 1 is refused | `> 0` for `> 1` |
| `EditorOpensOnlyFromAList` | only `Open` on a page or a search opens an editor | `search` opens an editor |
| `Cluster.PlanCountsEveryUnit` | an ack counts every unit, subs included | restated for `memory.Cluster` |

That children stop with their entry, nested ones before their parent (survey `NoOrphanEditors`), is the runner's rule, proved by MU5's Quint model and model-based test; the `nested` case runs it (three deep, with backs at each depth).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/stack-builder/cmd/stack-builder < examples/stack-builder/cases/nested/stdin
./bin/ergo test examples/stack-builder/stack
./bin/ergo test examples/stack-builder/memory
go run ./scripts/examples stack-builder
```

Cases: `browse` (pages, a part, a node composite), `nested` (a twin: three editors deep, a back at the third depth and at the second, a back from the top that changes nothing), `search` (a part opened from a search returns to it; close returns to the page), `full` (the cluster refuses a large plan).

## effect-machine side by side

effect-machine would host the editor with `.spawn(State.Editing, ({ self }) => self.spawn("editor", editorMachine, { input }))` and nested editors the same way with distinct ids; a screen would reach the current editor through the actor system by id, or through `ActorHost.acquire`; the hub would keep the page in its state for history, as here.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| reaching the current child | look up by string id, or `ActorHost` with a generation check | an `actor.Host` in the parent's state, which the entry's work fills with `actor.ChildIn` before the entry is shown; a `Get` after the entry ended fails `Stopped`, and a Send to an ended child fails `Stopped` | compiler (the type) + runner (host.qnt) |
| nested children of one machine | distinct ids per depth | identity, no id | runner |
| child output | `sendParent` with an event the parent may not handle | `toParent` gives the parent's signal type, which the parent's total step decides | compiler |
| stop order on leaving a state | not specified | innermost first, before the next entry | runner (MU5 model) |

Where Ergo could not improve:

- **A host names its address type.** `actor.NewHost[EditActor]()` writes the address type as a type argument, because a call infers no type argument from its expected type. The alias `EditActor` (F11) keeps it short.

## Benchmark

No em-parity §3 scenario is this program's own; its costs are a child start per editor and an `Ask` per key. The command for an end-to-end time of the cases is in the lane's `bench-pending.md` (not run).

## Limits found

- **F10** (fixed, fu-mb2) A machine's state had to be a union, so the record state needed a one-variant union and an unwrapping step. A record is now a union of one variant wherever a machine asks for a union.
- **F11** (fixed, fu-mb2) The address type `actor.Actor[Edit, EditSignal, Decision[Edit, EditOut]]` was written nine times: Ergo had no type alias. A union whose only variant is an existing type is now that type, so `type EditActor = actor.Actor[…]` names it once.
- **F4**, **F7** as in sign-in.
