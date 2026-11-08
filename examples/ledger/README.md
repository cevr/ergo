# ledger

A double-entry bank ledger behind a teller's counter. The teller types commands, one per line, on standard input: open an account (with an overdraft or none), deposit, withdraw, pay, ask a balance, list the accounts. Money only moves: every transfer takes from one account and gives to another, and money from outside comes from the account `world`, so the balances always add up to zero. Each transfer has a key, and a transfer sent again with its key is posted once. The ledger lives in memory for one run, or with `-journal FILE` in a journal file that outlives it.

Tag: `default`. It is an original program, not a port. Its goldens were made by running the program and checked by hand: the balances of the `basic` case add up to zero, and each refusal is the guard the rules name.

## Layout

The program is a module of packages (`ergo.mod`: `module ledger`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `ledger/` | The domain package. `book.ergo` (`Transfer`, `Receipt`, the typed failures `UnknownAccount`, `AccountExists`, `BadFloor`, `BadAmount`, `SameAccount`, `InsufficientFunds`, `KeyReused`, `Unavailable`; `Book` with unexported fields, `Empty`, `Open`, `Apply`, `Balance`, `Total`, `IDs`), `store.ergo` (the contract `Store` with the laws `TotalIsKept`, `PostIsIdempotent`, `FloorHolds`), `laws.ergo` (the Book's laws, and `Bank`, `Day`, `Posted`), `tests.ergo`. Pure. |
| `teller/` | The counter. `command.ergo` (the `Command` union, `Parse`, `ParseAll`, which fail `BadCommand`; `Dollars`, `Amount`), `counter.ergo` (`Do`, `Serve`, which need `ledger.Store`), `laws.ergo`, `tests.ergo`. |
| `memory/` | `Store`, a `ledger.Store` over one Book in memory; `Disk`, an `fs.Files` and `fs.Writes` over a Map, for tests. |
| `file/` | `Journal`, a `ledger.Store` over a journal file read and written through `fs.Files` and `fs.Writes`; `format.ergo` (`OpenLine`, `PostLine`, `Replay`, which fails `Corrupt`), `laws.ergo`, `tests.ergo`. |
| `cmd/ledger/` | The binary: the flag `-journal`, and the one place that chooses the store, `memory.New()` or `file.At(path, disk, disk)` over the host's `fs.NewHost()`. |

## What it shows

- **A contract with laws, kept by two stores.** `ledger.Store` declares `Open`, `Post`, `Balance` and `Accounts` with their typed failures, and three laws. `memory.Store` and `file.Journal` both keep it; each package's tests restate the laws over the store after a generated day of transfers. Both stores are thin: each operation is a step of the pure `Book`, so the rules live in one place.
- **Typed failures as answers.** `Apply`'s guards are in order (the key, the amount, the accounts, the floor), each its own failure with the facts a teller needs (`InsufficientFunds{ID, Balance, Amount, Floor}`, `KeyReused{Key, Was}`). `teller.Do` turns each refusal into an answer and goes on; only `Unavailable` (a disk that fails, a journal changed by hand) stops the counter with exit 1.
- **Idempotent by key.** A key posted before gives its first receipt and changes nothing (`#3 p1` twice in `basic`, and `d1` again in the `journal` case, posted on an earlier run); the same key for a different transfer is `KeyReused`.
- **Laws.** The Book: `Balances`, `ApplyIsIdempotent`, `FloorsHold`, `ApplyLeavesItsArgument`. The Store: `TotalIsKept`, `PostIsIdempotent`, `FloorHolds`. The journal: `ReplayRebuilds`, `AnyWordKeeps` (a key or account of any text, tabs and line feeds too, reads back). The counter: `AmountReadsDollars`. For each law a mutant of the program makes its test fail.
- **Exhaustive matches.** `Parse` matches the pair (verb, word count); `Do` matches every `Command`; `Replay` every journal line kind.
- **Providers chosen in one place.** Only `cmd/ledger/main.ergo` binds `ledger.Store`, `io.Stdout`, `io.Stdin` and `os.Process`, and builds the host's disk. The disk is a parameter of `run`, so the tests run the journal on a `memory.Disk`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/ledger/cmd/ledger -o /tmp/ledger
/tmp/ledger < examples/ledger/cases/basic/stdin
echo 'open alice
deposit d1 alice 10' | /tmp/ledger -journal /tmp/bank.journal
echo 'balance alice' | /tmp/ledger -journal /tmp/bank.journal
./bin/ergo test examples/ledger/ledger     # and teller, memory, file, cmd/ledger
go run ./scripts/examples ledger
```

## Cases

`basic` (a morning on a memory ledger: deposits, payments, a payment sent twice, an overdraft used and one exceeded, an unknown account, a self-payment, a reused key, a zero withdrawal), `journal` (a journal from an earlier run, made by `setup.sh`: its key is not paid twice), `nodir` (a journal in a missing directory: exit 1 at the first write), `badline` (an amount with three places, exit 2) and `help`.

## Limits found

Each gap is in the lane's gap file (`gaps-ex-b.md`) with a repro.

- **G-ex-b-13** (language; diagnostic): a law of `Store` cannot call a helper that needs `Store`, so `TotalIsKept` writes its sum out twice; and an operation named `Transfer` hid the type `Transfer` inside the laws ("Transfer is not a type"), so the operation is `Post`.
- **G-ex-b-14** (std): `fs.Writes` has no append. The journal reads the whole file and writes it back with one more line on each post: safe (the write is a rename), but O(n²) over a day.
- **G-ex-b-15** (std; #197): no `maps.Clone`, and a `Map` in a record is shared by every copy, so `Book` looks like a value and is not. `Apply` copies its Maps through a local generic `clone` (O(accounts) per transfer); the law `ApplyLeavesItsArgument` and the mutant `no-clone` hold it.
- **G-ex-b-3** (seen again): `memory.Store` keeps its Book as the one value of a `Map[int, Book]`, a reference cell in disguise.
- **G-ex-b-8** (seen again): `ergo run` passes no arguments, so `-journal` is tried with `ergo build` and the binary.
- By design, and still friction: `try` stands only at the start of an expression (`a == try f()` and `sum + try f()` do not parse), so many checks bind a `let` first, as Swift's rule asks. `number` is a prelude name and cannot name a function. Slices have no `==` (as in Go): `slices.Equal`. A provider's function cannot add a need (`Store` over `needs fs.Files` is S106, README Gap 17), so `Journal` holds its `fs.Files` and `fs.Writes` as fields. No decimal type and no codec (#49): money is `int64` cents with the program's own `Dollars` and `Amount`, and the journal format is hand-written with `strconv.Quote`. Laws are checked, not run (#162), so each is restated as a property test.
