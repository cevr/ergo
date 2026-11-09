# shop-domain

An event-sourced shop, modelled as the alchemy-domain skill describes (an aggregate that decides commands into events, rejections as typed failures, a view, a policy and a saga), in Ergo's own constructs: unions and exhaustive `match`, typed failures, an operation type with laws, contexts and a machine.

```sh
go build -o bin/ergo ./cmd/ergo
printf 'place a ana tea:2:150\npay a 300\n' | ./bin/ergo run examples/shop-domain/cmd/shop-domain
./bin/ergo test examples/shop-domain/order
```

The binary reads a script on standard input, one command per line: `place ORDER CUSTOMER SKU:QTY:CENTS ...`, `pay ORDER CENTS`, `ship ORDER`, `cancel ORDER REASON ...`, `wait MINUTES` and `sales`. It prints each command's events or its rejection, the commands the saga sends, and the sales view at the end. A line that is not a command exits 2.

| package | what it is |
| --- | --- |
| `order/` | The aggregate. `order.ergo` (`Command`, `Event`, `State`, the five rejections), `decide.ergo` (`Decide`, a `match` over (state, command); `Evolve`, total; `Replay`), `orders.ergo` (`Orders`, the contract the shop needs, with the laws `RejectedCommandEmitsNothing`, `AcceptedCommandIsRecorded` and `ReplayRebuildsState`; `Simulate`, the reference), `draw.ergo` (`Script`, seeded command scripts), `laws.ergo` (`PaymentIsTheTotal`, `RefundIsThePayment`), `tests.ergo`. Nothing in it needs a capability. |
| `sales/` | The view: `Project` folds events into `Sales`; the law `RevenueIsWhatOrdersHold` holds it to the replayed orders. |
| `fulfil/` | The policy and the saga. `saga.ergo` (`Starts`, the policy: a `Placed` event starts a saga; `Fulfilment`, a machine whose input is the order placed: a payment emits `Ship` and waits for the shipment, a deadline ends it with `Cancel`); `desk.ergo` (`Submit` and `Advance`, which need only `order.Orders`); `tests.ergo` (`EveryOrderSettles`). |
| `memory/` | The provider of `order.Orders`: a stream per order and a snapshot of its state, so a command is decided without a replay. Its tests run the contract's laws. |
| `cmd/shop-domain/main.ergo` | The script parser (`Parse`, failing `BadLine`), the printing, and the only place that chooses the providers. |

The laws: `RejectedCommandEmitsNothing` and `AcceptedCommandIsRecorded` (a store records what the aggregate decides, and nothing for a refusal), `ReplayRebuildsState` (the snapshot is what the history replays to), `PaymentIsTheTotal`, `RefundIsThePayment`, `RevenueIsWhatOrdersHold` and `EveryOrderSettles` (after every deadline, no order is left open or paid). `ergo test` runs the laws over seeded scripts of 40 commands on two orders (the provider's tests restate `Orders`' laws, G-exup-1), and each has a mutant that breaks it (`mutants.json` in the lane).

Because the saga ships an order as soon as it is paid, the desk never cancels a paid order; a refund is reached by deciding against the aggregate directly (the laws' scripts do).

## Limits found

- **A generated slice is short and its ints rarely meet** (G-ex-d-9, still open after T4). The laws draw their own scripts of 40 commands on two orders from a seed.
- **The contract's laws run only for providers in its package** (G-exup-1, with G-ex-c-5: a member law cannot be called). `memory`'s tests restate `Orders`' three laws for `Log`.
- **A prelude constraint cannot be declared** (G-ex-c-11, partly closed). Batch 12 admits a prelude type's name, but `number` is a predeclared constraint (S108), so the parser's number reader stays `whole`.

Closed in batch 12: G-ex-g-5 (V4: `Fulfilment`'s input is the `Placing`, and `Starts` takes `machines.Initial(machines.Start(Fulfilment{}, …))`); G-ex-a-9 and G-ex-b-12 (V5: a payment `Emit`s `Ship` and the saga waits in `AwaitingShipment` until the order is shipped or cancelled; `Feed` moves with `machines.Next`); G-ex-a-4 (L2: the row `Rejection` names the five rejections, one `case Rejection:` per catch); G-ex-b-3 (`memory.Log` keeps its streams and snapshots in one `sync.Ref`); G-ex-d-5 (W3: event logs compare with `==`); and the property tests that restated the aggregate's and the view's laws (V1 runs them).
