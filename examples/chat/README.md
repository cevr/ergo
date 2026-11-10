# chat

A chat room server with one task per client, replayed from a script. `chat` reads a script on standard input, runs the server over in-memory connections on a manual clock that starts at midnight, and prints the room's log, one line per signal, then how many lines each client was sent. A script line is `CLIENT> LINE` (the client sends LINE; its first line is its name), `+SECONDS` (the clock moves on) or `CLIENT!` (the client hangs up). In the room, `/quit` leaves and `/shut` closes the room. A bad script line exits 2.

Tag: `default`. It is an original program, not a port. Its goldens were written by hand from the rules below and then checked against the program.

## Layout

The program is a module of packages (`ergo.mod`: `module chat`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `room/` | The domain, pure and event-sourced. `room.ergo`: the unions `Signal` (`Join`, `Leave`, `Say`, `Shut`), `Event` (`Joined`, `Left`, `Said` with its time, `Closed`) and `State` (`Open`, `Shutdown`); `Decide`, which fails `NameTaken | NotMember | RoomClosed`; `Evolve`; the `Room` machine; `Render`. `laws.ergo` (four laws, and `RoomOf`, `Apply`), `tests.ergo`. |
| `server/` | The server and its contract. The operation type `Conn` (`Read` fails `Hangup`, `Peer`, `Close`; law `HangupIsFinal`), a resource, and its sending side `Peer` (`Write`, `Hangup`; law `HangupIsIdempotent`), a shared handle. `Serve` takes each connection from an accept queue, starts the room's task (`host`) and one task per connection (`client`); clients send `Envelope`s to the room through one `queue.Queue`; the room decides each, writes events to every member's `Peer`, and pushes one line per signal to a log queue. |
| `memory/` | The `Conn` provider: `Pipe` gives two owned ends of one connection (as Go's `net.Pipe`), `Server` and `Client`, over two queues. `replay.ergo`: `Replay`, the driver: it moves each `Server` end into the accept queue, keeps the `Client` ends, takes one script step at a time and waits for the room's line for it, so a session is the same on every run. `tests.ergo`: whole sessions, with no host. |
| `cmd/chat/` | The binary: it parses the script and replays it on `memory` connections and a `time.Manual`; it is the only place that chooses them. |

## What it shows

- **Tasks and a queue.** `Serve` runs `1 + n` tasks: the room's and one per client. Every client sends to one inbox queue; only the room's task touches the room's state, so the state needs no lock. A task's failure row is in its type (`Task[room.State, queue.Closed]`), and the server waits for every task before it ends.
- **Typed failures over the room's rules.** A join under a member's name fails `NameTaken`; a say from a stranger `NotMember`; anything after `/shut` `RoomClosed`. The room's task catches each by name and answers the sender alone.
- **Laws.** `JoinAppendsOrRefuses`, `SayNeedsMember`, `LeaveUndoesJoin`, `ClosedRefusesAll` over the room, and `HangupIsFinal` over every connection, each restated as a property test.
- **Ownership (#197).** A `Conn` has `Close`, so it is a resource with one owner. `serveAll` moves each accepted connection into a `tasks.Set` member's root. The handler lends it for the run, and the root releases it before the Set's `Wait` returns. No code closes a connection by hand. The room keeps only `Peer`s, which have no `Close` and copy freely; `Peer.Hangup` lends, so the law can read the connection after it.
- **Virtual time.** The clock is a `time.Manual`: `+75` moves it by 75 seconds, and the next message is stamped `00:01:15`. No test sleeps.
- **A found bug.** A connection is an operation type's value, which has no `==`, so the first version could not tell a client whose join was refused from the member whose name it asked for: such a client could speak as that member. The server now numbers its clients and refuses "you are not ada" (`RefusalsGoToTheSender`).

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/chat/cmd/chat < examples/chat/cases/session/stdin
./bin/ergo test examples/chat/memory   # and room, cmd/chat
go run ./scripts/examples chat
```

## Limits found

New gap (in `gaps-ex-e.md` of the run):

- **G-ex-e-9**: no select. A client task blocked in `Read` cannot also wait for the room to close, so a closed room keeps its clients and refuses their signals until they hang up. Closing every connection from the room's task worked, but made the order of the leaves depend on the scheduler.

Gaps that other lanes found, met here too:

- No network in std (G-ex-c-7): the connections are in memory, and the binary replays a script instead of listening on a port.
- `serveAll` uses one fail-fast task Set, which drains all clients before the room's inbox ends.
- A virtual clock that advances when every task waits is now std `testing.VirtualClock` (G-ex-b-5 closed). The replay keeps `time.Manual`, because the script's `+SECONDS` lines say when time moves; an idle timeout is still not built.
- No actor runner and no supervision (#57, #101): the room's task is a loop over `Decide` and `Evolve`. `Room`'s `Step` now emits its events (G-ex-a-9 closed), but a machine has no clock, so it stamps a say at time zero and the server, which stamps each say with its clock, does not use it.
- A machine's state is a union (G-ex-c-16): `State` has two variants where `Shutdown` would do as a flag.
- `+SECONDS` is `time.Times(1s, n)` (G-ex-b-4 closed).
- `ergo test` skips the `Conn` and `Peer` laws for `memory.Server` and `memory.Sender` (each holds a queue, and `Pipe` takes a size; `expected-skips`), so `memory`'s tests restate them; `with` is a keyword, so a helper cannot be named `with`.
