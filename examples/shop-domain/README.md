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
| `fulfil/` | The policy and the saga. `saga.ergo` (`Starts`, the policy: a `Placed` event starts a saga; `Fulfilment`, a machine: a payment ends it with `Ship`, a deadline with `Cancel`); `desk.ergo` (`Submit` and `Advance`, which need only `order.Orders`); `tests.ergo` (`EveryOrderSettles`). |
| `memory/` | The provider of `order.Orders`: a stream per order and a snapshot of its state, so a command is decided without a replay. Its tests run the contract's laws. |
| `cmd/shop-domain/main.ergo` | The script parser (`Parse`, failing `BadLine`), the printing, and the only place that chooses the providers. |

The laws: `RejectedCommandEmitsNothing` and `AcceptedCommandIsRecorded` (a store records what the aggregate decides, and nothing for a refusal), `ReplayRebuildsState` (the snapshot is what the history replays to), `PaymentIsTheTotal`, `RefundIsThePayment`, `RevenueIsWhatOrdersHold` and `EveryOrderSettles` (after every deadline, no order is left open or paid). Each runs as a property test over seeded scripts of 40 commands on two orders, and each has a mutant that breaks it (`mutants.json` in the lane).

Because the saga ships an order as soon as it is paid, the desk never cancels a paid order; a refund is reached by deciding against the aggregate directly (the laws' scripts do).

## Limits found

- **A machine's initial is one constant** (G-ex-g-5). Each saga starts from its order and the minute it was placed; `Fulfilment`'s initial is a placeholder no run uses, and the policy `Starts` builds the first state.
- **A machine has no runner, and its step cannot emit and go on** (G-ex-a-9, G-ex-b-12, also hit by shop-domain). `Feed` calls `Step` itself; a saga that shipped and then waited for delivery would need a second machine.
- **A failure row has no name** (G-ex-a-4, also hit by shop-domain). The five rejections are listed in full eight times (`Decide`, `Orders.Send`, the provider's `Send`, three laws, `Script` and `Simulate`), and once per arm in three more catches.
- **A provider keeps state only in a Map** (G-ex-b-3, also hit by shop-domain). `memory.Log` writes its streams and snapshots through local copies of its two Map fields, which every copy of the Log shares.
- **A slice has no `==`** (G-ex-d-5, also hit by shop-domain). The tests compare event logs with a hand-written `same`; `Command`, which holds its lines, has no `==` at all.
- **A generated slice holds at most three elements** (G-ex-d-9, also hit by shop-domain). The laws draw their own scripts from a seeded `Random`.
- **Laws are checked, not run** (G-ex-c-5, #162, also hit by shop-domain). Each law is restated as a property test; the contract's laws run in the provider's tests.
- **A prelude name cannot be declared** (G-ex-c-11, also hit by shop-domain). The parser's number reader is `whole`, not `number`.
