# fk-chat

A chat with an echo server over one connection. Connect dials. The chat's state owns the line from the dial until the line ends, and a send while connected comes back as an echo. When the server closes the line, the conversation goes; when the line fails, the reason shows and Try again dials anew. It is a port of Foldkit's `websocket-chat` example (`conformance/upstream/foldkit-d21db423/examples/websocket-chat`, MIT). This is stage a of FK14: the model, with no view yet.

The program runs the chat as an actor on an in-process echo server, plays a script of the user's acts and the server's, and shows after each line what changed and how many lines are held.

Tag: `default`.

```sh
fk-chat < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`:

- the user: `connect`, `type`, `submit`;
- the server: `push`, `hangup`, `break`, `refuse`, `vanish`.

## The shape

- Package `talk` is the domain.
  - `Line` is a resource: its release hangs up.
  - The operation type `Channel` (`Dial`, `Send`) has the laws `FramesKeepArrivalOrder` and `ClosedLineTakesNoSend`.
  - The machine `Chat` over `Session` has the link `Offline | Connecting | Online{Log, Out} | Broken{Why}` and the field's text. Only `Online` has a conversation and an outbox, so a send with no line cannot be written.
- **The line belongs to an entry.** Connect enters `Connecting`, and that state's work dials and holds the line. `Opened` is a `Stay` into `Online`, so the entry, and the line with it, lasts through `Online`.
  - The work passes each frame on as `Received`, a server's close as `Closed`, and a failure as `Failed`.
  - `Closed`, `Failed` and an `Unsent` send are each an `Enter` out of the entry. The runner then stops the work and waits for it to end (runtime.md, Machines and actors, item 11, rule 11), and the line is released on every path.
  - Upstream gets the same lifetime from `ManagedResource` (`modelToMaybeRequirements`, `acquire` and `release`) plus a `Subscription` that streams the socket's events while connected.
- **The outbox.** In Ergo, an `Emit` ends the entry, so a step that keeps the line cannot also give output. `Submit` therefore puts the trimmed text in `Online`'s outbox, and the host sends it and answers `Sent` (or `Unsent` when no line is open). Upstream's `SendMessage` command reads the socket from the managed resource and fails "Socket unavailable" the same way.
- Package `memory` is the provider `Echo`, an echo server in process (upstream uses `wss://ws.postman-echo.com/raw`).
  - It also exposes the server's side: `Push`, `Hangup`, `Break`, `Refuse` and `Vanish`.
  - It counts the lines dialed and the lines held.
  - `Hold` and `Flush` keep an echo until its send is done, so the conversation reads the same on every run.
- Package `script` is the host. It starts the actor with `actor.StartMachine`, asks each act, and waits with `actor.WaitFor` for the news of the work (an opened line, a frame, an end). Nothing waits for a time.

How the port differs from upstream:

- **No timestamps.** Upstream stamps each line with a zoned time in its own command. The time is the view's, so it waits for FK14b.
- **The log lives in `Online`.** Upstream keeps the messages in the model across an error, and they show again after Try again. Here the conversation is `Online`'s, so a new line starts with an empty one. This is upstream's Disconnected behaviour, applied to every end.
- **No connection timeout.** Upstream times out a dial after 5 s. `memory.Echo` answers each dial at once, and a refusal stands in for the timeout.
- **The plan's provider.** The plan named `examples/chat`'s `room` package as the provider. That package is in another module (`module chat`), and an example cannot import another, so the provider here is an echo server, as upstream's is.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 12 story tests and 8 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would:

- `SendOnlyWhenOnline` covers the four submit stories.
- `ReconnectIsAFreshEntry` covers Connect, and an error that a release keeps.
- `DisconnectReleasesTheConnection` covers the server's close, a refused dial and a failed send.
- `ConversationKeepsOrder` covers the received and sent lines.
- The `tour` case runs all 12 on the actor and the provider.

The scene tests need a view and are stage b.

## Laws

- `talk`:
  - `SendOnlyWhenOnline`, `ReconnectIsAFreshEntry`, `DisconnectReleasesTheConnection` and `ConversationKeepsOrder`, over drawn states and signals.
  - `Channel.FramesKeepArrivalOrder` and `Channel.ClosedLineTakesNoSend`, which `ergo test` runs on `memory.Echo`.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`:
  - Offline, a send is ignored.
  - A refused dial, then Try again.
  - Blank and whitespace-only sends.
  - A send and its echo, and the server speaking unasked.
  - The server's close, which takes the conversation with it.
  - A failure, which releases the line.
  - A line the server dropped, so the next send fails "Socket unavailable".
  - A last connect, whose line the stop releases.
- `badscript`: an unknown word. The program says which line and exits 1.
