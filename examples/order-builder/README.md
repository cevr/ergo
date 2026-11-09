# order-builder

An order builder: a hub browses a menu by pages, searches it, and opens an item editor as a child actor. A combo's editor opens a nested editor of the same machine for each of its slots, so editors nest as deep as the menu does (a family box of two meals, each a main and a side, is three deep). Each nested editor's line goes into its parent's line. When an editor ends, the hub goes back to the page or the search it was opened from. The order goes to a kitchen at checkout. A script on standard input plays the person.

Tag: `default`. It is an original program, a neutral re-creation of a common ordering pattern (survey E7). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module orderbuilder`.
- `order/`: the domain.
  - `editor.ergo`: the `Editor` machine (`Choosing | Picking | Nested`); its `Nested` state's work starts a child `Editor` for the chosen item.
  - `hub.ergo`: the `Builder` machine. Its state is the record `Hub{Menu, Basket, Screen}`, whose `Screen` (`Browsing | Searching | Editing`) is a region of the state; `Editing`'s work starts the item's `Editor`.
  - `kitchen.ergo`: the contract `Kitchen` with its law `TicketCountsEveryLine`.
  - `laws.ergo`: the hub's and the editor's rules as laws.
- `menu/`: the menu, as data.
- `memory/`: `Kitchen`, an in-memory provider of `order.Kitchen`, and a property that runs its law.
- `screen/`: the line protocol. It runs the hub with `actor.StartMachine`, finds the open editors through the addresses kept in the states, asks the innermost one, and waits for each to settle with `actor.WaitFor`.
- `cmd/order-builder/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Recursive children (MU5).** `Editor`'s work starts an `Editor`: one machine is a child of the hub and of itself. Each child belongs to the entry that started it, so when a hub or an editor leaves `Editing` or `Nested`, its editor and that editor's nested editors stop first, innermost first.
- **A child's address kept in the parent's state.** The work tells its own machine the child's address (`send(Opened{Sub: actor.Child(…)})`); the state keeps it as `Option[EditActor]`, where `type EditActor = actor.Actor[…]` is an alias (a union whose only variant is an existing type is that type), and the screen reads it to reach the innermost editor. No registry, no names: an address is a value (MQ10).
- **Typed output routed by the parent.** An editor ends with `Added{L} | WentBack`; `toHub` and `toPart` map it to the parent's signal. A `Back` in any editor leaves the basket as it was.
- **History as data.** `Editing.From` is `FromMenu{Page} | FromSearch{Query, Page}`, so the hub returns to the page or the search after an editor, and a search's `close` returns to its page.
- **An eventless step.** A combo with a slot left starts in `Picking` (`settle`).
- **A contract with a typed failure.** `Kitchen.Send` fails `Closed`; the screen tells the person.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants.sh`) |
| --- | --- | --- |
| `HistoryRestores` | an editor from a page or a search returns the hub there, after Back or Add | the hub returns to page 1 |
| `BackChangesNothing` | Back leaves the basket equal; Add adds exactly the line | a line added twice |
| `NestedLinesInsideParent` | a combo's line holds one part per slot, each the nested editor's line, at the price of the item and its parts | a part rebuilt with quantity 1; the parts' price dropped |
| `QtyStaysPositive` | Less at quantity 1 is refused | `> 0` for `> 1` |
| `EditorOpensOnlyFromAList` | only `Open` on a page or a search opens an editor | `search` opens an editor |
| `Kitchen.TicketCountsEveryLine` | a ticket counts every line, parts included | restated for `memory.Kitchen` |

That children stop with their entry, nested ones before their parent (survey `NoOrphanEditors`), is the runner's rule, proved by MU5's Quint model and model-based test; the `nested` case runs it (three deep, with backs at each depth).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/order-builder/cmd/order-builder < examples/order-builder/cases/nested/stdin
./bin/ergo test examples/order-builder/order
./bin/ergo test examples/order-builder/memory
go run ./scripts/examples order-builder
```

Cases: `browse` (pages, an item, a meal combo), `nested` (a family box: three editors deep, a back at the third depth and at the second, a back from the top that changes nothing), `search` (an item opened from a search returns to it; close returns to the page), `closed` (the kitchen refuses a large order).

## effect-machine side by side

effect-machine would host the editor with `.spawn(State.Editing, ({ self }) => self.spawn("editor", editorMachine, { input }))` and nested editors the same way with distinct ids; a screen would reach the current editor through the actor system by id, or through `ActorHost.acquire`; the hub would keep the page in its state for history, as here.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| reaching the current child | look up by string id, or `ActorHost` with a generation check | the address is a value in the parent's state; a Send to an ended child fails `Stopped` | compiler (the type) + runner |
| nested children of one machine | distinct ids per depth | identity, no id | runner |
| child output | `sendParent` with an event the parent may not handle | `toParent` gives the parent's signal type, which the parent's total step decides | compiler |
| stop order on leaving a state | not specified | innermost first, before the next entry | runner (MU5 model) |

Where Ergo could not improve:

- **The screen waits by hand for each address.** A child's address reaches its parent as a signal after the entry opens, so the screen waits for each editor to settle (`hubSettled`, `editSettled`) before it asks the innermost one. A lazily acquired child (effect-machine `ActorHost.acquire`, em-parity M63) would wait for the screen.

## Benchmark

No em-parity §3 scenario is this program's own; its costs are a child start per editor and an `Ask` per key. The command for an end-to-end time of the cases is in the lane's `bench-pending.md` (not run).

## Limits found

- **F10** (fixed, fu-mb2) A machine's state had to be a union, so the record state needed a one-variant union and an unwrapping step. A record is now a union of one variant wherever a machine asks for a union.
- **F11** (fixed, fu-mb2) The address type `actor.Actor[Edit, EditSignal, Decision[Edit, EditOut]]` was written nine times: Ergo had no type alias. A union whose only variant is an existing type is now that type, so `type EditActor = actor.Actor[…]` names it once.
- **F4**, **F7** as in sign-in.
