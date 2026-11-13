# inbox

An inbox that files its messages into threads: a Map from a thread's name
to its messages, oldest first, and a count of the unread ones. Filing a
message reads its thread out of the Map, appends the message and stores
the thread back.

`inbox N` files N messages into seven threads (message i into thread
`thread<i % 7>`), reads `thread0`, and prints the threads, the longest
thread, what was read and what is left unread, and the last message.

## Layout

- `threads/`: `Msg`, `Inbox`, `File` (returning the union
  `Opened | Joined | EmptyBody`, matched exhaustively), `Thread`,
  `Unread`, `MarkRead`, and the laws `InOrder`, `ThreadsApart`,
  `EmptyRefused` and `ReadClears`.
- `cmd/inbox/`: the binary.

## What it shows: a thread is appended to in place

`File` is a read-modify-write of one entry:

```ergo
let mut msgs = []Msg{}
match b.threads.Get(m.Thread) {
case Some[[]Msg] as t:
	msgs = t.Value
case None:
}
msgs = msgs.Append(m)
b.threads[m.Thread] = msgs
```

Nothing between the read and the store reads `b`, and `msgs` dies at the
store, so the lowering moves the thread out of the Map and back, and the
`Append` writes it in place (the entry move-out, docs/spec/types.md,
Bindings). Before it the bind `msgs = t.Value` copied the thread, so
filing cost the thread's length, and a thread of n messages cost n² to
build. `ThreadsApart` holds that a copy of the inbox taken before is not
changed by the filing after: the Map type is entry-written, so that copy
copies the threads.

The example found a bug: a law's body decided no copies, so in
`ThreadsApart` `let before = b` shared `b`'s Maps and the filing through
`&b` reached `before`. Laws now decide their copies as functions do
(`TestLawBodyCopies`, internal/sketch/lower).

Counted receipt: allocations of one `File` into a thread of an inbox of n
messages in seven threads (scratch harness, Go's `runtime.MemStats`):
base e02194103, the entry rule (MAPCOPY round 1, 983a6a13a), and the
move-out.

| inbox | base | entry rule | move-out |
| --- | --- | --- | --- |
| 7000 messages | 9 allocations, 408928 B | 2 allocations, 57409 B | 1 allocation, 434 B |
| 14000 messages | 9 allocations, 799604 B | 2 allocations, 105250 B | 1 allocation, 24 B |

Copy sites in `File`'s Go: 2, 1, 0. The one allocation left is the
`Append`'s, when the thread's array is full.
