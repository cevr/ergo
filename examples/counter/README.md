# counter

A ten-position counter wheel, and a rewrite law the compiler proves and uses.

- `counter.ergo` declares the contract: `Counter` with `Add(n Digit)` and `Total() Digit`, where `Digit` is `0..9`, and the law `AddJoins`: two `Add` calls are one `Add` of their sum on the wheel (`Plus`).
- `wheel/` is one provider: a record whose `*Wheel` operations move the position.
- `cmd/counter/main.ergo` chooses the provider and adds 1, 2 and 4, then prints the total, `7`.

The law runner proves `AddJoins` for the binary's `Wheel` over every reachable state and every pair of digits (`ok law counter.Counter.AddJoins for Wheel (proved: all 1000 cases)`), because every parameter is listable. `ergo test cmd/counter` writes the verdict, with the site's constants `(1, 2)` and `(3, 4)`, to `ergo.laws`, the checked-in law ledger beside `ergo.mod` ([runtime.md](../../docs/spec/runtime.md#tests), Tests 10). `ergo build` reads the entry, and while it is current it lowers the three `Add` calls as one `Add(7)` (the law joins 1 and 2, then 3 and 4). After an edit the law reaches, the build keeps the plain calls and prints one `ergo.laws: … is stale; run ergo test` line until `ergo test` runs again; `ergo build --locked` refuses instead. The program prints the same output either way.
