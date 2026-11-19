# fk-chat

A chat with an echo server over one connection. Connect dials. The chat's state owns the line from the dial until the line ends, and a send while connected comes back as an echo. When the server closes the line, the conversation goes; when the line fails or a dial takes more than 5 seconds, the reason shows and Try Again dials anew. It is a port of Foldkit's `websocket-chat` example (`conformance/upstream/foldkit-d21db423/examples/websocket-chat`, MIT). This is stage b of FK14: upstream's view, on std ui's test host.

The program runs the chat as an actor on an in-process echo server and shows it on the test host (`ui.TestHost`). It plays a script of the user's acts, on the screen, and the server's, and shows the screen in a line after each.

Tag: `default`.

```sh
fk-chat < cases/scenes/stdin
```

The script's words are listed in `script/script.ergo`:

- the user, on the screen: `connect` (Connect to Chat, or Try Again), `type`, `submit`, `enter`;
- the server: `push`, `hangup`, `break`, `refuse`, `stall`, `vanish`;
- the host: `wait MS`, `held`, `stats`, `quiet`.

## The shape

- Package `talk` is the domain.
  - `Line` is a resource: its release hangs up.
  - The operation type `Channel` (`Dial`, `Send`) has the laws `FramesKeepArrivalOrder` and `ClosedLineTakesNoSend`.
  - The machine `Chat` over `Session` has the link `Offline | Connecting | Online{Log, Out} | Broken{Why}` and the field's text. Only `Online` has a conversation and an outbox, so a send with no line cannot be written.
- **The line belongs to an entry.** Connect enters `Connecting`, and that state's work dials and holds the line. `Opened` is a `Stay` into `Online`, so the entry, and the line with it, lasts through `Online`.
  - The work passes each frame on as `Received`, a server's close as `Closed`, and a failure as `Failed`.
  - `Closed`, `Failed` and an `Unsent` send are each an `Enter` out of the entry. The runner then stops the work and waits for it to end (runtime.md, Machines and actors, item 11, rule 11), and the line is released on every path.
  - Upstream gets the same lifetime from `ManagedResource` (`modelToMaybeRequirements`, `acquire` and `release`) plus a `Subscription` that streams the socket's events while connected.
- **The dial's timeout is a timer.** `Connecting`'s timer (`after: dialTimer`) gives `TimedOut` after `DialTimeout`, 5 s, upstream's `Effect.timeout` on the acquire. `TimedOut` is an `Enter` into `Broken{"Connection timeout"}`, so the entry ends and the runner stops the dial at its wait. Once the line is open the state is `Online`, which sets no timer.
- **The stamps are signal data.** A frame is stamped by the work (`time.Clock.Now()`) and a send by the host when it answers `Sent`, so the step needs no clock and a law passes the times it wants. Upstream stamps each line in a command of its own.
- **The outbox.** In Ergo, an `Emit` ends the entry, so a step that keeps the line cannot also give output. `Submit` therefore puts the trimmed text in `Online`'s outbox, and the host sends it and answers `Sent` (or `Unsent` when no line is open). Upstream's `SendMessage` command reads the socket from the managed resource and fails "Socket unavailable" the same way.
- Package `screen` is upstream's view: the heading, the status (`Disconnected`, `Connecting...`, `Connected`, `Error`), the conversation or "No messages yet", and under it Connect to Chat, a word while the dial is under way, the field and Send, or the error and Try Again.
  - Send is in a disabled fieldset while the field is blank or spaces only (upstream's `isDisabled`), and Enter in the field sends, as the form's submit does.
  - The conversation is virtual rows (`ui.Rows`, H5b), so a frame that arrives at the end of a long log writes only the rows the viewport shows.
- Package `memory` is the provider `Echo`, an echo server in process (upstream uses `wss://ws.postman-echo.com/raw`).
  - It also exposes the server's side: `Push`, `Hangup`, `Break`, `Refuse`, `Stall` (the next dial gets no answer) and `Vanish`.
  - It counts the lines dialed and the lines held.
  - `Hold` and `Flush` keep an echo until its send is done, so the conversation reads the same on every run.
- Package `script` is the host. It starts the actor with `actor.StartMachine` and shows it on `ui.NewTestHost`. It plays the user's acts on the screen and settles after each (`ui.Settle`), so the work's news (an opened line, a frame, an end) is taken. The clock moves only on `wait`. It reports a viewport 600 high for the conversation once it shows.

How the port differs from upstream:

- **UTC stamps.** Upstream shows the local zone. Std `time` has no zones, so the stamps are UTC (fk-ledger row 14).
- **The log lives in `Online`.** Upstream keeps the messages in the model across an error, and they show again after Try Again. Here the conversation is `Online`'s, so a new line starts with an empty one. This is upstream's Disconnected behaviour, applied to every end.
- **The plan's provider.** The plan named `examples/chat`'s `room` package as the provider. That package is in another module (`module chat`), and an example cannot import another, so the provider here is an echo server, as upstream's is.
- **No auto-scroll.** The viewport stays at the top, as upstream's list does not scroll itself.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 12 story tests and 8 scene tests. No story or scene is an Ergo test, because a law or a case already kills every mutant that a test of it would:

- `SendOnlyWhenOnline` covers the four submit stories.
- `ReconnectIsAFreshEntry` covers Connect, and an error that a release keeps.
- `DisconnectReleasesTheConnection` covers the server's close, a refused dial, a timed-out dial and a failed send.
- `ConversationKeepsOrder` covers the received and sent lines.
- The `tour` case runs all 12 on the screen, the actor and the provider.
- The `scenes` case restates the 8 scene tests in order, on one chat.

## Laws

- `talk`:
  - `SendOnlyWhenOnline`, `ReconnectIsAFreshEntry`, `DisconnectReleasesTheConnection` and `ConversationKeepsOrder`, over drawn states and signals.
  - `Channel.FramesKeepArrivalOrder` and `Channel.ClosedLineTakesNoSend`, which `ergo test` runs on `memory.Echo`.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`:
  - Offline, there is no field and no Send.
  - A refused dial, then Try Again.
  - Send disabled while the field is blank or spaces only; Enter does nothing then.
  - A send and its echo, stamped, and the server speaking unasked a minute later.
  - Enter sends as Send does.
  - A wait past the dial's timeout while online: the line stays.
  - The server's close, which takes the conversation with it.
  - A failure, which releases the line.
  - A dial with no answer: still Connecting at 4,999 ms, timed out at 5,000 ms, its line let go.
  - A line the server dropped, so the next send fails "Socket unavailable".
  - A last connect, whose line the stop releases.
- `scenes`: upstream's 8 scene tests.
- `badscript`: an unknown word. The program says which line and exits 1.

## Receipts

`fk-chat-b.json` in the run dir (plan §18.3 FK14): the work of the pass that shows a frame arriving at the end of a log of 100, 1,000 and 10,000 lines, and the live heap after 100 and 1,000 connect-and-drop cycles.
