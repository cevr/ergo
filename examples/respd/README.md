# respd

A key-value server that speaks RESP, the protocol of Redis, with leases as commands of their own. This first build (RS1) answers the RESP requests it reads on standard input, one after another, on standard output, as one connection would; the server over sockets (`respd -addr 127.0.0.1:0`) comes with std `net` (RS2). The keys live in memory for one run. `-at SECOND` stops the clock at a Unix second, so expiries and lease ends read the same on every run.

```sh
printf '*1\r\n$4\r\nPING\r\n' | respd          # +PONG
respd -at 1700000000 < examples/respd/cases/session/stdin
```

Tag: `default`. It is an original program, written from the neutral contract of the plan for this wave (§2); no private source was read. Its goldens were made by running the program and read by hand against the replies the Redis documentation gives for the same commands (`PING`, `SET` with `NX` and `EX`, `GET`, `INCR`, `INCRBY`, `TTL`, `PTTL`, `DEL`, `HELLO 3`, the unknown-command and arity errors); no Redis server was run (RS3 runs one).

## Layout

The program is a module of packages (`ergo.mod`: `module respd`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `resp/` | The protocol format. `value.ergo` (the `Value` union of RESP2 and RESP3: `Simple`, `Fault`, `Integer`, `Bulk`, `Null`, `Array`, `Boolean`, `Dict`, `Set`; `Down`, the RESP2 form; `Equal`), `read.ergo` (`Parse`, `ParseAt`, the `Reader` that frames values out of chunks of any size, in place on one buffer; `Incomplete` and `Malformed`), `write.ergo` (`Append` to the caller's buffer, in a `Protocol`), `gen.ergo` (values and noise from a seed), `laws.ergo`, `tests.ergo`. Pure. |
| `store/` | The domain. `engine.ergo` (the operation type `Engine`: `Get`, `Set` with a `When` and an expiry, `Del`, `Expire`, `TTL`, `Incr`, each given the time; its laws), `lease.ergo` (the operation type `Leases`: `Acquire`, `Renew`, `Release` with fencing tokens; its laws; `Slot`, `Ask`, `Step` and the `Lock` machine that decides one name's lease), `decimal.ergo` (the integer text `Incr` reads). Pure. |
| `memory/` | `Engine`, the provider of both contracts: sixteen shards of a `Map` behind a `sync.Ref` each, picked by FNV-1a of the key; an expiry is a time stored beside the value and compared with the time each operation is given, so there is no timer per key. One operation on a key is one `sync.Modify` of its shard. Leases are a `Map` of `Slot`s behind one `Ref`, each changed by `store.Step`. |
| `command/` | `Command`, the union of the requests respd knows, `Parse` from a RESP frame, and `Run`, which answers one on the `Engine`, the `Leases` and the `time.Clock` in its context, reading the clock once per command. |
| `cmd/respd/` | The binary: the flag, the loop that feeds standard input's chunks to a `resp.Reader` and writes each chunk's replies at once, and the context that binds `memory.Engine` and the clock. |

## What it shows

- **Laws on the contract, run against the provider.** `store.Engine` has nine laws (`GetAfterSet`, `SetNXKeepsTheFirst`, `ExpiredIsMissing`, `SetClearsTheExpiry`, `ExpireInThePastDeletes`, `DelRemoves`, `SetKeepsOthers`, `IncrAdds`, `IncrRefusesText`) and `store.Leases` six (`AcquireIsExclusive`, `TokensRise`, `ReleaseNeedsTheToken`, `RenewKeepsTheToken`, `ReleaseFrees`, `NamesAreApart`). `ergo test ./memory` runs all fifteen against `memory.Engine`, built fresh for each case by `NewEngine`. A provider over a real Redis-compatible server runs the same laws (RS3).
- **Time is a parameter.** No engine operation reads a clock: each takes `now`. The laws draw their times, so they run expiry at any instant, the expiry's own included, with no sleep; only `command.Run` reads the clock, once per command.
- **The lease as a machine.** `type Lock machine[Slot, Ask]`: a name is `Free{Last}` or `Taken{Owner, Token, Until}`; `Step` grants a free name the next token (`Emit`), refuses a taken one (`Tell`), and renews or frees only for the grant's own token. An expired grant reads as `Free` with its token kept, so tokens rise across expiry. In Redis this is a script each client must carry and get right; here it is a command with typed refusals (`Held`, `Lost`) and laws.
- **The codec's laws.** `ParseInvertsAppend` (both protocols), `SplitAnywhereParsesTheSame` (a stream cut at every byte reads as the same values, and never a value early), `MalformedNeverPanics` (generated noise and damaged encodings: a frame, `Incomplete` or `Malformed`, never a crash), `BulkTakesItsLength`.
- **Explored schedules.** `IncrIsAtomic` (memory) runs two tasks that `Incr` one key; the `explored` file has the examples gate search every schedule of one preemption. An `Incr` that reads its shard and then writes it (`testdata/mutants.json`) loses a count in a schedule the search finds.
- **Exhaustive matches.** Every `match` over `Value`, `Command`, `When`, `Expiry`, `Slot`, `Ask`, `Answer` and `Decision` names each variant.

## Run it

```sh
CGO_ENABLED=0 go build -o bin/ergo ./cmd/ergo
./bin/ergo build examples/respd/cmd/respd -o /tmp/respd
/tmp/respd -at 1700000000 < examples/respd/cases/session/stdin
./bin/ergo test examples/respd/resp     # and store, memory
go run ./scripts/examples respd
```

## Cases

| case | what it shows |
| --- | --- |
| `session` | `PING`, `SET`/`GET`, `NX` kept, a `SET` with a missing argument, a request that is not an array of bulk strings, `INCR`/`INCRBY` and its refusal, `EX` and `TTL`/`PTTL`, `DEL` of two keys, an unknown command, an arity error, a lease acquired, refused, renewed with the wrong and the right token, released and acquired again with a greater token, and `HELLO 3` switching the replies to RESP3. |
| `malformed` | A bulk string whose CRLF is not where its length puts it: one protocol error, and status 1. |
| `usage` | A bad `-at`: status 2. |

## Commands

`PING [msg]`, `ECHO msg`, `GET key`, `SET key value [NX|XX] [EX s|PX ms]`, `DEL key...`, `EXPIRE key s`, `PEXPIRE key ms`, `PERSIST key`, `TTL key`, `PTTL key`, `INCR key`, `DECR key`, `INCRBY key n`, `HELLO [2|3]`, and the leases:

- `LEASE.ACQUIRE name owner ms` → `[token, until-ms]`, or the error `HELD owner ms-left`.
- `LEASE.RENEW name owner token ms` → `[token, until-ms]`, or `LOST name token`.
- `LEASE.RELEASE name owner token` → `OK`, or `LOST name token`.

A token is greater than every token the name had in this run. respd keeps nothing on disk, so a restart starts tokens again from 1: a resource that keeps the greatest token it has seen must be reset with it, or respd must not be restarted while leases are out.

## Limits found

- A request that is not an array of bulk strings gets an error and the connection goes on; Redis closes it. Inline commands (`PING` typed into telnet) are not read.
- An expired key is dropped when it is next written, never by a sweep: a key set with an expiry and never touched again keeps its memory.
- `PERSIST` reads the expiry and then clears it in two engine operations.
- Not built: persistence, replication, pub/sub, scripts, cluster, and every command not listed above.
