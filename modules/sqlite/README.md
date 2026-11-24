# sqlite

Pure-Go SQLite provider for Ergo's `std/sql` (`sql.DB`, `sql.Conn`, and `sql.Transaction`), backed by `modernc.org/sqlite` with `CGO_ENABLED=0`.

## Concurrency and Integrity Defaults

`sqlite.Open(path)` configures every physical connection in the pool via the SQLite URI DSN:

- `PRAGMA busy_timeout=5000` (set before `journal_mode` so contended opens wait cleanly)
- `PRAGMA journal_mode=WAL` (on file-backed databases; omitted for `:memory:`)
- `PRAGMA foreign_keys=ON`
- `PRAGMA synchronous=NORMAL`
- `_txlock=immediate` so `DB.Begin()` (and therefore `sql.Transaction` and `sqlite.WriteTransaction`) starts `BEGIN IMMEDIATE`, acquiring SQLite's RESERVED write lock up front instead of upgrading mid-transaction and failing with `SQLITE_BUSY`.

Use `sqlite.ReadOnly(db)` when you want a `sql.DB` view whose `Begin()` starts `BEGIN DEFERRED` read-only snapshot transactions.

## Error Classification

`sqlite.ClassifyCode` and `sqlite.Dialect` map SQLite extended integer result codes into `std/sql.Failure`:

- `SQLITE_CONSTRAINT_UNIQUE` (`2067`), `SQLITE_CONSTRAINT_PRIMARYKEY` (`1555`) -> `sql.Unique`
- `SQLITE_BUSY` (`5`, including extended `261`/`517`/`773`), `SQLITE_LOCKED` (`6`, `262`) -> `sql.Serialization`
- `SQLITE_INTERRUPT` (`9`), context deadline/cancellation -> `sql.Timeout`
- Closed database/connection/transaction -> `sql.Closed`
- Other SQLite errors (including `SQLITE_CONSTRAINT_FOREIGNKEY` `787`) -> `sql.Other` with the decimal SQLite code preserved in `Other.Code`.

## Using from Another Ergo Module

Add `trust go github.com/cevr/ergo/modules/sqlite` to `ergo.mod`, require `github.com/cevr/ergo/modules/sqlite` in `go.mod` (with a local `replace` inside this repository), populate `go.sum`, and generate the Go binding for `driver` in the consuming module root:

```sh
go get github.com/cevr/ergo/modules/sqlite/driver@v0.0.0
ergo bind go github.com/cevr/ergo/modules/sqlite/driver
```

Then open and bind `sql.DB` in `main`:

```ergo
let db = try sqlite.Open(path)
with context { sql.DB: db } {
	try sqlite.WriteTransaction() {
		let _ = try sql.Tx.Exec("INSERT INTO items(name) VALUES (?)", []sql.Value{sql.Text{S: "a"}})
	}
}
```
