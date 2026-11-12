# events

A tally of connection events. `events` reads a log of events from standard input and writes how many connections opened, how many sends there were and the bytes they carried, how many connections closed or failed, and how many are still open.

```text
$ events gen 8 | events stream
opened 2
sent 4 (110866 bytes)
closed 1
failed 1
open 0
```

An event is a JSON object whose `"_tag"` names its kind (`Opened`, `Sent`, `Closed`, `Failed`): the derived codec of the union `conn.Event`, so the log is read with no hand-written parsing and counted by an exhaustive `match` (`conn.Add`). The laws of `conn` say that every event counts once (`AddCountsEach`), that a tally does not depend on the order of the events (`AddIsOrderFree`), and that a generated log of whole connections leaves none open (`GeneratedIsBalanced`).

The program shows std json's `Elements` (stdlib.md, json 8), reading a log much larger than memory one event at a time:

- `events stream` reads a JSON array with `json.NewElements` over `json.NewStream(stdin)`: each `Next` decodes one event, so the program holds one event and one chunk of input, however long the log.
- `events lines` reads newline-delimited JSON, one event per line, with `json.NewSequenceStream`.
- `events whole` reads the input whole and decodes it with `json.Decode` into a slice: the reference, which holds the text and every event.

The three agree on every log: `Next`'s last answer is `Decode`'s verdict on the whole text (law `ElementsEqualWholeDecode`). A log that breaks partway is refused with the byte where it breaks, a log with an event of no known kind or a missing field with the event's place, and the exit status is 1.

`events gen N` prints a generated log of N events, the input of the counted receipt: `workload` runs `events stream` at two sizes against the hand Go control in `testdata/control` (encoding/json's `Decoder`), and the same workload with `args whole` is the `json.Decode` path.

Layout: `conn` is the domain (events, the tally, its laws, the generator and `Count`, which reads an `Elements`); `cmd/events/main.ergo` is the binary and the only place that chooses the input, the host's standard input.
