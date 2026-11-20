# echo-chat and its original

What `examples/echo-chat` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `websocket-chat` example (`conformance/upstream/foldkit-d21db423/examples/websocket-chat`, MIT).
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

## Comparisons moved from the README

- upstream's view, on std ui's test host.
- Upstream gets the same lifetime from `ManagedResource` (`modelToMaybeRequirements`, `acquire` and `release`) plus a `Subscription` that streams the socket's events while connected.
- Upstream stamps each line in a command of its own.
- Upstream's `SendMessage` command reads the socket from the managed resource and fails "Socket unavailable" the same way.
